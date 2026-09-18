package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

const (
	stateFile      = ".install-state.json"
	wantsPath      = "/etc/systemd/system/multi-user.target.wants"
	bootScriptPath = "/etc/init.post_boot.sh"
	dhcpHook       = "#!/bin/sh\nexec " + AppDir + "/action-control dhcp \"$@\"\n"
	bootHook       = "\n# Reload units created in the persistent /etc overlay after systemd's boot scan.\nsystemctl daemon-reload && systemctl --no-block start action-control.service action-control-network.service\n"
)

var sharedPaths = []string{knownPath, "/data/misc/wifi/wpa_supplicant.conf", "/data/misc/wifi/hostapd.conf", hotspotPath, "/etc/resolv.conf", bootScriptPath}

type pathRecord struct {
	Path   string `json:"path"`
	Parent string `json:"parent"`
	Kind   string `json:"kind"`
	Mode   uint32 `json:"mode"`
	UID    int    `json:"uid"`
	GID    int    `json:"gid"`
	SHA256 string `json:"sha256,omitempty"`
	Target string `json:"target,omitempty"`
	Backup string `json:"backup,omitempty"`
}
type payloadFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Mode   uint32 `json:"mode"`
}
type payloadManifest struct {
	Schema  int           `json:"schema"`
	Version string        `json:"version"`
	Files   []payloadFile `json:"files"`
}
type updateRecord struct {
	Phase          string       `json:"phase"`
	OldVersion     string       `json:"old_version"`
	OldOwned       []pathRecord `json:"old_owned"`
	NewOwned       []pathRecord `json:"new_owned"`
	Rollback       []pathRecord `json:"rollback"`
	NetworkChanged bool         `json:"network_changed"`
}
type installState struct {
	Schema          int           `json:"schema"`
	Version         string        `json:"version"`
	Phase           string        `json:"phase"`
	Activated       bool          `json:"activated"`
	BackupsComplete bool          `json:"backups_complete"`
	Backups         []pathRecord  `json:"backups"`
	Owned           []pathRecord  `json:"owned"`
	Dirs            []pathRecord  `json:"directories"`
	Update          *updateRecord `json:"update,omitempty"`
}

