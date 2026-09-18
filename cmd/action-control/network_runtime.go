package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

func belongsToUnit(a *App, pid int, unit string) bool {
	b, err := os.ReadFile(a.Path(fmt.Sprintf("/proc/%d/cgroup", pid)))
	if err != nil {
		return false
	}
	return cgroupHasUnit(b, unit)
}
func cgroupHasUnit(b []byte, unit string) bool {
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.SplitN(line, ":", 3)
		if len(fields) == 3 {
			parts := strings.Split(fields[2], "/")
			if slices.Contains(parts, unit) {
				return true
			}
		}
	}
	return false
}
func networkLock(a *App, wait bool) (*os.File, error) {
	if err := privateDir(a.RunDir); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(a.RunDir, "network.lock"), os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	flags := unix.LOCK_EX
	if !wait {
		flags |= unix.LOCK_NB
	}
	if err = unix.Flock(int(f.Fd()), flags); err != nil {
		f.Close()
		return nil, fmt.Errorf("network operation in progress: %w", err)
	}
	return f, nil
}
func pendingNetwork(a *App) error {
	_, err := os.Lstat(filepath.Join(a.RunDir, "network-request.json"))
	if err == nil {
		return errors.New("network request already queued")
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
func writeNetworkOperation(a *App, op networkOperation) error {
	b, err := json.Marshal(op)
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(a.RunDir, "network-operation.json"), b, 0600)
}
func queueNetwork(a *App, p networkRequest) error {
	if err := requireManagedNetwork(a); err != nil {
		return err
	}
	if err := a.RequireManaged(); err != nil {
		return err
	}
	lock, err := networkLock(a, false)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = requireManagedNetwork(a); err != nil {
		return err
	}
	if err = pendingNetwork(a); err != nil {
		return err
	}
	if !slices.Contains([]string{"connect", "hotspot", "off", "native_start", "native_stop"}, p.Action) {
		return errors.New("invalid network action")
	}
	if p.Action == "connect" {
		if err = validateBSSID(p.BSSID); err != nil {
			return err
		}
		if p.SSID == "" && p.BSSID != "" {
			networks, e := scanWiFi(a, a.Ctx)
			if e != nil {
				return e
			}
			for _, n := range networks {
				if strings.EqualFold(n.BSSID, p.BSSID) {
					p.SSID = n.SSID
					break
				}
			}
		}
		if err = validateSSID(p.SSID); err != nil {
			return err
		}
		if p.Password == nil {
			known, e := readKnown(a)
			if e != nil {
				return e
			}
			for _, n := range known {
				if n.SSID == p.SSID {
					password := n.Password
					p.Password = &password
					break
				}
			}
			if p.Password == nil {
				return errors.New("password required; send an empty string for an open network")
			}
		}
		if err = validatePassword(*p.Password); err != nil {
			return err
		}
	}
	if p.Hotspot != nil {
		if p.Action != "hotspot" {
			return errors.New("hotspot settings require hotspot mode")
		}
		h, _, err := readHotspot(a)
		if err != nil {
			return err
		}
		h.SSID, h.Band, h.Channel = p.Hotspot.SSID, p.Hotspot.Band, p.Hotspot.Channel
		if p.Hotspot.Password != nil {
			h.password = *p.Hotspot.Password
		}
		if err := validateHotspot(h); err != nil {
			return err
		}
	}
	if p.Action == "connect" || p.Action == "hotspot" {
		p.Recovery, err = captureNetworkRecovery(a, p.Hotspot)
		if err != nil {
			return err
		}
	}
	p.ID = rand.Text()
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	requestFile := filepath.Join(a.RunDir, "network-request.json")
	if err = atomicWrite(requestFile, data, 0600); err != nil {
		return err
	}
	op := networkOperation{ID: p.ID, Action: p.Action, Status: "queued"}
	if p.Recovery != nil {
		op.RecoveryMode, op.RecoverySSID = p.Recovery.Mode, p.Recovery.SSID
	}
	if err = writeNetworkOperation(a, op); err != nil {
		_ = os.Remove(requestFile)
		return err
	}
	if _, err = a.Run(a.Ctx, 10*time.Second, "systemctl", "--no-block", "restart", NetworkServiceName); err != nil {
		_ = os.Remove(requestFile)
		op.Status = "failed"
		op.Error = err.Error()
		return errors.Join(err, writeNetworkOperation(a, op))
	}
	return nil
}
func getWiFiStatus(a *App, ctx context.Context) (wifiStatus, error) {
	st := wifiStatus{Mode: "unknown", Role: "unknown", Owner: "unknown", State: "unknown"}
	cfg, err := readNetworkConfig(a)
	if err != nil {
		return st, err
	}
	st.Control = cfg.NetworkControl
	if err := a.RequireDevice(); err != nil {
		return st, err
	}
	iface, err := net.InterfaceByName("wlan0")
	if err != nil {
		return st, err
	}
	st.State = readText(a, "/sys/class/net/wlan0/operstate")
	if iface.Flags&net.FlagUp == 0 {
		st.Mode = "closed"
	}
	addresses, err := iface.Addrs()
	if err != nil {
		return st, err
	}
	for _, address := range addresses {
		if ip, _, e := net.ParseCIDR(address.String()); e == nil && ip.To4() != nil {
			st.IP = ip.String()
			break
		}
	}
	out, err := a.Run(ctx, 5*time.Second, "iw", "dev", "wlan0", "info")
	if err != nil {
		return st, err
	}
	for _, raw := range strings.Split(string(out), "\n") {
		line := strings.TrimLeft(raw, " \t")
		if strings.HasPrefix(line, "type ") && st.Mode != "closed" {
			switch strings.TrimSpace(strings.TrimPrefix(line, "type ")) {
			case "AP":
				st.Mode = "hotspot"
			case "managed":
				st.Mode = "client"
			}
		}
		if strings.HasPrefix(line, "ssid ") {
			st.SSID = decodeSSID(strings.TrimPrefix(line, "ssid "))
		}
	}
	if st.Mode == "client" {
		out, err = a.Run(ctx, 5*time.Second, "iw", "dev", "wlan0", "link")
		if err != nil {
			return st, err
		}
		if strings.Contains(string(out), "Not connected.") {
			st.State = "disconnected"
		} else if strings.Contains(string(out), "Connected to ") {
			st.State = "connected"
		}
		for _, raw := range strings.Split(string(out), "\n") {
			line := strings.TrimLeft(raw, " \t")
			switch {
			case strings.HasPrefix(line, "SSID: "):
				st.SSID = decodeSSID(strings.TrimPrefix(line, "SSID: "))
			case strings.HasPrefix(line, "signal:"):
				st.Signal = firstNumber(strings.TrimPrefix(line, "signal:"))
			case strings.HasPrefix(line, "tx bitrate:"):
				st.TX = firstNumber(strings.TrimPrefix(line, "tx bitrate:"))
			case strings.HasPrefix(line, "rx bitrate:"):
				st.RX = firstNumber(strings.TrimPrefix(line, "rx bitrate:"))
			case strings.HasPrefix(line, "freq:"):
				st.Frequency = firstNumber(strings.TrimPrefix(line, "freq:"))
			}
		}
	}
	st.Role = st.Mode
	native, e := a.Run(ctx, 5*time.Second, "systemctl", "is-active", "dji_network.service")
	if e == nil && strings.TrimSpace(string(native)) == "active" {
		st.Owner = "native"
		// Retain the legacy mode field for existing clients. Role describes the
		// actual interface even when the native service is running with Wi-Fi off.
		st.Mode = "native"
	} else if pids, e := managedNetworkPIDs(a); e == nil && len(pids) > 0 {
		st.Owner = "action-control"
	} else if e == nil && st.Role == "closed" {
		st.Owner = "none"
	}
	if b, e := os.ReadFile(filepath.Join(a.RunDir, "network-operation.json")); e == nil {
		var op networkOperation
		if e = json.Unmarshal(b, &op); e != nil {
			return st, e
		}
		st.Operation = &op
	} else if !errors.Is(e, os.ErrNotExist) {
		return st, e
	}
	return st, nil
}
