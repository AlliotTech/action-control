package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	AppDir             = "/blackbox/upgrade/action-control"
	RunDir             = "/run/action-control"
	ServiceName        = "action-control.service"
	NetworkServiceName = "action-control-network.service"
)

var Version = "0.1.10"

type App struct {
	Root         string
	Dir          string
	RunDir       string
	Ctx          context.Context
	Config       *ConfigStore
	Restart      chan struct{}
	cancel       context.CancelFunc
	nativeReader nativeStateReader
	nativeIO     nativeCameraGate
}

func appAt(root string) (*App, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	a := &App{Root: root, Ctx: ctx, cancel: cancel, Restart: make(chan struct{}, 1)}
	a.Dir, a.RunDir = a.Path(AppDir), a.Path(RunDir)
	return a, nil
}

func NewApp(root string) (*App, error) {
	a, err := appAt(root)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			a.cancel()
		}
	}()
	for _, dir := range []string{a.Dir, a.RunDir} {
		if err = privateDir(dir); err != nil {
			return nil, err
		}
	}
	a.Config, err = loadConfig(filepath.Join(a.Dir, "config.json"))
	if err != nil {
		return nil, err
	}
	ok = true
	return a, nil
}

func privateDir(name string) error {
	info, err := os.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		if err = os.MkdirAll(name, 0700); err != nil {
			return err
		}
		info, err = os.Lstat(name)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("private directory is not a real directory: %s", name)
	}
	if info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("private directory must have mode 0700: %s", name)
	}
	return nil
}