func fileDigest(name string) (string, error) {
	f, err := os.OpenFile(name, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return "", errors.New("checksum source is not a regular file")
	}
	hash := sha256.New()
	if _, err = io.Copy(hash, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
func bytesDigest(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
func exists(name string) bool     { _, err := os.Lstat(name); return !errors.Is(err, os.ErrNotExist) }
func unitPath(name string) string { return "/etc/systemd/system/" + name }

// Resolve within the selected filesystem, including absolute symlink targets.
// Never let an offline verification root's /etc link escape into the host /etc.
func installPath(a *App, name string, followLeaf bool) (string, error) {
	if !filepath.IsAbs(name) || filepath.Clean(name) != name {
		return "", errors.New("installation path must be canonical and absolute")
	}
	parts := strings.Split(strings.TrimPrefix(name, "/"), "/")
	current := "/"
	links := 0
	for i := 0; i < len(parts); i++ {
		if parts[i] == "" {
			continue
		}
		current = filepath.Join(current, parts[i])
		last := i == len(parts)-1
		if last && !followLeaf {
			return a.Path(current), nil
		}
		st, err := os.Lstat(a.Path(current))
		if errors.Is(err, os.ErrNotExist) && last {
			return a.Path(current), nil
		}
		if err != nil {
			return "", err
		}
		if st.Mode()&os.ModeSymlink != 0 {
			links++
			if links > 40 {
				return "", errors.New("symlink loop in installation path")
			}
			target, err := os.Readlink(a.Path(current))
			if err != nil {
				return "", err
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(current), target)
			}
			target = filepath.Clean(target)
			parts = append(strings.Split(strings.TrimPrefix(target, "/"), "/"), parts[i+1:]...)
			current = "/"
			i = -1
		} else if !last && !st.IsDir() {
			return "", errors.New("installation parent is not a directory")
		}
	}
	return a.Path(current), nil
}
func logicalPath(a *App, actual string) (string, error) {
	relative, err := filepath.Rel(a.Root, actual)
	if err != nil || relative == ".." || strings.HasPrefix(relative, "../") {
		return "", errors.New("path escaped installation root")
	}
	if relative == "." {
		return "/", nil
	}
	return "/" + filepath.ToSlash(relative), nil
}
func describePath(a *App, name string) (pathRecord, error) {
	parent, err := installPath(a, filepath.Dir(name), true)
	if err != nil {
		return pathRecord{}, err
	}
	logical, err := logicalPath(a, parent)
	if err != nil {
		return pathRecord{}, err
	}
	entry := pathRecord{Path: name, Parent: logical, Kind: "absent"}
	actual, err := installPath(a, name, false)
	if err != nil {
		return entry, err
	}
	st, err := os.Lstat(actual)
	if errors.Is(err, os.ErrNotExist) {
		return entry, nil
	}
	if err != nil {
		return entry, err
	}
	native, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return entry, errors.New("file ownership metadata unavailable")
	}
	entry.UID, entry.GID = int(native.Uid), int(native.Gid)
	entry.Mode = uint32(st.Mode())
	switch {
	case st.Mode()&os.ModeSymlink != 0:
		entry.Kind = "link"
		entry.Target, err = os.Readlink(actual)
	case st.IsDir():
		entry.Kind = "dir"
	case st.Mode().IsRegular():
		if native.Nlink != 1 {
			return entry, errors.New("hard-linked installation/shared file is unsupported")
		}
		entry.Kind = "file"
		entry.SHA256, err = fileDigest(actual)
	default:
		err = errors.New("unsupported installation/shared file type")
	}
	return entry, err
}
func writeState(a *App, state *installState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(a.Dir, stateFile), append(data, '\n'), 0600)
}
func loadState(a *App) (*installState, error) {
	if err := privateDirExisting(a.Dir); err != nil {
		return nil, err
	}
	name := filepath.Join(a.Dir, stateFile)
	st, err := os.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm() != 0600 || st.Size() > 1<<20 {
		return nil, errors.New("invalid installation record")
	}
	data, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}
	var state installState
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&state); err != nil {
		return nil, err
	}
	if state.Schema != 1 || state.Version == "" || !slices.Contains([]string{"preparing", "prepared", "activating", "active", "updating", "restoring", "removing"}, state.Phase) {
		return nil, errors.New("unsupported installation state")
	}
	return &state, nil
}
func privateDirExisting(name string) error {
	st, err := os.Lstat(name)
	if err != nil {
		return err
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm() != 0700 {
		return errors.New("installation directory must be a real private directory")
	}
	return nil
}
func safeOwnedPath(name string) bool {
	if filepath.Clean(name) != name || !filepath.IsAbs(name) {
		return false
	}
	for _, unit := range []string{ServiceName, NetworkServiceName} {
		if name == unitPath(unit) || name == filepath.Join(wantsPath, unit) {
			return true
		}
	}
	if !strings.HasPrefix(name, AppDir+"/") {
		return false
	}
	relative := strings.TrimPrefix(name, AppDir+"/")
	return relative == "action-control" || relative == "dhcp-hook.sh" || strings.HasPrefix(relative, "bin/") || strings.HasPrefix(relative, "licenses/")
}
func verifyOwned(a *App, entries []pathRecord, allowMissing bool) error {
	seen := map[string]bool{}
	for _, entry := range entries {
		if !safeOwnedPath(entry.Path) || seen[entry.Path] {
			return errors.New("invalid owned-path record")
		}
		seen[entry.Path] = true
		current, err := describePath(a, entry.Path)
		if err != nil {
			return err
		}
		if current.Kind == "absent" && allowMissing {
			continue
		}
		if current.Kind != entry.Kind || current.Parent != entry.Parent || current.Mode != entry.Mode || current.UID != entry.UID || current.GID != entry.GID || current.SHA256 != entry.SHA256 || current.Target != entry.Target {
			return fmt.Errorf("owned file changed; nothing removed: %s", entry.Path)
		}
	}
	for _, unit := range []string{ServiceName, NetworkServiceName} {
		for _, name := range []string{unitPath(unit) + ".d", "/run/systemd/system/" + unit, "/run/systemd/system/" + unit + ".d", "/usr/lib/systemd/system/" + unit, "/lib/systemd/system/" + unit} {
			if exists(a.Path(name)) {
				return fmt.Errorf("unrecorded service override: %s", name)
			}
		}
	}
	return nil
}
func checkBackupsFor(a *App, state *installState, required []string) error {
	if !state.BackupsComplete {
		return errors.New("restoration baseline is incomplete")
	}
	byPath := map[string]pathRecord{}
	for _, entry := range state.Backups {
		if !filepath.IsAbs(entry.Path) || filepath.Clean(entry.Path) != entry.Path || entry.Path == AppDir || strings.HasPrefix(entry.Path, AppDir+"/") || strings.HasPrefix(entry.Path, RunDir+"/") {
			return errors.New("invalid shared backup path")
		}
		if _, ok := byPath[entry.Path]; ok {
			return errors.New("duplicate backup path")
		}
		byPath[entry.Path] = entry
	}
	needed := slices.Clone(required)
	for i := 0; i < len(needed); i++ {
		entry, ok := byPath[needed[i]]
		if !ok {
			return fmt.Errorf("missing baseline for %s", needed[i])
		}
		if entry.Kind == "link" {
			target := entry.Target
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(entry.Path), target)
			}
			target = filepath.Clean(target)
			if !slices.Contains(needed, target) {
				needed = append(needed, target)
			}
		}
	}
	if len(needed) != len(state.Backups) {
		return errors.New("unexpected backup record")
	}
	for _, entry := range state.Backups {
		current, err := describePath(a, entry.Path)
		if err != nil {
			return err
		}
		if current.Parent != entry.Parent {
			return fmt.Errorf("shared parent changed: %s", entry.Path)
		}
		if current.Kind != "absent" && current.Kind != "file" && current.Kind != "link" {
			return fmt.Errorf("shared file type changed: %s", entry.Path)
		}
		if current.Kind == "link" && (entry.Kind != "link" || current.Target != entry.Target) {
			return fmt.Errorf("shared symlink changed: %s", entry.Path)
		}
		if entry.Kind == "link" && current.Kind == "file" {
			return fmt.Errorf("shared symlink replaced: %s", entry.Path)
		}
		if entry.Kind == "absent" {
			if entry.Backup != "" || entry.SHA256 != "" {
				return errors.New("invalid absent backup")
			}
			continue
		}
		if entry.Kind != "file" && entry.Kind != "link" {
			return errors.New("unsupported backup type")
		}
		expected := ".restore/" + bytesDigest([]byte(entry.Path)) + ".bin"
		if entry.Backup != expected {
			return errors.New("invalid backup filename")
		}
		hash, err := fileDigest(filepath.Join(a.Dir, entry.Backup))
		if err != nil || hash != entry.SHA256 {
			return fmt.Errorf("missing/corrupt restoration backup: %s", entry.Path)
		}
		if entry.Kind == "link" && bytesDigest([]byte(entry.Target)) != entry.SHA256 {
			return errors.New("symlink backup digest mismatch")
		}
	}
	return nil
}

