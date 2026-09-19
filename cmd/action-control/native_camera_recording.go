package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sync"
	"time"
)

// This gate only serializes access to our native client. It never represents
// camera ownership, and it does not lock out physical buttons or native clients.
type nativeCameraGate struct {
	once sync.Once
	held chan struct{}
}

func (gate *nativeCameraGate) acquire(ctx context.Context) error {
	gate.once.Do(func() { gate.held = make(chan struct{}, 1) })
	select {
	case gate.held <- struct{}{}:
		if err := ctx.Err(); err != nil {
			gate.release()
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (gate *nativeCameraGate) release() { <-gate.held }

type NativeRecordingControls struct {
	Start bool `json:"start"`
	Stop  bool `json:"stop"`
}

func nativeRecordingControls(observation NativeCameraObservation) NativeRecordingControls {
	var controls NativeRecordingControls
	if recording := nativeRecording(observation); recording != nil {
		controls.Stop = *recording
		controls.Start = !*recording && observation.CaptureState != nil && *observation.CaptureState == 0
	}
	return controls
}

type nativeRecordingRequest struct {
	Action    string `json:"action"`
	RequestID string `json:"request_id"`
}

type NativeRecordingState struct {
	RecordState  *int32 `json:"record_state"`
	CaptureState *int32 `json:"capture_state"`
	ModeProfile  *int32 `json:"mode_profile,omitempty"`
}

type NativeRecordingResult struct {
	Source      string                `json:"source"`
	RequestID   string                `json:"request_id"`
	Action      string                `json:"action"`
	Outcome     string                `json:"outcome"`
	Dispatched  *bool                 `json:"dispatched"`
	NativeCode  *int32                `json:"native_code"`
	Before      *NativeRecordingState `json:"before"`
	After       *NativeRecordingState `json:"after"`
	Message     string                `json:"message"`
	Reason      string                `json:"reason,omitempty"`
	CompletedAt string                `json:"completed_at"`
	Replayed    bool                  `json:"replayed"`
}

func validNativeRecordingAction(action string) bool {
	return action == "start_recording" || action == "stop_recording"
}

// capture and mode switch share the same native client, operation lock, and
// dedup as recording, so no two of them can dispatch at the same time.
func validNativeAction(action string) bool {
	return validNativeRecordingAction(action) || action == "capture" || validNativeModeAction(action)
}

// Only the two verified profiles are exposed; unknown mode integers are never
// offered or sent. mode_photo=5, mode_video=1.
func validNativeModeAction(action string) bool {
	return action == "mode_photo" || action == "mode_video"
}

func nativeModeTarget(action string) int32 {
	if action == "mode_photo" {
		return 5
	}
	return 1
}

type NativeModeControls struct {
	Photo bool `json:"photo"`
	Video bool `json:"video"`
}

// A mode switch dispatches only from the verified idle state. Each button is
// offered only when idle and not already in that profile.
func nativeModeControls(observation NativeCameraObservation) NativeModeControls {
	recording := nativeRecording(observation)
	idle := recording != nil && !*recording &&
		observation.CaptureState != nil && *observation.CaptureState == 0
	profile := observation.ModeProfile
	return NativeModeControls{
		Photo: idle && profile != nil && *profile != 5,
		Video: idle && profile != nil && *profile != 1,
	}
}

type NativeCaptureControls struct {
	Capture bool `json:"capture"`
}

// A photo dispatches only from the verified idle state (not recording, capture
// state zero). Unknown states never enable the button.
func nativeCaptureControls(observation NativeCameraObservation) NativeCaptureControls {
	recording := nativeRecording(observation)
	idle := recording != nil && !*recording &&
		observation.CaptureState != nil && *observation.CaptureState == 0
	return NativeCaptureControls{Capture: idle}
}

func decodeNativeRecording(data []byte, action string) (NativeRecordingResult, error) {
	var wire struct {
		Schema     int                   `json:"schema"`
		Action     string                `json:"action"`
		Outcome    string                `json:"outcome"`
		Dispatched *bool                 `json:"dispatched"`
		NativeCode *int32                `json:"native_code"`
		Before     *NativeRecordingState `json:"before"`
		After      *NativeRecordingState `json:"after"`
		Reason     string                `json:"reason"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return NativeRecordingResult{}, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return NativeRecordingResult{}, errors.New("extra native recording output")
	}
	if wire.Schema != 1 || !validNativeAction(action) || wire.Action != action || wire.Dispatched == nil {
		return NativeRecordingResult{}, errors.New("incomplete native recording result")
	}
	for _, state := range []*NativeRecordingState{wire.Before, wire.After} {
		if state != nil && (state.RecordState == nil || state.CaptureState == nil) {
			return NativeRecordingResult{}, errors.New("incomplete native recording state")
		}
	}
	dispatched, accepted := *wire.Dispatched, wire.NativeCode != nil && *wire.NativeCode == 0
	valid := false
	if action == "capture" {
		// A photo has no stable target state, so no "confirmed"/"already".
		switch wire.Outcome {
		case "accepted":
			valid = dispatched && accepted && wire.Before != nil
		case "blocked":
			valid = !dispatched && wire.NativeCode == nil && wire.Before != nil && wire.After == nil
		case "rejected":
			valid = dispatched && wire.NativeCode != nil && *wire.NativeCode != 0 && wire.Before != nil
		case "not_sent":
			valid = !dispatched && wire.NativeCode == nil && wire.After == nil
		case "unknown":
			valid = dispatched && wire.After == nil
		}
	} else if validNativeModeAction(action) {
		target := nativeModeTarget(action)
		beforeProfile := wire.Before != nil && wire.Before.ModeProfile != nil
		targetObserved := wire.After != nil && wire.After.ModeProfile != nil && *wire.After.ModeProfile == target
		switch wire.Outcome {
		case "confirmed":
			valid = dispatched && accepted && beforeProfile && targetObserved
		case "accepted":
			valid = dispatched && accepted && beforeProfile
		case "already":
			valid = !dispatched && wire.NativeCode == nil && beforeProfile && *wire.Before.ModeProfile == target && targetObserved
		case "blocked":
			valid = !dispatched && wire.NativeCode == nil && beforeProfile && wire.After == nil
		case "rejected":
			valid = dispatched && wire.NativeCode != nil && *wire.NativeCode != 0 && beforeProfile
		case "not_sent":
			valid = !dispatched && wire.NativeCode == nil && wire.After == nil
		case "unknown":
			valid = dispatched && wire.After == nil
		}
	} else {
		target := int32(1)
		if action == "stop_recording" {
			target = 3
		}
		targetObserved := wire.After != nil && *wire.After.RecordState == target
		switch wire.Outcome {
		case "confirmed":
			valid = dispatched && accepted && wire.Before != nil && targetObserved
		case "accepted":
			valid = dispatched && accepted && wire.Before != nil
		case "already":
			valid = !dispatched && wire.NativeCode == nil && wire.Before != nil && *wire.Before.RecordState == target && targetObserved
		case "blocked":
			valid = !dispatched && wire.NativeCode == nil && wire.Before != nil && wire.After == nil
		case "rejected":
			valid = dispatched && wire.NativeCode != nil && *wire.NativeCode != 0 && wire.Before != nil
		case "not_sent":
			valid = !dispatched && wire.NativeCode == nil && wire.After == nil
		case "unknown":
			valid = dispatched && wire.After == nil
		}
	}
	if !valid {
		return NativeRecordingResult{}, errors.New("inconsistent native recording result")
	}
	return NativeRecordingResult{
		Action: action, Outcome: wire.Outcome, Dispatched: wire.Dispatched,
		NativeCode: wire.NativeCode, Before: wire.Before, After: wire.After, Reason: wire.Reason,
	}, nil
}

func nativeRecordingNotSent(reason string) NativeRecordingResult {
	dispatched := false
	return NativeRecordingResult{Outcome: "not_sent", Dispatched: &dispatched, Reason: reason}
}

func runNativeRecording(ctx context.Context, a *App, action string) NativeRecordingResult {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if !validNativeAction(action) {
		return nativeRecordingNotSent("不支持的原生相机操作。")
	}
	if err := a.nativeIO.acquire(ctx); err != nil {
		return nativeRecordingNotSent("等待原生相机访问已超时或取消。")
	}
	defer a.nativeIO.release()
	a.nativeReader.invalidate()
	defer a.nativeReader.invalidate()
	output, err := a.Run(ctx, 8*time.Second, "python3", "-I", "-B", "-c", nativeCameraReaderScript, action)
	if err != nil || ctx.Err() != nil {
		// The child may have sent a command before timing out or failing. Neither
		// missing output nor process termination proves that the camera stopped.
		return NativeRecordingResult{Outcome: "unknown", Reason: "原生调用未完整返回；请先查看相机状态，勿自动重发。"}
	}
	result, err := decodeNativeRecording(output, action)
	if err != nil {
		return NativeRecordingResult{Outcome: "unknown", Reason: "原生调用结果无效：" + err.Error()}
	}
	return result
}

func (c *cameraController) executeNativeRecording(action string) NativeRecordingResult {
	ctx, cancel := context.WithTimeout(c.app.Ctx, 8*time.Second)
	defer cancel()
	c.mu.Lock()
	independent := c.run != nil
	c.mu.Unlock()
	if independent || c.app.ScreenRequired() {
		return nativeRecordingNotSent("独立采集已接管原生相机，请先恢复原生服务。")
	}
	status := readNativeCameraServices(ctx, c.app)
	available := false
	for _, service := range status.Services {
		if service.Unit == "dji_camera3.service" && service.State == "running" {
			available = true
		}
	}
	if !available || status.Reason != "" {
		return nativeRecordingNotSent("未确认原生相机服务正常运行。")
	}
	return runNativeRecording(ctx, c.app, action)
}

func nativeRecordingMessage(result NativeRecordingResult) string {
	if result.Action == "capture" {
		switch result.Outcome {
		case "accepted":
			return "相机已受理拍照请求，请在相册确认新照片。"
		case "blocked":
			return "相机当前状态不支持拍照，请先停止录像并稍后刷新状态。"
		case "rejected":
			return fmt.Sprintf("相机返回错误（代码 %d），请查看相机提示和当前状态。", *result.NativeCode)
		case "not_sent":
			return "拍照请求未发送，请检查原生相机状态。"
		default:
			return "拍照请求结果未知，请先查看原生状态，确认后再操作。"
		}
	}
	if validNativeModeAction(result.Action) {
		mode := "拍照"
		if result.Action == "mode_video" {
			mode = "视频"
		}
		switch result.Outcome {
		case "confirmed":
			return "已切换到" + mode + "模式。"
		case "already":
			return "相机已处于" + mode + "模式，未重复切换。"
		case "accepted":
			return "已受理切换到" + mode + "模式，暂未确认。"
		case "blocked":
			return "相机当前状态不支持切换模式，请先停止录像并稍后刷新。"
		case "rejected":
			return fmt.Sprintf("相机返回错误（代码 %d），请查看相机提示和当前状态。", *result.NativeCode)
		case "not_sent":
			return "切换模式请求未发送，请检查原生相机状态。"
		default:
			return "切换模式结果未知，请先查看原生状态，确认后再操作。"
		}
	}
	verb := "开始"
	if result.Action == "stop_recording" {
		verb = "停止"
	}
	switch result.Outcome {
	case "confirmed":
		return "已观察到相机" + verb + "录像。"
	case "already":
		if result.Action == "stop_recording" {
			return "相机当前未录像，未重复发送停止指令。"
		}
		return "相机当前已在录像，未重复发送开始指令。"
	case "accepted":
		return "相机已受理" + verb + "录像请求，暂未确认状态变化。"
	case "blocked":
		return "相机当前状态不支持此操作，请稍后刷新状态。"
	case "rejected":
		return fmt.Sprintf("相机返回错误（代码 %d），请查看相机提示和当前状态。", *result.NativeCode)
	case "not_sent":
		return "录像请求未发送，请检查原生相机状态。"
	default:
		return "录像请求结果未知，请先查看原生状态，确认后再操作。"
	}
}

type nativeRecordingAttempt struct {
	action    string
	done      chan struct{}
	result    NativeRecordingResult
	completed time.Time
}

type nativeRecordingController struct {
	camera   *cameraController
	mu       sync.Mutex
	attempts map[string]*nativeRecordingAttempt
}

var (
	nativeRequestIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,80}$`)
	errNativeRequestReuse  = errors.New("同一 request_id 不能用于不同录像操作")
	errNativeActionBusy    = errors.New("已有相机操作正在执行，请稍后刷新状态")
	errNativeRequestLimit  = errors.New("录像请求过于频繁，请稍后重试")
)

// Store request results, never a desired recording state. A disconnected HTTP
// waiter does not cancel or repeat a physical action. The shared operation lock
// also excludes Action Control's independent capture/takeover operations.
func (controller *nativeRecordingController) begin(request nativeRecordingRequest, run func() NativeRecordingResult) (*nativeRecordingAttempt, bool, error) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	now := time.Now()
	for id, attempt := range controller.attempts {
		if !attempt.completed.IsZero() && now.Sub(attempt.completed) >= 10*time.Minute {
			delete(controller.attempts, id)
		}
	}
	if attempt := controller.attempts[request.RequestID]; attempt != nil {
		if attempt.action != request.Action {
			return nil, false, errNativeRequestReuse
		}
		return attempt, true, nil
	}
	if len(controller.attempts) >= 256 {
		return nil, false, errNativeRequestLimit
	}
	if !controller.camera.operation.TryLock() {
		return nil, false, errNativeActionBusy
	}
	attempt := &nativeRecordingAttempt{action: request.Action, done: make(chan struct{})}
	if controller.attempts == nil {
		controller.attempts = make(map[string]*nativeRecordingAttempt)
	}
	controller.attempts[request.RequestID] = attempt
	go func() {
		result := run()
		result.Source, result.Action, result.RequestID = "native_binder", request.Action, request.RequestID
		result.CompletedAt = time.Now().UTC().Format(time.RFC3339Nano)
		result.Message = nativeRecordingMessage(result)
		controller.mu.Lock()
		attempt.result, attempt.completed = result, time.Now()
		controller.camera.operation.Unlock()
		close(attempt.done)
		controller.mu.Unlock()
	}()
	return attempt, false, nil
}

func waitNativeRecording(ctx context.Context, attempt *nativeRecordingAttempt, replayed bool) (NativeRecordingResult, error) {
	select {
	case <-ctx.Done():
		return NativeRecordingResult{}, ctx.Err()
	case <-attempt.done:
		result := attempt.result
		result.Replayed = replayed
		return result, nil
	}
}

func (controller *nativeRecordingController) handleRecording(w http.ResponseWriter, r *http.Request) {
	controller.serve(w, r, validNativeRecordingAction, "需要明确的录像操作与 8–80 位 request_id")
}

func (controller *nativeRecordingController) handleCapture(w http.ResponseWriter, r *http.Request) {
	controller.serve(w, r, func(a string) bool { return a == "capture" }, "需要 action=capture 与 8–80 位 request_id")
}

func (controller *nativeRecordingController) handleMode(w http.ResponseWriter, r *http.Request) {
	controller.serve(w, r, validNativeModeAction, "需要 action=mode_photo|mode_video 与 8–80 位 request_id")
}

func (controller *nativeRecordingController) serve(w http.ResponseWriter, r *http.Request, allowed func(string) bool, invalidMsg string) {
	var request nativeRecordingRequest
	if !readJSON(w, r, &request) {
		return
	}
	if !allowed(request.Action) || !nativeRequestIDPattern.MatchString(request.RequestID) {
		jsonError(w, 400, "invalid_native_recording_request", errors.New(invalidMsg))
		return
	}
	if err := controller.camera.app.RequireManaged(); err != nil {
		jsonError(w, 503, "device_unavailable", err)
		return
	}
	if r.Context().Err() != nil {
		return
	}
	attempt, replayed, err := controller.begin(request, func() NativeRecordingResult {
		return controller.camera.executeNativeRecording(request.Action)
	})
	if err != nil {
		code := "native_recording_busy"
		status := http.StatusConflict
		if errors.Is(err, errNativeRequestReuse) {
			code = "native_recording_request_reused"
		} else if errors.Is(err, errNativeRequestLimit) {
			code, status = "native_recording_request_limit", http.StatusTooManyRequests
		}
		jsonError(w, status, code, err)
		return
	}
	result, err := waitNativeRecording(r.Context(), attempt, replayed)
	if err != nil {
		return
	}
	// The operation report is available even on native rejection or uncertainty.
	// Clients must inspect outcome; HTTP 200 is not proof of recording success.
	jsonResponse(w, http.StatusOK, result)
}
