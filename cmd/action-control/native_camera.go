package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

var nativeCameraUnits = []string{
	"dji_camera3.service",
	"dji_media.service",
	"gui.service",
	"sub_gui.service",
	"dji_sw_uav.service",
}

type NativeCameraService struct {
	Unit        string `json:"unit"`
	State       string `json:"state"`
	LoadState   string `json:"load_state"`
	ActiveState string `json:"active_state"`
	SubState    string `json:"sub_state"`
	MainPID     int    `json:"main_pid"`
}

// Service liveness does not establish recording or preview state. Recording is
// filled only by the native Binder reader. Independent preview subscription is
// unverified; the passive Mimo mirror reports its own separate transport status.
type NativeCameraStatus struct {
	Source            string                  `json:"source"`
	ServiceSource     string                  `json:"service_source"`
	ServiceState      string                  `json:"service_state"`
	NativeState       NativeCameraObservation `json:"native_state"`
	Recording         *bool                   `json:"recording"`
	Previewing        *bool                   `json:"previewing"`
	ControlAvailable  bool                    `json:"control_available"`
	RecordingControls NativeRecordingControls `json:"recording_controls"`
	CaptureControls   NativeCaptureControls   `json:"capture_controls"`
	ModeControls      NativeModeControls      `json:"mode_controls"`
	PreviewAvailable  bool                    `json:"preview_available"`
	Services          []NativeCameraService   `json:"services"`
	ObservedAt        string                  `json:"observed_at"`
	Reason            string                  `json:"reason,omitempty"`
}

func emptyNativeCameraStatus() NativeCameraStatus {
	return NativeCameraStatus{
		Source:        "systemd",
		ServiceSource: "systemd",
		ServiceState:  "unknown",
		NativeState:   nativeObservationError("unavailable", "尚未读取原生拍摄状态。"),
		Services:      []NativeCameraService{},
		ObservedAt:    time.Now().UTC().Format(time.RFC3339),
	}
}

func parseNativeCameraStatus(output string) NativeCameraStatus {
	status := emptyNativeCameraStatus()
	units := map[string]map[string]string{}
	duplicates := map[string]bool{}
	for _, block := range strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n\n") {
		fields := map[string]string{}
		invalid := false
		for _, line := range strings.Split(block, "\n") {
			key, value, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			if _, exists := fields[key]; exists {
				invalid = true
			}
			fields[key] = value
		}
		unit := fields["Id"]
		if _, exists := units[unit]; exists || invalid {
			duplicates[unit] = true
		}
		units[unit] = fields
	}

	counts := map[string]int{}
	for _, unit := range nativeCameraUnits {
		service := NativeCameraService{Unit: unit, State: "unknown", LoadState: "unknown", ActiveState: "unknown", SubState: "unknown"}
		if fields, ok := units[unit]; ok && !duplicates[unit] {
			for name, dest := range map[string]*string{"LoadState": &service.LoadState, "ActiveState": &service.ActiveState, "SubState": &service.SubState} {
				if fields[name] != "" {
					*dest = fields[name]
				}
			}
			pid, err := strconv.Atoi(fields["MainPID"])
			if err == nil && pid >= 0 {
				service.MainPID = pid
				if service.LoadState == "loaded" {
					switch {
					case service.ActiveState == "active" && service.SubState == "running" && pid > 0:
						service.State = "running"
					case service.ActiveState == "inactive" && service.SubState == "dead" && pid == 0:
						service.State = "stopped"
					case service.ActiveState == "failed" && service.SubState == "failed" && pid == 0:
						service.State = "failed"
					case service.ActiveState == "activating" || service.ActiveState == "deactivating":
						service.State = "transitioning"
					}
				}
			}
		}
		counts[service.State]++
		status.Services = append(status.Services, service)
	}
	switch {
	case counts["unknown"] > 0:
		status.ServiceState = "unknown"
	case counts["running"] == len(nativeCameraUnits):
		status.ServiceState = "running"
	case counts["stopped"] == len(nativeCameraUnits):
		status.ServiceState = "stopped"
	case counts["transitioning"] > 0:
		status.ServiceState = "transitioning"
	default:
		status.ServiceState = "degraded"
	}
	return status
}

func readNativeCameraServices(ctx context.Context, a *App) NativeCameraStatus {
	status := emptyNativeCameraStatus()
	if err := a.RequireDevice(); err != nil {
		status.ServiceState = "unavailable"
		status.Reason = "仅在相机上读取原生服务状态；本地模式不连接硬件。"
		status.NativeState = nativeObservationError("unavailable", "本地模式不连接相机硬件。")
		return status
	}
	args := []string{"show", "-p", "Id", "-p", "LoadState", "-p", "ActiveState", "-p", "SubState", "-p", "MainPID"}
	output, err := a.Run(ctx, 3*time.Second, "systemctl", append(args, nativeCameraUnits...)...)
	status = parseNativeCameraStatus(string(output))
	if err != nil {
		status.ServiceState = "unknown"
		status.Reason = "原生服务状态读取未完成：" + err.Error()
		return status
	}
	return status
}

func readNativeCameraStatus(ctx context.Context, a *App) NativeCameraStatus {
	status := readNativeCameraServices(ctx, a)
	if status.Reason == "" {
		addNativeCameraObservation(ctx, a, &status)
	}
	return status
}

// A standalone diagnostic permits device verification without installing the
// candidate server or restarting any camera, network, or display service.
func runNativeCameraStatus(w io.Writer) error {
	a, err := appAt("/")
	if err != nil {
		return err
	}
	defer a.cancel()
	status := readNativeCameraStatus(a.Ctx, a)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(status); err != nil {
		return err
	}
	if status.ServiceState == "unknown" || status.ServiceState == "unavailable" {
		return errors.New("native camera service status is unavailable or incomplete")
	}
	return nativeObservationDiagnosticError(status.NativeState)
}

var errNativeCameraProtected = errors.New("独立采集会停止原生相机、屏幕和 Mimo 通信服务；请在高级独立采集中明确允许接管")

type cameraTakeoverRequest struct {
	Confirm        bool `json:"confirm"`
	TakeoverNative bool `json:"takeover_native"`
}

func requireCameraTakeover(w http.ResponseWriter, request cameraTakeoverRequest) bool {
	if !request.Confirm {
		jsonError(w, http.StatusBadRequest, "confirmation_required", errors.New("启动会停止原生服务，必须明确确认"))
		return false
	}
	if !request.TakeoverNative {
		jsonError(w, http.StatusConflict, "native_camera_protected", errNativeCameraProtected)
		return false
	}
	return true
}
