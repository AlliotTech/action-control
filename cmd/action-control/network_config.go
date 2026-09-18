package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const knownPath = "/data/misc/wifi/known.json"
const hotspotPath = "/data/misc/wifi/user_config.conf"

type knownNetwork struct {
	SSID          string `json:"ssid"`
	Password      string `json:"password"`
	BSSID         string `json:"bssid,omitempty"`
	LastConnected string `json:"last_connected,omitempty"`
}
type wifiNetwork struct {
	SSID      string   `json:"ssid"`
	BSSID     string   `json:"bssid"`
	Signal    *float64 `json:"signal"`
	Frequency int      `json:"freq"`
	Band      string   `json:"band"`
	Encrypted bool     `json:"encrypted"`
}
type networkRequest struct {
	ID       string               `json:"id"`
	Action   string               `json:"action"`
	SSID     string               `json:"ssid,omitempty"`
	Password *string              `json:"password,omitempty"`
	BSSID    string               `json:"bssid,omitempty"`
	Hotspot  *hotspotChange       `json:"hotspot,omitempty"`
	Recovery *networkRecoveryPlan `json:"recovery,omitempty"`
}
type networkOperation struct {
	ID             string `json:"id"`
	Action         string `json:"action"`
	Status         string `json:"status"`
	Error          string `json:"error,omitempty"`
	RecoveryStatus string `json:"recovery_status,omitempty"`
	RecoveryMode   string `json:"recovery_mode,omitempty"`
	RecoverySSID   string `json:"recovery_ssid,omitempty"`
}
type wifiStatus struct {
	Mode      string            `json:"mode"`
	State     string            `json:"state"`
	SSID      string            `json:"ssid"`
	IP        string            `json:"ip"`
	Signal    *float64          `json:"signal"`
	TX        *float64          `json:"tx_bitrate_mbps"`
	RX        *float64          `json:"rx_bitrate_mbps"`
	Frequency *float64          `json:"freq_mhz"`
	Operation *networkOperation `json:"operation,omitempty"`
}
type hotspotSettings struct {
	SSID              string `json:"ssid"`
	Band              string `json:"band"`
	Channel           int    `json:"channel"`
	Open              bool   `json:"open"`
	HasPassword       bool   `json:"has_password"`
	password, country string
}

func validateSSID(ssid string) error {
	if len(ssid) < 1 || len(ssid) > 32 || !utf8.ValidString(ssid) || strings.ContainsAny(ssid, "\x00\r\n") {
		return errors.New("SSID must contain 1–32 UTF-8 bytes without NUL or line breaks")
	}
	return nil
}
func validatePassword(password string) error {
	if password == "" {
		return nil
	}
	if len(password) == 64 {
		if _, err := hex.DecodeString(password); err == nil {
			return nil
		}
	}
	if len(password) < 8 || len(password) > 63 || strings.ContainsAny(password, "\x00\r\n") || !utf8.ValidString(password) {
		return errors.New("WPA2 password must contain 8–63 bytes, or be a 64-digit hexadecimal PSK")
	}
	return nil
}
func validateBSSID(value string) error {
	if value == "" {
		return nil
	}
	address, err := net.ParseMAC(value)
	if err != nil || len(address) != 6 || address[0]&1 != 0 || strings.ToLower(value) != address.String() || value == "00:00:00:00:00:00" {
		return errors.New("BSSID must be a nonzero unicast address in colon-separated form")
	}
	return nil
}
func readKnown(a *App) ([]knownNetwork, error) {
	b, err := os.ReadFile(a.Path(knownPath))
	if errors.Is(err, os.ErrNotExist) {
		return []knownNetwork{}, nil
	}
	if err != nil {
		return nil, err
	}
	var items []knownNetwork
	if err = json.Unmarshal(b, &items); err != nil {
		return nil, err
	}
	if items == nil {
		items = []knownNetwork{}
	}
	for _, item := range items {
		if err = validateSSID(item.SSID); err != nil {
			return nil, err
		}
		if err = validatePassword(item.Password); err != nil {
			return nil, err
		}
		if err = validateBSSID(item.BSSID); err != nil {
			return nil, err
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].LastConnected > items[j].LastConnected })
	return items, nil
}
func saveKnown(a *App, items []knownNetwork) error {
	b, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	return writeManagedShared(a, knownPath, append(b, '\n'), 0600)
}
func decodeSSID(value string) string {
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		if value[i] == '\\' && i+3 < len(value) && value[i+1] == 'x' {
			if n, err := strconv.ParseUint(value[i+2:i+4], 16, 8); err == nil {
				out.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		out.WriteByte(value[i])
	}
	return out.String()
}
func firstNumber(value string) *float64 {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return nil
	}
	n, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
		return nil
	}
	return &n
}
func parseScan(output string) []wifiNetwork {
	result := []wifiNetwork{}
	var current *wifiNetwork
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimLeft(strings.TrimSuffix(raw, "\r"), " \t")
		if strings.HasPrefix(line, "BSS ") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				current = nil
				continue
			}
			bssid := strings.SplitN(fields[1], "(", 2)[0]
			if validateBSSID(bssid) != nil {
				current = nil
				continue
			}
			result = append(result, wifiNetwork{BSSID: bssid})
			current = &result[len(result)-1]
			continue
		}
		if current == nil {
			continue
		}
		switch {
		case strings.HasPrefix(line, "SSID: "):
			current.SSID = decodeSSID(strings.TrimPrefix(line, "SSID: "))
		case strings.HasPrefix(line, "freq:"):
			if n := firstNumber(strings.TrimPrefix(line, "freq:")); n != nil {
				current.Frequency = int(*n)
				switch {
				case *n >= 5925:
					current.Band = "6G"
				case *n >= 4900:
					current.Band = "5G"
				case *n > 0:
					current.Band = "2.4G"
				}
			}
		case strings.HasPrefix(line, "signal:"):
			current.Signal = firstNumber(strings.TrimPrefix(line, "signal:"))
		case strings.Contains(line, "Privacy"), strings.HasPrefix(line, "RSN:"), strings.HasPrefix(line, "WPA:"):
			current.Encrypted = true
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Signal == nil {
			return false
		}
		if result[j].Signal == nil {
			return true
		}
		return *result[i].Signal > *result[j].Signal
	})
	return result
}
func scanWiFi(a *App, ctx context.Context) ([]wifiNetwork, error) {
	out, err := a.Run(ctx, 15*time.Second, "iw", "dev", "wlan0", "scan")
	if err != nil {
		return nil, err
	}
	return parseScan(string(out)), nil
}
