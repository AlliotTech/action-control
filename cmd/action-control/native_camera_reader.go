package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// The native library stays outside the Go server. Python is already present on
// the verified firmware; -I and -B isolate imports and prevent bytecode writes.
//
//go:embed native_camera_reader.py
var nativeCameraReaderScript string

type NativeCameraObservation struct {
	Status        string               `json:"status"`
	Workmode      *int32               `json:"workmode"`
	ModeProfile   *int32               `json:"mode_profile"`
	RecordState   *int32               `json:"record_state"`
	CaptureState  *int32               `json:"capture_state"`
	VideoSettings *NativeVideoSettings `json:"video_settings,omitempty"`
	ObservedAt    string               `json:"observed_at,omitempty"`
	Reason        string               `json:"reason,omitempty"`
}

// NativeVideoSettings carries the raw values read from the native client plus
// labels resolved from DJI's documented enums. Enum tables are ported from the
// official DJI Osmo GPS controller demo (github, MIT, Copyright DJI); only the
// value->name mappings are reused, no code. Unknown values keep the raw int and
// an empty label rather than a guess.
type NativeVideoSettings struct {
	Resolution      *int32 `json:"resolution"`
	ResolutionLabel string `json:"resolution_label,omitempty"`
	ResolutionRaw   *int32 `json:"resolution_raw,omitempty"`
	FPS             *int32 `json:"fps"`
	FPSLabel        string `json:"fps_label,omitempty"`
	Codec           *int32 `json:"codec"`
	Storage         *int32 `json:"storage"`
	EIS             *int32 `json:"eis"`
	EISLabel        string `json:"eis_label,omitempty"`
}

// Value->name maps from the DJI demo's camera_status_push documentation.
var nativeResolutionLabels = map[int32]string{
	10: "1080P", 16: "4K 16:9", 45: "2.7K 16:9", 66: "1080P 9:16",
	67: "2.7K 9:16", 95: "2.7K 4:3", 103: "4K 4:3", 109: "4K 9:16",
}
var nativeFPSLabels = map[int32]string{
	1: "24", 2: "25", 3: "30", 4: "48", 5: "50", 6: "60", 10: "100", 7: "120", 19: "200", 8: "240",
}
var nativeEISLabels = map[int32]string{0: "关闭", 1: "RS", 2: "HS", 3: "RS+", 4: "HB"}

func labelVideoSettings(v *NativeVideoSettings) *NativeVideoSettings {
	if v == nil {
		return nil
	}
	if v.Resolution != nil {
		v.ResolutionLabel = nativeResolutionLabels[*v.Resolution]
	}
	if v.FPS != nil {
		v.FPSLabel = nativeFPSLabels[*v.FPS]
	}
	if v.EIS != nil {
		v.EISLabel = nativeEISLabels[*v.EIS]
	}
	return v
}

func nativeObservationError(status, reason string) NativeCameraObservation {
	return NativeCameraObservation{Status: status, Reason: reason}
}

