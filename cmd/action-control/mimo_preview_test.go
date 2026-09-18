package main

import (
	"action-control/internal/mimopreview"
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type previewTestSource struct {
	writer *io.PipeWriter
	waited chan struct{}
}

type httpResponseForPreview struct {
	response *http.Response
	err      error
}

func previewTestController(t *testing.T) (*mimoPreviewController, chan *previewTestSource, *atomic.Int32) {
	t.Helper()
	a, _ := testApplication(t)
	c := &cameraController{app: a, state: "stopped"}
	p := newMimoPreview(c)
	opened, count := make(chan *previewTestSource, 8), &atomic.Int32{}
	p.open = func(ctx context.Context) (io.ReadCloser, func() error, error) {
		reader, writer := io.Pipe()
		source := &previewTestSource{writer: writer, waited: make(chan struct{})}
		count.Add(1)
		stop := context.AfterFunc(ctx, func() { _ = reader.CloseWithError(ctx.Err()); _ = writer.CloseWithError(ctx.Err()) })
		opened <- source
		return reader, func() error { stop(); writer.Close(); close(source.waited); return nil }, nil
	}
	t.Cleanup(func() {
		if err := p.stop(); err != nil {
			t.Error(err)
		}
	})
	return p, opened, count
}

func waitPreview(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("preview resource was not released")
	}
}

