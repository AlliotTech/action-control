package main

import (
	"errors"
	"fmt"
	"os"
	"time"
)

type hotspotChange struct {
	SSID     string  `json:"ssid"`
	Password *string `json:"password"`
	Band     string  `json:"band"`
	Channel  int     `json:"channel"`
}

// Only the private queued request contains credentials. Operation status never does.
type networkRecoveryPlan struct {
	Mode        string        `json:"mode"`
	SSID        string        `json:"ssid,omitempty"`
	Client      *knownNetwork `json:"client,omitempty"`
	HotspotData []byte        `json:"hotspot_data,omitempty"`
	WPAData     []byte        `json:"wpa_data,omitempty"`
}

func recoveryForStatus(status wifiStatus, known []knownNetwork, hotspotData []byte) (*networkRecoveryPlan, error) {
	plan := &networkRecoveryPlan{Mode: "unavailable"}
	switch status.Mode {
	case "native":
		plan.Mode, plan.SSID = "native", status.SSID
	case "closed":
		plan.Mode = "closed"
	case "hotspot":
		h, err := parseHotspot(hotspotData)
		if err != nil {
			return nil, err
		}
		if err := validateHotspot(h); err != nil {
			return nil, err
		}
		if h.SSID != status.SSID {
			return plan, nil
		}
		plan.Mode, plan.SSID = "hotspot", h.SSID
		plan.HotspotData = append([]byte(nil), hotspotData...)
	case "client":
		if status.State != "connected" {
			return plan, nil
		}
		for _, network := range known {
			if network.SSID == status.SSID {
				if err := validateSSID(network.SSID); err != nil {
					return nil, err
				}
				if err := validatePassword(network.Password); err != nil {
					return nil, err
				}
				if err := validateBSSID(network.BSSID); err != nil {
					return nil, err
				}
				copy := network
				plan.Mode, plan.SSID, plan.Client = "client", network.SSID, &copy
				break
			}
		}
	}
	return plan, nil
}

func captureNetworkRecovery(a *App, change *hotspotChange) (*networkRecoveryPlan, error) {
	status, err := getWiFiStatus(a, a.Ctx)
	if err != nil {
		return nil, fmt.Errorf("无法确认切换前的网络状态：%w", err)
	}
	var known []knownNetwork
	if status.Mode == "client" {
		known, err = readKnown(a)
		if err != nil {
			return nil, err
		}
	}
	var data []byte
	if status.Mode == "hotspot" || change != nil {
		_, data, err = readHotspot(a)
		if err != nil {
			return nil, err
		}
	}
	plan, err := recoveryForStatus(status, known, data)
	if err == nil && plan.Mode == "native" {
		plan.WPAData, err = os.ReadFile(a.Path("/data/misc/wifi/wpa_supplicant.conf"))
	}
	if err == nil && change != nil {
		// Restore the pre-change configuration even when the previous mode was client/native.
		plan.HotspotData = append([]byte(nil), data...)
	}
	return plan, err
}

func restoreNetwork(a *App, plan *networkRecoveryPlan) error {
	if len(plan.HotspotData) > 0 {
		if err := writeManagedShared(a, hotspotPath, plan.HotspotData, 0600); err != nil {
			return err
		}
	}
	if plan.Mode == "native" {
		if len(plan.WPAData) > 0 {
			if err := writeManagedShared(a, "/data/misc/wifi/wpa_supplicant.conf", plan.WPAData, 0600); err != nil {
				return err
			}
		}
		_, err := a.Run(a.Ctx, 20*time.Second, "systemctl", "restart", "dji_network.service")
		return err
	}
	if plan.Mode == "closed" {
		_, err := a.Run(a.Ctx, 5*time.Second, "ip", "link", "set", "wlan0", "down")
		return err
	}
	if err := prepareWireless(a); err != nil {
		return err
	}
	switch plan.Mode {
	case "client":
		if plan.Client == nil {
			return errors.New("missing previous WiFi credentials")
		}
		return connectWireless(a, *plan.Client)
	case "hotspot":
		return startHotspot(a)
	default:
		return errors.New("没有可恢复的先前连接，请通过 USB ADB 访问")
	}
}

// A restored network must keep its oneshot systemd unit active. Returning the
// original operation error would make systemd kill the restored child processes.
func finishNetworkOperation(op networkOperation, cause error, cleanup func() error, restore func() error, persist func(networkOperation) error) error {
	if cause == nil {
		op.Status = "succeeded"
		if err := persist(op); err != nil {
			fmt.Printf("network ready; status could not be saved: %v\n", err)
		}
		return nil
	}
	op.Status, op.Error = "failed", cause.Error()
	if err := cleanup(); err != nil {
		op.RecoveryStatus = "failed"
		op.Error = errors.Join(cause, err).Error()
		return errors.Join(cause, err, persist(op))
	}
	if restore == nil {
		if op.RecoveryMode != "" {
			op.RecoveryStatus = "unavailable"
		}
		return errors.Join(cause, persist(op))
	}
	op.Status, op.RecoveryStatus = "running", "restoring"
	if err := persist(op); err != nil {
		fmt.Printf("network recovery status: %v\n", err)
	}
	err := restore()
	op.Status = "failed"
	if err != nil {
		op.RecoveryStatus = "failed"
		cleanupErr := cleanup()
		op.Error = errors.Join(cause, fmt.Errorf("恢复先前网络失败：%w", err), cleanupErr).Error()
		return errors.Join(cause, err, cleanupErr, persist(op))
	}
	op.RecoveryStatus = "restored"
	if err := persist(op); err != nil {
		// Preserve the working connection even if the status file cannot be saved.
		fmt.Printf("network restored; status could not be saved: %v\n", err)
	}
	return nil
}
