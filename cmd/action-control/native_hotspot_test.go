package main

import "testing"

func TestDecodeNativeHotspotKeepsStatusAndCodeVerbatim(t *testing.T) {
	ok := `{"status":"ok","action":"start","native_code":0,"pid":2464,` +
		`"before":{"operstate":"down","role":"managed"},` +
		`"after":{"operstate":"up","role":"AP","ssid":"OsmoAction6Pro"}}`
	result, err := decodeNativeHotspot([]byte(ok), "start")
	if err != nil {
		t.Fatalf("valid result rejected: %v", err)
	}
	if result.Status != "ok" || result.NativeCode == nil || *result.NativeCode != 0 {
		t.Fatalf("status/code lost: %+v", result)
	}
	if result.After == nil || result.After.Role != "AP" || result.After.SSID != "OsmoAction6Pro" {
		t.Fatalf("readback lost: %+v", result.After)
	}
	if result.Action != "start" {
		t.Fatalf("action not stamped: %q", result.Action)
	}

	// A non-zero native code is preserved, not turned into success or a guess.
	rej, err := decodeNativeHotspot([]byte(`{"status":"ok","native_code":-1010}`), "start")
	if err != nil || rej.NativeCode == nil || *rej.NativeCode != -1010 {
		t.Fatalf("non-zero native code not preserved verbatim: %+v %v", rej, err)
	}

	// Firmware/unavailable statuses survive as reasons, not fabricated success.
	fw, err := decodeNativeHotspot([]byte(`{"status":"unsupported_firmware","reason":"x"}`), "start")
	if err != nil || fw.Status != "unsupported_firmware" {
		t.Fatalf("firmware gate status lost: %+v %v", fw, err)
	}
}

func TestDecodeNativeHotspotRejectsGarbageAndStatusless(t *testing.T) {
	for _, bad := range []string{``, `null`, `{}`, `{"native_code":0}`, `not json`, `[]`} {
		if result, err := decodeNativeHotspot([]byte(bad), "start"); err == nil {
			t.Fatalf("statusless/garbage became a result: %q -> %+v", bad, result)
		}
	}
}

func TestValidNativeHotspotActionRejectsUnknown(t *testing.T) {
	for _, a := range []string{"start", "stop"} {
		if !validNativeHotspotAction(a) {
			t.Fatalf("valid action rejected: %q", a)
		}
	}
	for _, a := range []string{"", "START", "toggle", "on", "off", "restart"} {
		if validNativeHotspotAction(a) {
			t.Fatalf("invalid action accepted: %q", a)
		}
	}
}
