package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	networkControlNative  = "native"
	networkControlManaged = "managed"
)

var errNativeNetworkControl = errors.New("当前使用相机原生网络，请在相机上设置无线连接；独立网络控制仅在高级管理模式可用")
var errManagedNetworkBusy = errors.New("Action Control 仍在管理网络，请先交还原生网络，待操作完成后再切换管理方式或升级")

func networkJSONError(w http.ResponseWriter, status int, code string, err error) {
	switch {
	case errors.Is(err, errNativeNetworkControl):
		status, code = http.StatusConflict, "native_network_control"
	case errors.Is(err, errManagedNetworkBusy):
		status, code = http.StatusConflict, "network_control_busy"
	}
	jsonError(w, status, code, err)
}

// Workers and DHCP hooks are separate processes. Read the current policy from
// disk instead of trusting a possibly older HTTP process's cached ConfigStore.
func readNetworkConfig(a *App) (Config, error) {
	data, err := os.ReadFile(filepath.Join(a.Dir, "config.json"))
	if errors.Is(err, os.ErrNotExist) {
		return defaultConfig(), nil
	}
	if err != nil {
		return Config{}, err
	}
	return decodeConfig(data)
}

func requireManagedNetwork(a *App) error {
	c, err := readNetworkConfig(a)
	if err != nil {
		return err
	}
	if c.NetworkControl != networkControlManaged {
		return errNativeNetworkControl
	}
	return nil
}

// A request left by an older process must not spring back to life when managed
// mode is enabled later. Only our own queue/status files are changed here.
func discardNativeNetworkRequest(a *App) error {
	name := filepath.Join(a.RunDir, "network-request.json")
	if _, err := os.Lstat(name); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	lock, err := networkLock(a, true)
	if err != nil {
		return err
	}
	defer lock.Close()
	// Another process may have changed policy while we waited for the lock.
	if err = requireManagedNetwork(a); !errors.Is(err, errNativeNetworkControl) {
		return err
	}
	return discardNativeNetworkRequestLocked(a)
}

func discardNativeNetworkRequestLocked(a *App) error {
	name := filepath.Join(a.RunDir, "network-request.json")
	data, err := os.ReadFile(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var request networkRequest
	if err = json.Unmarshal(data, &request); err != nil {
		return err
	}
	op := networkOperation{ID: request.ID, Action: request.Action, Status: "failed", Error: errNativeNetworkControl.Error()}
	if err = writeNetworkOperation(a, op); err != nil {
		return err
	}
	return os.Remove(name)
}

// Inspect cgroup ownership directly: process names can change, and a failed
// /proc/stat or cmdline read must not hide a live network worker or DHCP hook.
func managedNetworkPIDs(a *App) ([]int, error) {
	entries, err := os.ReadDir(a.Path("/proc"))
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		data, err := os.ReadFile(a.Path(fmt.Sprintf("/proc/%d/cgroup", pid)))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if cgroupHasUnit(data, NetworkServiceName) {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

func checkNativeNetworkIdle(a *App) error {
	pids, err := managedNetworkPIDs(a)
	if err != nil {
		return err
	}
	if len(pids) != 0 {
		return errManagedNetworkBusy
	}
	return nil
}

func checkNetworkControlChange(a *App, previous, next Config) error {
	if err := validateConfig(next); err != nil {
		return err
	}
	if next.NetworkControl != networkControlNative {
		return nil
	}
	if err := checkNativeNetworkIdle(a); err != nil {
		return err
	}
	if previous.NetworkControl == networkControlManaged && a.Root == "/" {
		out, err := a.Run(a.Ctx, 5*time.Second, "systemctl", "is-active", "dji_network.service")
		if err != nil || strings.TrimSpace(string(out)) != "active" {
			return errors.New("请先交还原生网络并确认服务运行，再使用原生管理模式")
		}
	}
	return nil
}

func updateUserConfig(a *App, change func(*Config) error, networkChange bool) error {
	if !networkChange {
		return a.Config.Update(change)
	}
	lock, err := networkLock(a, false)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = pendingNetwork(a); err != nil {
		return err
	}
	return a.Config.Update(func(next *Config) error {
		previous := *next
		if err := change(next); err != nil {
			return err
		}
		return checkNetworkControlChange(a, previous, *next)
	})
}
