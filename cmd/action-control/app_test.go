package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func putTestFile(t *testing.T, name string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, data, mode); err != nil {
		t.Fatal(err)
	}
}
func testFilesystem(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"blackbox/upgrade", "mnt/media_rw/sd", "mnt/media_rw/emulated", "data/misc/wifi", "etc/systemd/system", "run/native"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	putTestFile(t, filepath.Join(root, "data/misc/wifi/wpa_supplicant.conf"), []byte("original WPA\n"), 0640)
	putTestFile(t, filepath.Join(root, "data/misc/wifi/user_config.conf"), []byte("ssid=相机 热点\npasswd=\nband=0\nchannel=6\ncountry=FF\n"), 0600)
	putTestFile(t, filepath.Join(root, "etc/init.post_boot.sh"), []byte("#!/bin/sh\necho stock\n"), 0755)
	putTestFile(t, filepath.Join(root, "run/native/resolv.conf"), []byte("nameserver 192.0.2.53\n"), 0644)
	if err := os.Symlink("/run/native/resolv.conf", filepath.Join(root, "etc/resolv.conf")); err != nil {
		t.Fatal(err)
	}
	putTestFile(t, filepath.Join(root, "proc/sys/kernel/random/boot_id"), []byte("first-boot"), 0600)
	putTestFile(t, filepath.Join(root, "mnt/media_rw/sd/用户媒体.bin"), []byte{0, 255, 13, 10, 10}, 0600)
	return root
}
func testApplication(t *testing.T) (*App, http.Handler) {
	t.Helper()
	a, err := NewApp(testFilesystem(t))
	if err != nil {
		t.Fatal(err)
	}
	handler, closeAll, err := newHandler(a)
	if err != nil {
		a.cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		a.cancel()
		if err := closeAll(); err != nil {
			t.Error(err)
		}
	})
	return a, handler
}
func requestTest(a *App, h http.Handler, method, target string, body io.Reader, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, body)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		r.Header.Set(key, value)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func multipartTest(t *testing.T, name string, data []byte) ([]byte, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("files", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes(), writer.FormDataContentType()
}

func TestNativeHotspotBandMapping(t *testing.T) {
	for _, tc := range []struct {
		native, band string
		channel      int
	}{{"0", "2.4G", 6}, {"1", "5G", 44}, {"2", "5G", 149}} {
		a, _, _ := testInstallation(t)
		testNetworkControl(t, a, networkControlManaged)
		data := []byte("ssid=Action\npasswd=password\nband=" + tc.native + "\nchannel=" + strconv.Itoa(tc.channel) + "\nchannel_auto=1\ncountry=CN\n")
		putTestFile(t, a.Path(hotspotPath), data, 0600)
		h, original, err := readHotspot(a)
		if err != nil || h.Band != tc.band || h.Channel != tc.channel {
			t.Fatalf("native band %s was not decoded: %+v, %v", tc.native, h, err)
		}
		if err = setHotspot(a, h, original); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(a.Path(hotspotPath))
		if err != nil || configValues(got)["band"] != tc.native {
			t.Fatalf("native band %s was not preserved: %q, %v", tc.native, got, err)
		}
		a.cancel()
	}
}

func TestRemovedDuplicateAPIs(t *testing.T) {
	a, h := testApplication(t)
	for _, request := range []struct {
		method string
		path   string
	}{{"GET", "/api/camera_stream_info"}, {"GET", "/api/wifi_mode"}, {"POST", "/api/wifi_mode"}, {"POST", "/api/wifi_on"}} {
		w := requestTest(a, h, request.method, request.path, nil, nil)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s %s remains exposed: %d", request.method, request.path, w.Code)
		}
	}
}

func TestListingAndRecursiveAlbumUseStorageDescriptors(t *testing.T) {
	a, h := testApplication(t)
	data := []byte("photo bytes\n")
	putTestFile(t, a.Path("/mnt/media_rw/sd/DCIM/中文 照片.jpg"), data, 0600)
	if err := os.Symlink(a.Dir, a.Path("/mnt/media_rw/sd/alias")); err != nil {
		t.Fatal(err)
	}
	listing := requestTest(a, h, "GET", "/api/list?dir=/sd/DCIM", nil, nil)
	var files struct {
		Items []FileItem `json:"items"`
	}
	if err := json.Unmarshal(listing.Body.Bytes(), &files); err != nil {
		t.Fatal(err)
	}
	if listing.Code != 200 || len(files.Items) != 1 || files.Items[0].Name != "中文 照片.jpg" || files.Items[0].Size != int64(len(data)) {
		t.Fatalf("directory listing lost real files: %s", listing.Body.String())
	}
	album := requestTest(a, h, "GET", "/api/media_list?dir=/sd", nil, nil)
	var media struct {
		Items []MediaItem `json:"items"`
	}
	if err := json.Unmarshal(album.Body.Bytes(), &media); err != nil {
		t.Fatal(err)
	}
	if album.Code != 200 || len(media.Items) != 1 || media.Items[0].Path != "/sd/DCIM/中文 照片.jpg" {
		t.Fatalf("recursive album lost files or followed symlink: %s", album.Body.String())
	}
}

