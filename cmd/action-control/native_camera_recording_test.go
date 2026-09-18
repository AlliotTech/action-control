package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const nativeStartConfirmed = `{"schema":1,"action":"start_recording","outcome":"confirmed","dispatched":true,"native_code":0,"before":{"record_state":3,"capture_state":0},"after":{"record_state":1,"capture_state":0},"reason":""}`

func TestNativeRecordingRejectsUnprovenOrMismatchedResults(t *testing.T) {
	good, err := decodeNativeRecording([]byte(nativeStartConfirmed), "start_recording")
	if err != nil || good.Outcome != "confirmed" || *good.After.RecordState != 1 {
		t.Fatalf("valid result lost: %+v %v", good, err)
	}
	for _, data := range []string{
		`{}`, `null`, nativeStartConfirmed + "\n{}", "log\n" + nativeStartConfirmed,
		strings.Replace(nativeStartConfirmed, `"schema":1`, `"schema":2`, 1),
		strings.Replace(nativeStartConfirmed, "start_recording", "stop_recording", 1),
		strings.Replace(nativeStartConfirmed, `"dispatched":true`, `"dispatched":false`, 1),
		strings.Replace(nativeStartConfirmed, `"dispatched":true`, `"dispatched":null`, 1),
		strings.Replace(nativeStartConfirmed, `"native_code":0`, `"native_code":-1001`, 1),
		strings.Replace(nativeStartConfirmed, `"native_code":0`, `"native_code":null`, 1),
		strings.Replace(nativeStartConfirmed, `"record_state":1`, `"record_state":3`, 1),
		strings.Replace(nativeStartConfirmed, `"record_state":1`, `"record_state":null`, 1),
		strings.Replace(nativeStartConfirmed, `"record_state":1`, `"record_state":2147483648`, 1),
		strings.Replace(nativeStartConfirmed, `"capture_state":0`, `"capture_state":null`, 1),
		strings.Replace(nativeStartConfirmed, `"confirmed"`, `"invented"`, 1),
	} {
		if result, err := decodeNativeRecording([]byte(data), "start_recording"); err == nil {
			t.Fatalf("invalid evidence became a result: %s -> %+v", data, result)
		}
	}
}

func TestNativeRecordingKeepsAcceptanceAndRejectionSeparateFromSuccess(t *testing.T) {
	for _, data := range []string{
		`{"schema":1,"action":"start_recording","outcome":"accepted","dispatched":true,"native_code":0,"before":{"record_state":3,"capture_state":0},"after":{"record_state":0,"capture_state":0}}`,
		`{"schema":1,"action":"start_recording","outcome":"rejected","dispatched":true,"native_code":-1010,"before":{"record_state":3,"capture_state":0}}`,
		`{"schema":1,"action":"start_recording","outcome":"unknown","dispatched":true,"native_code":0,"before":{"record_state":3,"capture_state":0}}`,
		`{"schema":1,"action":"start_recording","outcome":"not_sent","dispatched":false}`,
		`{"schema":1,"action":"start_recording","outcome":"blocked","dispatched":false,"before":{"record_state":2,"capture_state":0}}`,
		`{"schema":1,"action":"start_recording","outcome":"already","dispatched":false,"before":{"record_state":1,"capture_state":0},"after":{"record_state":1,"capture_state":0}}`,
	} {
		result, err := decodeNativeRecording([]byte(data), "start_recording")
		if err != nil || result.Outcome == "confirmed" || nativeRecordingMessage(result) == "" {
			t.Fatalf("lost distinct native outcome: %+v %v", result, err)
		}
	}
	stop := strings.NewReplacer("start_recording", "stop_recording", `"record_state":3`, `"record_state":1`, `"record_state":1`, `"record_state":3`).Replace(nativeStartConfirmed)
	if result, err := decodeNativeRecording([]byte(stop), "stop_recording"); err != nil || result.Outcome != "confirmed" {
		t.Fatalf("stop result lost: %+v %v", result, err)
	}
}

