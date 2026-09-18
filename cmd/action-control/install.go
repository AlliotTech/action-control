package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

func installNew(a *App, source string) error {
	manifest, err := readPayload(source)
	if err != nil {
		return err
	}
	if err = checkDeviceDependencies(a); err != nil {
		return err
	}
	for _, name := range []string{"/blackbox", "/data/misc/wifi", "/etc/systemd/system"} {
		st, e := os.Lstat(a.Path(name))
		if e != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("required real directory missing: %s", name)
		}
	}
	for _, name := range []string{AppDir, RunDir, unitPath(ServiceName), unitPath(NetworkServiceName), filepath.Join(wantsPath, ServiceName), filepath.Join(wantsPath, NetworkServiceName)} {
		if exists(a.Path(name)) {
			return fmt.Errorf("occupied installation path; nothing changed: %s", name)
		}
	}
	if err = verifyOwned(a, nil, true); err != nil {
		return err
	}
	if a.Root == "/" {
		listener, e := net.Listen("tcp", ":8080")
		if e != nil {
			return fmt.Errorf("HTTP port occupied: %w", e)
		}
		listener.Close()
	}
	if err = os.Mkdir(a.Dir, 0700); err != nil {
		return err
	}
	if err = os.Chown(a.Dir, os.Geteuid(), os.Getegid()); err != nil {
		return err
	}
	state := &installState{Schema: 1, Version: manifest.Version, Phase: "preparing", Backups: []pathRecord{}, Owned: []pathRecord{}, Dirs: []pathRecord{}}
	if err = writeState(a, state); err != nil {
		return err
	}
	for _, name := range []string{AppDir + "/.restore", AppDir + "/bin", AppDir + "/licenses", RunDir} {
		if err = addDirectory(a, state, name, 0700); err != nil {
			return err
		}
	}
	if err = snapshotShared(a, state); err != nil {
		return err
	}
	for _, file := range manifest.Files {
		target := AppDir + "/" + file.Path
		dir := filepath.Dir(target)
		if dir != AppDir && dir != AppDir+"/bin" && dir != AppDir+"/licenses" {
			return errors.New("nested payload paths are not supported")
		}
		entry, e := describePath(a, target)
		if e != nil {
			return e
		}
		if entry.Kind != "absent" {
			return errors.New("payload target became occupied")
		}
		entry.Kind = "file"
		entry.Mode = file.Mode
		entry.UID, entry.GID = os.Geteuid(), os.Getegid()
		entry.SHA256 = file.SHA256
		state.Owned = append(state.Owned, entry)
		if err = writeState(a, state); err != nil {
			return err
		}
		if err = copyPayloadFile(filepath.Join(source, file.Path), a.Path(target), file, true); err != nil {
			return err
		}
	}
	hook, err := plannedFile(a, AppDir+"/dhcp-hook.sh", []byte(dhcpHook), 0755)
	if err != nil {
		return err
	}
	state.Owned = append(state.Owned, hook)
	if err = writeState(a, state); err != nil {
		return err
	}
	if err = replaceData(a.Path(hook.Path), []byte(dhcpHook), 0755, hook.UID, hook.GID, true); err != nil {
		return err
	}
	initialized, err := NewApp(a.Root)
	if err != nil {
		return err
	}
	initialized.cancel()
	if err = addDirectory(a, state, wantsPath, 0755); err != nil {
		return err
	}
	for _, unit := range []string{ServiceName, NetworkServiceName} {
		entry, e := plannedFile(a, unitPath(unit), unitContent(unit), 0644)
		if e != nil {
			return e
		}
		state.Owned = append(state.Owned, entry)
		link, e := describePath(a, filepath.Join(wantsPath, unit))
		if e != nil {
			return e
		}
		if link.Kind != "absent" {
			return errors.New("service link occupied")
		}
		link.Kind = "link"
		link.Mode = uint32(os.ModeSymlink | 0777)
		link.UID, link.GID = os.Geteuid(), os.Getegid()
		link.Target = unitPath(unit)
		state.Owned = append(state.Owned, link)
	}
	state.Phase = "prepared"
	if err = writeState(a, state); err != nil {
		return err
	}
	if a.Root != "/" {
		fmt.Println("Prepared offline filesystem only; no system service or hardware has been activated.")
		return nil
	}
	if err = checkBackups(a, state); err != nil {
		return err
	}
	state.Activated = true
	state.Phase = "activating"
	if err = writeState(a, state); err != nil {
		return err
	}
	for _, unit := range []string{ServiceName, NetworkServiceName} {
		if err = replaceData(a.Path(unitPath(unit)), unitContent(unit), 0644, os.Geteuid(), os.Getegid(), true); err != nil {
			return err
		}
		if err = os.Symlink(unitPath(unit), a.Path(filepath.Join(wantsPath, unit))); err != nil {
			return err
		}
	}
	if err = enablePersistentUnits(a); err != nil {
		return err
	}
	if _, err = a.Run(a.Ctx, 10*time.Second, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	if _, err = a.Run(a.Ctx, 20*time.Second, "systemctl", "start", ServiceName); err != nil {
		return err
	}
	if err = waitReady(a, manifest.Version); err != nil {
		return err
	}
	if _, err = a.Run(a.Ctx, 10*time.Second, "systemctl", "--no-block", "start", NetworkServiceName); err != nil {
		return err
	}
	state.Phase = "active"
	if err = writeState(a, state); err != nil {
		return err
	}
	fmt.Println("Installed Action Control", manifest.Version)
	return nil
}
func waitReady(a *App, version string) error {
	if a.Root != "/" {
		return errors.New("HTTP readiness can only be checked on the camera")
	}
	client := http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get("http://127.0.0.1:8080/api/health")
		if err == nil {
			var health struct {
				OK      bool   `json:"ok"`
				Version string `json:"version"`
				Device  bool   `json:"device"`
			}
			decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&health)
			resp.Body.Close()
			if resp.StatusCode == 200 && decodeErr == nil && health.OK && health.Device && health.Version == version {
				out, e := a.Run(a.Ctx, 5*time.Second, "systemctl", "show", ServiceName, "--property=MainPID", "--value")
				pid, e2 := strconv.Atoi(strings.TrimSpace(string(out)))
				if e == nil && e2 == nil && pid > 1 && belongsToUnit(a, pid, ServiceName) {
					return nil
				}
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	return errors.New("managed HTTP service did not become ready with the expected version")
}
func stopUnits(a *App, names ...string) error {
	if a.Root != "/" {
		return nil
	}
	if _, err := a.Run(a.Ctx, 45*time.Second, "systemctl", append([]string{"stop"}, names...)...); err != nil {
		return err
	}
	processes, err := readProcesses(a)
	if err != nil {
		return err
	}
	for _, p := range processes {
		for _, unit := range names {
			if belongsToUnit(a, p.PID, unit) {
				return fmt.Errorf("owned process %d still alive after stopping %s", p.PID, unit)
			}
		}
	}
	return nil
}
func checkPrivateTree(a *App, state *installState) error {
	// Legacy image files remain app-owned so updates and uninstall can handle old installs.
	allowed := map[string]bool{stateFile: true, "config.json": true, cameraPresetFile: true, "background.image": true}
	for _, entry := range state.Owned {
		if strings.HasPrefix(entry.Path, AppDir+"/") {
			allowed[strings.TrimPrefix(entry.Path, AppDir+"/")] = true
		}
	}
	if err := os.Remove(filepath.Join(a.Dir, ".access-token")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return filepath.WalkDir(a.Dir, func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name == a.Dir {
			return nil
		}
		relative, _ := filepath.Rel(a.Dir, name)
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("unexpected application symlink: %s", relative)
		}
		if entry.IsDir() {
			if slices.Contains([]string{".restore", ".update", "bin", "licenses"}, relative) {
				return nil
			}
			return fmt.Errorf("unrecorded application directory: %s", relative)
		}
		if strings.HasPrefix(relative, ".restore/") || strings.HasPrefix(relative, ".update/") || strings.HasPrefix(filepath.Base(relative), ".action-control-write-") || strings.HasPrefix(filepath.Base(relative), ".background-") {
			return nil
		}
		if !allowed[relative] {
			return fmt.Errorf("unrecorded application file: %s", relative)
		}
		return nil
	})
}
func checkDirectories(a *App, state *installState) error {
	for _, entry := range state.Dirs {
		if entry.Path != RunDir && entry.Path != wantsPath && !strings.HasPrefix(entry.Path, AppDir+"/") {
			return errors.New("invalid owned directory")
		}
		if !exists(a.Path(entry.Path)) {
			continue
		}
		current, err := describePath(a, entry.Path)
		if err != nil {
			return err
		}
		if current.Kind != "dir" || current.Parent != entry.Parent || current.Mode != entry.Mode || current.UID != entry.UID || current.GID != entry.GID {
			return fmt.Errorf("owned directory changed: %s", entry.Path)
		}
	}
	return nil
}
func uninstallApp(a *App, reboot bool) error {
	state, err := loadState(a)
	if err != nil {
		return err
	}
	if state.Activated && !reboot {
		return errors.New("uninstall requires --reboot; native runtime is restored by reboot, not by deleting files")
	}
	if state.Update != nil {
		if err = rollbackUpdate(a, state); err != nil {
			return err
		}
	}
	allowMissing := state.Phase != "active"
	if err = verifyOwned(a, state.Owned, allowMissing); err != nil {
		return err
	}
	if err = checkDirectories(a, state); err != nil {
		return err
	}
	if err = checkPrivateTree(a, state); err != nil {
		return err
	}
	if state.Activated {
		if err = checkBackups(a, state); err != nil {
			return err
		}
	}
	if err = stopUnits(a, ServiceName, NetworkServiceName); err != nil {
		return err
	}
	if state.Activated && state.Phase != "removing" {
		state.Phase = "restoring"
		if err = writeState(a, state); err != nil {
			return err
		}
		if err = restoreShared(a, state); err != nil {
			return err
		}
	}
	state.Phase = "removing"
	if err = writeState(a, state); err != nil {
		return err
	}
	for _, entry := range state.Owned {
		if strings.HasPrefix(entry.Path, AppDir+"/") {
			continue
		}
		if err = os.Remove(a.Path(entry.Path)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if a.Root == "/" {
		if _, err = a.Run(a.Ctx, 10*time.Second, "systemctl", "daemon-reload"); err != nil {
			return err
		}
	}
	if exists(a.RunDir) {
		if err = os.RemoveAll(a.RunDir); err != nil {
			return err
		}
	}
	for i := len(state.Dirs) - 1; i >= 0; i-- {
		entry := state.Dirs[i]
		if entry.Path != wantsPath {
			continue
		}
		if err = os.Remove(a.Path(entry.Path)); err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, unix.ENOTEMPTY) {
			return err
		}
	}
	// Everything outside this exclusive app directory is restored before the last evidence is removed.
	if err = os.RemoveAll(a.Dir); err != nil {
		return err
	}
	fmt.Println("Uninstalled owned files and restored recorded WiFi/DNS. Reboot is still required for native runtime.")
	return nil
}

func importConfig(a *App, file, known string) error {
	state, err := loadState(a)
	if err != nil {
		return err
	}
	if !state.Activated || state.Phase != "active" {
		return errors.New("configuration import requires an active installation")
	}
	if err = checkBackups(a, state); err != nil {
		return err
	}
	if err = verifyOwned(a, state.Owned, false); err != nil {
		return err
	}
	if file == "" && known == "" {
		return errors.New("specify --file or --known")
	}
	var config *Config
	var networks []knownNetwork
	if file != "" {
		data, e := os.ReadFile(file)
		if e != nil {
			return e
		}
		c, e := decodeConfig(data)
		if e != nil {
			return e
		}
		config = &c
	}
	if known != "" {
		data, e := os.ReadFile(known)
		if e != nil {
			return e
		}
		if e = json.Unmarshal(data, &networks); e != nil {
			return e
		}
		for _, n := range networks {
			if e = validateSSID(n.SSID); e != nil {
				return e
			}
			if e = validatePassword(n.Password); e != nil {
				return e
			}
			if e = validateBSSID(n.BSSID); e != nil {
				return e
			}
		}
	}
	lock, err := networkLock(a, false)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = pendingNetwork(a); err != nil {
		return err
	}
	previous, err := readNetworkConfig(a)
	if err != nil {
		return err
	}
	next := previous
	if config != nil {
		next = *config
	}
	if err = checkNetworkControlChange(a, previous, next); err != nil {
		return err
	}
	if known != "" && next.NetworkControl != networkControlManaged {
		return errNativeNetworkControl
	}
	if err = stopUnits(a, ServiceName); err != nil {
		return err
	}
	defer func() {
		if a.Root == "/" {
			if _, e := a.Run(a.Ctx, 20*time.Second, "systemctl", "start", ServiceName); e != nil {
				fmt.Fprintln(os.Stderr, "service restart after import:", e)
			}
		}
	}()
	if config != nil {
		store, e := loadConfig(filepath.Join(a.Dir, "config.json"))
		if e != nil {
			return e
		}
		if e = store.Update(func(c *Config) error { *c = *config; return nil }); e != nil {
			return e
		}
	}
	if known != "" {
		if networks == nil {
			networks = []knownNetwork{}
		}
		if err = saveKnown(a, networks); err != nil {
			return err
		}
	}
	fmt.Println("Imported selected user settings; original restoration baseline was not modified.")
	return nil
}
func verifyInstallation(a *App, removed bool) error {
	if removed {
		for _, name := range []string{AppDir, RunDir, unitPath(ServiceName), unitPath(NetworkServiceName), filepath.Join(wantsPath, ServiceName), filepath.Join(wantsPath, NetworkServiceName)} {
			if exists(a.Path(name)) {
				return fmt.Errorf("uninstall residue: %s", name)
			}
		}
		if a.Root == "/" {
			processes, err := readProcesses(a)
			if err != nil {
				return err
			}
			for _, p := range processes {
				if belongsToUnit(a, p.PID, ServiceName) || belongsToUnit(a, p.PID, NetworkServiceName) {
					return fmt.Errorf("owned process still running: %d", p.PID)
				}
			}
		}
		fmt.Println("No Action Control application, units or owned processes remain.")
		return nil
	}
	if err := validateRuntimeInstallation(a); err != nil {
		return err
	}
	state, err := loadState(a)
	if err != nil {
		return err
	}
	if state.Phase != "active" || state.Version != Version {
		return errors.New("expected active release version is not installed")
	}
	if err = verifyOwned(a, state.Owned, false); err != nil {
		return err
	}
	if a.Root == "/" {
		if err = waitReady(a, Version); err != nil {
			return err
		}
	}
	fmt.Println("Verified Action Control", Version)
	return nil
}
func RunInstaller(command string, args []string) error {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	root := flags.String("root", "/", "offline filesystem root; never activates hardware")
	source := flags.String("source", "", "payload directory")
	reboot := flags.Bool("reboot", false, "confirm subsequent host-managed reboot")
	file := flags.String("file", "", "selected JSON configuration to import")
	known := flags.String("known", "", "selected known-network JSON to import")
	removed := flags.Bool("removed", false, "verify that uninstalled paths and cgroups are absent")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected installer arguments")
	}
	a, err := appAt(*root)
	if err != nil {
		return err
	}
	defer a.cancel()
	if a.Root == "/" {
		if err = a.RequireDevice(); err != nil {
			return err
		}
		executable, e := os.Executable()
		if e != nil {
			return e
		}
		real, e := filepath.EvalSymlinks(executable)
		if e != nil {
			return e
		}
		if real == a.Dir || strings.HasPrefix(real, a.Dir+"/") || belongsToUnit(a, os.Getpid(), ServiceName) || belongsToUnit(a, os.Getpid(), NetworkServiceName) {
			return errors.New("run installer from a separate executable staging directory outside managed cgroups")
		}
	}
	if command == "verify" {
		return verifyInstallation(a, *removed)
	}
	lockName := a.Path("/run/action-control-installer.lock")
	lock, err := os.OpenFile(lockName, os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return errors.New("another installer is active")
	}
	switch command {
	case "install", "update":
		if *source == "" {
			return errors.New("--source payload directory required")
		}
		resolved, e := filepath.Abs(*source)
		if e != nil {
			return e
		}
		resolved, e = filepath.EvalSymlinks(resolved)
		if e != nil {
			return e
		}
		if command == "install" {
			return installNew(a, resolved)
		}
		return updateApp(a, resolved, *reboot)
	case "uninstall":
		return uninstallApp(a, *reboot)
	case "import-config":
		return importConfig(a, *file, *known)
	default:
		return errors.New("unknown installer operation")
	}
}