func decodeNativeObservation(data []byte) (NativeCameraObservation, error) {
	var wire struct {
		Schema        int                  `json:"schema"`
		Status        string               `json:"status"`
		CameraID      *int                 `json:"camera_id"`
		CameraAmount  *int                 `json:"camera_amount"`
		Workmode      *int32               `json:"workmode"`
		ModeProfile   *int32               `json:"mode_profile"`
		RecordState   *int32               `json:"record_state"`
		CaptureState  *int32               `json:"capture_state"`
		VideoSettings *NativeVideoSettings `json:"video_settings"`
		Reason        string               `json:"reason"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return NativeCameraObservation{}, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return NativeCameraObservation{}, errors.New("extra native reader output")
	}
	if wire.Schema != 1 {
		return NativeCameraObservation{}, errors.New("unsupported native reader schema")
	}
	if wire.Status != "ok" {
		switch wire.Status {
		case "unavailable", "unsupported_firmware", "busy", "error":
			return nativeObservationError(wire.Status, wire.Reason), nil
		default:
			return NativeCameraObservation{}, errors.New("invalid native reader status")
		}
	}
	if wire.CameraID == nil || *wire.CameraID != 0 || wire.CameraAmount == nil || *wire.CameraAmount != 1 || wire.RecordState == nil || wire.CaptureState == nil || wire.Workmode == nil || wire.ModeProfile == nil {
		return NativeCameraObservation{}, errors.New("incomplete native camera observation")
	}
	return NativeCameraObservation{
		Status:        "ok",
		Workmode:      wire.Workmode,
		ModeProfile:   wire.ModeProfile,
		RecordState:   wire.RecordState,
		CaptureState:  wire.CaptureState,
		VideoSettings: labelVideoSettings(wire.VideoSettings),
		ObservedAt:    time.Now().UTC().Format(time.RFC3339Nano),
	}, nil
}

func probeNativeCamera(ctx context.Context, a *App) NativeCameraObservation {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := a.nativeIO.acquire(ctx); err != nil {
		return nativeObservationError("timeout", "等待原生相机访问超时或已取消。")
	}
	defer a.nativeIO.release()
	output, err := a.Run(ctx, 3*time.Second, "python3", "-I", "-B", "-c", nativeCameraReaderScript)
	if ctx.Err() != nil {
		return nativeObservationError("timeout", "原生状态读取超时或已取消。")
	}
	if err != nil {
		return nativeObservationError("error", "原生状态读取未完成："+err.Error())
	}
	observation, err := decodeNativeObservation(output)
	if err != nil {
		return nativeObservationError("error", "原生状态结果无效："+err.Error())
	}
	return observation
}

// Only the idle and recording codes have been established from the native GUI.
// Starting/stopping/pre-record/error values must not become guessed booleans.
func nativeRecording(observation NativeCameraObservation) *bool {
	if observation.Status != "ok" || observation.RecordState == nil {
		return nil
	}
	var recording bool
	switch *observation.RecordState {
	case 1:
		recording = true
	case 3:
		recording = false
	default:
		return nil
	}
	return &recording
}

type nativeStateReader struct {
	mu          sync.Mutex
	inflight    chan struct{}
	pid         int
	observation NativeCameraObservation
	expires     time.Time
	generation  uint64
}

// A command invalidates even a read whose subprocess has finished but whose
// goroutine has not yet published its cache entry.
func (reader *nativeStateReader) invalidate() {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	reader.generation++
	reader.expires = time.Time{}
	reader.observation = NativeCameraObservation{}
}

// A shared observation prevents tabs and concurrent refreshes from creating a
// Binder client per request. Failures discard old values and back off for 30 s.
// Request cancellation stops only that waiter; the shared child is bounded by
// its deadline and the application's lifetime.
func (reader *nativeStateReader) read(ctx, lifetime context.Context, pid int, probe func(context.Context) NativeCameraObservation) NativeCameraObservation {
	for {
		if err := ctx.Err(); err != nil {
			return nativeObservationError("error", "状态请求已取消。")
		}
		reader.mu.Lock()
		if reader.pid == pid && time.Now().Before(reader.expires) {
			observation := reader.observation
			reader.mu.Unlock()
			return observation
		}
		if reader.inflight == nil {
			reader.inflight = make(chan struct{})
			generation := reader.generation
			go func() {
				observation := probe(lifetime)
				ttl := 2 * time.Second
				if observation.Status != "ok" {
					ttl = 30 * time.Second
				}
				reader.mu.Lock()
				if reader.generation == generation {
					reader.observation, reader.pid = observation, pid
					reader.expires = time.Now().Add(ttl)
				}
				close(reader.inflight)
				reader.inflight = nil
				reader.mu.Unlock()
			}()
		}
		done := reader.inflight
		reader.mu.Unlock()
		select {
		case <-ctx.Done():
			return nativeObservationError("error", "状态请求已取消。")
		case <-done:
		}
	}
}

func addNativeCameraObservation(ctx context.Context, a *App, status *NativeCameraStatus) {
	status.NativeState = nativeObservationError("unavailable", "原生拍摄服务未运行，暂不查询拍摄状态。")
	for _, service := range status.Services {
		if service.Unit != "dji_camera3.service" || service.State != "running" {
			continue
		}
		status.NativeState = a.nativeReader.read(ctx, a.Ctx, service.MainPID, func(ctx context.Context) NativeCameraObservation {
			return probeNativeCamera(ctx, a)
		})
		status.Recording = nativeRecording(status.NativeState)
		if status.NativeState.Status == "ok" {
			status.Source = "native_binder"
			status.ControlAvailable = true
			status.RecordingControls = nativeRecordingControls(status.NativeState)
			status.CaptureControls = nativeCaptureControls(status.NativeState)
			status.ModeControls = nativeModeControls(status.NativeState)
		}
		return
	}
}

func nativeObservationDiagnosticError(observation NativeCameraObservation) error {
	if observation.Status == "ok" {
		return nil
	}
	return fmt.Errorf("native camera state unavailable (%s): %s", observation.Status, observation.Reason)
}
