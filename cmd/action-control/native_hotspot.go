package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"
)

// The ptrace call injector stays outside the Go server. Python is already
// present on the verified firmware; -I and -B isolate imports and prevent
// bytecode writes. The adapter drives dji_network's own wifi manager to bring
// the native SoftAP up or down without taking over the interface or rebuilding
// the DUSS stack. See NETWORK_HOTSPOT_FEASIBILITY.md.
//
//go:embed native_hotspot.py
var nativeHotspotScript string

// NativeHotspotWlan is the read-back interface state after the injected call.
type NativeHotspotWlan struct {
	Operstate string `json:"operstate,omitempty"`
	Role      string `json:"role,omitempty"`
	SSID      string `json:"ssid,omitempty"`
}

// NativeHotspotResult mirrors the adapter's JSON. Status is one of ok,
// unavailable, unsupported_firmware, error. NativeCode is the raw return of
// wifi_mgmt_start/stop_wifi (0 = success); it is reported verbatim, not guessed.
type NativeHotspotResult struct {
	Status     string             `json:"status"`
	Action     string             `json:"action,omitempty"`
	NativeCode *int               `json:"native_code,omitempty"`
	PID        int                `json:"pid,omitempty"`
	Before     *NativeHotspotWlan `json:"before,omitempty"`
	After      *NativeHotspotWlan `json:"after,omitempty"`
	ObservedAt string             `json:"observed_at,omitempty"`
	Reason     string             `json:"reason,omitempty"`
}

func validNativeHotspotAction(action string) bool {
	return action == "start" || action == "stop"
}

// One injection at a time: ptrace attach is exclusive per tracer, and two
// concurrent attaches to dji_network would fail. This lock also keeps start and
// stop from racing. keepaliveCancel (guarded by mu) drives a background goroutine
// that periodically resets dji_network's adaptive low-power timer while the AP is
// up, otherwise it tears the SoftAP down after ~60s without a DJI App session.
type nativeHotspotController struct {
	app             *App
	mu              sync.Mutex
	keepaliveCancel context.CancelFunc
}

// Caller holds mu. Cancels any prior loop, then resets the low-power timer every
// 25s (native window ~60s) until stop or service shutdown. If the service dies,
// the loop dies and the AP drops on the native timeout — a safe failsafe, no orphan.
func (h *nativeHotspotController) startKeepalive() {
	h.stopKeepalive()
	ctx, cancel := context.WithCancel(h.app.Ctx)
	h.keepaliveCancel = cancel
	go func() {
		t := time.NewTicker(25 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				h.mu.Lock()
				if ctx.Err() == nil {
					h.run(ctx, "keepalive")
				}
				h.mu.Unlock()
			}
		}
	}()
}

// Caller holds mu.
func (h *nativeHotspotController) stopKeepalive() {
	if h.keepaliveCancel != nil {
		h.keepaliveCancel()
		h.keepaliveCancel = nil
	}
}

func nativeHotspotError(action, status, reason string) NativeHotspotResult {
	return NativeHotspotResult{Status: status, Action: action, Reason: reason}
}

func (h *nativeHotspotController) run(ctx context.Context, action string) NativeHotspotResult {
	if err := h.app.RequireDevice(); err != nil {
		return nativeHotspotError(action, "unavailable", err.Error())
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	// 8s injection window matches the recording adapter; hostapd/dnsmasq spawn
	// inside the injected call, so allow headroom before the outer deadline.
	output, err := h.app.Run(ctx, 12*time.Second, "python3", "-I", "-B", "-c", nativeHotspotScript, action)
	if ctx.Err() != nil {
		return nativeHotspotError(action, "error", "原生热点操作超时或已取消。")
	}
	if err != nil {
		return nativeHotspotError(action, "error", "原生热点操作未完成："+err.Error())
	}
	result, e := decodeNativeHotspot(output, action)
	if e != nil {
		return nativeHotspotError(action, "error", "原生热点结果无效。")
	}
	return result
}

// A malformed or statusless adapter payload is an error, never fabricated
// success. native_code is preserved verbatim; 0 means the native entry returned
// success, non-zero is reported as-is without guessing an error enum.
func decodeNativeHotspot(output []byte, action string) (NativeHotspotResult, error) {
	var result NativeHotspotResult
	if e := json.Unmarshal(output, &result); e != nil {
		return NativeHotspotResult{}, e
	}
	if result.Status == "" {
		return NativeHotspotResult{}, errors.New("missing status")
	}
	result.Action = action
	return result, nil
}

func (h *nativeHotspotController) handle(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action string `json:"action"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if !validNativeHotspotAction(req.Action) {
		jsonError(w, 400, "invalid_action", errors.New("需要 action=start|stop"))
		return
	}
	if !h.mu.TryLock() {
		jsonError(w, 409, "hotspot_busy", errors.New("原生热点操作正在进行"))
		return
	}
	defer h.mu.Unlock()
	if req.Action == "stop" {
		h.stopKeepalive() // stop the loop before teardown so it cannot re-arm the timer
	}
	result := h.run(r.Context(), req.Action)
	if req.Action == "start" && result.Status == "ok" && result.NativeCode != nil && *result.NativeCode == 0 {
		h.startKeepalive() // hold off dji_network's adaptive low-power teardown
	}
	status := http.StatusOK
	switch result.Status {
	case "unavailable":
		status = http.StatusServiceUnavailable
	case "unsupported_firmware":
		status = http.StatusConflict
	case "error":
		status = http.StatusBadGateway
	}
	jsonResponse(w, status, result)
}
