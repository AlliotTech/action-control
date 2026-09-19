package main

import (
	"errors"
	"net/http"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

// consoleURL is the fixed web console address served on the native AP subnet.
const consoleURL = "http://192.168.2.1:8080"

// escapeWiFi backslash-escapes the characters the WIFI QR grammar treats as
// delimiters, so an SSID/password containing them still round-trips.
func escapeWiFi(s string) string {
	return strings.NewReplacer(
		`\`, `\\`,
		`;`, `\;`,
		`,`, `\,`,
		`:`, `\:`,
		`"`, `\"`,
	).Replace(s)
}

// wifiQRContent builds the standard WIFI: join payload phones auto-connect from.
func wifiQRContent(h hotspotSettings) string {
	auth, pass := "WPA", escapeWiFi(h.password)
	if h.password == "" {
		auth, pass = "nopass", ""
	}
	return "WIFI:S:" + escapeWiFi(h.SSID) + ";T:" + auth + ";P:" + pass + ";;"
}

// qrMatrix renders content to a module matrix as "0"/"1" row strings. Rows of
// chars parse trivially in the C GUI plugin (no nested-array JSON walk) and the
// go-qrcode Bitmap already carries the 4-module quiet zone.
func qrMatrix(content string) (int, []string, error) {
	code, err := qrcode.New(content, qrcode.Medium)
	if err != nil {
		return 0, nil, err
	}
	bmp := code.Bitmap()
	rows := make([]string, len(bmp))
	for y, row := range bmp {
		var b strings.Builder
		b.Grow(len(row))
		for _, on := range row {
			if on {
				b.WriteByte('1')
			} else {
				b.WriteByte('0')
			}
		}
		rows[y] = b.String()
	}
	return len(bmp), rows, nil
}

// hotspotQRHandler serves the module matrix for the Wi-Fi join or console URL QR.
// The Wi-Fi payload embeds the AP password; the response deliberately omits the
// plaintext content so it never lands in logs — the matrix itself is the code.
func hotspotQRHandler(a *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var content string
		switch r.URL.Query().Get("kind") {
		case "url":
			content = consoleURL
		case "wifi":
			h, _, err := readHotspot(a)
			if err != nil {
				fileError(w, err)
				return
			}
			content = wifiQRContent(h)
		default:
			jsonError(w, 400, "invalid_kind", errors.New("需要 kind=wifi|url"))
			return
		}
		size, matrix, err := qrMatrix(content)
		if err != nil {
			jsonError(w, 500, "qr_encode", err)
			return
		}
		jsonResponse(w, 200, map[string]any{"size": size, "matrix": matrix})
	}
}