func TestNativeRecordingTimeoutNeverAssumesTheCommandWasNotSent(t *testing.T) {
	a, err := appAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.cancel()
	bin := filepath.Join(a.Dir, "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	// Emit apparent success and hang after it. This must stay uncertain, without
	// accepting partial output, restarting the helper, or sending a rollback stop.
	script := "#!/bin/sh\nprintf '%s\\n' '" + nativeStartConfirmed + "'\nexec /bin/sleep 30\n"
	if err := os.WriteFile(filepath.Join(bin, "python3"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	a.nativeReader.observation = NativeCameraObservation{Status: "ok"}
	a.nativeReader.expires = time.Now().Add(time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	result := runNativeRecording(ctx, a, "start_recording")
	if result.Outcome != "unknown" || result.Dispatched != nil || result.After != nil || time.Since(started) > 2*time.Second {
		t.Fatalf("timed-out operation was misrepresented: %+v", result)
	}
	if !a.nativeReader.expires.IsZero() || a.nativeReader.observation.Status != "" {
		t.Fatal("command timeout retained a pre-command status cache")
	}
}

func TestNativeRecordingDeduplicatesAndSurvivesDisconnectedWaiter(t *testing.T) {
	controller := &nativeRecordingController{camera: &cameraController{}}
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	run := func() NativeRecordingResult {
		calls.Add(1)
		close(started)
		<-release
		return NativeRecordingResult{Outcome: "unknown"}
	}
	request := nativeRecordingRequest{Action: "start_recording", RequestID: "record-test-1"}
	first, replayed, err := controller.begin(request, run)
	if err != nil || replayed {
		t.Fatalf("new command rejected: %v", err)
	}
	<-started
	second, replayed, err := controller.begin(request, run)
	if err != nil || !replayed || second != first {
		t.Fatal("duplicate request created another command")
	}
	if _, _, err := controller.begin(nativeRecordingRequest{Action: "stop_recording", RequestID: request.RequestID}, run); !errors.Is(err, errNativeRequestReuse) {
		t.Fatal("same id was allowed to change actions")
	}
	if _, _, err := controller.begin(nativeRecordingRequest{Action: "stop_recording", RequestID: "different-id"}, run); !errors.Is(err, errNativeActionBusy) {
		t.Fatal("another action was queued behind a physical command")
	}
	if controller.camera.operation.TryLock() {
		controller.camera.operation.Unlock()
		t.Fatal("independent takeover was allowed during native control")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := waitNativeRecording(ctx, first, false); err == nil {
		t.Fatal("cancelled waiter did not return")
	}
	close(release)
	result, err := waitNativeRecording(context.Background(), second, true)
	if err != nil || !result.Replayed || result.Outcome != "unknown" || result.RequestID != request.RequestID || result.CompletedAt == "" {
		t.Fatalf("uncertain result was not retained: %+v %v", result, err)
	}
	third, replayed, err := controller.begin(request, run)
	if err != nil || !replayed || third != first || calls.Load() != 1 {
		t.Fatal("retry after an uncertain result reissued the command")
	}
	if !controller.camera.operation.TryLock() {
		t.Fatal("completed command kept the camera operation locked")
	}
	controller.camera.operation.Unlock()
}

func TestNativeRecordingSharesTakeoverLockAndBoundsRequestMemory(t *testing.T) {
	controller := &nativeRecordingController{camera: &cameraController{}, attempts: map[string]*nativeRecordingAttempt{}}
	request := nativeRecordingRequest{Action: "stop_recording", RequestID: "stop-test-1"}
	run := func() NativeRecordingResult {
		t.Error("blocked request reached native action")
		return NativeRecordingResult{}
	}
	controller.camera.operation.Lock()
	if _, _, err := controller.begin(request, run); !errors.Is(err, errNativeActionBusy) {
		t.Fatal("control bypassed an independent camera operation")
	}
	controller.camera.operation.Unlock()
	for i := range 256 {
		controller.attempts[string(rune(i))] = &nativeRecordingAttempt{completed: time.Now()}
	}
	if _, _, err := controller.begin(request, run); !errors.Is(err, errNativeRequestLimit) {
		t.Fatal("request cache limit was bypassed")
	}
}

func TestNativeReaderInvalidationDiscardsAnInflightOldSample(t *testing.T) {
	var reader nativeStateReader
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	probe := func(context.Context) NativeCameraObservation {
		if calls.Add(1) == 1 {
			close(started)
			<-release
			return NativeCameraObservation{Status: "ok", ObservedAt: "before command"}
		}
		return NativeCameraObservation{Status: "ok", ObservedAt: "after command"}
	}
	result := make(chan NativeCameraObservation, 1)
	go func() { result <- reader.read(context.Background(), context.Background(), 1250, probe) }()
	<-started
	reader.invalidate()
	close(release)
	select {
	case observation := <-result:
		if observation.ObservedAt != "after command" || calls.Load() != 2 {
			t.Fatalf("old inflight result survived invalidation: %+v", observation)
		}
	case <-time.After(time.Second):
		t.Fatal("cache invalidation stalled the reader")
	}
}

func TestNativeCameraAccessWaitIsCancellable(t *testing.T) {
	var gate nativeCameraGate
	if err := gate.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := gate.acquire(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("busy native access ignored cancellation: %v", err)
	}
	gate.release()
	if err := gate.acquire(context.Background()); err != nil {
		t.Fatal("cancelled waiter retained the native access gate")
	}
	gate.release()
}

func TestNativeRecordingEndpointRejectsInvalidRequestsBeforeHardware(t *testing.T) {
	a, handler := testApplication(t)
	for _, data := range []string{
		`{}`, `{"action":"start_recording"}`, `{"action":"toggle","request_id":"request-1"}`,
		`{"action":"capture","request_id":"request-1"}`, `{"action":"stop_recording","request_id":"$(uname)"}`,
		`{"action":"stop_recording","request_id":"short"}`, `{"action":"stop_recording","request_id":"request-1","takeover_native":true}`,
	} {
		response := requestTest(a, handler, "POST", "/api/native_recording", strings.NewReader(data), nil)
		if response.Code != 400 {
			t.Fatalf("bad recording request passed validation: %s %d %s", data, response.Code, response.Body.String())
		}
	}
	response := requestTest(a, handler, "POST", "/api/native_recording", strings.NewReader(`{"action":"start_recording","request_id":"request-1"}`), nil)
	if response.Code != 503 || !strings.Contains(response.Body.String(), "device_unavailable") {
		t.Fatal("local mode passed the hardware guard: ", response.Body.String())
	}
	response = requestTest(a, handler, "GET", "/api/native_camera_status", nil, nil)
	var status NativeCameraStatus
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil || status.ControlAvailable || status.RecordingControls.Start || status.RecordingControls.Stop {
		t.Fatalf("local mode advertised recording controls: %+v %v", status, err)
	}
}