func TestAlbumRetainsReadableFilesWhenDirectoryFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires an unprivileged user to exercise filesystem permissions")
	}
	a, h := testApplication(t)
	blocked := a.Path("/mnt/media_rw/sd/blocked")
	putTestFile(t, filepath.Join(blocked, "hidden.jpg"), []byte("hidden"), 0600)
	putTestFile(t, a.Path("/mnt/media_rw/sd/visible.jpg"), []byte("visible"), 0600)
	if err := os.Chmod(blocked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(blocked, 0700); err != nil {
			t.Error(err)
		}
	})
	response := requestTest(a, h, "GET", "/api/media_list?dir=/sd", nil, nil)
	var result struct {
		Items    []MediaItem `json:"items"`
		Warnings []string    `json:"warnings"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || len(result.Items) != 1 || result.Items[0].Path != "/sd/visible.jpg" || len(result.Warnings) != 1 || !strings.HasPrefix(result.Warnings[0], "/sd/blocked: ") {
		t.Fatalf("readable media or failure details lost: %d %s", response.Code, response.Body.String())
	}
	response = requestTest(a, h, "GET", "/api/media_list?dir=/sd/blocked", nil, nil)
	if response.Code != http.StatusForbidden {
		t.Fatalf("unreadable requested directory must remain an error: %d %s", response.Code, response.Body.String())
	}
}

func TestHTTPAccessAndFileBoundaries(t *testing.T) {
	a, h := testApplication(t)
	putTestFile(t, a.Path("/blackbox/记录.txt"), []byte("private media\n"), 0600)
	if err := os.Symlink(a.Path(AppDir), a.Path("/blackbox/alias")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"/../etc/passwd", "/action-control/config.json", "/alias/config.json", "/sd/../../etc/passwd"} {
		w := requestTest(a, h, "GET", "/api/download?file="+url.QueryEscape(name), nil, nil)
		if w.Code < 400 {
			t.Errorf("escaped path %q: %d", name, w.Code)
		}
	}
	if w := requestTest(nil, h, "GET", "/api/download?file=/记录.txt", nil, nil); w.Code != 200 {
		t.Fatalf("direct media access: %d", w.Code)
	}
	if w := requestTest(a, h, "POST", "/api/config", strings.NewReader(`{"theme_mode":"dark"}`), map[string]string{"Origin": "https://untrusted.example"}); w.Code != 403 {
		t.Fatalf("cross-origin mutation: %d", w.Code)
	}
	for _, body := range []string{`{"cam_w":321}`, `{"unexpected":true}`, `{"theme_mode":"dark"} {}`} {
		if w := requestTest(a, h, "POST", "/api/config", strings.NewReader(body), nil); w.Code != 400 {
			t.Fatalf("invalid config accepted: %s (%d)", body, w.Code)
		}
	}
	if w := requestTest(a, h, "GET", "/api/delete", nil, nil); w.Code != 405 {
		t.Fatalf("unsafe method: %d", w.Code)
	}
	if w := requestTest(a, h, "GET", "/api/not-a-route", nil, nil); w.Code != 404 || !strings.Contains(w.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("unknown API fell through: %d", w.Code)
	}
	for _, name := range []string{"/sd", "/emulated", "/"} {
		if w := requestTest(a, h, "DELETE", "/api/delete", strings.NewReader(`{"path":"`+name+`"}`), nil); w.Code != 403 {
			t.Errorf("storage root deletion accepted: %s", name)
		}
	}
	if w := requestTest(a, h, "POST", "/api/camera_start", strings.NewReader(`{"width":1280,"height":720,"fps":30,"ext_port":8554,"quality":1,"bitrate":4.4,"confirm":true,"takeover_native":true}`), nil); w.Code != 503 {
		t.Fatalf("offline root allowed hardware capture: %d", w.Code)
	}
	if err := os.Chmod(a.Path("/blackbox/记录.txt"), 0); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() != 0 {
		if w := requestTest(a, h, "GET", "/api/download?file=/记录.txt", nil, nil); w.Code != 403 {
			t.Fatalf("permission error: %d", w.Code)
		}
	}
}

func TestUploadIntegrityConflictAndInterruption(t *testing.T) {
	a, h := testApplication(t)
	data := []byte{0, 255, 128, 13, 10, 0, 13, 10, 10}
	body, contentType := multipartTest(t, "二进制 ' 名称.bin", data)
	upload := func(b []byte) *httptest.ResponseRecorder {
		return requestTest(a, h, "POST", "/api/upload?dir=/sd", bytes.NewReader(b), map[string]string{"Content-Type": contentType})
	}
	if w := upload(body); w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	name := a.Path("/mnt/media_rw/sd/二进制 ' 名称.bin")
	got, err := os.ReadFile(name)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("binary upload changed: %v %x", err, got)
	}
	if w := upload(body); w.Code != 409 {
		t.Fatalf("same-name upload did not conflict: %d", w.Code)
	}
	interrupted, kind := multipartTest(t, "中断.bin", bytes.Repeat([]byte{42}, 4096))
	if w := requestTest(a, h, "POST", "/api/upload?dir=/sd", bytes.NewReader(interrupted[:len(interrupted)-100]), map[string]string{"Content-Type": kind}); w.Code < 400 {
		t.Fatalf("truncated upload succeeded: %d", w.Code)
	}
	if _, err = os.Stat(a.Path("/mnt/media_rw/sd/中断.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("partial file committed", err)
	}
	entries, err := os.ReadDir(a.Path("/mnt/media_rw/sd"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".action-control-upload-") {
			t.Fatal("orphaned upload", entry.Name())
		}
	}
	if os.Geteuid() != 0 {
		directory := a.Path("/blackbox/read-only")
		if err = os.Mkdir(directory, 0500); err != nil {
			t.Fatal(err)
		}
		if w := requestTest(a, h, "POST", "/api/upload?dir=/read-only", bytes.NewReader(body), map[string]string{"Content-Type": contentType}); w.Code < 400 {
			t.Fatal("unwritable destination accepted")
		}
		if err = os.Chmod(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	got, err = os.ReadFile(name)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("conflict/error changed original upload")
	}
}

func TestMediaIndexScanDeleteAndCleanup(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 unavailable")
	}
	a, h := testApplication(t)
	sd, _ := mediaIndexLocationFor("sd")
	db := a.Path(sd.Database)
	if err := os.MkdirAll(filepath.Dir(db), 0700); err != nil {
		t.Fatal(err)
	}
	live := a.Path("/mnt/media_rw/sd/DCIM/live.jpg")
	putTestFile(t, live, []byte("photo"), 0600)
	cmd := exec.Command("sqlite3", db, `
CREATE TABLE gis_info_table (ID INTEGER PRIMARY KEY, file_name TEXT);
INSERT INTO gis_info_table(file_name) VALUES
('/mnt/media_rw/sd/DCIM/live.jpg'),
('/mnt/media_rw/sd/DCIM/live.lrf'),
('/mnt/media_rw/sd/DCIM/stale.mp4'),
('/unsafe/path.jpg');`)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create media index: %v: %s", err, output)
	}

	status := requestTest(a, h, "GET", "/api/media_index_status?storage=sd", nil, nil)
	var scanned MediaIndexStatus
	if err := json.Unmarshal(status.Body.Bytes(), &scanned); err != nil {
		t.Fatal(err)
	}
	if status.Code != 200 || !scanned.Available || scanned.Total != 4 || scanned.Stale != 2 || scanned.Invalid != 1 || scanned.ByType["MP4"] != 1 || scanned.ByType["LRF"] != 1 {
		t.Fatalf("unexpected index scan: %d %s", status.Code, status.Body.String())
	}

	if w := requestTest(a, h, "POST", "/api/rename", strings.NewReader(`{"path":"/sd/DCIM/live.jpg","name":"renamed.jpg"}`), nil); w.Code != 409 {
		t.Fatalf("indexed media rename accepted: %d %s", w.Code, w.Body.String())
	}
	if w := requestTest(a, h, "DELETE", "/api/delete", strings.NewReader(`{"path":"/sd/DCIM/live.jpg"}`), nil); w.Code != 200 {
		t.Fatalf("indexed media delete failed: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(live); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("indexed media remained on disk", err)
	}

	store := &fileStore{app: a}
	result, err := store.cleanMediaIndex(context.Background(), "sd")
	if err != nil || result.Deleted != 2 || result.Remaining != 0 || result.Backup == "" {
		t.Fatalf("cleanup failed: %+v %v", result, err)
	}
	if _, err = os.Stat(a.Path(strings.Replace(result.Backup, "/sd/", "/mnt/media_rw/sd/", 1))); err != nil {
		t.Fatal("cleanup backup missing", err)
	}
	out, err := exec.Command("sqlite3", db, "SELECT group_concat(file_name, '|') FROM gis_info_table ORDER BY ID;").Output()
	if err != nil || strings.TrimSpace(string(out)) != "/unsafe/path.jpg" {
		t.Fatalf("cleanup removed unexpected rows: %q %v", out, err)
	}

	emulated, _ := mediaIndexLocationFor("emulated")
	emulatedDB := a.Path(emulated.Database)
	if err = os.MkdirAll(filepath.Dir(emulatedDB), 0700); err != nil {
		t.Fatal(err)
	}
	emulatedLive := a.Path("/mnt/media_rw/emulated/DCIM/live.jpg")
	putTestFile(t, emulatedLive, []byte("photo"), 0600)
	cmd = exec.Command("sqlite3", emulatedDB, `
CREATE TABLE gis_info_table (ID INTEGER PRIMARY KEY, file_name TEXT);
INSERT INTO gis_info_table(file_name) VALUES
('/mnt/media_rw/emulated/DCIM/live.jpg'),
('/mnt/media_rw/emulated/DCIM/stale.jpg');`)
	if output, createErr := cmd.CombinedOutput(); createErr != nil {
		t.Fatalf("create internal media index: %v: %s", createErr, output)
	}
	status = requestTest(a, h, "GET", "/api/media_index_status?storage=emulated", nil, nil)
	if err = json.Unmarshal(status.Body.Bytes(), &scanned); err != nil || status.Code != 200 || scanned.Storage != "emulated" || scanned.Stale != 1 {
		t.Fatalf("unexpected internal index scan: %d %s %v", status.Code, status.Body.String(), err)
	}
	if w := requestTest(a, h, "POST", "/api/rename", strings.NewReader(`{"path":"/emulated/DCIM/live.jpg","name":"renamed.jpg"}`), nil); w.Code != 409 {
		t.Fatalf("indexed internal media rename accepted: %d %s", w.Code, w.Body.String())
	}
	if w := requestTest(a, h, "DELETE", "/api/delete", strings.NewReader(`{"path":"/emulated/DCIM/live.jpg"}`), nil); w.Code != 200 {
		t.Fatalf("indexed internal media delete failed: %d %s", w.Code, w.Body.String())
	}
	result, err = store.cleanMediaIndex(context.Background(), "emulated")
	if err != nil || result.Storage != "emulated" || result.Deleted != 1 || result.Remaining != 0 || !strings.HasPrefix(result.Backup, "/emulated/MISC/") {
		t.Fatalf("internal cleanup failed: %+v %v", result, err)
	}
}

func TestMediaIndexCleanupLockedLeavesNoBackup(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 not installed")
	}
	a, _ := testApplication(t)
	sd, _ := mediaIndexLocationFor("sd")
	db := a.Path(sd.Database)
	if err := os.MkdirAll(filepath.Dir(db), 0700); err != nil {
		t.Fatal(err)
	}
	// One stale row so cleanup has work to do and reaches the write-lock probe.
	create := exec.Command("sqlite3", db, `
CREATE TABLE gis_info_table (ID INTEGER PRIMARY KEY, file_name TEXT);
INSERT INTO gis_info_table(file_name) VALUES ('/mnt/media_rw/sd/DCIM/stale.mp4');`)
	if out, err := create.CombinedOutput(); err != nil {
		t.Fatalf("create media index: %v: %s", err, out)
	}

	// Hold the write lock from a peer connection, mimicking dji_media_server.
	holder := exec.Command("sqlite3", db)
	stdin, err := holder.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = holder.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close(); _ = holder.Wait() }()
	if _, err = io.WriteString(stdin, "BEGIN IMMEDIATE;\n"); err != nil {
		t.Fatal(err)
	}
	locked := func() bool {
		probe := exec.Command("sqlite3", db)
		probe.Stdin = strings.NewReader(".bail on\n.timeout 200\nBEGIN IMMEDIATE;\nROLLBACK;\n")
		return probe.Run() != nil
	}
	deadline := time.Now().Add(5 * time.Second)
	for !locked() {
		if time.Now().After(deadline) {
			t.Fatal("peer connection did not acquire the write lock")
		}
		time.Sleep(20 * time.Millisecond)
	}

	store := &fileStore{app: a}
	if _, err = store.cleanMediaIndex(context.Background(), "sd"); !errors.Is(err, errMediaIndexLocked) {
		t.Fatalf("locked cleanup did not report a locked index: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(db))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "AC004.db.bak_") {
			t.Fatalf("locked cleanup left an orphan backup: %s", entry.Name())
		}
	}
}

func TestThumbnailUsesEmbeddedScreennail(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 not installed")
	}
	a, h := testApplication(t)
	sd, _ := mediaIndexLocationFor("sd")
	db := a.Path(sd.Database)
	if err := os.MkdirAll(filepath.Dir(db), 0700); err != nil {
		t.Fatal(err)
	}
	// A decodable JPEG standing in for the screennail the camera embeds.
	var jbuf bytes.Buffer
	if err := jpeg.Encode(&jbuf, image.NewRGBA(image.Rect(0, 0, 300, 200)), &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	screennail := jbuf.Bytes()
	const offset = 5000
	// Bytes at offset 0 are neither a decodable image nor a video, so a valid
	// JPEG response proves the thumbnail came from the embedded screennail and
	// never touched the ffmpeg fallback.
	file := make([]byte, offset)
	file = append(file, screennail...)
	file = append(file, make([]byte, 1024)...)
	putTestFile(t, a.Path("/mnt/media_rw/sd/DCIM/clip.mp4"), file, 0600)

	create := exec.Command("sqlite3", db,
		"CREATE TABLE gis_info_table (ID INTEGER PRIMARY KEY, file_name TEXT, video_index INT, image_index INT);"+
			"CREATE TABLE image_info_table (ID INTEGER PRIMARY KEY, scr_offset INT, scr_size INT);"+
			"CREATE TABLE video_info_table (ID INTEGER PRIMARY KEY, scr_offset64 INT, scr_size64 INT);"+
			"INSERT INTO video_info_table(ID, scr_offset64, scr_size64) VALUES (1, "+strconv.Itoa(offset)+", "+strconv.Itoa(len(screennail))+");"+
			"INSERT INTO gis_info_table(file_name, video_index, image_index) VALUES ('/mnt/media_rw/sd/DCIM/clip.mp4', 1, 0);")
	if out, err := create.CombinedOutput(); err != nil {
		t.Fatalf("create media index: %v: %s", err, out)
	}

	w := requestTest(a, h, "GET", "/api/thumbnail?file=/sd/DCIM/clip.mp4", nil, nil)
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("embedded thumbnail not served: %d %s", w.Code, w.Body.String())
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		t.Fatalf("thumbnail is not a decodable JPEG: %v", err)
	}
	if cfg.Width != 300 || cfg.Height != 200 {
		t.Fatalf("unexpected thumbnail dimensions %dx%d", cfg.Width, cfg.Height)
	}
}

func TestNativeMediaMetaQueriesExtendedFields(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 not installed")
	}
	a, _ := testApplication(t)
	sd, _ := mediaIndexLocationFor("sd")
	db := a.Path(sd.Database)
	if err := os.MkdirAll(filepath.Dir(db), 0700); err != nil {
		t.Fatal(err)
	}
	// Real column layout so the SELECT column list is exercised end to end; a
	// typo in the query would fail here, which the pure parse test cannot catch.
	create := exec.Command("sqlite3", db, `
CREATE TABLE gis_info_table (ID INTEGER PRIMARY KEY, file_name TEXT, star INT, highlight INT, video_index INT, image_index INT);
CREATE TABLE video_info_table (ID INTEGER PRIMARY KEY, duration INT, resolution_width INT, resolution_height INT, frame_num INT, frame_den INT, encode_format INT, steady_mode INT, nd_value INT, ev_bias INT, ei_value INT, aperture INT, rotation INT, slowmotion_rate INT, fov_type INT, gps_status INT);
INSERT INTO video_info_table VALUES (1, 24000, 3840, 2160, 100000, 1000, 2, 2, 0, 17, 0, 280, 0, 1, 1, 0);
INSERT INTO gis_info_table VALUES (1, '/mnt/media_rw/sd/DCIM/clip.mp4', 2, 1, 1, 0);`)
	if out, err := create.CombinedOutput(); err != nil {
		t.Fatalf("create media index: %v: %s", err, out)
	}
	store := &fileStore{app: a}
	meta, err := store.nativeMediaMeta(context.Background(), "/sd/DCIM/clip.mp4")
	if err != nil || meta == nil || !meta.Indexed {
		t.Fatalf("native meta failed: %+v %v", meta, err)
	}
	if meta.ApertureRaw == nil || *meta.ApertureRaw != 280 {
		t.Fatalf("aperture_raw not queried: %+v", meta.ApertureRaw)
	}
	if meta.FOVTypeRaw == nil || *meta.FOVTypeRaw != 1 {
		t.Fatalf("fov_type_raw not queried: %+v", meta.FOVTypeRaw)
	}
	if meta.EVBias == nil || *meta.EVBias != 17 {
		t.Fatalf("ev_bias: %+v", meta.EVBias)
	}
	if meta.SlowmotionRate == nil || *meta.SlowmotionRate != 1 {
		t.Fatalf("slowmotion_rate: %+v", meta.SlowmotionRate)
	}
}

func TestDeleteUnderLockedIndexReturnsSoftWarning(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 not installed")
	}
	a, h := testApplication(t)
	sd, _ := mediaIndexLocationFor("sd")
	db := a.Path(sd.Database)
	if err := os.MkdirAll(filepath.Dir(db), 0700); err != nil {
		t.Fatal(err)
	}
	target := a.Path("/mnt/media_rw/sd/DCIM/clip.mp4")
	putTestFile(t, target, []byte("data"), 0600)
	create := exec.Command("sqlite3", db, `
CREATE TABLE gis_info_table (ID INTEGER PRIMARY KEY, file_name TEXT);
INSERT INTO gis_info_table(file_name) VALUES ('/mnt/media_rw/sd/DCIM/clip.mp4');`)
	if out, err := create.CombinedOutput(); err != nil {
		t.Fatalf("create media index: %v: %s", err, out)
	}

	// Hold the write lock like dji_media_server so the index sync cannot commit.
	holder := exec.Command("sqlite3", db)
	stdin, err := holder.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = holder.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close(); _ = holder.Wait() }()
	if _, err = io.WriteString(stdin, "BEGIN IMMEDIATE;\n"); err != nil {
		t.Fatal(err)
	}
	locked := func() bool {
		probe := exec.Command("sqlite3", db)
		probe.Stdin = strings.NewReader(".bail on\n.timeout 200\nBEGIN IMMEDIATE;\nROLLBACK;\n")
		return probe.Run() != nil
	}
	deadline := time.Now().Add(5 * time.Second)
	for !locked() {
		if time.Now().After(deadline) {
			t.Fatal("peer connection did not acquire the write lock")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The file unlink succeeds; the locked index sync must degrade to a soft
	// warning (200), never a hard 500, or the user sees a spurious delete error.
	w := requestTest(a, h, "DELETE", "/api/delete", strings.NewReader(`{"path":"/sd/DCIM/clip.mp4"}`), nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "warning") {
		t.Fatalf("locked delete was not a soft warning: %d %s", w.Code, w.Body.String())
	}
	if _, err = os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file was not deleted: %v", err)
	}
}

func TestRangeHeadAndLargeSparseMedia(t *testing.T) {
	a, h := testApplication(t)
	file, err := os.Create(a.Path("/blackbox/large.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	const size = int64(8 << 30)
	if err = file.Truncate(size); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if _, err = file.WriteAt([]byte("last-bytes\n"), size-11); err != nil {
		file.Close()
		t.Fatal(err)
	}
	file.Close()
	w := requestTest(a, h, "GET", "/api/video_stream?file=/large.mp4", nil, map[string]string{"Range": "bytes=-11"})
	if w.Code != 206 || w.Body.String() != "last-bytes\n" || w.Header().Get("Content-Range") != "bytes 8589934581-8589934591/8589934592" {
		t.Fatalf("range: %d %q %q", w.Code, w.Body.String(), w.Header().Get("Content-Range"))
	}
	w = requestTest(a, h, "HEAD", "/api/download?file=/large.mp4", nil, nil)
	if w.Code != 200 || w.Body.Len() != 0 || w.Header().Get("Content-Length") != "8589934592" {
		t.Fatalf("HEAD did not preserve size without body: %d", w.Code)
	}
	if w = requestTest(a, h, "GET", "/api/video_stream?file=/large.mp4", nil, map[string]string{"Range": "bytes=9000000000-"}); w.Code != 416 {
		t.Fatalf("invalid range: %d", w.Code)
	}
}
