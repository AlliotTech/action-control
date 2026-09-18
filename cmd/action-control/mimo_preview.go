package main

import (
	"action-control/internal/mimopreview"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

const mimoPreviewClients = 4

type mimoPreviewStatus struct {
	Source           string            `json:"source"`
	RequiresMimo     bool              `json:"requires_mimo"`
	State            string            `json:"state"`
	Clients          int               `json:"clients"`
	Bytes            uint64            `json:"bytes"`
	LastFrameAt      string            `json:"last_frame_at,omitempty"`
	ClockCorrections uint64            `json:"clock_corrections"`
	Stats            mimopreview.Stats `json:"stats"`
	Error            string            `json:"error,omitempty"`
}

type mimoPreviewClient struct {
	chunks chan []byte
	closed chan struct{}
	ready  bool
}

type mimoPreviewRun struct {
	cancel    context.CancelFunc
	done      chan struct{}
	clients   map[*mimoPreviewClient]struct{}
	started   time.Time
	lastFrame time.Time
	stopping  bool
	status    mimoPreviewStatus
}

type mimoPreviewController struct {
	camera                      *cameraController
	mu                          sync.Mutex
	run                         *mimoPreviewRun
	last                        mimoPreviewStatus
	open                        func(context.Context) (io.ReadCloser, func() error, error)
	waitTimeout, silenceTimeout time.Duration
}

func newMimoPreview(c *cameraController) *mimoPreviewController {
	p := &mimoPreviewController{camera: c, waitTimeout: 8 * time.Second, silenceTimeout: 3 * time.Second}
	p.open = p.openDevice
	p.last = mimoPreviewStatus{Source: "native_mimo_mirror", RequiresMimo: true, State: "idle"}
	return p
}

func (p *mimoPreviewController) openDevice(ctx context.Context) (io.ReadCloser, func() error, error) {
	a := p.camera.app
	if err := a.RequireManaged(); err != nil {
		return nil, nil, err
	}
	if p.camera.status().State != "stopped" || a.RebootRequired() {
		return nil, nil, errors.New("请先恢复原生相机服务，再连接 Mimo 预览")
	}
	services := readNativeCameraServices(ctx, a)
	for _, unit := range []string{"dji_camera3.service", "dji_sw_uav.service"} {
		found := false
		for _, service := range services.Services {
			found = found || (service.Unit == unit && service.State == "running")
		}
		if !found {
			return nil, nil, errors.New("原生相机或 Mimo 通信服务未运行")
		}
	}
	iface, err := net.InterfaceByName("wlan0")
	if err != nil {
		return nil, nil, err
	}
	addresses, err := iface.Addrs()
	if err != nil {
		return nil, nil, err
	}
	var source string
	for _, address := range addresses {
		ip, _, e := net.ParseCIDR(address.String())
		if e == nil && ip.To4() != nil && !ip.IsUnspecified() {
			if source != "" {
				return nil, nil, errors.New("wlan0 有多个 IPv4 地址，无法确定原生视频来源")
			}
			source = ip.String()
		}
	}
	if source == "" || iface.Flags&net.FlagUp == 0 {
		return nil, nil, errors.New("请先使用 Mimo 连接相机并打开实时预览")
	}
	program, err := a.Tool("tcpdump")
	if err != nil {
		return nil, nil, err
	}
	// Fixed interface, non-promiscuous mode, outbound camera video only. No
	// packet injection, UDP port binding, native subscription, or capture file.
	filter := fmt.Sprintf("udp and src port 9004 and src host %s and udp[14] = 2", source)
	// Keeping the existing root identity also preserves Linux PDEATHSIG; a
	// tcpdump privilege transition could otherwise clear that cleanup signal.
	cmd := ownedCommand(ctx, program, "-n", "-i", "wlan0", "-p", "-s", "0", "-U", "-B", "1024", "-Z", "root", "-w", "-", filter)
	protectPreviewChild(cmd)
	stderr := &boundedBuffer{limit: 8 << 10}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err = cmd.Start(); err != nil {
		stdout.Close()
		return nil, nil, err
	}
	return stdout, func() error {
		if err := cmd.Wait(); err != nil {
			return fmt.Errorf("video observer: %w: %s", err, stderr.Text())
		}
		return nil
	}, nil
}

func (p *mimoPreviewController) status() mimoPreviewStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.run == nil {
		return p.last
	}
	status := p.run.status
	status.Clients = len(p.run.clients)
	if p.run.stopping && status.Error == "" {
		status.State = "stopping"
	}
	return status
}

func (p *mimoPreviewController) subscribe(ctx context.Context) (*mimoPreviewRun, *mimoPreviewClient, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if err := p.camera.app.Ctx.Err(); err != nil {
		return nil, nil, err
	}
	if p.run == nil {
		// Share the existing operation lock only during startup. A live mirror
		// must not block native recording or claim camera ownership.
		if !p.camera.operation.TryLock() {
			return nil, nil, errors.New("相机操作正在进行，请稍后连接预览")
		}
		defer p.camera.operation.Unlock()
		runCtx, cancel := context.WithCancel(p.camera.app.Ctx)
		source, wait, err := p.open(runCtx)
		if err != nil {
			cancel()
			p.last.State, p.last.Error = "error", err.Error()
			return nil, nil, err
		}
		run := &mimoPreviewRun{
			cancel: cancel, done: make(chan struct{}), started: time.Now(),
			clients: make(map[*mimoPreviewClient]struct{}),
			status:  mimoPreviewStatus{Source: "native_mimo_mirror", RequiresMimo: true, State: "waiting"},
		}
		p.run = run
		go p.pump(runCtx, run, source, wait)
	}
	run := p.run
	if run.stopping {
		return nil, nil, errors.New("视频转发正在退出，请稍后重新连接")
	}
	if len(run.clients) >= mimoPreviewClients {
		return nil, nil, errors.New("最多同时打开 4 个网页预览")
	}
	client := &mimoPreviewClient{chunks: make(chan []byte, 16), closed: make(chan struct{})}
	run.clients[client] = struct{}{}
	return run, client, nil
}

