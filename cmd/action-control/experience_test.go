package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type mediaPageResult struct {
	Items  []MediaItem `json:"items"`
	Count  int         `json:"count"`
	Cursor string      `json:"next_cursor"`
}

func readMediaPage(t *testing.T, a *App, handler http.Handler, query url.Values) mediaPageResult {
	t.Helper()
	response := requestTest(a, handler, "GET", "/api/media_list?"+query.Encode(), nil, nil)
	if response.Code != 200 {
		t.Fatalf("media list: %d %s", response.Code, response.Body.String())
	}
	var page mediaPageResult
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	return page
}

func TestMediaPaginationStableSnapshotAndRefresh(t *testing.T) {
	a, handler := testApplication(t)
	for i := 0; i < 123; i++ {
		name := a.Path(fmt.Sprintf("/mnt/media_rw/sd/DCIM/%d/照片 %03d.JPG", i%3, i))
		putTestFile(t, name, bytes.Repeat([]byte("x"), i+1), 0600)
		when := time.Unix(1000+int64(i), 0)
		if err := os.Chtimes(name, when, when); err != nil {
			t.Fatal(err)
		}
	}
	putTestFile(t, a.Path("/mnt/media_rw/sd/DCIM/照片视频.MP4"), []byte("video"), 0600)
	query := url.Values{"dir": {"/sd"}, "type": {"image"}, "search": {"照片"}, "sort": {"newest"}, "limit": {"17"}}
	seen := map[string]bool{}
	previous := int64(1 << 62)
	pageCount := 0
	for {
		page := readMediaPage(t, a, handler, query)
		if page.Count != 123 || len(page.Items) > 17 {
			t.Fatalf("unexpected page size/count: %+v", page)
		}
		for _, item := range page.Items {
			if seen[item.Path] || item.Mtime > previous || item.Type != "image" {
				t.Fatalf("duplicate, unordered or unfiltered item: %+v", item)
			}
			seen[item.Path], previous = true, item.Mtime
		}
		pageCount++
		if pageCount == 1 {
			putTestFile(t, a.Path("/mnt/media_rw/sd/DCIM/新增照片.JPG"), []byte("new"), 0600)
		}
		if page.Cursor == "" {
			break
		}
		query.Set("cursor", page.Cursor)
	}
	if len(seen) != 123 || pageCount != 8 {
		t.Fatalf("missing media: %d items in %d pages", len(seen), pageCount)
	}
	query.Del("cursor")
	query.Set("refresh", "true")
	page := readMediaPage(t, a, handler, query)
	if page.Count != 124 || page.Items[0].Name != "新增照片.JPG" {
		t.Fatalf("refresh did not see external camera changes: %+v", page)
	}
}

func TestMediaCursorInvalidationAndInputValidation(t *testing.T) {
	a, handler := testApplication(t)
	for _, name := range []string{"A.jpg", "B.jpg", "C.jpg"} {
		putTestFile(t, a.Path("/blackbox/"+name), []byte("photo"), 0600)
	}
	query := url.Values{"dir": {"/"}, "limit": {"1"}, "sort": {"name"}}
	first := readMediaPage(t, a, handler, query)
	if first.Cursor == "" || first.Items[0].Name != "A.jpg" {
		t.Fatal("missing cursor or wrong sort")
	}
	notDir := requestTest(a, handler, "GET", "/api/media_list?dir=/A.jpg", nil, nil)
	if notDir.Code != http.StatusBadRequest {
		t.Fatalf("media listing accepted a regular file: %d", notDir.Code)
	}
	for _, suffix := range []string{"limit=0", "limit=201", "limit=not-a-number", "cursor=broken", "type=audio", "sort=random"} {
		response := requestTest(a, handler, "GET", "/api/media_list?dir=/sd&"+suffix, nil, nil)
		if response.Code != 400 {
			t.Fatalf("invalid pagination accepted: %s: %d", suffix, response.Code)
		}
	}
	query.Set("cursor", first.Cursor)
	query.Set("sort", "oldest")
	response := requestTest(a, handler, "GET", "/api/media_list?"+query.Encode(), nil, nil)
	if response.Code != 400 {
		t.Fatalf("cursor reused with different filter: %d", response.Code)
	}
	query.Set("sort", "name")
	response = requestTest(a, handler, "DELETE", "/api/delete", strings.NewReader(`{"path":"/A.jpg"}`), nil)
	if response.Code != 200 {
		t.Fatalf("delete: %s", response.Body.String())
	}
	response = requestTest(a, handler, "GET", "/api/media_list?"+query.Encode(), nil, nil)
	if response.Code != http.StatusGone {
		t.Fatalf("obsolete media cursor accepted: %d", response.Code)
	}
	query.Del("cursor")
	page := readMediaPage(t, a, handler, query)
	if page.Count != 2 || page.Items[0].Name != "B.jpg" {
		t.Fatalf("mutation left stale media: %+v", page)
	}
}