func TestMimoPreviewReadinessChecksDoNotStartObserver(t *testing.T) {
	p, _, count := previewTestController(t)
	for _, method := range []string{"HEAD", "POST"} {
		w := httptest.NewRecorder()
		p.stream(w, httptest.NewRequest(method, "/api/mimo_preview_stream", nil))
		if w.Code != 405 {
			t.Fatal(w.Code)
		}
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/mimo_preview_stream", nil)
	r.Header.Set("Origin", "https://elsewhere.invalid")
	p.stream(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := p.subscribe(ctx); err == nil {
		t.Fatal("accepted canceled browser")
	}
	p.camera.operation.Lock()
	_, _, err := p.subscribe(context.Background())
	p.camera.operation.Unlock()
	if err == nil {
		t.Fatal("did not respect camera operation lock")
	}
	if count.Load() != 0 || p.status().State != "idle" {
		t.Fatal("readiness check started observer")
	}
}

func TestMimoPreviewSharedSourceKeyframeJoinAndLastDisconnect(t *testing.T) {
	p, opened, count := previewTestController(t)
	run, first, err := p.subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	source := <-opened
	_, second, err := p.subscribe(context.Background())
	if err != nil || count.Load() != 1 {
		t.Fatal("not sharing observer", err)
	}
	_, late, err := p.subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, fourth, err := p.subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.subscribe(context.Background()); err == nil {
		t.Fatal("client limit ignored")
	}
	mux := &mimopreview.Muxer{}
	p.broadcast(run, &mimopreview.AccessUnit{}, []byte("dependent frame"), mux)
	if len(late.chunks) != 0 {
		t.Fatal("new client received video before keyframe")
	}
	p.broadcast(run, &mimopreview.AccessUnit{Keyframe: true}, []byte("keyframe and headers"), mux)
	for _, client := range []*mimoPreviewClient{first, second, late, fourth} {
		if !bytes.Equal(<-client.chunks, []byte("keyframe and headers")) {
			t.Fatal("keyframe missing")
		}
	}
	p.unsubscribe(run, first)
	select {
	case <-source.waited:
		t.Fatal("one browser stopped other consumers")
	default:
	}
	if !p.camera.operation.TryLock() {
		t.Fatal("mirror holds native recording lock")
	}
	p.camera.operation.Unlock()
	for _, client := range []*mimoPreviewClient{second, late, fourth} {
		p.unsubscribe(run, client)
	}
	waitPreview(t, run.done)
	waitPreview(t, source.waited)
	if st := p.status(); st.Clients != 0 || st.State != "idle" {
		t.Fatal(st)
	}
	if p.camera.status().State != "stopped" || p.camera.app.Ctx.Err() != nil {
		t.Fatal("mirror changed camera/app lifecycle")
	}
}

func TestMimoPreviewSlowBrowserDoesNotBlockOthers(t *testing.T) {
	p, opened, _ := previewTestController(t)
	run, slow, _ := p.subscribe(context.Background())
	<-opened
	_, fast, _ := p.subscribe(context.Background())
	for i := 0; i < 20; i++ {
		p.broadcast(run, &mimopreview.AccessUnit{Keyframe: true}, []byte{byte(i)}, &mimopreview.Muxer{})
		if data := <-fast.chunks; len(data) != 1 || data[0] != byte(i) {
			t.Fatal("fast browser lost data")
		}
	}
	waitPreview(t, slow.closed)
	if p.status().Clients != 1 {
		t.Fatal("slow browser still subscribed")
	}
	p.unsubscribe(run, fast)
	waitPreview(t, run.done)
}

func TestMimoPreviewIdleTimeoutReapsObserver(t *testing.T) {
	p, opened, _ := previewTestController(t)
	p.waitTimeout, p.silenceTimeout = 40*time.Millisecond, 40*time.Millisecond
	run, client, err := p.subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	source := <-opened
	waitPreview(t, run.done)
	waitPreview(t, client.closed)
	waitPreview(t, source.waited)
	if st := p.status(); st.State != "error" || st.Error == "" || st.Clients != 0 {
		t.Fatal(st)
	}
}

func TestMimoPreviewUnavailableHTTPIsNotEmptySuccess(t *testing.T) {
	p, opened, _ := previewTestController(t)
	p.waitTimeout, p.silenceTimeout = 40*time.Millisecond, 40*time.Millisecond
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { p.stream(w, httptest.NewRequest("GET", "/api/mimo_preview_stream", nil)); close(done) }()
	source := <-opened
	waitPreview(t, done)
	waitPreview(t, source.waited)
	if w.Code != 503 || !bytes.Contains(w.Body.Bytes(), []byte("mimo_preview_unavailable")) {
		t.Fatalf("not an explicit unavailable response: %d %s", w.Code, w.Body.String())
	}
}

func TestMimoPreviewAppShutdownStopsObserver(t *testing.T) {
	p, opened, count := previewTestController(t)
	run, _, err := p.subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	source := <-opened
	p.camera.app.cancel()
	waitPreview(t, run.done)
	waitPreview(t, source.waited)
	if _, _, err := p.subscribe(context.Background()); err == nil || count.Load() != 1 {
		t.Fatal("observer reopened during shutdown")
	}
}

func TestMimoPreviewBrowserCancellationStopsOwnedSource(t *testing.T) {
	p, opened, _ := previewTestController(t)
	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest("GET", "/api/mimo_preview_stream", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() { p.stream(httptest.NewRecorder(), r); close(done) }()
	source := <-opened
	cancel()
	waitPreview(t, done)
	waitPreview(t, source.waited)
}

func TestMimoPreviewMalformedPCAPStopsOnlyMirror(t *testing.T) {
	p, opened, _ := previewTestController(t)
	run, _, _ := p.subscribe(context.Background())
	source := <-opened
	_, _ = source.writer.Write(make([]byte, 24))
	waitPreview(t, run.done)
	waitPreview(t, source.waited)
	if st := p.status(); st.State != "error" || st.Stats.OutputFrames != 0 {
		t.Fatal(st)
	}
	if p.camera.app.Ctx.Err() != nil {
		t.Fatal("parser failure canceled app")
	}
}

func TestMimoPreviewHTTPDeliversTSAndCleansUp(t *testing.T) {
	p, opened, _ := previewTestController(t)
	server := httptest.NewServer(http.HandlerFunc(p.stream))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest("GET", server.URL, nil).WithContext(ctx)
	request.RequestURI = ""
	responses := make(chan *httpResponseForPreview, 1)
	go func() {
		response, err := server.Client().Do(request)
		responses <- &httpResponseForPreview{response, err}
	}()
	source := <-opened
	// Synthetic I-frame headers, transported through real pcap/SW/MM decoding.
	nals := []byte{0, 0, 0, 1, 0x67, 0x64, 0, 0x20, 1, 0, 0, 0, 1, 0x68, 0xee, 0, 0, 0, 1, 0x65, 0xb8}
	media := append([]byte{0, 0, 1, 255, 0, 0, 0, 0, 0x90, 0x11, 0, 0, 0, 0, 0, 0}, nals...)
	binary.LittleEndian.PutUint32(media[4:8], uint32(len(nals)))
	for _, value := range media[:10] {
		media[10] ^= value
	}
	sw := make([]byte, 20)
	binary.LittleEndian.PutUint16(sw[:2], uint16(0x8000+len(media)+20))
	sw[6], sw[16], sw[17] = 2, 1, 1
	for _, value := range sw[:7] {
		sw[7] ^= value
	}
	sw = append(sw, media...)
	packet := make([]byte, 42)
	packet[12], packet[14], packet[22], packet[23] = 8, 0x45, 64, 17
	binary.BigEndian.PutUint16(packet[16:18], uint16(28+len(sw)))
	copy(packet[26:34], []byte{192, 168, 2, 1, 192, 168, 2, 2})
	binary.BigEndian.PutUint16(packet[34:36], 9004)
	binary.BigEndian.PutUint16(packet[36:38], 45000)
	binary.BigEndian.PutUint16(packet[38:40], uint16(8+len(sw)))
	packet = append(packet, sw...)
	header := make([]byte, 40)
	binary.LittleEndian.PutUint32(header[:4], 0xa1b2c3d4)
	binary.LittleEndian.PutUint16(header[4:6], 2)
	binary.LittleEndian.PutUint16(header[6:8], 4)
	binary.LittleEndian.PutUint32(header[16:20], 65535)
	binary.LittleEndian.PutUint32(header[20:24], 1)
	binary.LittleEndian.PutUint32(header[24:28], 100)
	binary.LittleEndian.PutUint32(header[32:36], uint32(len(packet)))
	binary.LittleEndian.PutUint32(header[36:40], uint32(len(packet)))
	if _, err := source.writer.Write(append(header, packet...)); err != nil {
		t.Fatal(err)
	}
	var result *httpResponseForPreview
	select {
	case result = <-responses:
	case <-time.After(2 * time.Second):
		t.Fatal("HTTP preview did not start")
	}
	if result.err != nil {
		t.Fatal(result.err)
	}
	defer result.response.Body.Close()
	if result.response.StatusCode != 200 || result.response.Header.Get("Content-Type") != "video/MP2T" {
		t.Fatal(result.response.Status)
	}
	raw := make([]byte, 188*3)
	if _, err := io.ReadFull(result.response.Body, raw); err != nil {
		t.Fatal(err)
	}
	if raw[0] != 0x47 || raw[188] != 0x47 || raw[376] != 0x47 {
		t.Fatal("not MPEG-TS")
	}
	cancel()
	waitPreview(t, source.waited)
}