func (p *mimoPreviewController) dropLocked(run *mimoPreviewRun, client *mimoPreviewClient) {
	if _, ok := run.clients[client]; !ok {
		return
	}
	delete(run.clients, client)
	close(client.closed)
	close(client.chunks)
	if len(run.clients) == 0 {
		run.stopping = true
		run.cancel()
	}
}

func (p *mimoPreviewController) unsubscribe(run *mimoPreviewRun, client *mimoPreviewClient) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dropLocked(run, client)
}

func (p *mimoPreviewController) broadcast(run *mimoPreviewRun, unit *mimopreview.AccessUnit, data []byte, mux *mimopreview.Muxer) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if run.stopping {
		return
	}
	run.lastFrame = time.Now()
	run.status.State = "streaming"
	run.status.LastFrameAt = run.lastFrame.UTC().Format(time.RFC3339Nano)
	run.status.Bytes += uint64(len(data))
	run.status.ClockCorrections = mux.ClockCorrections
	for client := range run.clients {
		if !client.ready && !unit.Keyframe {
			continue
		}
		client.ready = true
		select {
		case client.chunks <- data:
		default:
			// Never let a slow browser back up the packet reader or grow memory.
			p.dropLocked(run, client)
		}
	}
}

func (p *mimoPreviewController) watchdog(ctx context.Context, run *mimoPreviewRun) {
	interval := min(250*time.Millisecond, p.waitTimeout/2, p.silenceTimeout/2)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.mu.Lock()
			last, timeout := run.lastFrame, p.silenceTimeout
			if last.IsZero() {
				last, timeout = run.started, p.waitTimeout
			}
			if !run.stopping && time.Since(last) > timeout {
				run.status.Error = "未收到可播放的 Mimo 视频。请让 Mimo 停留在实时预览页，然后重新连接。"
				run.stopping = true
				run.cancel()
			}
			p.mu.Unlock()
		}
	}
}

func (p *mimoPreviewController) pump(ctx context.Context, run *mimoPreviewRun, source io.ReadCloser, wait func() error) {
	go p.watchdog(ctx, run)
	decoder, mux := &mimopreview.Decoder{}, &mimopreview.Muxer{}
	err := mimopreview.ReadPCAP(source, func(at time.Time, packet []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		unit, err := decoder.Feed(at, packet)
		p.mu.Lock()
		run.status.Stats = decoder.Stats
		p.mu.Unlock()
		if unit != nil {
			p.broadcast(run, unit, mux.Write(unit), mux)
		}
		return err
	})
	cancelled := ctx.Err() != nil
	run.cancel()
	_ = source.Close()
	waitErr := wait() // Reap only our observer before permitting another run.
	p.mu.Lock()
	defer p.mu.Unlock()
	if !cancelled && run.status.Error == "" {
		if err = errors.Join(err, waitErr); err == nil {
			err = errors.New("视频观察进程已退出")
		}
		run.status.Error = "视频转发已停止：" + err.Error()
	}
	for client := range run.clients {
		p.dropLocked(run, client)
	}
	run.status.State = "idle"
	if run.status.Error != "" {
		run.status.State = "error"
	}
	p.last = run.status
	if p.run == run {
		p.run = nil
	}
	close(run.done)
}

func (p *mimoPreviewController) stop() error {
	p.mu.Lock()
	run := p.run
	if run != nil {
		run.stopping = true
		run.cancel()
	}
	p.mu.Unlock()
	if run != nil {
		select {
		case <-run.done:
		case <-time.After(4 * time.Second):
			return errors.New("视频观察进程尚未退出")
		}
	}
	return nil
}

func (p *mimoPreviewController) stream(w http.ResponseWriter, r *http.Request) {
	// Go's GET patterns also match HEAD; a link checker must not start capture.
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		jsonError(w, 405, "method_not_allowed", errors.New("GET required"))
		return
	}
	if !sameOrigin(r) {
		jsonError(w, 403, "origin_forbidden", errors.New("cross-origin preview is forbidden"))
		return
	}
	run, client, err := p.subscribe(r.Context())
	if err != nil {
		jsonError(w, 503, "mimo_preview_unavailable", err)
		return
	}
	defer p.unsubscribe(run, client)
	deadline := time.NewTimer(p.waitTimeout)
	defer deadline.Stop()
	controller := http.NewResponseController(w)
	defer controller.SetWriteDeadline(time.Time{})
	started := false
	unavailable := func() {
		if !started {
			jsonError(w, 503, "mimo_preview_unavailable", errors.New("Mimo 预览未就绪，请保持手机实时预览后重新连接"))
		}
	}
	for {
		select {
		case <-client.closed:
			unavailable()
			return
		default:
		}
		select {
		case <-r.Context().Done():
			return
		case <-client.closed:
			unavailable()
			return
		case <-deadline.C:
			if !started {
				jsonError(w, 503, "mimo_preview_unavailable", errors.New("等待 Mimo 关键帧超时，请保持手机实时预览后重新连接"))
				return
			}
		case chunk, ok := <-client.chunks:
			if !ok {
				unavailable()
				return
			}
			if !started {
				w.Header().Set("Content-Type", "video/MP2T")
				w.Header().Set("Cache-Control", "no-store")
				w.Header().Set("X-Accel-Buffering", "no")
				started = true
				deadline.Stop()
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
