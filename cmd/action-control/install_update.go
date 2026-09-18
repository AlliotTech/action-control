package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

func recoverUpdateFiles(a *App, entries []pathRecord) error {
	for _, entry := range entries {
		if !safeOwnedPath(entry.Path) {
			return errors.New("invalid update rollback path")
		}
		if entry.Kind == "absent" {
			continue
		}
		if entry.Kind != "file" || entry.Backup != ".update/"+bytesDigest([]byte(entry.Path))+".bin" {
			return errors.New("invalid update backup")
		}
		hash, err := fileDigest(filepath.Join(a.Dir, entry.Backup))
		if err != nil || hash != entry.SHA256 {
			return errors.New("update rollback backup damaged")
		}
	}
	for _, entry := range entries {
		if entry.Kind == "absent" {
			if err := os.Remove(a.Path(entry.Path)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			continue
		}
		st, err := os.Stat(filepath.Join(a.Dir, entry.Backup))
		if err != nil {
			return err
		}
		file := payloadFile{Path: entry.Path, SHA256: entry.SHA256, Size: st.Size(), Mode: entry.Mode}
		if err = copyPayloadFile(filepath.Join(a.Dir, entry.Backup), a.Path(entry.Path), file, false); err != nil {
			return err
		}
	}
	return nil
}
func finishUpdate(a *App, state *installState) error {
	directory := filepath.Join(a.Dir, ".update")
	if exists(directory) {
		if err := privateDirExisting(directory); err != nil {
			return err
		}
		allowed := map[string]bool{}
		for _, entry := range state.Update.Rollback {
			if entry.Kind == "file" {
				allowed[bytesDigest([]byte(entry.Path))+".bin"] = true
			}
		}
		entries, err := os.ReadDir(directory)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if !entry.Type().IsRegular() || (!allowed[entry.Name()] && !strings.HasPrefix(entry.Name(), ".action-control-write-")) {
				return errors.New("unrecorded update recovery file; nothing removed")
			}
		}
		if err = os.RemoveAll(directory); err != nil {
			return err
		}
	}
	state.Update = nil
	state.Phase = "active"
	return writeState(a, state)
}
func verifyUpdateFiles(a *App, update *updateRecord) error {
	possible := map[string][]pathRecord{}
	for _, entries := range [][]pathRecord{update.OldOwned, update.NewOwned} {
		seen := map[string]bool{}
		for _, entry := range entries {
			if !safeOwnedPath(entry.Path) || seen[entry.Path] {
				return errors.New("invalid update ownership record")
			}
			seen[entry.Path] = true
			possible[entry.Path] = append(possible[entry.Path], entry)
		}
	}
	for name, expected := range possible {
		current, err := describePath(a, name)
		if err != nil {
			return err
		}
		if current.Kind == "absent" && len(expected) == 1 {
			continue
		}
		if !slices.Contains(expected, current) {
			return fmt.Errorf("file changed outside recorded update: %s", name)
		}
	}
	return verifyOwned(a, nil, false)
}
func rollbackUpdate(a *App, state *installState) error {
	update := state.Update
	if update == nil {
		return nil
	}
	if err := checkBackups(a, state); err != nil {
		return err
	}
	if err := checkDirectories(a, state); err != nil {
		return err
	}
	switch update.Phase {
	case "preparing":
		if err := verifyOwned(a, update.OldOwned, false); err != nil {
			return err
		}
		return finishUpdate(a, state)
	case "complete":
		if err := verifyOwned(a, state.Owned, false); err != nil {
			return err
		}
		return finishUpdate(a, state)
	case "replacing":
	default:
		return errors.New("invalid update recovery phase")
	}
	if err := verifyUpdateFiles(a, update); err != nil {
		return err
	}
	for _, entry := range update.Rollback {
		if !safeOwnedPath(entry.Path) {
			return errors.New("invalid rollback path")
		}
		if entry.Kind == "absent" {
			continue
		}
		if entry.Kind != "file" || entry.Backup != ".update/"+bytesDigest([]byte(entry.Path))+".bin" {
			return errors.New("invalid rollback backup")
		}
		hash, err := fileDigest(filepath.Join(a.Dir, entry.Backup))
		if err != nil || hash != entry.SHA256 {
			return errors.New("rollback evidence damaged; no services stopped")
		}
	}
	units := []string{ServiceName}
	if update.NetworkChanged {
		units = append(units, NetworkServiceName)
	}
	if err := stopUnits(a, units...); err != nil {
		return err
	}
	if err := recoverUpdateFiles(a, update.Rollback); err != nil {
		return err
	}
	state.Version = update.OldVersion
	state.Owned = update.OldOwned
	state.Phase = "active"
	if err := writeState(a, state); err != nil {
		return err
	}
	if a.Root == "/" {
		if _, err := a.Run(a.Ctx, 10*time.Second, "systemctl", "daemon-reload"); err != nil {
			return err
		}
		if _, err := a.Run(a.Ctx, 20*time.Second, "systemctl", "start", ServiceName); err != nil {
			return err
		}
		if err := waitReady(a, state.Version); err != nil {
			return err
		}
	}
	update.Phase = "complete"
	if err := writeState(a, state); err != nil {
		return err
	}
	return finishUpdate(a, state)
}
func updateApp(a *App, source string, reboot bool) error {
	manifest, err := readPayload(source)
	if err != nil {
		return err
	}
	state, err := loadState(a)
	if err != nil {
		return err
	}
	if state.Update != nil {
		if err = rollbackUpdate(a, state); err != nil {
			return err
		}
		return errors.New("interrupted update recovered; rerun update explicitly")
	}
	if state.Phase != "active" || !state.Activated {
		return errors.New("only a fully active installation may be updated")
	}
	if err = checkCompatibleBackups(a, state); err != nil {
		return err
	}
	if err = verifyOwned(a, state.Owned, false); err != nil {
		return err
	}
	if err = checkDirectories(a, state); err != nil {
		return err
	}
	if err = checkPrivateTree(a, state); err != nil {
		return err
	}
	lock, err := networkLock(a, false)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = pendingNetwork(a); err != nil {
		return err
	}
	// A legacy DHCP hook will execute the newly installed binary on renewal.
	// Refuse a policy migration while the old managed connection is still live.
	cfg, err := readNetworkConfig(a)
	if err != nil {
		return err
	}
	if cfg.NetworkControl == networkControlNative {
		if err = checkNativeNetworkIdle(a); err != nil {
			return err
		}
	}
	if err = migrateBootBackup(a, state); err != nil {
		return err
	}
	if err = enablePersistentUnits(a); err != nil {
		return err
	}
	if exists(filepath.Join(a.Dir, ".update")) {
		return errors.New("unrecorded update directory; refusing overwrite")
	}
	update := &updateRecord{Phase: "preparing", OldVersion: state.Version, OldOwned: slices.Clone(state.Owned), NewOwned: []pathRecord{}, Rollback: []pathRecord{}}
	payloads := map[string]payloadFile{}
	for _, file := range manifest.Files {
		payloads[AppDir+"/"+file.Path] = file
	}
	generated := map[string][]byte{AppDir + "/dhcp-hook.sh": []byte(dhcpHook), unitPath(ServiceName): unitContent(ServiceName), unitPath(NetworkServiceName): unitContent(NetworkServiceName)}
	for name, data := range generated {
		mode := uint32(0644)
		if strings.HasSuffix(name, ".sh") {
			mode = 0755
		}
		payloads[name] = payloadFile{Path: name, SHA256: bytesDigest(data), Size: int64(len(data)), Mode: mode}
	}
	all := map[string]bool{}
	for name := range payloads {
		all[name] = true
	}
	for _, entry := range state.Owned {
		if entry.Kind == "file" {
			all[entry.Path] = true
		} else {
			update.NewOwned = append(update.NewOwned, entry)
		}
	}
	names := make([]string, 0, len(all))
	for name := range all {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		old, e := describePath(a, name)
		if e != nil {
			return e
		}
		if old.Kind != "absent" && old.Kind != "file" {
			return errors.New("invalid update target type")
		}
		next, keep := payloads[name]
		if keep {
			entry := old
			entry.Kind = "file"
			entry.Mode = next.Mode
			entry.UID, entry.GID = os.Geteuid(), os.Getegid()
			entry.SHA256 = next.SHA256
			entry.Backup = ""
			update.NewOwned = append(update.NewOwned, entry)
		}
		if keep && old.Kind == "file" && old.SHA256 == next.SHA256 && old.Mode == next.Mode {
			continue
		}
		if name == unitPath(NetworkServiceName) {
			update.NetworkChanged = true
		}
		if old.Kind == "file" {
			old.Backup = ".update/" + bytesDigest([]byte(name)) + ".bin"
		}
		update.Rollback = append(update.Rollback, old)
	}
	if update.NetworkChanged && !reboot {
		return errors.New("network unit changes require update --reboot; no service has been stopped")
	}
	state.Update = update
	state.Phase = "updating"
	if err = writeState(a, state); err != nil {
		return err
	}
	fail := func(cause error) error {
		if rollbackErr := rollbackUpdate(a, state); rollbackErr != nil {
			return errors.Join(cause, fmt.Errorf("recovery incomplete; evidence retained: %w", rollbackErr))
		}
		return fmt.Errorf("update failed; previous version preserved: %w", cause)
	}
	if err = os.Mkdir(filepath.Join(a.Dir, ".update"), 0700); err != nil {
		return fail(err)
	}
	for _, old := range update.Rollback {
		if old.Kind != "file" {
			continue
		}
		st, e := os.Stat(a.Path(old.Path))
		if e != nil {
			return fail(e)
		}
		file := payloadFile{SHA256: old.SHA256, Size: st.Size(), Mode: 0600}
		if err = copyPayloadFile(a.Path(old.Path), filepath.Join(a.Dir, old.Backup), file, true); err != nil {
			return fail(err)
		}
	}
	update.Phase = "replacing"
	if err = writeState(a, state); err != nil {
		return err
	}
	units := []string{ServiceName}
	if update.NetworkChanged {
		units = append(units, NetworkServiceName)
	}
	if err = stopUnits(a, units...); err != nil {
		return err
	}
	for _, old := range update.Rollback {
		file, keep := payloads[old.Path]
		if !keep {
			if err = os.Remove(a.Path(old.Path)); err != nil {
				return fail(err)
			}
			continue
		}
		if data, generatedFile := generated[old.Path]; generatedFile {
			err = replaceData(a.Path(old.Path), data, os.FileMode(file.Mode), os.Geteuid(), os.Getegid(), false)
		} else {
			err = copyPayloadFile(filepath.Join(source, file.Path), a.Path(old.Path), file, false)
		}
		if err != nil {
			return fail(err)
		}
	}
	state.Owned = update.NewOwned
	state.Version = manifest.Version
	if err = writeState(a, state); err != nil {
		return fail(err)
	}
	if a.Root == "/" {
		if _, err = a.Run(a.Ctx, 10*time.Second, "systemctl", "daemon-reload"); err != nil {
			return fail(err)
		}
		if _, err = a.Run(a.Ctx, 20*time.Second, "systemctl", "start", ServiceName); err != nil {
			return fail(err)
		}
		if err = waitReady(a, manifest.Version); err != nil {
			return fail(err)
		}
	}
	state.Phase = "active"
	update.Phase = "complete"
	if err = writeState(a, state); err != nil {
		return err
	}
	if err = finishUpdate(a, state); err != nil {
		return err
	}
	fmt.Println("Updated Action Control", manifest.Version, "without replacing the original restoration baseline.")
	return nil
}