func TestCameraSuccessfulPresetSurvivesRejectedStartAndFirmwareChange(t *testing.T) {
	a, handler := testApplication(t)
	putTestFile(t, a.Path("/build.prop"), []byte("ro.build.version=first\n"), 0600)
	if err := saveCameraPreset(a, defaultConfig()); err != nil {
		t.Fatal(err)
	}
	preset, err := readCameraPreset(a)
	if err != nil || preset == nil || !preset.FirmwareMatch || preset.Config.Width != 1280 {
		t.Fatalf("successful preset: %+v %v", preset, err)
	}
	response := requestTest(a, handler, "POST", "/api/camera_start", strings.NewReader(`{"confirm":true,"takeover_native":true,"width":3840,"height":2160,"fps":240,"ext_port":8554,"quality":1,"bitrate":6}`), nil)
	if response.Code < 400 {
		t.Fatal("local test must never start hardware")
	}
	preset, err = readCameraPreset(a)
	if err != nil || preset.Config.Width != 1280 {
		t.Fatal("failed start overwrote the working preset", err)
	}
	putTestFile(t, a.Path("/build.prop"), []byte("ro.build.version=second\n"), 0600)
	preset, err = readCameraPreset(a)
	if err != nil || preset.FirmwareMatch {
		t.Fatal("preset claimed validation across firmware changes", err)
	}
	response = requestTest(a, handler, "GET", "/api/camera_presets", nil, nil)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"firmware_match":false`) {
		t.Fatalf("preset API: %s", response.Body.String())
	}
	putTestFile(t, filepath.Join(a.Dir, "camera-last-success.json"), []byte(`{"config":{"cam_w":-1}}`), 0600)
	if _, err := readCameraPreset(a); err == nil {
		t.Fatal("corrupt preset was accepted")
	}
}

func TestNetworkRecoveryPreservesPreviousCredentialsAndHidesThemFromStatus(t *testing.T) {
	networks := []knownNetwork{{SSID: "Original", Password: "original-password"}}
	plan, err := recoveryForStatus(wifiStatus{Mode: "client", State: "connected", SSID: "Original"}, networks, nil)
	if err != nil || plan.Mode != "client" || plan.Client == nil {
		t.Fatalf("recovery plan: %+v %v", plan, err)
	}
	networks[0].Password = "replacement-password"
	if plan.Client.Password != "original-password" {
		t.Fatal("recovery credentials were changed by the new request")
	}
	public, err := json.Marshal(networkOperation{Status: "failed", RecoveryMode: plan.Mode, RecoverySSID: plan.SSID})
	if err != nil || bytes.Contains(public, []byte("password")) {
		t.Fatalf("credentials leaked into operation status: %s", public)
	}
	hotspot := []byte("ssid=Original AP\npasswd=original-secret\nband=0\nchannel=6\ncountry=CN\n")
	plan, err = recoveryForStatus(wifiStatus{Mode: "hotspot", SSID: "Original AP"}, nil, hotspot)
	if err != nil || plan.Mode != "hotspot" {
		t.Fatalf("hotspot recovery plan: %+v %v", plan, err)
	}
	hotspot[0] = 'X'
	if !bytes.HasPrefix(plan.HotspotData, []byte("ssid=")) {
		t.Fatal("hotspot backup aliased the editable settings")
	}
	plan, err = recoveryForStatus(wifiStatus{Mode: "client", State: "connected", SSID: "Unknown"}, nil, nil)
	if err != nil || plan.Mode != "unavailable" {
		t.Fatal("invented credentials for an unknown connection")
	}
}

func TestNetworkRecoveryKeepsRestoredUnitAliveWithoutReportingSwitchSuccess(t *testing.T) {
	original := errors.New("new network rejected the password")
	var states []networkOperation
	cleanups, restores := 0, 0
	err := finishNetworkOperation(networkOperation{ID: "switch", RecoveryMode: "client"}, original,
		func() error { cleanups++; return nil },
		func() error { restores++; return nil },
		func(op networkOperation) error { states = append(states, op); return nil })
	if err != nil || cleanups != 1 || restores != 1 {
		t.Fatalf("restored connection would be killed: %v cleanup=%d restore=%d", err, cleanups, restores)
	}
	if len(states) != 2 || states[0].RecoveryStatus != "restoring" || states[1].Status != "failed" || states[1].RecoveryStatus != "restored" || states[1].Error != original.Error() {
		t.Fatalf("switch/recovery status conflated: %+v", states)
	}
}

func TestFailedRecoveryCleansUpAndCannotClaimRestoration(t *testing.T) {
	cleanups := 0
	var final networkOperation
	err := finishNetworkOperation(networkOperation{RecoveryMode: "hotspot"}, errors.New("switch failed"),
		func() error { cleanups++; return nil },
		func() error { return errors.New("old hotspot unavailable") },
		func(op networkOperation) error { final = op; return nil })
	if err == nil || cleanups != 2 || final.Status != "failed" || final.RecoveryStatus != "failed" {
		t.Fatalf("failed recovery left live children or wrong status: %v %+v %d", err, final, cleanups)
	}
	restoreCalled := false
	err = finishNetworkOperation(networkOperation{}, errors.New("switch failed"), func() error { return errors.New("foreign process boundary") }, func() error { restoreCalled = true; return nil }, func(networkOperation) error { return nil })
	if err == nil || restoreCalled {
		t.Fatal("recovery continued after unsafe cleanup")
	}
}

func TestWorkingNetworkSurvivesStatusPersistenceFailure(t *testing.T) {
	for _, cause := range []error{nil, errors.New("switch failed")} {
		err := finishNetworkOperation(networkOperation{}, cause, func() error { return nil }, func() error { return nil }, func(networkOperation) error { return errors.New("read-only status file") })
		if err != nil {
			t.Fatal("status write failure would stop a working network", err)
		}
	}
}

func TestHotspotApplyRequiresConfirmationAndHardware(t *testing.T) {
	a, handler := testApplication(t)
	for _, body := range []string{`{"ssid":"Camera","band":"2.4G","channel":6}`, `{"ssid":"Camera","band":"2.4G","channel":6,"confirm":true}`} {
		response := requestTest(a, handler, "POST", "/api/hotspot_apply", strings.NewReader(body), nil)
		if response.Code < 400 {
			t.Fatal("local request must not mutate hardware")
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/api/health", nil))
	if response.Code != 200 {
		t.Fatal("health API failed")
	}
}

func TestHealthReportsListeningPortBehindForwarding(t *testing.T) {
	_, handler := testApplication(t)
	r := httptest.NewRequest("GET", "http://forwarded-host:18080/api/health", nil)
	r = r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, &net.TCPAddr{Port: 8081}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, r)
	var health struct {
		Port string `json:"http_port"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || health.Port != "8081" {
		t.Fatalf("reconnect guide would use the forwarded port: %s", response.Body.String())
	}
}
