package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type CameraStatus struct {
	Source  string  `json:"source"`
	Scope   string  `json:"scope"`
	State   string  `json:"state"`
	Running bool    `json:"running"`
	Width   int     `json:"width"`
	Height  int     `json:"height"`
	FPS     int     `json:"fps"`
	Port    int     `json:"ext_port"`
	Codec   string  `json:"codec"`
	Bitrate float64 `json:"bitrate_kbps"`
	Bytes   uint64  `json:"bytes_sent"`
	Uptime  float64 `json:"uptime_sec"`
	Clients int     `json:"clients"`
	Reboot  bool    `json:"reboot_required"`
	Error   string  `json:"error,omitempty"`
}
type streamClient struct {
	chunks chan []byte
	closed chan struct{}
	conn   net.Conn
}
type cameraRun struct {
	cancel   context.CancelFunc
	listener net.Listener
	cmd      *exec.Cmd
	done     chan struct{}
	first    chan struct{}
	start    time.Time
	bytes    uint64
	clients  map[*streamClient]struct{}
	ended    bool
}
type cameraController struct {
	app          *App
	operation    sync.Mutex
	mu           sync.Mutex
	run          *cameraRun
	state        string
	lastError    string
	config       Config
	screens      []*exec.Cmd
	screenCancel context.CancelFunc
	screenDone   []chan struct{}
	screenState  sync.Mutex
}

