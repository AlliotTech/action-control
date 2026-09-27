package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// The rear-screen GUI loads display plugins from this config; the bundled
// library wraps the stock image loader and is registered by repointing that
// entry. The library ships as an ordinary owned payload file under bin/.
const (
	guiDisplayConfig = "/etc/disp0_plugins_config.json"
	guiPluginCreator = "gui_image_loader_create"
	guiPluginLibrary = AppDir + "/bin/libaction_control_gui.so"
	guiPluginService = "gui.service"
	guiPluginSidecar = ".restore/gui-plugin.json"
)

// guiPluginRecord persists in .restore/ (excluded from the private-tree audit)
// so uninstall can restore the pre-activation config and updates can skip a
// redundant gui.service restart when the library is unchanged.
type guiPluginRecord struct {
	OriginalConfig string `json:"original_config"`
	AppliedSHA     string `json:"applied_sha"`
}

func guiSidecarPath(a *App) string {
	return filepath.Join(a.Dir, filepath.FromSlash(guiPluginSidecar))
}

func loadGuiRecord(a *App) (*guiPluginRecord, error) {
	data, err := os.ReadFile(guiSidecarPath(a))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var record guiPluginRecord
	if err = json.Unmarshal(data, &record); err != nil {
		return nil, err
	}
	return &record, nil
}

func writeGuiRecord(a *App, record *guiPluginRecord) error {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	return replaceData(guiSidecarPath(a), append(data, '\n'), 0600, os.Geteuid(), os.Getegid(), false)
}

// guiImageEntry returns the plugins_configs entry that registers the image
// loader, or ok=false when this firmware's display config lacks it.
func guiImageEntry(root map[string]any) (map[string]any, bool) {
	list, ok := root["plugins_configs"].([]any)
	if !ok {
		return nil, false
	}
	for _, item := range list {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if name, _ := entry["creator_func_name"].(string); name == guiPluginCreator {
			return entry, true
		}
	}
	return nil, false
}

// writeGuiConfig replaces the display config in place, preserving owner and mode
// so the display service still reads it after the /etc overlay is remounted.
func writeGuiConfig(a *App, config string, data []byte) error {
	mode := os.FileMode(0644)
	uid, gid := os.Geteuid(), os.Getegid()
	if st, err := os.Lstat(config); err == nil {
		if !st.Mode().IsRegular() {
			return errors.New("display config is not a regular file")
		}
		mode = st.Mode()
		if native, ok := st.Sys().(*syscall.Stat_t); ok {
			uid, gid = int(native.Uid), int(native.Gid)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return replaceData(config, data, mode, uid, gid, false)
}

// applyGuiPlugin registers the bundled library by repointing the image-loader
// entry and restarts gui.service. Best-effort and idempotent: it is a no-op on
// firmware whose display config lacks the entry (the library also self-gates on
// the GUI binary's SHA and forwards to the stock loader on a mismatch), and
// gui.service restarts only when the library or the registration actually
// changes. Callers treat failure as a warning, not an install failure.
func applyGuiPlugin(a *App) error {
	config, err := installPath(a, guiDisplayConfig, true)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(config)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var root map[string]any
	if err = json.Unmarshal(data, &root); err != nil {
		return err
	}
	entry, ok := guiImageEntry(root)
	if !ok {
		return nil
	}
	library, err := installPath(a, guiPluginLibrary, false)
	if err != nil {
		return err
	}
	soSHA, err := fileDigest(library)
	if err != nil {
		return err
	}
	record, err := loadGuiRecord(a)
	if err != nil {
		return err
	}
	firstApply := record == nil
	if firstApply {
		record = &guiPluginRecord{OriginalConfig: string(data)}
	}
	restart := firstApply || record.AppliedSHA != soSHA
	if current, _ := entry["plugin"].(string); current != guiPluginLibrary {
		entry["plugin"] = guiPluginLibrary
		edited, e := json.MarshalIndent(root, "", "    ")
		if e != nil {
			return e
		}
		if err = writeGuiConfig(a, config, append(edited, '\n')); err != nil {
			return err
		}
		restart = true
	}
	record.AppliedSHA = soSHA
	if err = writeGuiRecord(a, record); err != nil {
		return err
	}
	if restart && a.Root == "/" {
		if _, err = a.Run(a.Ctx, 30*time.Second, "systemctl", "restart", guiPluginService); err != nil {
			return err
		}
	}
	return nil
}

// restoreGuiPlugin reverts the display config to the state captured before the
// plugin was first activated and restarts gui.service. No-op when the plugin
// was never applied.
func restoreGuiPlugin(a *App) error {
	record, err := loadGuiRecord(a)
	if err != nil || record == nil || record.OriginalConfig == "" {
		return err
	}
	config, err := installPath(a, guiDisplayConfig, true)
	if err != nil {
		return err
	}
	if err = writeGuiConfig(a, config, []byte(record.OriginalConfig)); err != nil {
		return err
	}
	if a.Root == "/" {
		if _, err = a.Run(a.Ctx, 30*time.Second, "systemctl", "restart", guiPluginService); err != nil {
			return err
		}
	}
	return nil
}
