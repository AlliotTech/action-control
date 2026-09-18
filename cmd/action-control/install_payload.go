package main

import (
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func unitContent(name string) []byte {
	if name == NetworkServiceName {
		return []byte("[Unit]\nDescription=Action Control wireless lifecycle\nAfter=local-fs.target dji_network.service\n\n[Service]\nType=oneshot\nExecStart=" + AppDir + "/action-control network\nRemainAfterExit=yes\nKillMode=control-group\nTimeoutStartSec=300\nTimeoutStopSec=15\nUMask=0077\n\n[Install]\nWantedBy=multi-user.target\n")
	}
	return []byte("[Unit]\nDescription=Action Control camera console\nAfter=local-fs.target\n\n[Service]\nType=simple\nExecStart=" + AppDir + "/action-control serve --listen :8080\nRestart=on-failure\nRestartSec=2\nKillMode=control-group\nTimeoutStopSec=15\nUMask=0077\n\n[Install]\nWantedBy=multi-user.target\n")
}
func validateELF(name string) error {
	file, err := elf.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	if file.Class != elf.ELFCLASS64 || file.Machine != elf.EM_AARCH64 {
		return errors.New("expected a Linux ARM64 executable")
	}
	for _, program := range file.Progs {
		if program.Type == elf.PT_INTERP {
			return errors.New("release executable has a dynamic interpreter")
		}
	}
	libraries, err := file.ImportedLibraries()
	if err != nil {
		return err
	}
	if len(libraries) != 0 {
		return errors.New("release executable has dynamic dependencies")
	}
	return nil
}
func readPayload(source string) (payloadManifest, error) {
	var manifest payloadManifest
	st, err := os.Lstat(source)
	if err != nil {
		return manifest, err
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return manifest, errors.New("payload must be a real directory")
	}
	b, err := os.ReadFile(filepath.Join(source, "manifest.json"))
	if err != nil {
		return manifest, err
	}
	if len(b) > 1<<20 {
		return manifest, errors.New("payload manifest too large")
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&manifest); err != nil {
		return manifest, err
	}
	if manifest.Schema != 1 || manifest.Version != Version {
		return manifest, errors.New("payload/bootstrap version mismatch")
	}
	seen := map[string]bool{}
	for _, entry := range manifest.Files {
		if entry.Path == "" || filepath.IsAbs(entry.Path) || filepath.Clean(entry.Path) != entry.Path || strings.Contains(entry.Path, "\\") || !safeOwnedPath(AppDir+"/"+entry.Path) || seen[entry.Path] || entry.Size < 0 || entry.Size > 512<<20 || (entry.Mode != 0644 && entry.Mode != 0755) {
			return manifest, errors.New("invalid payload file")
		}
		seen[entry.Path] = true
		actual := filepath.Join(source, entry.Path)
		parent, err := filepath.EvalSymlinks(filepath.Dir(actual))
		if err != nil {
			return manifest, err
		}
		relative, err := filepath.Rel(source, parent)
		if err != nil || relative == ".." || strings.HasPrefix(relative, "../") {
			return manifest, errors.New("payload symlink escape")
		}
		st, err := os.Lstat(actual)
		if err != nil || !st.Mode().IsRegular() || st.Size() != entry.Size {
			return manifest, fmt.Errorf("payload size/type mismatch: %s", entry.Path)
		}
		digest, err := fileDigest(actual)
		if err != nil || digest != entry.SHA256 {
			return manifest, fmt.Errorf("payload checksum mismatch: %s", entry.Path)
		}
	}
	for _, name := range []string{"action-control", "bin/ffmpeg", "bin/ffprobe"} {
		if !seen[name] {
			return manifest, fmt.Errorf("missing required payload %s", name)
		}
		if err = validateELF(filepath.Join(source, name)); err != nil {
			return manifest, fmt.Errorf("%s: %w", name, err)
		}
	}
	return manifest, nil
}
func copyPayloadFile(source, target string, entry payloadFile, exclusive bool) (err error) {
	input, err := os.OpenFile(source, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.CreateTemp(filepath.Dir(target), ".action-control-write-*")
	if err != nil {
		return err
	}
	tmp := output.Name()
	defer os.Remove(tmp)
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(output, hash), io.LimitReader(input, entry.Size+1))
	if err == nil && (size != entry.Size || hex.EncodeToString(hash.Sum(nil)) != entry.SHA256) {
		err = errors.New("payload changed during copy")
	}
	if err == nil {
		err = output.Chown(os.Geteuid(), os.Getegid())
	}
	if err == nil {
		err = output.Chmod(os.FileMode(entry.Mode))
	}
	if err == nil {
		err = output.Sync()
	}
	err = errors.Join(err, output.Close())
	if err != nil {
		return err
	}
	if exclusive {
		parent, e := os.Open(filepath.Dir(target))
		if e != nil {
			return e
		}
		defer parent.Close()
		err = renameNoReplace(int(parent.Fd()), filepath.Base(tmp), int(parent.Fd()), filepath.Base(target))
	} else {
		err = os.Rename(tmp, target)
	}
	if err != nil {
		return err
	}
	parent, err := os.Open(filepath.Dir(target))
	if err != nil {
		return err
	}
	return errors.Join(parent.Sync(), parent.Close())
}
func plannedFile(a *App, name string, data []byte, mode os.FileMode) (pathRecord, error) {
	entry, err := describePath(a, name)
	if err != nil {
		return entry, err
	}
	if entry.Kind != "absent" {
		return entry, fmt.Errorf("unowned path occupied: %s", name)
	}
	entry.Kind = "file"
	entry.Mode = uint32(mode)
	entry.UID, entry.GID = os.Geteuid(), os.Getegid()
	entry.SHA256 = bytesDigest(data)
	return entry, nil
}
func checkDeviceDependencies(a *App) error {
	if a.Root != "/" {
		return nil
	}
	if err := a.RequireDevice(); err != nil {
		return err
	}
	if runtime.GOARCH != "arm64" {
		return errors.New("ARM64 camera required")
	}
	// Standalone Wi-Fi tools are checked by the managed-mode worker only.
	for _, name := range []string{"systemctl", "gst-launch-1.0", "gst-inspect-1.0", "qmmf-server", "weston-terminal", "iw", "reboot", "sqlite3"} {
		if _, err := a.Tool(name); err != nil {
			return err
		}
	}
	for _, plugin := range []string{"djiqmmfsrc", "djicamc2venc", "mpegtsmux"} {
		if _, err := a.Run(a.Ctx, 10*time.Second, "gst-inspect-1.0", plugin); err != nil {
			return err
		}
	}
	return nil
}
