package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNativeRecordingUsesOnlyVerifiedCodes(t *testing.T) {
	for _, code := range []int{-1, 0, 1, 2, 3, 4, 5, 100} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			observation, err := decodeNativeObservation([]byte(fmt.Sprintf(`{"schema":1,"status":"ok","camera_id":0,"camera_amount":1,"workmode":3,"mode_profile":5,"record_state":%d,"capture_state":0}`, code)))
			if err != nil {
				t.Fatal(err)
			}
			recording := nativeRecording(observation)
			if code == 1 || code == 3 {
				if recording == nil || *recording != (code == 1) {
					t.Fatalf("native code %d was misread: %+v", code, observation)
				}
			} else if recording != nil {
				t.Fatalf("unverified code %d became a boolean", code)
			}
			if observation.RecordState == nil || int(*observation.RecordState) != code || observation.ObservedAt == "" {
				t.Fatalf("lost original evidence: %+v", observation)
			}
		})
	}
}

func TestNativeReaderRejectsIncompleteOrContaminatedOutput(t *testing.T) {
	good := `{"schema":1,"status":"ok","camera_id":0,"camera_amount":1,"workmode":3,"mode_profile":5,"record_state":1,"capture_state":0}`
	for _, data := range []string{
		`{}`, `null`, `{"schema":1,"status":"ok"}`,
		strings.Replace(good, `"schema":1`, `"schema":2`, 1),
		strings.Replace(good, `"camera_id":0`, `"camera_id":1`, 1),
		strings.Replace(good, `"camera_amount":1`, `"camera_amount":2`, 1),
		strings.Replace(good, `"record_state":1`, `"record_state":null`, 1),
		strings.Replace(good, `"record_state":1`, `"record_state":true`, 1),
		strings.Replace(good, `"record_state":1`, `"record_state":2147483648`, 1),
		strings.Replace(good, `"workmode":3`, `"workmode":null`, 1),
		strings.Replace(good, `"mode_profile":5`, `"mode_profile":null`, 1),
		strings.Replace(good, `"status":"ok"`, `"status":"invented"`, 1),
		good + "\n{}", "native log\n" + good, good + "\nunfinished log",
	} {
		if observation, err := decodeNativeObservation([]byte(data)); err == nil {
			t.Fatalf("bad evidence accepted: %s -> %+v", data, observation)
		}
	}
	for _, status := range []string{"unavailable", "unsupported_firmware", "busy", "error"} {
		data := strings.Replace(good, `"status":"ok"`, `"status":"`+status+`"`, 1)
		observation, err := decodeNativeObservation([]byte(data))
		if err != nil || observation.RecordState != nil || observation.CaptureState != nil || observation.ObservedAt != "" || nativeRecording(observation) != nil {
			t.Fatalf("failure retained camera evidence: %+v %v", observation, err)
		}
	}
}

func TestNativeReaderSharesInflightReadWithoutBlockingCancelledWaiter(t *testing.T) {
	var reader nativeStateReader
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	probe := func(context.Context) NativeCameraObservation {
		calls.Add(1)
		close(started)
		<-release
		return NativeCameraObservation{Status: "ok", ObservedAt: "sample time"}
	}
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan NativeCameraObservation, 1)
	go func() { first <- reader.read(ctx, context.Background(), 1250, probe) }()
	<-started
	cancel()
	select {
	case observation := <-first:
		if observation.Status != "error" {
			t.Fatalf("cancelled request returned evidence: %+v", observation)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled waiter did not return")
	}
	var waiters sync.WaitGroup
	for range 20 {
		waiters.Go(func() {
			observation := reader.read(context.Background(), context.Background(), 1250, probe)
			if observation.Status != "ok" || observation.ObservedAt != "sample time" {
				t.Errorf("shared observation lost: %+v", observation)
			}
		})
	}
	close(release)
	waiters.Wait()
	if calls.Load() != 1 {
		t.Fatalf("concurrent requests created %d readers", calls.Load())
	}
}

func TestNativeReaderDropsOldValuesAndInvalidatesCacheForNewService(t *testing.T) {
	var reader nativeStateReader
	var calls int
	code := int32(1)
	probe := func(context.Context) NativeCameraObservation {
		calls++
		if calls == 1 {
			return NativeCameraObservation{Status: "ok", RecordState: &code}
		}
		return nativeObservationError("timeout", "test timeout")
	}
	read := func(pid int) NativeCameraObservation {
		return reader.read(context.Background(), context.Background(), pid, probe)
	}
	if state := read(1250); state.Status != "ok" || nativeRecording(state) == nil {
		t.Fatal(state)
	}
	reader.mu.Lock()
	reader.expires = time.Now().Add(-time.Second)
	reader.mu.Unlock()
	for range 3 {
		if state := read(1250); state.Status != "timeout" || state.RecordState != nil || nativeRecording(state) != nil {
			t.Fatalf("failure displayed stale recording: %+v", state)
		}
	}
	if calls != 2 {
		t.Fatalf("failed read did not back off: %d", calls)
	}
	read(1251)
	if calls != 3 {
		t.Fatal("new service reused an old observation")
	}
}

func TestNativeReaderKillsHungChildAndDiscardsPartialOutput(t *testing.T) {
	a, err := appAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.cancel()
	bin := filepath.Join(a.Dir, "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	// No native calls: a fake child returns plausible JSON and then hangs. A
	// timeout must invalidate its output and terminate the owned process group.
	script := "#!/bin/sh\nprintf '%s\\n' '{\"schema\":1,\"status\":\"ok\",\"camera_id\":0,\"camera_amount\":1,\"workmode\":3,\"mode_profile\":5,\"record_state\":1,\"capture_state\":0}'\nexec /bin/sleep 30\n"
	if err := os.WriteFile(filepath.Join(bin, "python3"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	observation := probeNativeCamera(ctx, a)
	if observation.Status != "timeout" || observation.RecordState != nil || time.Since(started) > 2*time.Second {
		t.Fatalf("hung helper was not bounded: %+v", observation)
	}
}