func checkBackups(a *App, state *installState) error {
	return checkBackupsFor(a, state, sharedPaths)
}
func hasBootBackup(state *installState) bool {
	for _, entry := range state.Backups {
		if entry.Path == bootScriptPath {
			return true
		}
	}
	return false
}

func checkCompatibleBackups(a *App, state *installState) error {
	if hasBootBackup(state) {
		return checkBackups(a, state)
	}
	return checkBackupsFor(a, state, sharedPaths[:len(sharedPaths)-1])
}

func migrateBootBackup(a *App, state *installState) error {
	if hasBootBackup(state) {
		return checkBackups(a, state)
	}
	if err := checkCompatibleBackups(a, state); err != nil {
		return err
	}
	entry, err := describePath(a, bootScriptPath)
	if err != nil {
		return err
	}
	if entry.Kind != "file" {
		return errors.New("persistent post-boot script is not a regular file")
	}
	actual, err := installPath(a, bootScriptPath, false)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(actual)
	if err != nil {
		return err
	}
	if len(data) > 8<<20 || bytesDigest(data) != entry.SHA256 {
		return errors.New("persistent post-boot script changed during backup")
	}
	entry.Backup = ".restore/" + bytesDigest([]byte(bootScriptPath)) + ".bin"
	backup := filepath.Join(a.Dir, entry.Backup)
	if exists(backup) {
		digest, e := fileDigest(backup)
		if e != nil || digest != entry.SHA256 {
			return errors.New("existing post-boot restoration backup is invalid")
		}
	} else if err = replaceData(backup, data, 0600, os.Geteuid(), os.Getegid(), true); err != nil {
		return err
	}
	state.Backups = append(state.Backups, entry)
	return writeState(a, state)
}

