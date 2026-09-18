package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testNetworkControl(t *testing.T, a *App, control string) {
	t.Helper()
	store, err := loadConfig(filepath.Join(a.Dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Update(func(c *Config) error {
		c.NetworkControl = control
		c.AutoConnect = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if a.Config != nil {
		a.Config = store
	}
}

func snapshotNetworkFiles(t *testing.T, a *App) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	for _, name := range []string{hotspotPath, knownPath, "/data/misc/wifi/wpa_supplicant.conf", "/run/native/resolv.conf"} {
		data, err := os.ReadFile(a.Path(name))
		if errors.Is(err, os.ErrNotExist) {
			files[name] = nil
		} else if err != nil {
			t.Fatal(err)
		} else {
			files[name] = data
		}
	}
	return files
}

func assertNetworkFilesUnchanged(t *testing.T, a *App, before map[string][]byte) {
	t.Helper()
	after := snapshotNetworkFiles(t, a)
	for name, data := range before {
		if !bytes.Equal(data, after[name]) || (data == nil) != (after[name] == nil) {
			t.Errorf("native network file changed: %s", name)
		}
	}
	if _, err := os.Stat(filepath.Join(a.RunDir, "reboot-required.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("native mode marked the device for reboot: %v", err)
	}
}

func TestNetworkPolicyDefaultsAndLegacyConfig(t *testing.T) {
	a, _ := testApplication(t)
	if c := a.Config.Read(); c.NetworkControl != networkControlNative || c.AutoConnect {
		t.Fatalf("new installation takes over networking: %+v", c)
	}
	name := filepath.Join(a.Dir, "config.json")
	legacy := []byte(`{"schema":1,"theme_mode":"dark","auto_connect":true,"thumb_concurrent":2,"cam_w":1920,"cam_h":1080}`)
	putTestFile(t, name, legacy, 0600)
	store, err := loadConfig(name)
	if err != nil {
		t.Fatal(err)
	}
	c := store.Read()
	if c.NetworkControl != networkControlNative || c.AutoConnect || c.ThemeMode != "dark" || c.ThumbConcurrent != 2 || c.CamW != 1920 {
		t.Fatalf("legacy migration lost settings or implicitly enabled takeover: %+v", c)
	}
	if got, _ := os.ReadFile(name); !bytes.Equal(got, legacy) {
		t.Fatal("reading legacy settings rewrote the original file")
	}
	for _, raw := range []string{
		`{"network_control":"unknown"}`,
		`{"network_control":"native","auto_connect":true}`,
	} {
		if _, err := decodeConfig([]byte(raw)); err == nil {
			t.Fatalf("invalid network policy accepted: %s", raw)
		}
	}
	if c, err := decodeConfig([]byte(`{"network_control":"managed","auto_connect":true}`)); err != nil || !c.AutoConnect {
		t.Fatalf("explicit managed mode lost auto-connect: %+v %v", c, err)
	}
}

func TestNativePolicyBlocksNetworkAPIsBeforeAnySideEffects(t *testing.T) {
	a, handler := testApplication(t)
	before := snapshotNetworkFiles(t, a)
	configBefore, err := os.ReadFile(filepath.Join(a.Dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	requests := []struct{ method, path, body string }{
		{"GET", "wifi_scan", ""},
		{"POST", "wifi_connect", `{"ssid":"Example","password":"example-password","confirm":true}`},
		{"POST", "wifi_off", `{"confirm":true}`},
		{"POST", "hotspot_on", `{"confirm":true}`},
		{"POST", "hotspot_off", `{"confirm":true}`},
		{"POST", "hotspot_apply", `{"ssid":"Example","band":"2.4G","channel":6,"confirm":true}`},
		{"POST", "hotspot_config", `{"ssid":"Example","band":"2.4G","channel":6}`},
		{"POST", "known_forget", `{"ssid":"Example","confirm":true}`},
		{"POST", "dji_network", `{"action":"start","confirm":true}`},
		{"POST", "dji_network", `{"action":"stop","confirm":true}`},
		{"POST", "auto_connect", `{"enabled":true}`},
		{"POST", "config", `{"auto_connect":true}`},
	}
	for _, tc := range requests {
		t.Run(tc.path+tc.body, func(t *testing.T) {
			response := requestTest(a, handler, tc.method, "/api/"+tc.path, strings.NewReader(tc.body), nil)
			if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"code":"native_network_control"`) {
				t.Fatalf("request reached the old control path: %d %s", response.Code, response.Body.String())
			}
		})
	}
	assertNetworkFilesUnchanged(t, a, before)
	if after, _ := os.ReadFile(filepath.Join(a.Dir, "config.json")); !bytes.Equal(after, configBefore) {
		t.Fatal("rejected network request changed configuration")
	}
	for _, name := range []string{"network-request.json", "network-operation.json", "lease.json", "client.conf", "hostapd.conf"} {
		if _, err := os.Stat(filepath.Join(a.RunDir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("rejected network request created %s: %v", name, err)
		}
	}
	for _, path := range []string{"/api/health", "/api/hotspot_config", "/api/known_list", "/api/list?dir=/sd", "/api/download?file=/sd/%E7%94%A8%E6%88%B7%E5%AA%92%E4%BD%93.bin"} {
		response := requestTest(a, handler, "GET", path, nil, nil)
		if response.Code != http.StatusOK {
			t.Errorf("native mode disabled a read-only feature: %s %d %s", path, response.Code, response.Body.String())
		}
	}
}

func TestNativePolicyBlocksWorkersAndSharedWrites(t *testing.T) {
	a, _ := testApplication(t)
	before := snapshotNetworkFiles(t, a)
	h, data, err := readHotspot(a)
	if err != nil {
		t.Fatal(err)
	}
	operations := map[string]func() error{
		"prepare":        func() error { return prepareWireless(a) },
		"connect":        func() error { return connectWireless(a, knownNetwork{SSID: "Example"}) },
		"hotspot":        func() error { return startHotspot(a) },
		"hotspot_config": func() error { return setHotspot(a, h, data) },
		"known":          func() error { return saveKnown(a, []knownNetwork{{SSID: "Example"}}) },
		"dhcp_bound":     func() error { return RunDHCP(a, []string{"bound"}) },
		"dhcp_renew":     func() error { return RunDHCP(a, []string{"renew"}) },
		"dhcp_deconfig":  func() error { return RunDHCP(a, []string{"deconfig"}) },
		"restore": func() error {
			return restoreNetwork(a, &networkRecoveryPlan{Mode: "native", HotspotData: []byte("replacement")})
		},
	}
	for name, run := range operations {
		t.Run(name, func(t *testing.T) {
			if err := run(); !errors.Is(err, errNativeNetworkControl) {
				t.Fatalf("worker bypassed native policy: %v", err)
			}
		})
	}
	if err := RunNetwork(a); err != nil {
		t.Fatalf("native boot worker should be a no-op: %v", err)
	}
	assertNetworkFilesUnchanged(t, a, before)
}

func TestNativeWorkerDiscardsOldRequestsWithoutReplayingCredentials(t *testing.T) {
	for _, action := range []string{"connect", "hotspot", "off", "native_start", "native_stop"} {
		t.Run(action, func(t *testing.T) {
			a, _ := testApplication(t)
			before := snapshotNetworkFiles(t, a)
			password := "must-not-appear-in-status"
			request, _ := json.Marshal(networkRequest{ID: "old-request", Action: action, SSID: "Example", Password: &password})
			name := filepath.Join(a.RunDir, "network-request.json")
			putTestFile(t, name, request, 0600)
			if err := RunNetwork(a); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(name); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("old request could be replayed later", err)
			}
			status, err := os.ReadFile(filepath.Join(a.RunDir, "network-operation.json"))
			var op networkOperation
			if err != nil || json.Unmarshal(status, &op) != nil || op.ID != "old-request" || op.Status != "failed" || bytes.Contains(status, []byte(password)) {
				t.Fatalf("request cancellation lost status or exposed credentials: %s %v", status, err)
			}
			assertNetworkFilesUnchanged(t, a, before)
		})
	}
}

func TestNetworkControlSwitchOnlyChangesPolicy(t *testing.T) {
	a, handler := testApplication(t)
	before := snapshotNetworkFiles(t, a)
	for _, body := range []string{`{"network_control":"managed","auto_connect":true}`, `{"network_control":"native"}`} {
		response := requestTest(a, handler, "POST", "/api/config", strings.NewReader(body), nil)
		if response.Code != http.StatusOK {
			t.Fatalf("idle policy change failed: %d %s", response.Code, response.Body.String())
		}
	}
	c, err := readNetworkConfig(a)
	if err != nil || c.NetworkControl != networkControlNative || c.AutoConnect {
		t.Fatalf("return to native left takeover enabled: %+v %v", c, err)
	}
	assertNetworkFilesUnchanged(t, a, before)
	if _, err := os.Stat(filepath.Join(a.RunDir, "network-request.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("selecting policy scheduled a wireless reconfiguration", err)
	}
}

func TestPolicyChangeDoesNotAbandonLiveOrQueuedNetwork(t *testing.T) {
	for _, running := range []bool{true, false} {
		t.Run(map[bool]string{true: "live-cgroup", false: "queued"}[running], func(t *testing.T) {
			a, handler := testApplication(t)
			testNetworkControl(t, a, networkControlManaged)
			if running {
				// No stat, cmdline, or process name is present: cgroup ownership suffices.
				putTestFile(t, a.Path("/proc/65001/cgroup"), []byte("0::/system.slice/action-control-network.service/child\n"), 0600)
			} else {
				putTestFile(t, filepath.Join(a.RunDir, "network-request.json"), []byte(`{"id":"pending","action":"connect"}`), 0600)
			}
			before, _ := os.ReadFile(filepath.Join(a.Dir, "config.json"))
			response := requestTest(a, handler, "POST", "/api/config", strings.NewReader(`{"network_control":"native"}`), nil)
			if response.Code < 400 {
				t.Fatalf("policy abandoned an active connection: %s", response.Body.String())
			}
			if after, _ := os.ReadFile(filepath.Join(a.Dir, "config.json")); !bytes.Equal(before, after) {
				t.Fatal("rejected policy change changed the file")
			}
		})
	}
}

func TestNetworkWorkerReadsCurrentPolicyInsteadOfCachedConfig(t *testing.T) {
	a, _ := testApplication(t)
	testNetworkControl(t, a, networkControlManaged)
	if a.Config.Read().NetworkControl != networkControlManaged {
		t.Fatal("test cache is not managed")
	}
	current, _ := json.Marshal(defaultConfig())
	putTestFile(t, filepath.Join(a.Dir, "config.json"), current, 0600)
	if err := requireManagedNetwork(a); !errors.Is(err, errNativeNetworkControl) {
		t.Fatal("stale cached configuration permitted takeover", err)
	}
}

func TestLegacyUpdateRefusesToBreakAnExistingManagedConnection(t *testing.T) {
	a, _, _ := testInstallation(t)
	legacy := []byte(`{"schema":1,"auto_connect":true}`)
	putTestFile(t, filepath.Join(a.Dir, "config.json"), legacy, 0600)
	putTestFile(t, a.Path("/proc/65002/cgroup"), []byte("1:name=systemd:/system.slice/action-control-network.service\n"), 0600)
	before, err := os.ReadFile(filepath.Join(a.Dir, "action-control"))
	if err != nil {
		t.Fatal(err)
	}
	if err = updateApp(a, testPayload(t, 1), false); !errors.Is(err, errManagedNetworkBusy) {
		t.Fatalf("legacy update did not protect its DHCP connection: %v", err)
	}
	if after, _ := os.ReadFile(filepath.Join(a.Dir, "action-control")); !bytes.Equal(after, before) {
		t.Fatal("refused update replaced the executable")
	}
	if after, _ := os.ReadFile(filepath.Join(a.Dir, "config.json")); !bytes.Equal(after, legacy) {
		t.Fatal("refused update changed legacy configuration")
	}
	state, err := loadState(a)
	if err != nil || state.Update != nil || state.Phase != "active" {
		t.Fatalf("refused update left an interrupted transaction: %+v %v", state, err)
	}
}

func TestImportCannotWriteSharedNetworksInNativeMode(t *testing.T) {
	a, _, _ := testInstallation(t)
	before := snapshotNetworkFiles(t, a)
	file := filepath.Join(t.TempDir(), "known.json")
	putTestFile(t, file, []byte(`[{"ssid":"Example","password":"example-password"}]`), 0600)
	if err := importConfig(a, "", file); !errors.Is(err, errNativeNetworkControl) {
		t.Fatalf("native import bypassed network policy: %v", err)
	}
	assertNetworkFilesUnchanged(t, a, before)
}
