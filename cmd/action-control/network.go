package main

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

func configValues(data []byte) map[string]string {
	values := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if key, value, ok := strings.Cut(strings.TrimSuffix(line, "\r"), "="); ok {
			values[key] = value
		}
	}
	return values
}
func readHotspot(a *App) (hotspotSettings, []byte, error) {
	data, err := os.ReadFile(a.Path(hotspotPath))
	if err != nil {
		return hotspotSettings{}, nil, err
	}
	h, err := parseHotspot(data)
	return h, data, err
}
func parseHotspot(data []byte) (hotspotSettings, error) {
	values := configValues(data)
	h := hotspotSettings{SSID: values["ssid"], password: values["passwd"], country: values["country"]}
	switch values["band"] {
	case "0":
		h.Band = "2.4G"
	case "1", "2":
		h.Band = "5G"
	default:
		return h, errors.New("unknown native hotspot band")
	}
	channel, err := strconv.Atoi(values["channel"])
	h.Channel = channel
	if err != nil {
		return h, err
	}
	h.Open = h.password == ""
	h.HasPassword = !h.Open
	return h, nil
}
func validateHotspot(h hotspotSettings) error {
	if err := validateSSID(h.SSID); err != nil {
		return err
	}
	if err := validatePassword(h.password); err != nil {
		return err
	}
	if h.Band == "2.4G" {
		if h.Channel < 1 || h.Channel > 14 {
			return errors.New("2.4G channel must be 1–14")
		}
	} else if h.Band == "5G" {
		if !slices.Contains([]int{36, 40, 44, 48, 52, 56, 60, 64, 100, 104, 108, 112, 116, 120, 124, 128, 132, 136, 140, 144, 149, 153, 157, 161, 165}, h.Channel) {
			return errors.New("invalid 5G channel")
		}
	} else {
		return errors.New("band must be 2.4G or 5G")
	}
	if len(h.country) != 2 || h.country[0] < 'A' || h.country[0] > 'Z' || h.country[1] < 'A' || h.country[1] > 'Z' {
		return errors.New("invalid native country code")
	}
	return nil
}
func setHotspot(a *App, h hotspotSettings, data []byte) error {
	if err := requireManagedNetwork(a); err != nil {
		return err
	}
	if err := validateHotspot(h); err != nil {
		return err
	}
	band := "0"
	if h.Band == "5G" {
		band = "1"
		if h.Channel >= 149 {
			band = "2"
		}
	}
	updates := map[string]string{"ssid": h.SSID, "passwd": h.password, "band": band, "channel": strconv.Itoa(h.Channel), "channel_auto": "0"}
	seen := map[string]bool{}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	for i, line := range lines {
		key, _, ok := strings.Cut(strings.TrimSuffix(line, "\r"), "=")
		if value, exists := updates[key]; ok && exists {
			if seen[key] {
				return errors.New("duplicate hotspot configuration key")
			}
			lines[i] = key + "=" + value
			seen[key] = true
		}
	}
	for _, key := range []string{"ssid", "passwd", "band", "channel", "channel_auto"} {
		if !seen[key] {
			lines = append(lines, key+"="+updates[key])
		}
	}
	return writeManagedShared(a, hotspotPath, []byte(strings.Join(lines, "\n")+"\n"), 0600)
}
func wpaConfig(a *App, n knownNetwork) ([]byte, error) {
	if err := validateSSID(n.SSID); err != nil {
		return nil, err
	}
	if err := validateBSSID(n.BSSID); err != nil {
		return nil, err
	}
	if err := validatePassword(n.Password); err != nil {
		return nil, err
	}
	body := "ctrl_interface=" + filepath.Join(a.RunDir, "wpa") + "\nupdate_config=0\nap_scan=1\nnetwork={\n ssid=" + hex.EncodeToString([]byte(n.SSID)) + "\n scan_ssid=1\n"
	if n.BSSID != "" {
		mac, _ := net.ParseMAC(n.BSSID)
		body += " bssid=" + mac.String() + "\n"
	}
	if n.Password == "" {
		body += " key_mgmt=NONE\n"
	} else {
		psk := n.Password
		if len(psk) != 64 {
			key, err := pbkdf2.Key(sha1.New, psk, []byte(n.SSID), 4096, 32)
			if err != nil {
				return nil, err
			}
			psk = hex.EncodeToString(key)
		}
		body += " key_mgmt=WPA-PSK\n psk=" + psk + "\n"
	}
	return []byte(body + "}\n"), nil
}
func stopOwnedNetwork(a *App) error {
	processes, err := readProcesses(a)
	if err != nil {
		return err
	}
	var stopped []ProcessInfo
	for _, p := range processes {
		if p.PID == os.Getpid() || !belongsToUnit(a, p.PID, NetworkServiceName) {
			continue
		}
		if err = signalProcess(p.PID, p.Start, 9, a); err != nil && !errors.Is(err, unix.ESRCH) && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		stopped = append(stopped, p)
	}
	deadline := time.Now().Add(4 * time.Second)
	for _, p := range stopped {
		for {
			current, e := readProcess(a, p.PID)
			if errors.Is(e, os.ErrNotExist) || (e == nil && current.Start != p.Start) {
				break
			}
			if e != nil {
				return e
			}
			if time.Now().After(deadline) {
				return errors.New("network child did not exit")
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	return nil
}
func prepareWireless(a *App) error {
	if err := requireManagedNetwork(a); err != nil {
		return err
	}
	if err := a.MarkRebootRequired("无线运行态已更改", false); err != nil {
		return err
	}
	if _, err := a.Run(a.Ctx, 20*time.Second, "systemctl", "stop", "dji_network.service"); err != nil {
		return err
	}
	processes, err := readProcesses(a)
	if err != nil {
		return err
	}
	for _, p := range processes {
		if slices.Contains([]string{"wpa_supplicant", "hostapd", "dnsmasq", "udhcpc"}, p.Name) && !belongsToUnit(a, p.PID, NetworkServiceName) {
			return fmt.Errorf("network tool PID %d is not owned by Action Control; refusing takeover", p.PID)
		}
	}
	for _, args := range [][]string{{"link", "set", "wlan0", "down"}, {"addr", "flush", "dev", "wlan0"}, {"link", "set", "wlan0", "up"}} {
		if _, err = a.Run(a.Ctx, 5*time.Second, "ip", args...); err != nil {
			return err
		}
	}
	return nil
}
func wirelessStatus(a *App, directory string) (map[string]string, error) {
	local := filepath.Join(a.RunDir, "ctrl-"+rand.Text()[:12])
	defer os.Remove(local)
	conn, err := net.DialUnix("unixgram", &net.UnixAddr{Name: local, Net: "unixgram"}, &net.UnixAddr{Name: filepath.Join(a.RunDir, directory, "wlan0"), Net: "unixgram"})
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err = conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return nil, err
	}
	if _, err = conn.Write([]byte("STATUS")); err != nil {
		return nil, err
	}
	buffer := make([]byte, 8192)
	n, err := conn.Read(buffer)
	if err != nil {
		return nil, err
	}
	return configValues(buffer[:n]), nil
}
func connectWireless(a *App, n knownNetwork) error {
	if err := requireManagedNetwork(a); err != nil {
		return err
	}
	data, err := wpaConfig(a, n)
	if err != nil {
		return err
	}
	if err = privateDir(filepath.Join(a.RunDir, "wpa")); err != nil {
		return err
	}
	conf := filepath.Join(a.RunDir, "client.conf")
	if err = atomicWrite(conf, data, 0600); err != nil {
		return err
	}
	if _, err = a.Run(a.Ctx, 8*time.Second, "wpa_supplicant", "-B", "-i", "wlan0", "-c", conf, "-P", filepath.Join(a.RunDir, "wpa.pid")); err != nil {
		return err
	}
	deadline := time.Now().Add(25 * time.Second)
	associated := false
	for time.Now().Before(deadline) {
		values, e := wirelessStatus(a, "wpa")
		if e == nil && values["wpa_state"] == "COMPLETED" && decodeSSID(values["ssid"]) == n.SSID && (n.BSSID == "" || strings.EqualFold(n.BSSID, values["bssid"])) {
			associated = true
			break
		}
		select {
		case <-time.After(time.Second):
		case <-a.Ctx.Done():
			return a.Ctx.Err()
		}
	}
	if !associated {
		return errors.New("wireless association did not complete; check network, BSSID and password")
	}
	leaseFile := filepath.Join(a.RunDir, "lease.json")
	if err = os.Remove(leaseFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err = a.Run(a.Ctx, 12*time.Second, "udhcpc", "-b", "-i", "wlan0", "-p", filepath.Join(a.RunDir, "dhcp.pid"), "-s", filepath.Join(a.Dir, "dhcp-hook.sh"), "-t", "3", "-T", "2"); err != nil {
		return err
	}
	deadline = time.Now().Add(20 * time.Second)
	leased := false
	for time.Now().Before(deadline) {
		var lease struct {
			IP string `json:"ip"`
		}
		if b, e := os.ReadFile(leaseFile); e == nil && json.Unmarshal(b, &lease) == nil && net.ParseIP(lease.IP) != nil {
			st, e := getWiFiStatus(a, a.Ctx)
			if e == nil && st.State == "connected" && st.SSID == n.SSID && st.IP == lease.IP {
				leased = true
				break
			}
		}
		select {
		case <-time.After(time.Second):
		case <-a.Ctx.Done():
			return a.Ctx.Err()
		}
	}
	if !leased {
		return errors.New("associated but no verified DHCP lease; network operation failed")
	}
	if err = writeManagedShared(a, "/data/misc/wifi/wpa_supplicant.conf", data, 0600); err != nil {
		return err
	}
	known, err := readKnown(a)
	if err != nil {
		return err
	}
	n.LastConnected = time.Now().UTC().Format(time.RFC3339)
	found := false
	for i := range known {
		if known[i].SSID == n.SSID {
			known[i] = n
			found = true
			break
		}
	}
	if !found {
		known = append(known, n)
	}
	return saveKnown(a, known)
}
func startHotspot(a *App) error {
	if err := requireManagedNetwork(a); err != nil {
		return err
	}
	h, _, err := readHotspot(a)
	if err != nil {
		return err
	}
	if err = validateHotspot(h); err != nil {
		return err
	}
	mode := "g"
	if h.Band == "5G" {
		mode = "a"
	}
	conf := fmt.Sprintf("interface=wlan0\ndriver=nl80211\nctrl_interface=%s\nssid2=%s\nchannel=%d\nhw_mode=%s\ncountry_code=%s\nauth_algs=1\nwmm_enabled=1\n", filepath.Join(a.RunDir, "hostapd"), hex.EncodeToString([]byte(h.SSID)), h.Channel, mode, h.country)
	if h.password == "" {
		conf += "wpa=0\n"
	} else {
		psk := h.password
		if len(psk) != 64 {
			key, e := pbkdf2.Key(sha1.New, psk, []byte(h.SSID), 4096, 32)
			if e != nil {
				return e
			}
			psk = hex.EncodeToString(key)
		}
		conf += "wpa=2\nwpa_psk=" + psk + "\nwpa_key_mgmt=WPA-PSK\nrsn_pairwise=CCMP\nieee80211w=1\n"
	}
	name := filepath.Join(a.RunDir, "hostapd.conf")
	if err = atomicWrite(name, []byte(conf), 0600); err != nil {
		return err
	}
	if _, err = a.Run(a.Ctx, 5*time.Second, "ip", "addr", "add", "192.168.2.1/24", "dev", "wlan0"); err != nil {
		return err
	}
	if _, err = a.Run(a.Ctx, 8*time.Second, "hostapd", "-B", "-P", filepath.Join(a.RunDir, "hostapd.pid"), name); err != nil {
		return err
	}
	if _, err = a.Run(a.Ctx, 8*time.Second, "dnsmasq", "--conf-file=/dev/null", "--interface=wlan0", "--bind-interfaces", "--port=0", "--dhcp-range=192.168.2.2,192.168.2.254,255.255.255.0,12h", "--dhcp-option=3", "--dhcp-option=6", "--dhcp-leasefile="+filepath.Join(a.RunDir, "hotspot.leases"), "--pid-file="+filepath.Join(a.RunDir, "dnsmasq.pid")); err != nil {
		return err
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		values, e := wirelessStatus(a, "hostapd")
		if e == nil && values["state"] == "ENABLED" {
			return nil
		}
		time.Sleep(time.Second)
	}
	return errors.New("hotspot did not reach ENABLED state")
}
func RunNetwork(a *App) (err error) {
	cfg, err := readNetworkConfig(a)
	if err != nil {
		return err
	}
	if cfg.NetworkControl == networkControlNative {
		return discardNativeNetworkRequest(a)
	}
	if err = a.RequireManaged(); err != nil {
		return err
	}
	if !belongsToUnit(a, os.Getpid(), NetworkServiceName) {
		return errors.New("network command must run inside its systemd network unit")
	}
	lock, err := networkLock(a, true)
	if err != nil {
		return err
	}
	defer lock.Close()
	cfg, err = readNetworkConfig(a)
	if err != nil {
		return err
	}
	if cfg.NetworkControl == networkControlNative {
		return discardNativeNetworkRequestLocked(a)
	}
	name := filepath.Join(a.RunDir, "network-request.json")
	data, err := os.ReadFile(name)
	automatic := errors.Is(err, os.ErrNotExist)
	var request networkRequest
	if automatic {
		if !cfg.AutoConnect {
			return nil
		}
		request = networkRequest{ID: rand.Text(), Action: "client"}
	} else {
		if err != nil {
			return err
		}
		if err = json.Unmarshal(data, &request); err != nil {
			return err
		}
		if err = os.Remove(name); err != nil {
			return err
		}
	}
	op := networkOperation{ID: request.ID, Action: request.Action, Status: "running"}
	if request.Recovery != nil {
		op.RecoveryMode, op.RecoverySSID = request.Recovery.Mode, request.Recovery.SSID
	}
	defer func() {
		var restore func() error
		if automatic {
			op.RecoveryMode = "native"
			restore = func() error { return restoreNetwork(a, &networkRecoveryPlan{Mode: "native"}) }
		} else if request.Recovery != nil && request.Recovery.Mode != "unavailable" {
			restore = func() error { return restoreNetwork(a, request.Recovery) }
		}
		err = finishNetworkOperation(op, err, func() error { return stopOwnedNetwork(a) }, restore, func(value networkOperation) error { return writeNetworkOperation(a, value) })
	}()
	if err = writeNetworkOperation(a, op); err != nil {
		return err
	}
	for _, tool := range []string{"ip", "iw", "systemctl"} {
		if _, err = a.Tool(tool); err != nil {
			return err
		}
	}
	switch request.Action {
	case "connect", "client":
		for _, tool := range []string{"wpa_supplicant", "udhcpc"} {
			if _, err = a.Tool(tool); err != nil {
				return err
			}
		}
	case "hotspot":
		for _, tool := range []string{"hostapd", "dnsmasq"} {
			if _, err = a.Tool(tool); err != nil {
				return err
			}
		}
		h, data, e := readHotspot(a)
		if e != nil {
			return e
		}
		if request.Hotspot != nil {
			h.SSID, h.Band, h.Channel = request.Hotspot.SSID, request.Hotspot.Band, request.Hotspot.Channel
			if request.Hotspot.Password != nil {
				h.password = *request.Hotspot.Password
			}
		}
		if e = validateHotspot(h); e != nil {
			return e
		}
		if request.Hotspot != nil {
			if e = setHotspot(a, h, data); e != nil {
				return e
			}
		}
	case "off", "native_start", "native_stop":
	default:
		return errors.New("invalid queued network action")
	}
	if err = prepareWireless(a); err != nil {
		return err
	}
	switch request.Action {
	case "connect":
		if request.Password == nil {
			return errors.New("queued password is missing")
		}
		return connectWireless(a, knownNetwork{SSID: request.SSID, Password: *request.Password, BSSID: request.BSSID})
	case "client":
		known, e := readKnown(a)
		if e != nil {
			return e
		}
		visible, e := scanWiFi(a, a.Ctx)
		if e != nil {
			return e
		}
		for _, saved := range known {
			for _, network := range visible {
				if saved.SSID == network.SSID && (saved.BSSID == "" || strings.EqualFold(saved.BSSID, network.BSSID)) {
					return connectWireless(a, saved)
				}
			}
		}
		return errors.New("no visible saved network; scan and connect explicitly")
	case "hotspot":
		return startHotspot(a)
	case "native_start":
		_, err = a.Run(a.Ctx, 20*time.Second, "systemctl", "restart", "dji_network.service")
		return err
	case "off", "native_stop":
		_, err = a.Run(a.Ctx, 5*time.Second, "ip", "link", "set", "wlan0", "down")
		return err
	}
	return nil
}

// udhcpc invokes this through the installed hook, inside the independent network cgroup.
func RunDHCP(a *App, args []string) error {
	if err := requireManagedNetwork(a); err != nil {
		return err
	}
	if err := a.RequireManaged(); err != nil {
		return err
	}
	if !belongsToUnit(a, os.Getpid(), NetworkServiceName) || os.Getenv("interface") != "wlan0" {
		return errors.New("DHCP hook requires the managed wlan0 network unit")
	}
	if len(args) != 1 {
		return errors.New("DHCP event required")
	}
	leaseFile := filepath.Join(a.RunDir, "lease.json")
	if args[0] == "deconfig" {
		_, err := a.Run(a.Ctx, 5*time.Second, "ip", "addr", "flush", "dev", "wlan0")
		e := os.Remove(leaseFile)
		if errors.Is(e, os.ErrNotExist) {
			e = nil
		}
		return errors.Join(err, e)
	}
	if args[0] != "bound" && args[0] != "renew" {
		return nil
	}
	ip := net.ParseIP(os.Getenv("ip"))
	mask := net.ParseIP(os.Getenv("subnet"))
	if ip == nil || ip.To4() == nil || ip.IsUnspecified() || ip.IsMulticast() || mask == nil || mask.To4() == nil {
		return errors.New("invalid DHCP IPv4 address or mask")
	}
	ones, bits := net.IPMask(mask.To4()).Size()
	if bits != 32 {
		return errors.New("noncontiguous DHCP subnet mask")
	}
	routers := strings.Fields(os.Getenv("router"))
	var router string
	if len(routers) > 0 {
		parsed := net.ParseIP(routers[0])
		if parsed == nil || parsed.To4() == nil || parsed.IsUnspecified() || parsed.IsMulticast() {
			return errors.New("invalid DHCP router")
		}
		router = parsed.String()
	}
	dns := strings.Fields(os.Getenv("dns"))
	if len(dns) > 8 {
		return errors.New("too many DHCP DNS servers")
	}
	resolv := ""
	for _, value := range dns {
		parsed := net.ParseIP(value)
		if parsed == nil || parsed.IsUnspecified() || parsed.IsMulticast() {
			return errors.New("invalid DHCP DNS server")
		}
		resolv += "nameserver " + parsed.String() + "\n"
	}
	if _, err := a.Run(a.Ctx, 5*time.Second, "ip", "addr", "replace", fmt.Sprintf("%s/%d", ip.String(), ones), "dev", "wlan0"); err != nil {
		return err
	}
	if router != "" {
		if _, err := a.Run(a.Ctx, 5*time.Second, "ip", "route", "replace", "default", "via", router, "dev", "wlan0", "metric", "600"); err != nil {
			return err
		}
	}
	if err := writeManagedShared(a, "/etc/resolv.conf", []byte(resolv), 0644); err != nil {
		return err
	}
	b, err := json.Marshal(map[string]string{"ip": ip.String()})
	if err != nil {
		return err
	}
	return atomicWrite(leaseFile, b, 0600)
}

func RegisterNetwork(mux *http.ServeMux, a *App) (func() error, error) {
	mux.HandleFunc("GET /api/wifi_status", func(w http.ResponseWriter, r *http.Request) {
		st, err := getWiFiStatus(a, r.Context())
		if err != nil {
			jsonError(w, 503, "wifi_status", err)
			return
		}
		jsonResponse(w, 200, st)
	})
	hotspot := &nativeHotspotController{app: a}
	mux.HandleFunc("POST /api/native_hotspot", hotspot.handle)
	mux.HandleFunc("GET /api/wifi_scan", func(w http.ResponseWriter, r *http.Request) {
		if err := requireManagedNetwork(a); err != nil {
			networkJSONError(w, 409, "network_control", err)
			return
		}
		if err := a.RequireDevice(); err != nil {
			jsonError(w, 503, "device_unavailable", err)
			return
		}
		lock, err := networkLock(a, false)
		if err != nil {
			jsonError(w, 409, "network_busy", err)
			return
		}
		defer lock.Close()
		if err = pendingNetwork(a); err != nil {
			jsonError(w, 409, "network_busy", err)
			return
		}
		networks, err := scanWiFi(a, r.Context())
		if err != nil {
			networkJSONError(w, 502, "wifi_scan", err)
			return
		}
		jsonResponse(w, 200, map[string]any{"networks": networks})
	})
	for route, action := range map[string]string{"wifi_connect": "connect", "wifi_off": "off", "hotspot_on": "hotspot", "hotspot_off": "off"} {
		mux.HandleFunc("POST /api/"+route, func(w http.ResponseWriter, r *http.Request) {
			var input struct {
				SSID     string  `json:"ssid"`
				Password *string `json:"password"`
				BSSID    string  `json:"bssid"`
				Confirm  bool    `json:"confirm"`
			}
			if !readJSON(w, r, &input) {
				return
			}
			if !input.Confirm {
				jsonError(w, 400, "confirmation_required", errors.New("network changes can disconnect this browser; confirmation required"))
				return
			}
			if err := queueNetwork(a, networkRequest{Action: action, SSID: input.SSID, Password: input.Password, BSSID: input.BSSID}); err != nil {
				networkJSONError(w, 409, "network_operation", err)
				return
			}
			jsonResponse(w, 202, map[string]any{"ok": true, "status": "queued"})
		})
	}
	mux.HandleFunc("POST /api/hotspot_apply", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			hotspotChange
			Confirm bool `json:"confirm"`
		}
		if !readJSON(w, r, &input) {
			return
		}
		if !input.Confirm {
			jsonError(w, 400, "confirmation_required", errors.New("切换热点需要明确确认"))
			return
		}
		if err := queueNetwork(a, networkRequest{Action: "hotspot", Hotspot: &input.hotspotChange}); err != nil {
			networkJSONError(w, 409, "network_operation", err)
			return
		}
		jsonResponse(w, 202, map[string]any{"ok": true, "status": "queued"})
	})
	mux.HandleFunc("GET /api/known_list", func(w http.ResponseWriter, r *http.Request) {
		items, err := readKnown(a)
		if err != nil {
			jsonError(w, 500, "known_networks", err)
			return
		}
		result := []map[string]any{}
		for _, n := range items {
			result = append(result, map[string]any{"ssid": n.SSID, "bssid": n.BSSID, "last_connected": n.LastConnected, "has_password": n.Password != ""})
		}
		jsonResponse(w, 200, map[string]any{"networks": result})
	})
	mux.HandleFunc("POST /api/known_forget", func(w http.ResponseWriter, r *http.Request) {
		if err := requireManagedNetwork(a); err != nil {
			networkJSONError(w, 409, "network_control", err)
			return
		}
		var p struct {
			SSID    string `json:"ssid"`
			Confirm bool   `json:"confirm"`
		}
		if !readJSON(w, r, &p) {
			return
		}
		if !p.Confirm {
			jsonError(w, 400, "confirmation_required", errors.New("confirmation required"))
			return
		}
		if err := a.RequireManaged(); err != nil {
			jsonError(w, 503, "device_unavailable", err)
			return
		}
		lock, err := networkLock(a, false)
		if err != nil {
			jsonError(w, 409, "network_busy", err)
			return
		}
		defer lock.Close()
		if err = pendingNetwork(a); err != nil {
			jsonError(w, 409, "network_busy", err)
			return
		}
		items, err := readKnown(a)
		if err != nil {
			jsonError(w, 500, "known_networks", err)
			return
		}
		before := len(items)
		items = slices.DeleteFunc(items, func(n knownNetwork) bool { return n.SSID == p.SSID })
		if len(items) == before {
			jsonError(w, 404, "unknown_network", errors.New("saved network not found"))
			return
		}
		if err = saveKnown(a, items); err != nil {
			networkJSONError(w, 500, "known_write", err)
			return
		}
		jsonResponse(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/hotspot_config", func(w http.ResponseWriter, r *http.Request) {
		settings, _, err := readHotspot(a)
		if err != nil {
			fileError(w, err)
			return
		}
		jsonResponse(w, 200, settings)
	})
	mux.HandleFunc("POST /api/hotspot_config", func(w http.ResponseWriter, r *http.Request) {
		if err := requireManagedNetwork(a); err != nil {
			networkJSONError(w, 409, "network_control", err)
			return
		}
		var p struct {
			SSID     string  `json:"ssid"`
			Password *string `json:"password"`
			Band     string  `json:"band"`
			Channel  int     `json:"channel"`
		}
		if !readJSON(w, r, &p) {
			return
		}
		if err := a.RequireManaged(); err != nil {
			jsonError(w, 503, "device_unavailable", err)
			return
		}
		lock, err := networkLock(a, false)
		if err != nil {
			jsonError(w, 409, "network_busy", err)
			return
		}
		defer lock.Close()
		if err = pendingNetwork(a); err != nil {
			jsonError(w, 409, "network_busy", err)
			return
		}
		h, data, err := readHotspot(a)
		if err != nil {
			fileError(w, err)
			return
		}
		h.SSID, h.Band, h.Channel = p.SSID, p.Band, p.Channel
		if p.Password != nil {
			h.password = *p.Password
		}
		if err = setHotspot(a, h, data); err != nil {
			networkJSONError(w, 400, "hotspot_config", err)
			return
		}
		jsonResponse(w, 200, map[string]any{"ok": true, "restart_required": true})
	})
	return nil, nil
}
