package main

import "testing"

func TestWiFiQRContentEscapesDelimiters(t *testing.T) {
	got := wifiQRContent(hotspotSettings{SSID: `My;Net`, password: `p\a:s,s"`})
	want := `WIFI:S:My\;Net;T:WPA;P:p\\a\:s\,s\";;`
	if got != want {
		t.Fatalf("escaping wrong:\n got %q\nwant %q", got, want)
	}
}

func TestWiFiQRContentNoPassword(t *testing.T) {
	got := wifiQRContent(hotspotSettings{SSID: "Open", password: ""})
	if want := "WIFI:S:Open;T:nopass;P:;;"; got != want {
		t.Fatalf("nopass wrong: got %q want %q", got, want)
	}
}

func TestQRMatrixSquareWithQuietZone(t *testing.T) {
	size, rows, err := qrMatrix(consoleURL)
	if err != nil || size == 0 || len(rows) != size {
		t.Fatalf("matrix not square: size=%d rows=%d err=%v", size, len(rows), err)
	}
	for _, r := range rows {
		if len(r) != size {
			t.Fatalf("row width %d != size %d", len(r), size)
		}
	}
}