func (a *App) Path(absolute string) string {
	return filepath.Join(a.Root, strings.TrimPrefix(absolute, "/"))
}
func (a *App) RequireDevice() error {
	if runtime.GOOS != "linux" || a.Root != "/" || os.Geteuid() != 0 {
		return errors.New("此操作仅限相机上的 root 服务；本地文件预览不控制硬件")
	}
	b, err := os.ReadFile("/build.prop")
	if err != nil {
		return fmt.Errorf("cannot identify camera: %w", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "ro.product.name=qcs8550_ac204" {
			return nil
		}
	}
	return errors.New("unsupported device: expected qcs8550_ac204")
}
func (a *App) RequireManaged() error {
	if err := a.RequireDevice(); err != nil {
		return err
	}
	return validateRuntimeInstallation(a)
}
func (a *App) bootID() (string, error) {
	b, err := os.ReadFile(a.Path("/proc/sys/kernel/random/boot_id"))
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(b))
	if id == "" {
		return "", errors.New("empty boot ID")
	}
	return id, nil
}
func (a *App) MarkRebootRequired(reason string, camera bool) error {
	lock, err := os.OpenFile(filepath.Join(a.RunDir, "reboot-required.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	id, err := a.bootID()
	if err != nil {
		return err
	}
	var old struct {
		BootID string `json:"boot_id"`
		Camera bool   `json:"camera"`
	}
	file := filepath.Join(a.RunDir, "reboot-required.json")
	if data, e := os.ReadFile(file); e == nil {
		if err = json.Unmarshal(data, &old); err != nil {
			return err
		}
		camera = camera || (old.BootID == id && old.Camera)
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	b, err := json.Marshal(struct {
		BootID string `json:"boot_id"`
		Reason string `json:"reason"`
		Camera bool   `json:"camera"`
	}{id, reason, camera})
	if err != nil {
		return err
	}
	return atomicWrite(file, b, 0600)
}
func (a *App) RebootRequired() bool {
	b, err := os.ReadFile(filepath.Join(a.RunDir, "reboot-required.json"))
	if err != nil {
		return !errors.Is(err, os.ErrNotExist)
	}
	var marker struct {
		BootID string `json:"boot_id"`
	}
	if json.Unmarshal(b, &marker) != nil {
		return true
	}
	id, err := a.bootID()
	return err != nil || marker.BootID == id
}
func (a *App) ScreenRequired() bool {
	data, err := os.ReadFile(filepath.Join(a.RunDir, "reboot-required.json"))
	if err != nil {
		return false
	}
	var marker struct {
		BootID string `json:"boot_id"`
		Camera bool   `json:"camera"`
	}
	if json.Unmarshal(data, &marker) != nil {
		return false
	}
	id, err := a.bootID()
	return err == nil && marker.BootID == id && marker.Camera
}

func (a *App) Tool(name string) (string, error) {
	if filepath.Base(name) != name || name == "." {
		return "", errors.New("invalid tool name")
	}
	candidates := []string{filepath.Join(a.Dir, "bin", name)}
	for _, dir := range []string{"/usr/bin", "/usr/sbin", "/sbin", "/bin"} {
		candidates = append(candidates, a.Path(filepath.Join(dir, name)))
	}
	for _, path := range candidates {
		if st, e := os.Stat(path); e == nil && st.Mode().IsRegular() && st.Mode().Perm()&0111 != 0 {
			return path, nil
		}
	}
	if a.Root != "/" || runtime.GOOS != "linux" {
		if name == "ffmpeg" || name == "ffprobe" || name == "sqlite3" {
			return exec.LookPath(name)
		}
	}
	return "", fmt.Errorf("required tool unavailable: %s", name)
}

type boundedBuffer struct {
	mu sync.Mutex
	bytes.Buffer
	limit     int
	truncated bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	room := b.limit - b.Buffer.Len()
	if room > 0 {
		b.Buffer.Write(p[:min(room, n)])
	}
	if n > room {
		b.truncated = true
	}
	return n, nil
}
func (b *boundedBuffer) Text() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.Buffer.String()
	if b.truncated {
		s += "\n[output truncated]"
	}
	return s
}
func ownedCommand(ctx context.Context, program string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 2 * time.Second
	return cmd
}
func (a *App) Run(ctx context.Context, timeout time.Duration, name string, args ...string) ([]byte, error) {
	program, err := a.Tool(name)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := ownedCommand(ctx, program, args...)
	out, stderr := &boundedBuffer{limit: 1024 * 1024}, &boundedBuffer{limit: 64 * 1024}
	cmd.Stdout, cmd.Stderr = out, stderr
	if err = cmd.Run(); err != nil {
		return []byte(out.Text()), fmt.Errorf("%s: %w: %s", name, err, stderr.Text())
	}
	if out.truncated {
		return nil, fmt.Errorf("%s output exceeds limit", name)
	}
	return []byte(out.Text()), nil
}

func atomicWrite(name string, data []byte, mode os.FileMode) (err error) {
	f, err := os.CreateTemp(filepath.Dir(name), ".action-control-write-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = os.Rename(tmp, name); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(name))
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
func jsonResponse(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
func jsonError(w http.ResponseWriter, status int, code string, err error) {
	jsonResponse(w, status, map[string]any{"error": map[string]string{"code": code, "message": err.Error()}})
}
func readJSON(w http.ResponseWriter, r *http.Request, value any) bool {
	if media := strings.Split(r.Header.Get("Content-Type"), ";")[0]; media != "application/json" {
		jsonError(w, 415, "content_type", errors.New("expected application/json"))
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	err := dec.Decode(value)
	if err == nil {
		var extra any
		if e := dec.Decode(&extra); !errors.Is(e, io.EOF) {
			err = errors.New("expected one JSON value")
		}
	}
	if err != nil {
		status := 400
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			status = 413
		}
		jsonError(w, status, "invalid_json", err)
		return false
	}
	return true
}
func confirmRequest(w http.ResponseWriter, r *http.Request, a *App) bool {
	var data struct {
		Confirm bool `json:"confirm"`
	}
	if !readJSON(w, r, &data) {
		return false
	}
	if !data.Confirm {
		jsonError(w, 400, "confirmation_required", errors.New("explicit confirmation required"))
		return false
	}
	if err := a.RequireManaged(); err != nil {
		jsonError(w, 503, "device_unavailable", err)
		return false
	}
	return true
}
