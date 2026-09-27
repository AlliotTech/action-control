package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const guiFactoryConfig = `{"plugins_configs":[` +
	`{"creator_func_name":"gui_lpc_proxy_create","plugin":"/usr/lib/libgui_lpc_proxy.so","parameter":["a=1;"]},` +
	`{"creator_func_name":"gui_image_loader_create","plugin":"/usr/lib/libgui_image_loader.so","parameter":["image_loader_id=1;"]}]}`

func guiEntryPlugin(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("config is not valid JSON: %v", err)
	}
	entry, ok := guiImageEntry(root)
	if !ok {
		t.Fatal("image-loader entry missing")
	}
	return entry["plugin"].(string)
}

func setupGuiPlugin(t *testing.T) (*App, string) {
	t.Helper()
	a, _ := testApplication(t)
	for _, dir := range []string{AppDir + "/.restore", AppDir + "/bin"} {
		if err := os.MkdirAll(a.Path(dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(a.Path(guiPluginLibrary), []byte("v1"), 0755); err != nil {
		t.Fatal(err)
	}
	config := a.Path(guiDisplayConfig)
	if err := os.MkdirAll(filepath.Dir(config), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte(guiFactoryConfig), 0644); err != nil {
		t.Fatal(err)
	}
	return a, config
}

func TestApplyGuiPluginRepointsAndRestores(t *testing.T) {
	a, config := setupGuiPlugin(t)

	if err := applyGuiPlugin(a); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := guiEntryPlugin(t, config); got != guiPluginLibrary {
		t.Fatalf("plugin not repointed: %s", got)
	}
	if data, _ := os.ReadFile(config); !strings.Contains(string(data), "ac_mode=menu;") || !strings.Contains(string(data), "ac_state=/run/action-control-ui-persist;") || !strings.Contains(string(data), "image_loader_id=1;") {
		t.Fatalf("activation params not set: %s", data)
	}
	if data, _ := os.ReadFile(config); !strings.Contains(string(data), "gui_lpc_proxy_create") {
		t.Fatal("unrelated plugin entry was dropped")
	}
	record, err := loadGuiRecord(a)
	if err != nil || record == nil || !strings.Contains(record.OriginalConfig, "/usr/lib/libgui_image_loader.so") {
		t.Fatalf("original config not captured: %+v %v", record, err)
	}

	before, _ := os.ReadFile(config)
	if err := applyGuiPlugin(a); err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	if after, _ := os.ReadFile(config); string(after) != string(before) {
		t.Fatal("idempotent apply rewrote the config")
	}

	if err := os.WriteFile(a.Path(guiPluginLibrary), []byte("v2-different"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := applyGuiPlugin(a); err != nil {
		t.Fatalf("apply after library change: %v", err)
	}
	if updated, _ := loadGuiRecord(a); updated.AppliedSHA == record.AppliedSHA {
		t.Fatal("applied SHA not updated after library change")
	}

	if err := restoreGuiPlugin(a); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := guiEntryPlugin(t, config); got != "/usr/lib/libgui_image_loader.so" {
		t.Fatalf("config not restored to original: %s", got)
	}
	if data, _ := os.ReadFile(config); strings.Contains(string(data), "ac_mode=menu;") {
		t.Fatal("activation params not removed on restore")
	}
}

func TestApplyGuiPluginSkipsUnsupportedFirmware(t *testing.T) {
	a, config := setupGuiPlugin(t)
	noEntry := `{"plugins_configs":[{"creator_func_name":"other_create","plugin":"/usr/lib/other.so","parameter":[]}]}`
	if err := os.WriteFile(config, []byte(noEntry), 0644); err != nil {
		t.Fatal(err)
	}
	if err := applyGuiPlugin(a); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if data, _ := os.ReadFile(config); string(data) != noEntry {
		t.Fatal("config without image-loader entry was modified")
	}
	if rec, _ := loadGuiRecord(a); rec != nil {
		t.Fatal("sidecar created for unsupported firmware")
	}

	if err := os.Remove(config); err != nil {
		t.Fatal(err)
	}
	if err := applyGuiPlugin(a); err != nil {
		t.Fatalf("apply with absent config: %v", err)
	}
}
