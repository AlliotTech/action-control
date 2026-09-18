package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCameraStreamUsesBrowserCompatibleH264(t *testing.T) {
	args := cameraArgs(defaultConfig())
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "video/x-h264") || !strings.Contains(joined, "h264parse config-interval=-1") || strings.Contains(joined, "video/x-h265") {
		t.Fatalf("unexpected camera stream pipeline: %s", joined)
	}
}

func TestStreamSlowClientAndDisconnect(t *testing.T) {
	a, _ := testApplication(t)
	run := &cameraRun{first: make(chan struct{}), start: time.Now(), clients: map[*streamClient]struct{}{}}
	c := &cameraController{app: a, run: run, state: "running", config: defaultConfig()}
	_, slow, err := c.subscribe(nil)
	if err != nil {
		t.Fatal(err)
	}
	for range cap(slow.chunks) {
		slow.chunks <- []byte("queued")
	}
	_, fast, err := c.subscribe(nil)
	if err != nil {
		t.Fatal(err)
	}
	source := bytes.Repeat([]byte{0x47, 0x40, 0x00, 0x10}, 16384)
	c.relay(run, io.NopCloser(bytes.NewReader(source)))
	select {
	case <-slow.closed:
	default:
		t.Fatal("slow client was not disconnected")
	}
	var got []byte
	for len(fast.chunks) > 0 {
		got = append(got, (<-fast.chunks)...)
	}
	if !bytes.Equal(got, source) {
		t.Fatal("fast client received corrupted or reused data blocks")
	}
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest("GET", "/api/camera_stream", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() { c.stream(httptest.NewRecorder(), request); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled browser stream did not exit")
	}
	c.unsubscribe(run, fast)
	if !c.status().Running {
		t.Fatal("client disconnect stopped device capture")
	}
	c.operation.Lock()
	c.mu.Lock()
	c.run = nil
	c.state = "starting"
	c.mu.Unlock()
	w := httptest.NewRecorder()
	start := httptest.NewRequest("POST", "/api/camera_start", strings.NewReader(`{"confirm":true}`))
	start.Header.Set("Content-Type", "application/json")
	c.start(w, start)
	c.operation.Unlock()
	if w.Code != 409 {
		t.Fatalf("concurrent start was not refused: %d", w.Code)
	}
}

func TestConcurrentStopPreservesUnrelatedProcess(t *testing.T) {
	a, _ := testApplication(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := ownedCommand(ctx, "/bin/sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	foreign := exec.Command("/bin/sleep", "60")
	if err := foreign.Start(); err != nil {
		cancel()
		_ = cmd.Wait()
		t.Fatal(err)
	}
	defer func() { _ = foreign.Process.Kill(); _ = foreign.Wait() }()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		cancel()
		_ = cmd.Wait()
		t.Fatal(err)
	}
	defer listener.Close()
	run := &cameraRun{cancel: cancel, cmd: cmd, listener: listener, done: make(chan struct{}), start: time.Now(), clients: map[*streamClient]struct{}{}}
	go func() { _ = cmd.Wait(); close(run.done) }()
	controller := &cameraController{app: a, run: run, state: "running", config: defaultConfig()}
	results := make(chan error, 2)
	for range 2 {
		go func() { results <- controller.stop() }()
	}
	for range 2 {
		if err = <-results; err != nil {
			t.Fatal(err)
		}
	}
	if controller.status().Running || cmd.ProcessState == nil {
		t.Fatal("owned capture did not stop")
	}
	if err = foreign.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatal("unrelated process was stopped", err)
	}
	if conn, e := net.DialTimeout("tcp", listener.Addr().String(), time.Second); e == nil {
		conn.Close()
		t.Fatal("stream listener remained open after stop")
	}
}

func TestWirelessInputsAndControlSocket(t *testing.T) {
	a, _ := testApplication(t)
	config, err := wpaConfig(a, knownNetwork{SSID: "IEEE", Password: "password"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(config), "psk=f42c6fc52df0ebef9ebb4b90b38a5f902e83fe1b135a70e23aed762e9710a12e") {
		t.Fatal("WPA PSK does not match the published WPA2 test vector")
	}
	for _, ssid := range []string{"", strings.Repeat("中", 11), "SSID\nnetwork={}"} {
		if err := validateSSID(ssid); err == nil {
			t.Fatalf("invalid SSID accepted: %q", ssid)
		}
	}
	for _, password := range []string{"short", strings.Repeat("g", 64), "password\n"} {
		if err := validatePassword(password); err == nil {
			t.Fatalf("invalid PSK accepted: %q", password)
		}
	}
	config, err = wpaConfig(a, knownNetwork{SSID: "中文 空格 \"网络\""})
	if err != nil || !strings.Contains(string(config), "key_mgmt=NONE") {
		t.Fatal("open UTF-8 network rejected", err)
	}
	// A short socket path also runs on macOS, whose Unix path limit is 104 bytes.
	directory, err := os.MkdirTemp("", "ac-wpa-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	a.RunDir = directory
	if err = os.Mkdir(filepath.Join(directory, "wpa"), 0700); err != nil {
		t.Fatal(err)
	}
	socket, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: filepath.Join(directory, "wpa/wlan0"), Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	go func() {
		buffer := make([]byte, 128)
		_, address, e := socket.ReadFromUnix(buffer)
		if e == nil {
			_, _ = socket.WriteToUnix([]byte("wpa_state=COMPLETED\nssid=IEEE\n"), address)
		}
	}()
	values, err := wirelessStatus(a, "wpa")
	if err != nil || values["wpa_state"] != "COMPLETED" || values["ssid"] != "IEEE" {
		t.Fatal("wireless control socket failed", values, err)
	}
}
