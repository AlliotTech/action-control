package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type ProcessInfo struct {
	PID     int      `json:"pid"`
	PPID    int      `json:"ppid"`
	Name    string   `json:"name"`
	Command string   `json:"cmd"`
	User    string   `json:"user"`
	RSS     uint64   `json:"rss_bytes"`
	Virtual uint64   `json:"vsz_bytes"`
	CPU     *float64 `json:"cpu_percent"`
	Start   string   `json:"start_time"`
	ticks   uint64
}

func readProcess(a *App, pid int) (ProcessInfo, error) {
	base := a.Path(fmt.Sprintf("/proc/%d", pid))
	b, err := os.ReadFile(filepath.Join(base, "stat"))
	if err != nil {
		return ProcessInfo{}, err
	}
	line := string(b)
	left, right := strings.Index(line, "("), strings.LastIndex(line, ") ")
	if left < 0 || right < left {
		return ProcessInfo{}, errors.New("invalid process stat")
	}
	f := strings.Fields(line[right+2:])
	if len(f) < 22 {
		return ProcessInfo{}, errors.New("incomplete process stat")
	}
	p := ProcessInfo{PID: pid, Name: line[left+1 : right], Start: f[19]}
	p.PPID, _ = strconv.Atoi(f[1])
	p.Virtual, _ = strconv.ParseUint(f[20], 10, 64)
	rss, _ := strconv.ParseUint(f[21], 10, 64)
	p.RSS = rss * uint64(os.Getpagesize())
	ut, _ := strconv.ParseUint(f[11], 10, 64)
	st, _ := strconv.ParseUint(f[12], 10, 64)
	p.ticks = ut + st
	cmd, err := os.ReadFile(filepath.Join(base, "cmdline"))
	if err != nil {
		return ProcessInfo{}, err
	}
	p.Command = strings.TrimSpace(strings.ReplaceAll(string(cmd), "\x00", " "))
	if p.Command == "" {
		p.Command = "[" + p.Name + "]"
	}
	status, _ := os.ReadFile(filepath.Join(base, "status"))
	for _, line := range strings.Split(string(status), "\n") {
		if strings.HasPrefix(line, "Uid:") {
			values := strings.Fields(line)
			if len(values) > 1 {
				p.User = values[1]
				if p.User == "0" {
					p.User = "root"
				}
			}
		}
	}
	return p, nil
}
func readProcesses(a *App) ([]ProcessInfo, error) {
	entries, err := os.ReadDir(a.Path("/proc"))
	if err != nil {
		return nil, err
	}
	out := []ProcessInfo{}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		p, err := readProcess(a, pid)
		if err == nil {
			out = append(out, p)
		}
	}
	return out, nil
}
func cpuTicks(a *App) (total, busy uint64, err error) {
	b, err := os.ReadFile(a.Path("/proc/stat"))
	if err != nil {
		return 0, 0, err
	}
	fields := strings.Fields(strings.SplitN(string(b), "\n", 2)[0])
	if len(fields) < 5 {
		return 0, 0, errors.New("invalid CPU statistics")
	}
	var idle uint64
	// Guest counters are already included in user/nice; do not count them twice.
	for i, value := range fields[1:min(len(fields), 9)] {
		n, e := strconv.ParseUint(value, 10, 64)
		if e != nil {
			return 0, 0, e
		}
		total += n
		if i == 3 || i == 4 {
			idle += n
		}
	}
	return total, total - idle, nil
}
func readNumber(a *App, name string, scale float64) *float64 {
	b, err := os.ReadFile(a.Path(name))
	if err != nil {
		return nil
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
	if err != nil {
		return nil
	}
	value := n / scale
	return &value
}
func readText(a *App, name string) string {
	b, _ := os.ReadFile(a.Path(name))
	return strings.TrimSpace(string(b))
}

type systemController struct {
	app          *App
	mu           sync.Mutex
	total, busy  uint64
	processTotal uint64
	processes    map[string]uint64
	children     map[*exec.Cmd]chan struct{}
	closed       bool
}

func (s *systemController) info(w http.ResponseWriter, r *http.Request) {
	a := s.app
	var cpu *float64
	total, busy, err := cpuTicks(a)
	if err == nil {
		s.mu.Lock()
		if total > s.total && s.total > 0 && busy >= s.busy {
			v := float64(busy-s.busy) / float64(total-s.total) * 100
			cpu = &v
		}
		s.total, s.busy = total, busy
		s.mu.Unlock()
	}
	var memoryTotal, memoryUsed *uint64
	if b, err := os.ReadFile(a.Path("/proc/meminfo")); err == nil {
		values := map[string]uint64{}
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if len(f) >= 2 {
				n, e := strconv.ParseUint(f[1], 10, 64)
				if e == nil {
					values[strings.TrimSuffix(f[0], ":")] = n * 1024
				}
			}
		}
		if total, ok := values["MemTotal"]; ok {
			memoryTotal = &total
			if available, ok := values["MemAvailable"]; ok && available <= total {
				used := total - available
				memoryUsed = &used
			}
		}
	}
	var uptime *float64
	if f := strings.Fields(readText(a, "/proc/uptime")); len(f) > 0 {
		if v, e := strconv.ParseFloat(f[0], 64); e == nil {
			uptime = &v
		}
	}
	jsonResponse(w, 200, map[string]any{"uptime_sec": uptime, "kernel": readText(a, "/proc/sys/kernel/osrelease"), "cpu_percent": cpu, "mem_total": memoryTotal, "mem_used": memoryUsed, "battery_percent": readNumber(a, "/sys/class/power_supply/battery/capacity", 1), "battery_status": readText(a, "/sys/class/power_supply/battery/status"), "battery_temp_c": readNumber(a, "/sys/class/power_supply/battery/temp", 10), "battery_current_ma": readNumber(a, "/sys/class/power_supply/battery/current_now", 1000), "cpu_temp_c": readNumber(a, "/sys/class/thermal/thermal_zone31/temp", 1000), "device": a.RequireDevice() == nil, "reboot_required": a.RebootRequired()})
}
func (s *systemController) processList(w http.ResponseWriter, r *http.Request) {
	processes, err := readProcesses(s.app)
	if err != nil {
		jsonError(w, 503, "processes_unavailable", err)
		return
	}
	total, _, _ := cpuTicks(s.app)
	s.mu.Lock()
	next := map[string]uint64{}
	for i := range processes {
		p := &processes[i]
		key := fmt.Sprintf("%d:%s", p.PID, p.Start)
		next[key] = p.ticks
		if previous, ok := s.processes[key]; ok && p.ticks >= previous && total > s.processTotal && s.processTotal > 0 {
			percent := float64(p.ticks-previous) / float64(total-s.processTotal) * 100
			p.CPU = &percent
		}
	}
	s.processes = next
	s.processTotal = total
	s.mu.Unlock()
	jsonResponse(w, 200, map[string]any{"processes": processes, "count": len(processes)})
}
func (s *systemController) kill(w http.ResponseWriter, r *http.Request) {
	var p struct {
		PID     int    `json:"pid"`
		Signal  int    `json:"signal"`
		Start   string `json:"start_time"`
		Confirm bool   `json:"confirm"`
	}
	if !readJSON(w, r, &p) {
		return
	}
	if !p.Confirm || p.PID <= 1 || p.PID == os.Getpid() || (p.Signal != 15 && p.Signal != 9) || p.Start == "" {
		jsonError(w, 400, "invalid_process_signal", errors.New("explicit PID, start time, confirmation and signal 15/9 required"))
		return
	}
	if err := s.app.RequireManaged(); err != nil {
		jsonError(w, 503, "device_unavailable", err)
		return
	}
	current, err := readProcess(s.app, p.PID)
	if err != nil {
		fileError(w, err)
		return
	}
	if current.Start != p.Start {
		jsonError(w, 409, "process_changed", errors.New("PID was reused; refresh process list"))
		return
	}
	if current.Name == "adbd" || current.Name == "systemd" {
		jsonError(w, 403, "protected_process", errors.New("init and ADB processes are protected"))
		return
	}
	if strings.HasPrefix(current.Name, "dji_") || strings.HasPrefix(current.Name, "qmmf") || strings.HasPrefix(current.Name, "gui") {
		if err = s.app.MarkRebootRequired("原生进程被终止", current.Name != "dji_network"); err != nil {
			fileError(w, err)
			return
		}
	}
	if err = signalProcess(p.PID, p.Start, p.Signal, s.app); err != nil {
		jsonError(w, 409, "process_signal", err)
		return
	}
	jsonResponse(w, 200, map[string]any{"ok": true, "pid": p.PID, "signal": p.Signal})
}
func (s *systemController) command(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Command string `json:"cmd"`
		CWD     string `json:"cwd"`
		Timeout int    `json:"timeout"`
		Confirm bool   `json:"confirm"`
	}
	if !readJSON(w, r, &p) {
		return
	}
	if err := s.app.RequireManaged(); err != nil {
		jsonError(w, 503, "device_unavailable", err)
		return
	}
	if strings.TrimSpace(p.Command) == "" {
		jsonError(w, 400, "empty_command", errors.New("command required"))
		return
	}
	if p.CWD == "" {
		p.CWD = "/blackbox"
	}
	info, err := os.Stat(p.CWD)
	if err != nil || !filepath.IsAbs(p.CWD) || !info.IsDir() {
		jsonError(w, 400, "invalid_cwd", errors.New("working directory must exist and be absolute"))
		return
	}
	background := r.URL.Path == "/api/process_start"
	if background && !p.Confirm {
		jsonError(w, 400, "confirmation_required", errors.New("explicit confirmation required"))
		return
	}
	if background {
		cmd := ownedCommand(s.app.Ctx, "/bin/sh", "-c", p.Command)
		cmd.Dir = p.CWD
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			jsonError(w, 503, "shutting_down", errors.New("service is stopping"))
			return
		}
		if len(s.children) >= 16 {
			s.mu.Unlock()
			jsonError(w, 409, "process_limit", errors.New("at most 16 managed background commands"))
			return
		}
		if err = cmd.Start(); err != nil {
			s.mu.Unlock()
			jsonError(w, 500, "process_start", err)
			return
		}
		done := make(chan struct{})
		s.children[cmd] = done
		s.mu.Unlock()
		go func() { _ = cmd.Wait(); s.mu.Lock(); delete(s.children, cmd); s.mu.Unlock(); close(done) }()
		jsonResponse(w, 202, map[string]any{"ok": true, "pid": cmd.Process.Pid})
		return
	}
	if p.Timeout == 0 {
		p.Timeout = 15
	}
	if p.Timeout < 1 || p.Timeout > 120 {
		jsonError(w, 400, "invalid_timeout", errors.New("timeout must be 1–120 seconds"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(p.Timeout)*time.Second)
	defer cancel()
	cmd := ownedCommand(ctx, "/bin/sh", "-c", p.Command)
	cmd.Dir = p.CWD
	stdout, stderr := &boundedBuffer{limit: 1 << 20}, &boundedBuffer{limit: 1 << 20}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err = cmd.Run()
	exit := 0
	if err != nil {
		var failure *exec.ExitError
		if errors.As(err, &failure) {
			exit = failure.ExitCode()
		} else if ctx.Err() != nil {
			exit = -1
		} else {
			jsonError(w, 500, "command_failed", err)
			return
		}
	}
	result := map[string]any{"stdout": stdout.Text(), "stderr": stderr.Text(), "exit_code": exit, "cwd": p.CWD}
	if ctx.Err() != nil {
		result["stderr"] = stderr.Text() + "\n命令超时或请求取消，已终止进程组"
	}
	jsonResponse(w, 200, result)
}
func RegisterSystem(mux *http.ServeMux, a *App) (func() error, error) {
	s := &systemController{app: a, processes: map[string]uint64{}, children: map[*exec.Cmd]chan struct{}{}}
	mux.HandleFunc("GET /api/sysinfo", s.info)
	mux.HandleFunc("GET /api/process_list", s.processList)
	mux.HandleFunc("POST /api/process_kill", s.kill)
	mux.HandleFunc("POST /api/process_start", s.command)
	mux.HandleFunc("POST /api/shell_exec", s.command)
	mux.HandleFunc("POST /api/restart_dashboard", func(w http.ResponseWriter, r *http.Request) {
		if !confirmRequest(w, r, a) {
			return
		}
		jsonResponse(w, 202, map[string]any{"ok": true, "status": "restart_requested"})
		_ = http.NewResponseController(w).Flush()
		select {
		case a.Restart <- struct{}{}:
		default:
		}
	})
	mux.HandleFunc("POST /api/reboot", func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Mode    string `json:"mode"`
			Confirm bool   `json:"confirm"`
		}
		if !readJSON(w, r, &p) {
			return
		}
		if !p.Confirm {
			jsonError(w, 400, "confirmation_required", errors.New("explicit confirmation required"))
			return
		}
		if err := a.RequireManaged(); err != nil {
			jsonError(w, 503, "device_unavailable", err)
			return
		}
		modes := map[string]bool{"normal": true, "recovery": true, "bootloader": true, "edl": true}
		if !modes[p.Mode] {
			jsonError(w, 400, "reboot_mode", errors.New("unknown reboot mode"))
			return
		}
		program, err := a.Tool("reboot")
		if err != nil {
			jsonError(w, 503, "reboot_unavailable", err)
			return
		}
		args := []string{}
		if p.Mode != "normal" {
			args = append(args, p.Mode)
		}
		cmd := exec.Command(program, args...)
		if err = cmd.Start(); err != nil {
			jsonError(w, 500, "reboot_failed", err)
			return
		}
		go func() {
			if err := cmd.Wait(); err != nil {
				fmt.Fprintln(os.Stderr, "reboot command:", err)
			}
		}()
		jsonResponse(w, 202, map[string]any{"ok": true, "status": "reboot_requested"})
		_ = http.NewResponseController(w).Flush()
	})
	mux.HandleFunc("GET /api/dji_network", func(w http.ResponseWriter, r *http.Request) {
		if err := a.RequireDevice(); err != nil {
			jsonError(w, 503, "device_unavailable", err)
			return
		}
		out, err := a.Run(r.Context(), 5*time.Second, "systemctl", "is-active", "dji_network.service")
		status := strings.TrimSpace(string(out))
		if err != nil && status != "inactive" && status != "failed" {
			jsonError(w, 502, "service_status", err)
			return
		}
		jsonResponse(w, 200, map[string]string{"status": status})
	})
	mux.HandleFunc("POST /api/dji_network", func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Action  string `json:"action"`
			Confirm bool   `json:"confirm"`
		}
		if !readJSON(w, r, &p) {
			return
		}
		if !p.Confirm || (p.Action != "start" && p.Action != "stop") {
			jsonError(w, 400, "invalid_action", errors.New("start/stop and explicit confirmation required"))
			return
		}
		if err := a.RequireManaged(); err != nil {
			jsonError(w, 503, "device_unavailable", err)
			return
		}
		if err := queueNetwork(a, networkRequest{Action: "native_" + p.Action}); err != nil {
			jsonError(w, 409, "network_operation", err)
			return
		}
		jsonResponse(w, 202, map[string]any{"ok": true, "status": "queued"})
	})
	mux.HandleFunc("POST /api/clear_cache", func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Confirm bool `json:"confirm"`
		}
		if !readJSON(w, r, &p) {
			return
		}
		if !p.Confirm {
			jsonError(w, 400, "confirmation_required", errors.New("confirmation required"))
			return
		}
		entries, err := os.ReadDir(filepath.Join(a.RunDir, "thumbnails"))
		if err != nil {
			fileError(w, err)
			return
		}
		removed := 0
		for _, entry := range entries {
			if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".jpg") {
				if err = os.Remove(filepath.Join(a.RunDir, "thumbnails", entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
					fileError(w, err)
					return
				}
				removed++
			}
		}
		jsonResponse(w, 200, map[string]any{"ok": true, "removed": removed})
	})
	mux.HandleFunc("POST /api/clear_logs", func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Confirm bool `json:"confirm"`
		}
		if !readJSON(w, r, &p) {
			return
		}
		if !p.Confirm {
			jsonError(w, 400, "confirmation_required", errors.New("confirmation required"))
			return
		}
		for _, name := range []string{"action-control.log", "camera.log", "network.log"} {
			file := filepath.Join(a.RunDir, name)
			if st, err := os.Lstat(file); errors.Is(err, os.ErrNotExist) {
				continue
			} else if err != nil {
				fileError(w, err)
				return
			} else if !st.Mode().IsRegular() {
				jsonError(w, 409, "log_changed", errors.New("unexpected log file type"))
				return
			}
			if err := os.Truncate(file, 0); err != nil {
				fileError(w, err)
				return
			}
		}
		jsonResponse(w, 200, map[string]bool{"ok": true})
	})
	return func() error {
		s.mu.Lock()
		s.closed = true
		children := make(map[*exec.Cmd]chan struct{}, len(s.children))
		for cmd, done := range s.children {
			children[cmd] = done
		}
		s.mu.Unlock()
		var err error
		for cmd, done := range children {
			if cmd.Process != nil {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				err = errors.Join(err, errors.New("background process did not exit"))
			}
		}
		return err
	}, nil
}