func cameraArgs(c Config) []string {
	pixels := float64(c.CamW * c.CamH)
	adjusted := c.CamBitrate * 1000 / max(1, pixels/(1920*1080))
	qi, qp := 35, 37
	switch {
	case adjusted >= 500000:
		qi, qp = 2, 2
	case adjusted >= 50000:
		qi, qp = 6, 8
	case adjusted >= 20000:
		qi, qp = 10, 12
	case adjusted >= 8000:
		qi, qp = 14, 16
	case adjusted >= 3000:
		qi, qp = 18, 20
	case adjusted >= 1500:
		qi, qp = 24, 26
	case adjusted >= 800:
		qi, qp = 30, 32
	}
	buffers := min(32, max(2, int(float64(c.CamFPS)/15*pixels/(1920*1080))))
	return []string{"-q", "djiqmmfsrc", "camera=0", "!", fmt.Sprintf("video/x-raw,width=%d,height=%d,format=NV12,framerate=%d/1", c.CamW, c.CamH, c.CamFPS), "!", "djicamc2venc", "idr-interval=30", "control-rate=disable", fmt.Sprintf("quant-i-frames=%d", qi), fmt.Sprintf("quant-p-frames=%d", qp), "!", "video/x-h264,stream-format=byte-stream,alignment=au", "!", "h264parse", "config-interval=-1", "!", "mpegtsmux", "alignment=1", "!", "queue", fmt.Sprintf("max-size-buffers=%d", buffers), "!", "fdsink", "fd=1", "sync=false"}
}
func stopNativeServices(a *App, takeoverNative bool) ([]string, error) {
	if !takeoverNative {
		return nil, errNativeCameraProtected
	}
	out, err := a.Run(a.Ctx, 10*time.Second, "systemctl", "list-units", "--type=service", "--all", "--no-legend", "--plain")
	if err != nil {
		return nil, err
	}
	services := []string{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		name := fields[0]
		if name == "dji_network.service" {
			continue
		}
		if strings.HasPrefix(name, "dji_") || name == "gui.service" || name == "sub_gui.service" || name == "qmmf-server.service" {
			services = append(services, name)
		}
	}
	if len(services) == 0 {
		return nil, errors.New("no native camera services found; refusing an unverified takeover")
	}
	if err := a.MarkRebootRequired("原生相机服务已停止", true); err != nil {
		return nil, err
	}
	args := append([]string{"stop"}, services...)
	_, err = a.Run(a.Ctx, 40*time.Second, "systemctl", args...)
	return services, err
}
func (c *cameraController) status() CameraStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	cfg := c.config
	st := CameraStatus{Source: "action-control", Scope: "independent_capture", State: c.state, Running: c.state == "running", Width: cfg.CamW, Height: cfg.CamH, FPS: cfg.CamFPS, Port: cfg.CamExtPort, Codec: "H.264/AVC", Reboot: c.app.RebootRequired(), Error: c.lastError}
	if c.run != nil {
		st.Bytes = c.run.bytes
		st.Clients = len(c.run.clients)
		st.Uptime = time.Since(c.run.start).Seconds()
		if st.Uptime > 0 {
			st.Bitrate = float64(st.Bytes) * 8 / st.Uptime / 1000
		}
	}
	return st
}
func (c *cameraController) dropLocked(run *cameraRun, client *streamClient) {
	if _, ok := run.clients[client]; !ok {
		return
	}
	delete(run.clients, client)
	close(client.closed)
	close(client.chunks)
	if client.conn != nil {
		_ = client.conn.Close()
	}
}
func (c *cameraController) subscribe(conn net.Conn) (*cameraRun, *streamClient, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	run := c.run
	if run == nil || run.ended || c.state != "running" {
		return nil, nil, errors.New("camera is not producing a stream")
	}
	if len(run.clients) >= 16 {
		return nil, nil, errors.New("stream client limit reached")
	}
	client := &streamClient{chunks: make(chan []byte, 16), closed: make(chan struct{}), conn: conn}
	run.clients[client] = struct{}{}
	return run, client, nil
}
func (c *cameraController) unsubscribe(run *cameraRun, client *streamClient) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dropLocked(run, client)
}
func (c *cameraController) relay(run *cameraRun, source io.ReadCloser) {
	defer source.Close()
	first := true
	last := time.Time{}
	for {
		chunk := make([]byte, 32<<10)
		n, err := source.Read(chunk)
		if n > 0 {
			chunk = chunk[:n]
			c.mu.Lock()
			run.bytes += uint64(n)
			for client := range run.clients {
				select {
				case client.chunks <- chunk:
				default:
					c.dropLocked(run, client)
				}
			}
			if first {
				first = false
				close(run.first)
			}
			c.mu.Unlock()
			if time.Since(last) > 2*time.Second {
				c.writeScreenState()
				last = time.Now()
			}
		}
		if err != nil {
			return
		}
	}
}
func (c *cameraController) accept(run *cameraRun) {
	for {
		conn, err := run.listener.Accept()
		if err != nil {
			return
		}
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.SetNoDelay(true)
		}
		owned, client, err := c.subscribe(conn)
		if err != nil {
			conn.Close()
			continue
		}
		go func() {
			defer c.unsubscribe(owned, client)
			for {
				select {
				case <-client.closed:
					return
				default:
				}
				select {
				case <-client.closed:
					return
				case chunk, ok := <-client.chunks:
					if !ok {
						return
					}
					_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
					if _, err := conn.Write(chunk); err != nil {
						return
					}
				}
			}
		}()
	}
}
func (c *cameraController) writeScreenState() {
	c.screenState.Lock()
	defer c.screenState.Unlock()
	st := c.status()
	b, err := json.Marshal(st)
	if err == nil {
		if err = atomicWrite(filepath.Join(c.app.RunDir, "camera-status.json"), b, 0600); err != nil {
			fmt.Fprintln(os.Stderr, "camera status:", err)
		}
	}
}
func (c *cameraController) ensureScreens() error {
	alive := 0
	for _, done := range c.screenDone {
		select {
		case <-done:
		default:
			alive++
		}
	}
	if alive == 2 {
		return nil
	}
	if c.screenCancel != nil {
		c.screenCancel()
		for _, done := range c.screenDone {
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				return errors.New("previous screen process did not stop")
			}
		}
	}
	c.screens = nil
	c.screenDone = nil
	program, err := c.app.Tool("weston-terminal")
	if err != nil {
		return err
	}
	shell := filepath.Join(c.app.RunDir, "screen.sh")
	if err = atomicWrite(shell, []byte("#!/bin/sh\nexec "+AppDir+"/action-control screen\n"), 0700); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(c.app.Ctx)
	c.screenCancel = cancel
	for range 2 {
		cmd := ownedCommand(ctx, program, "-f", "--font-size=24", "--shell="+shell)
		cmd.Env = append(os.Environ(), "WAYLAND_DISPLAY=wayland-1", "XDG_RUNTIME_DIR=/run/user/root")
		if err = cmd.Start(); err != nil {
			cancel()
			return err
		}
		done := make(chan struct{})
		c.screens = append(c.screens, cmd)
		c.screenDone = append(c.screenDone, done)
		go func() { _ = cmd.Wait(); close(done) }()
	}
	return nil
}
func (c *cameraController) start(w http.ResponseWriter, r *http.Request) {
	var data struct {
		cameraTakeoverRequest
		Width   int     `json:"width"`
		Height  int     `json:"height"`
		FPS     int     `json:"fps"`
		Port    int     `json:"ext_port"`
		Bitrate float64 `json:"bitrate"`
		Quality int     `json:"quality"`
	}
	if !readJSON(w, r, &data) {
		return
	}
	if !requireCameraTakeover(w, data.cameraTakeoverRequest) {
		return
	}
	if !c.operation.TryLock() {
		jsonError(w, 409, "camera_busy", errors.New("camera operation in progress"))
		return
	}
	defer c.operation.Unlock()
	c.mu.Lock()
	busy := c.run != nil
	c.mu.Unlock()
	if busy {
		jsonError(w, 409, "camera_busy", errors.New("camera already started"))
		return
	}
	if err := c.app.RequireManaged(); err != nil {
		jsonError(w, 503, "device_unavailable", err)
		return
	}
	cfg := c.app.Config.Read()
	cfg.CamW, cfg.CamH, cfg.CamFPS, cfg.CamExtPort, cfg.CamBitrate, cfg.CamQuality = data.Width, data.Height, data.FPS, data.Port, data.Bitrate, data.Quality
	if err := validateConfig(cfg); err != nil {
		jsonError(w, 400, "camera_parameters", err)
		return
	}
	gst, err := c.app.Tool("gst-launch-1.0")
	if err != nil {
		jsonError(w, 503, "missing_dependency", err)
		return
	}
	qmmf, err := c.app.Tool("qmmf-server")
	if err != nil {
		jsonError(w, 503, "missing_dependency", err)
		return
	}
	if _, err = c.app.Tool("weston-terminal"); err != nil {
		jsonError(w, 503, "missing_dependency", err)
		return
	}
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.CamExtPort))
	if err != nil {
		jsonError(w, 409, "stream_port_busy", err)
		return
	}
	keepListener := false
	defer func() {
		if !keepListener {
			listener.Close()
		}
	}()
	if err = c.app.Config.Update(func(current *Config) error {
		current.CamW = cfg.CamW
		current.CamH = cfg.CamH
		current.CamFPS = cfg.CamFPS
		current.CamExtPort = cfg.CamExtPort
		current.CamQuality = cfg.CamQuality
		current.CamBitrate = cfg.CamBitrate
		return nil
	}); err != nil {
		jsonError(w, 500, "config_write", err)
		return
	}
	c.mu.Lock()
	c.state = "starting"
	c.lastError = ""
	c.config = cfg
	c.mu.Unlock()
	fail := func(err error) {
		c.mu.Lock()
		c.state = "error"
		c.lastError = err.Error()
		c.mu.Unlock()
		c.writeScreenState()
		jsonError(w, 502, "camera_start_failed", err)
	}
	if _, err = stopNativeServices(c.app, data.TakeoverNative); err != nil {
		fail(err)
		return
	}
	if err = c.ensureScreens(); err != nil {
		fail(err)
		return
	}
	ctx, cancel := context.WithCancel(c.app.Ctx)
	// A native service was stopped above; an independently owned QMMF process is reused, never killed by name.
	qdone := make(chan struct{})
	processes, err := readProcesses(c.app)
	if err != nil {
		cancel()
		fail(err)
		return
	}
	found := false
	for _, p := range processes {
		if p.Name == "qmmf-server" {
			found = true
			break
		}
	}
	if found {
		close(qdone)
	} else {
		qcmd := ownedCommand(ctx, qmmf)
		if err = qcmd.Start(); err != nil {
			cancel()
			fail(err)
			return
		}
		go func() { _ = qcmd.Wait(); close(qdone) }()
		defer func() {
			if !keepListener {
				cancel()
				select {
				case <-qdone:
				case <-time.After(3 * time.Second):
				}
			}
		}()
		select {
		case <-qdone:
			cancel()
			fail(errors.New("QMMF exited during startup"))
			return
		case <-time.After(2 * time.Second):
		case <-ctx.Done():
			cancel()
			fail(ctx.Err())
			return
		}
	}
	cmd := ownedCommand(ctx, gst, cameraArgs(cfg)...)
	stderr := &boundedBuffer{limit: 64 << 10}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		fail(err)
		return
	}
	if err = cmd.Start(); err != nil {
		cancel()
		stdout.Close()
		fail(err)
		return
	}
	run := &cameraRun{cancel: cancel, listener: listener, cmd: cmd, done: make(chan struct{}), first: make(chan struct{}), start: time.Now(), clients: map[*streamClient]struct{}{}}
	c.mu.Lock()
	c.run = run
	c.mu.Unlock()
	keepListener = true
	relayDone := make(chan struct{})
	go func() { c.relay(run, stdout); close(relayDone) }()
	go c.accept(run)
	go func() {
		err := cmd.Wait()
		cancel()
		listener.Close()
		c.mu.Lock()
		run.ended = true
		for client := range run.clients {
			c.dropLocked(run, client)
		}
		if c.run == run && c.state != "stopping" {
			c.state = "error"
			c.lastError = fmt.Sprintf("capture exited: %v %s", err, stderr.Text())
		}
		c.mu.Unlock()
		<-relayDone
		<-qdone
		close(run.done)
		c.writeScreenState()
	}()
	select {
	case <-run.first:
		c.mu.Lock()
		if run.ended {
			c.mu.Unlock()
			_ = c.stop()
			fail(errors.New("capture exited during startup"))
			return
		}
		c.state = "running"
		c.mu.Unlock()
		if err := saveCameraPreset(c.app, cfg); err != nil {
			c.mu.Lock()
			if c.state == "running" {
				c.lastError = "采集已启动，但成功参数未能保存：" + err.Error()
			}
			c.mu.Unlock()
		}
		c.writeScreenState()
		jsonResponse(w, 200, c.status())
	case <-run.done:
		_ = c.stop()
		fail(fmt.Errorf("capture produced no data: %s", stderr.Text()))
	case <-time.After(15 * time.Second):
		_ = c.stop()
		fail(errors.New("capture did not produce data within 15 seconds"))
	case <-c.app.Ctx.Done():
		_ = c.stop()
		fail(c.app.Ctx.Err())
	}
}
func (c *cameraController) stop() error {
	c.mu.Lock()
	run := c.run
	if run == nil {
		c.state = "stopped"
		c.mu.Unlock()
		return nil
	}
	c.state = "stopping"
	run.cancel()
	run.listener.Close()
	for client := range run.clients {
		c.dropLocked(run, client)
	}
	c.mu.Unlock()
	select {
	case <-run.done:
	case <-time.After(5 * time.Second):
		return errors.New("capture did not stop")
	}
	c.mu.Lock()
	if c.run == run {
		c.run = nil
		c.state = "stopped"
		c.lastError = ""
	}
	c.mu.Unlock()
	c.writeScreenState()
	return nil
}
func (c *cameraController) stream(w http.ResponseWriter, r *http.Request) {
	run, client, err := c.subscribe(nil)
	if err != nil {
		jsonError(w, 503, "camera_stopped", err)
		return
	}
	defer c.unsubscribe(run, client)
	w.Header().Set("Content-Type", "video/MP2T")
	w.Header().Set("Cache-Control", "no-store")
	controller := http.NewResponseController(w)
	defer controller.SetWriteDeadline(time.Time{})
	for {
		select {
		case <-client.closed:
			return
		default:
		}
		select {
		case <-r.Context().Done():
			return
		case <-client.closed:
			return
		case chunk, ok := <-client.chunks:
			if !ok {
				return
			}
			if controller.SetWriteDeadline(time.Now().Add(5*time.Second)) != nil {
				return
			}
			if _, err = w.Write(chunk); err != nil {
				return
			}
			if controller.Flush() != nil {
				return
			}
		}
	}
}
func (c *cameraController) snapshot(w http.ResponseWriter, r *http.Request) {
	st := c.status()
	if !st.Running {
		jsonError(w, 409, "camera_stopped", errors.New("start capture before taking a snapshot"))
		return
	}
	program, err := c.app.Tool("ffmpeg")
	if err != nil {
		jsonError(w, 503, "missing_dependency", err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	cmd := ownedCommand(ctx, program, "-v", "error", "-threads", "2", "-skip_frame", "nokey", "-fflags", "nobuffer", "-flags", "low_delay", "-i", fmt.Sprintf("tcp://127.0.0.1:%d", st.Port), "-frames:v", "1", "-threads", "2", "-f", "image2pipe", "-c:v", "mjpeg", "-q:v", "5", "pipe:1")
	stdout, stderr := &boundedBuffer{limit: 8 << 20}, &boundedBuffer{limit: 64 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err = cmd.Run(); err != nil || stdout.truncated || stdout.Len() == 0 {
		jsonError(w, 502, "snapshot_failed", fmt.Errorf("snapshot: %v %s", err, stderr.Text()))
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Disposition", "attachment; filename=snapshot.jpg")
	_, _ = w.Write(stdout.Bytes())
}
func RegisterCamera(mux *http.ServeMux, a *App) (func() error, error) {
	c := &cameraController{app: a, state: "stopped", config: a.Config.Read()}
	if a.RequireDevice() == nil && a.ScreenRequired() {
		if err := c.ensureScreens(); err != nil {
			return nil, err
		}
		c.writeScreenState()
	}
	mux.HandleFunc("GET /api/camera_status", func(w http.ResponseWriter, r *http.Request) { jsonResponse(w, 200, c.status()) })
	mux.HandleFunc("GET /api/native_camera_status", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, 200, readNativeCameraStatus(r.Context(), a))
	})
	nativeRecording := &nativeRecordingController{camera: c}
	mux.HandleFunc("POST /api/native_recording", nativeRecording.handle)
	mux.HandleFunc("POST /api/camera_start", c.start)
	mux.HandleFunc("GET /api/camera_presets", func(w http.ResponseWriter, r *http.Request) {
		preset, err := readCameraPreset(a)
		if err != nil {
			jsonError(w, 500, "camera_preset", err)
			return
		}
		jsonResponse(w, 200, map[string]any{"last_success": preset})
	})
	mux.HandleFunc("POST /api/camera_stop", func(w http.ResponseWriter, r *http.Request) {
		if !c.operation.TryLock() {
			jsonError(w, 409, "camera_busy", errors.New("camera operation in progress"))
			return
		}
		defer c.operation.Unlock()
		if err := c.stop(); err != nil {
			jsonError(w, 500, "camera_stop", err)
			return
		}
		jsonResponse(w, 200, c.status())
	})
	mux.HandleFunc("GET /api/camera_stream", c.stream)
	mux.HandleFunc("POST /api/camera_snapshot", c.snapshot)
	mux.HandleFunc("POST /api/dji_kill_all", func(w http.ResponseWriter, r *http.Request) {
		var data cameraTakeoverRequest
		if !readJSON(w, r, &data) || !requireCameraTakeover(w, data) {
			return
		}
		if err := a.RequireManaged(); err != nil {
			jsonError(w, 503, "device_unavailable", err)
			return
		}
		if !c.operation.TryLock() {
			jsonError(w, 409, "camera_busy", errors.New("camera operation in progress"))
			return
		}
		defer c.operation.Unlock()
		if c.status().Running {
			jsonError(w, 409, "camera_running", errors.New("stop capture first"))
			return
		}
		services, err := stopNativeServices(a, data.TakeoverNative)
		if err == nil {
			err = c.ensureScreens()
		}
		c.writeScreenState()
		if err != nil {
			jsonError(w, 502, "native_stop", err)
			return
		}
		jsonResponse(w, 200, map[string]any{"ok": true, "services": services, "reboot_required": true})
	})
	return func() error {
		c.operation.Lock()
		defer c.operation.Unlock()
		err := c.stop()
		if c.screenCancel != nil {
			c.screenCancel()
		}
		for _, done := range c.screenDone {
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				err = errors.Join(err, errors.New("screen process did not stop"))
			}
		}
		return err
	}, nil
}
func RunScreen() error {
	for {
		b, err := os.ReadFile(filepath.Join(RunDir, "camera-status.json"))
		var st CameraStatus
		if err == nil {
			err = json.Unmarshal(b, &st)
		}
		fmt.Print("\033[?25l\033[48;5;0m\033[38;5;245m\033[2J\033[H")
		if err == nil && st.Running {
			fmt.Printf("Action Control - Streaming\n\n  Size: %d x %d @ %d fps (configured)\n  Average bitrate: %.2f Mbps\n  TCP port: %d\n  Clients: %d\n", st.Width, st.Height, st.FPS, st.Bitrate/1000, st.Port, st.Clients)
		} else {
			fmt.Println("Action Control\n\nStream stopped - Reboot to use native camera")
		}
		time.Sleep(2 * time.Second)
	}
}