func enablePersistentUnits(a *App) error {
	data, err := os.ReadFile(a.Path(bootScriptPath))
	if err != nil {
		return err
	}
	if bytes.Contains(data, []byte(bootHook)) {
		return nil
	}
	return writeManagedShared(a, bootScriptPath, append(data, bootHook...), 0755)
}
func validateRuntimeInstallation(a *App) error {
	state, err := loadState(a)
	if err != nil {
		return err
	}
	if !state.Activated || !slices.Contains([]string{"activating", "active", "updating"}, state.Phase) {
		return errors.New("installation is not activated")
	}
	if err = checkBackups(a, state); err != nil {
		return err
	}
	units := []pathRecord{}
	for _, entry := range state.Owned {
		if !strings.HasPrefix(entry.Path, AppDir+"/") {
			units = append(units, entry)
		}
	}
	if len(units) != 4 {
		return errors.New("incomplete service ownership record")
	}
	return verifyOwned(a, units, false)
}
func writeManagedShared(a *App, absolute string, data []byte, mode os.FileMode) error {
	if !slices.Contains(sharedPaths, absolute) {
		return errors.New("unbacked shared write target")
	}
	if err := validateRuntimeInstallation(a); err != nil {
		return err
	}
	target, err := installPath(a, absolute, true)
	if err != nil {
		return err
	}
	state, err := loadState(a)
	if err != nil {
		return err
	}
	logical, err := logicalPath(a, target)
	if err != nil {
		return err
	}
	recorded := false
	for _, entry := range state.Backups {
		if entry.Path == logical && entry.Kind != "link" {
			recorded = true
			break
		}
	}
	if !recorded {
		return errors.New("shared symlink target is not backed up")
	}
	uid, gid := os.Geteuid(), os.Getegid()
	if st, e := os.Lstat(target); e == nil {
		if !st.Mode().IsRegular() {
			return errors.New("shared target is not regular")
		}
		native := st.Sys().(*syscall.Stat_t)
		if native.Nlink != 1 {
			return errors.New("shared target has hard links")
		}
		mode = st.Mode()
		uid, gid = int(native.Uid), int(native.Gid)
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	return replaceData(target, data, mode, uid, gid, false)
}
func replaceData(name string, data []byte, mode os.FileMode, uid, gid int, exclusive bool) (err error) {
	f, err := os.CreateTemp(filepath.Dir(name), ".action-control-write-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chown(uid, gid); err == nil {
		err = f.Chmod(mode)
	}
	if err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if exclusive {
		parent, e := os.Open(filepath.Dir(name))
		if e != nil {
			return e
		}
		defer parent.Close()
		err = renameNoReplace(int(parent.Fd()), filepath.Base(tmp), int(parent.Fd()), filepath.Base(name))
	} else {
		err = os.Rename(tmp, name)
	}
	if err != nil {
		return err
	}
	parent, err := os.Open(filepath.Dir(name))
	if err != nil {
		return err
	}
	return errors.Join(parent.Sync(), parent.Close())
}
func snapshotShared(a *App, state *installState) error {
	pending := slices.Clone(sharedPaths)
	for i := 0; i < len(pending); i++ {
		name := pending[i]
		entry, err := describePath(a, name)
		if err != nil {
			return err
		}
		if entry.Kind == "dir" {
			return fmt.Errorf("shared path is a directory: %s", name)
		}
		if strings.HasPrefix(name, AppDir+"/") || strings.HasPrefix(name, RunDir+"/") {
			return errors.New("shared file aliases application state")
		}
		if entry.Kind != "absent" {
			var data []byte
			if entry.Kind == "link" {
				if _, err = installPath(a, name, true); err != nil {
					return err
				}
				data = []byte(entry.Target)
				target := entry.Target
				if !filepath.IsAbs(target) {
					target = filepath.Join(filepath.Dir(name), target)
				}
				target = filepath.Clean(target)
				if !slices.Contains(pending, target) {
					pending = append(pending, target)
				}
			} else {
				actual, e := installPath(a, name, false)
				if e != nil {
					return e
				}
				st, e := os.Stat(actual)
				if e != nil {
					return e
				}
				if st.Size() > 8<<20 {
					return errors.New("shared configuration exceeds backup limit")
				}
				data, err = os.ReadFile(actual)
				if err != nil {
					return err
				}
				if bytesDigest(data) != entry.SHA256 {
					return errors.New("shared file changed during backup")
				}
			}
			entry.Backup = ".restore/" + bytesDigest([]byte(name)) + ".bin"
			entry.SHA256 = bytesDigest(data)
			if err = replaceData(filepath.Join(a.Dir, entry.Backup), data, 0600, os.Geteuid(), os.Getegid(), true); err != nil {
				return err
			}
		}
		state.Backups = append(state.Backups, entry)
		if err = writeState(a, state); err != nil {
			return err
		}
	}
	state.BackupsComplete = true
	return writeState(a, state)
}
func restoreShared(a *App, state *installState) error {
	if err := checkBackups(a, state); err != nil {
		return err
	}
	for i := len(state.Backups) - 1; i >= 0; i-- {
		entry := state.Backups[i]
		target, err := installPath(a, entry.Path, false)
		if err != nil {
			return err
		}
		switch entry.Kind {
		case "absent":
			if err = os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		case "file":
			data, e := os.ReadFile(filepath.Join(a.Dir, entry.Backup))
			if e != nil {
				return e
			}
			if err = replaceData(target, data, os.FileMode(entry.Mode), entry.UID, entry.GID, false); err != nil {
				return err
			}
		case "link":
			if exists(target) {
				if err = os.Remove(target); err != nil {
					return err
				}
			}
			if err = os.Symlink(entry.Target, target); err != nil {
				return err
			}
			if err = os.Lchown(target, entry.UID, entry.GID); err != nil {
				return err
			}
		}
	}
	return nil
}
func addDirectory(a *App, state *installState, name string, mode os.FileMode) error {
	if exists(a.Path(name)) {
		st, err := os.Lstat(a.Path(name))
		if err != nil {
			return err
		}
		if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("invalid directory %s", name)
		}
		return nil
	}
	parent, err := installPath(a, filepath.Dir(name), true)
	if err != nil {
		return err
	}
	logical, err := logicalPath(a, parent)
	if err != nil {
		return err
	}
	entry := pathRecord{Path: name, Parent: logical, Kind: "dir", Mode: uint32(os.ModeDir | mode), UID: os.Geteuid(), GID: os.Getegid()}
	state.Dirs = append(state.Dirs, entry)
	if err = writeState(a, state); err != nil {
		return err
	}
	if err = os.Mkdir(a.Path(name), mode); err != nil {
		return err
	}
	return os.Chown(a.Path(name), entry.UID, entry.GID)
}
