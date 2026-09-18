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

const nativeServicesFixture = `Id=dji_camera3.service
LoadState=loaded
ActiveState=active
SubState=running
MainPID=1250

Id=dji_media.service
LoadState=loaded
ActiveState=active
SubState=running
MainPID=1291

Id=gui.service
LoadState=loaded
ActiveState=active
SubState=running
MainPID=15303

Id=sub_gui.service
LoadState=loaded
ActiveState=active
SubState=running
MainPID=1359

Id=dji_sw_uav.service
LoadState=loaded
ActiveState=active
SubState=running
MainPID=1065
`

func TestNativeServiceLivenessNeverInventsRecordingOrPreview(t *testing.T) {
	status := parseNativeCameraStatus(nativeServicesFixture)
	if status.ServiceState != "running" || len(status.Services) != 5 || status.Services[0].MainPID != 1250 {
		t.Fatalf("live native services were not recognized: %+v", status)
	}
	data, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"recording":null`, `"previewing":null`, `"control_available":false`, `"preview_available":false`, `"source":"systemd"`} {
		if !bytes.Contains(data, []byte(field)) {
			t.Fatalf("service liveness was presented as camera protocol support: %s", data)
		}
	}
}

func TestNativeServiceStatusKeepsIncompleteEvidenceUnknown(t *testing.T) {
	cases := []struct {
		name, output, state, cameraState string
	}{
		{"empty", "", "unknown", "unknown"},
		{"truncated", strings.TrimSuffix(nativeServicesFixture, "MainPID=1065\n"), "unknown", "running"},
		{"invalid_pid", strings.Replace(nativeServicesFixture, "MainPID=1250", "MainPID=invalid", 1), "unknown", "unknown"},
		{"active_without_pid", strings.Replace(nativeServicesFixture, "MainPID=1250", "MainPID=0", 1), "unknown", "unknown"},
		{"missing_unit", strings.Replace(nativeServicesFixture, "LoadState=loaded", "LoadState=not-found", 1), "unknown", "unknown"},
		{"duplicate_unit", nativeServicesFixture + "\n" + strings.Split(nativeServicesFixture, "\n\n")[0], "unknown", "unknown"},
		{"camera_stopped", strings.Replace(nativeServicesFixture, "ActiveState=active\nSubState=running\nMainPID=1250", "ActiveState=inactive\nSubState=dead\nMainPID=0", 1), "degraded", "stopped"},
		{"camera_failed", strings.Replace(nativeServicesFixture, "ActiveState=active\nSubState=running\nMainPID=1250", "ActiveState=failed\nSubState=failed\nMainPID=0", 1), "degraded", "failed"},
		{"camera_starting", strings.Replace(nativeServicesFixture, "ActiveState=active\nSubState=running\nMainPID=1250", "ActiveState=activating\nSubState=start\nMainPID=1250", 1), "transitioning", "transitioning"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status := parseNativeCameraStatus(tc.output)
			if status.ServiceState != tc.state || status.Services[0].State != tc.cameraState {
				t.Fatalf("unexpected service evidence: %+v", status)
			}
			if status.Recording != nil || status.Previewing != nil {
				t.Fatal("unknown capture state was replaced by a guess")
			}
		})
	}
}

func TestCameraTakeoverRejectsOldRequestsBeforeSideEffects(t *testing.T) {
	a, handler := testApplication(t)
	before := snapshotNetworkFiles(t, a)
	configBefore, err := os.ReadFile(filepath.Join(a.Dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"camera_start", "dji_kill_all"} {
		for _, body := range []string{`{"confirm":true}`, `{"confirm":true,"takeover_native":false}`} {
			response := requestTest(a, handler, "POST", "/api/"+path, strings.NewReader(body), nil)
			if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"code":"native_camera_protected"`) {
				t.Fatalf("legacy request reached hardware handling: %s %d %s", path, response.Code, response.Body.String())
			}
		}
		response := requestTest(a, handler, "POST", "/api/"+path, strings.NewReader(`{"confirm":false,"takeover_native":true}`), nil)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("missing confirmation was accepted: %s %d", path, response.Code)
		}
		response = requestTest(a, handler, "POST", "/api/"+path, strings.NewReader(`{"confirm":true,"takeover_native":true}`), nil)
		if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), `"code":"device_unavailable"`) {
			t.Fatalf("explicit takeover bypassed device validation: %s %d %s", path, response.Code, response.Body.String())
		}
	}
	if _, err := stopNativeServices(a, false); !errors.Is(err, errNativeCameraProtected) {
		t.Fatalf("direct service stop bypassed takeover protection: %v", err)
	}
	assertNetworkFilesUnchanged(t, a, before)
	if after, _ := os.ReadFile(filepath.Join(a.Dir, "config.json")); !bytes.Equal(after, configBefore) {
		t.Fatal("rejected camera request changed configuration")
	}
	if _, err := os.Stat(filepath.Join(a.RunDir, "camera-status.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected camera request entered display handling: %v", err)
	}
}

func TestNativeAndIndependentStatusRemainSeparate(t *testing.T) {
	a, handler := testApplication(t)
	response := requestTest(a, handler, "GET", "/api/camera_status", nil, nil)
	var independent CameraStatus
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &independent) != nil || independent.Source != "action-control" || independent.Scope != "independent_capture" || independent.Running {
		t.Fatalf("independent status has ambiguous ownership: %s", response.Body.String())
	}
	response = requestTest(a, handler, "GET", "/api/native_camera_status", nil, nil)
	var native NativeCameraStatus
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &native) != nil || native.ServiceState != "unavailable" || native.Recording != nil || native.Previewing != nil {
		t.Fatalf("local mode claimed access to a native camera: %s", response.Body.String())
	}
}
