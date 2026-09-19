package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

type FileItem struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime"`
}
type fileStore struct {
	app      *App
	mu       sync.Mutex
	thumbs   chan struct{}
	cacheMu  sync.Mutex
	mediaMu  sync.Mutex
	catalogs []*mediaCatalog
}

func virtualPath(name string) (base string, parts []string, err error) {
	if name == "" {
		name = "/"
	}
	if !strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\x00\\") {
		return "", nil, fmt.Errorf("%w: invalid virtual path", os.ErrPermission)
	}
	for _, p := range strings.Split(name, "/") {
		if p == ".." || p == "." {
			return "", nil, fmt.Errorf("%w: relative traversal is forbidden", os.ErrPermission)
		}
	}
	clean := path.Clean(name)
	base = "/blackbox"
	if clean == "/sd" || strings.HasPrefix(clean, "/sd/") {
		base = "/mnt/media_rw/sd"
		clean = strings.TrimPrefix(clean, "/sd")
	} else if clean == "/emulated" || strings.HasPrefix(clean, "/emulated/") {
		base = "/mnt/media_rw/emulated"
		clean = strings.TrimPrefix(clean, "/emulated")
	}
	if clean != "" && clean != "/" {
		parts = strings.Split(strings.TrimPrefix(clean, "/"), "/")
	}
	if base == "/blackbox" && len(parts) > 0 && (parts[0] == "action-control" || parts[0] == "dashboard") {
		return "", nil, os.ErrPermission
	}
	return base, parts, nil
}
func validName(name string) bool {
	return name != "" && name != "." && name != ".." && len(name) <= 255 && !strings.ContainsAny(name, "/\\\x00") && !strings.HasPrefix(name, ".action-control-upload-")
}

// Every component is opened relative to an already-owned directory descriptor.
// O_NOFOLLOW rejects symlink aliases, including aliases to private app state.
func (s *fileStore) parent(name string) (*os.File, string, error) {
	base, parts, err := virtualPath(name)
	if err != nil {
		return nil, "", err
	}
	root, err := os.OpenRoot(s.app.Path(base))
	if err != nil {
		return nil, "", err
	}
	defer root.Close()
	dir, err := root.OpenFile(".", os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, "", err
	}
	for _, part := range parts[:max(0, len(parts)-1)] {
		fd, e := unix.Openat(int(dir.Fd()), part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		dir.Close()
		if e != nil {
			return nil, "", e
		}
		dir = os.NewFile(uintptr(fd), part)
	}
	leaf := "."
	if len(parts) > 0 {
		leaf = parts[len(parts)-1]
	}
	return dir, leaf, nil
}
func (s *fileStore) open(name string) (*os.File, error) {
	parent, leaf, err := s.parent(name)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	fd, err := unix.Openat(int(parent.Fd()), leaf, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	st, err := f.Stat()
	if err != nil || (!st.IsDir() && !st.Mode().IsRegular()) {
		f.Close()
		if err == nil {
			err = errors.New("unsupported file type")
		}
		return nil, err
	}
	return f, nil
}
func fileError(w http.ResponseWriter, err error) {
	status := 500
	code := "file_error"
	switch {
	case errors.Is(err, os.ErrNotExist):
		status = 404
		code = "not_found"
	case errors.Is(err, os.ErrPermission), errors.Is(err, unix.ELOOP):
		status = 403
		code = "path_forbidden"
	case errors.Is(err, os.ErrExist), errors.Is(err, unix.ENOTEMPTY):
		status = 409
		code = "already_exists"
	case errors.Is(err, unix.ENOTDIR):
		status = 400
		code = "not_directory"
	}
	jsonError(w, status, code, err)
}
func (s *fileStore) list(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("dir")
	if dir == "" {
		dir = "/"
	}
	f, err := s.open(dir)
	if err != nil {
		fileError(w, err)
		return
	}
	defer f.Close()
	entries, err := f.Readdirnames(-1)
	if err != nil {
		fileError(w, err)
		return
	}
	items := make([]FileItem, 0, len(entries))
	warnings := []string{}
	for _, entry := range entries {
		if strings.HasPrefix(entry, ".action-control-upload-") {
			continue
		}
		child := path.Join(dir, entry)
		if _, _, err := virtualPath(child); err != nil {
			continue
		}
		var info unix.Stat_t
		if err := unix.Fstatat(int(f.Fd()), entry, &info, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			warnings = append(warnings, fmt.Sprintf("%s: %v", child, err))
			continue
		}
		kind := info.Mode & unix.S_IFMT
		if kind != unix.S_IFDIR && kind != unix.S_IFREG {
			continue
		}
		items = append(items, FileItem{entry, child, kind == unix.S_IFDIR, info.Size, info.Mtim.Sec})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].IsDir != items[j].IsDir {
			return items[i].IsDir
		}
		return items[i].Name < items[j].Name
	})
	crumbs := []map[string]string{{"name": "Blackbox", "path": "/"}}
	if dir == "/sd" || strings.HasPrefix(dir, "/sd/") {
		crumbs = []map[string]string{{"name": "SD 卡", "path": "/sd"}}
	} else if dir == "/emulated" || strings.HasPrefix(dir, "/emulated/") {
		crumbs = []map[string]string{{"name": "内部存储", "path": "/emulated"}}
	}
	prefix := crumbs[0]["path"]
	rest := strings.TrimPrefix(path.Clean(dir), prefix)
	for _, part := range strings.Split(strings.Trim(rest, "/"), "/") {
		if part != "" {
			prefix = path.Join(prefix, part)
			crumbs = append(crumbs, map[string]string{"name": part, "path": prefix})
		}
	}
	parent := path.Dir(path.Clean(dir))
	if path.Clean(dir) == crumbs[0]["path"] {
		parent = path.Clean(dir)
	}
	jsonResponse(w, 200, map[string]any{"items": items, "path": path.Clean(dir), "parent": parent, "crumbs": crumbs, "warnings": warnings})
}
func (s *fileStore) download(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("file")
	f, err := s.open(name)
	if err != nil {
		fileError(w, err)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		fileError(w, err)
		return
	}
	if !st.Mode().IsRegular() {
		jsonError(w, 400, "not_file", errors.New("expected a regular file"))
		return
	}
	disposition := "inline"
	ext := strings.ToLower(path.Ext(name))
	if r.URL.Query().Get("dl") == "1" || ext == ".html" || ext == ".htm" || ext == ".svg" || ext == ".xml" {
		disposition = "attachment"
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": path.Base(name)}))
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	if disposition == "attachment" && (ext == ".html" || ext == ".htm" || ext == ".svg" || ext == ".xml") {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	http.ServeContent(w, r, path.Base(name), st.ModTime(), f)
}
func (s *fileStore) upload(w http.ResponseWriter, r *http.Request) {
	defer s.invalidateMedia()
	dir := r.URL.Query().Get("dir")
	if dir == "" {
		dir = "/"
	}
	f, err := s.open(dir)
	if err != nil {
		fileError(w, err)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.IsDir() {
		jsonError(w, 400, "not_directory", errors.New("upload destination must be a directory"))
		return
	}
	// Bounded total transfer, never buffered in memory; sufficient for long camera recordings.
	r.Body = http.MaxBytesReader(w, r.Body, 256<<30)
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(2 * time.Hour))
	defer http.NewResponseController(w).SetReadDeadline(time.Time{})
	mr, err := r.MultipartReader()
	if err != nil {
		jsonError(w, 400, "multipart", err)
		return
	}
	saved := []string{}
	fail := func(err error, status int) {
		jsonResponse(w, status, map[string]any{"error": map[string]string{"code": "upload_failed", "message": err.Error()}, "files": saved})
	}
	for {
		part, e := mr.NextPart()
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			fail(e, 400)
			return
		}
		name := part.FileName()
		if !validName(name) {
			part.Close()
			fail(errors.New("invalid file name"), 400)
			return
		}
		if _, _, e = virtualPath(path.Join(dir, name)); e != nil {
			part.Close()
			fail(e, 403)
			return
		}
		id := make([]byte, 16)
		if _, e = rand.Read(id); e != nil {
			part.Close()
			fail(e, 500)
			return
		}
		tmp := ".action-control-upload-" + hex.EncodeToString(id)
		fd, e := unix.Openat(int(f.Fd()), tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
		if e != nil {
			part.Close()
			fail(e, 500)
			return
		}
		out := os.NewFile(uintptr(fd), tmp)
		_, e = io.Copy(out, part)
		e = errors.Join(e, part.Close())
		if e == nil {
			e = out.Sync()
		}
		e = errors.Join(e, out.Close())
		if e == nil {
			s.mu.Lock()
			e = renameNoReplace(int(f.Fd()), tmp, int(f.Fd()), name)
			s.mu.Unlock()
		}
		if e != nil {
			_ = unix.Unlinkat(int(f.Fd()), tmp, 0)
			status := 500
			if errors.Is(e, unix.EEXIST) {
				status = 409
			} else if r.Context().Err() != nil {
				status = 400
			}
			fail(e, status)
			return
		}
		saved = append(saved, name)
		if e = f.Sync(); e != nil {
			fail(e, 500)
			return
		}
	}
	if len(saved) == 0 {
		fail(errors.New("no files uploaded"), 400)
		return
	}
	jsonResponse(w, 201, map[string]any{"ok": true, "files": saved})
}
func removeAt(fd int, name string) error {
	var st unix.Stat_t
	if err := unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return unix.Unlinkat(fd, name, 0)
	}
	child, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(child), name)
	defer f.Close()
	for {
		entries, e := f.Readdirnames(100)
		for _, entry := range entries {
			if e := removeAt(child, entry); e != nil {
				return e
			}
		}
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			return e
		}
	}
	return unix.Unlinkat(fd, name, unix.AT_REMOVEDIR)
}
func (s *fileStore) change(w http.ResponseWriter, r *http.Request) {
	var data struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if !readJSON(w, r, &data) {
		return
	}
	parent, leaf, err := s.parent(data.Path)
	if err != nil {
		fileError(w, err)
		return
	}
	defer parent.Close()
	if leaf == "." && !strings.HasSuffix(r.URL.Path, "mkdir") {
		jsonError(w, 403, "root_protected", errors.New("storage roots cannot be changed"))
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var indexedDir bool
	defer s.invalidateMedia()
	switch r.URL.Path {
	case "/api/delete":
		var st unix.Stat_t
		if err = unix.Fstatat(int(parent.Fd()), leaf, &st, unix.AT_SYMLINK_NOFOLLOW); err == nil {
			indexedDir = st.Mode&unix.S_IFMT == unix.S_IFDIR
			err = removeAt(int(parent.Fd()), leaf)
		}
	case "/api/rename":
		if !validName(data.Name) {
			jsonError(w, 400, "invalid_name", errors.New("invalid destination name"))
			return
		}
		var st unix.Stat_t
		if _, _, indexedStorage := mediaIndexForPath(data.Path); indexedStorage && unix.Fstatat(int(parent.Fd()), leaf, &st, unix.AT_SYMLINK_NOFOLLOW) == nil {
			indexed, indexErr := s.mediaIndexContains(r.Context(), data.Path, st.Mode&unix.S_IFMT == unix.S_IFDIR)
			if indexErr != nil {
				jsonError(w, 409, "media_index_unavailable", indexErr)
				return
			}
			if indexed {
				jsonError(w, 409, "indexed_media_rename", errors.New("AC004.db 中的媒体不能安全重命名；请使用相机原生界面"))
				return
			}
		}
		if _, _, err = virtualPath(path.Join(path.Dir(data.Path), data.Name)); err == nil {
			err = renameNoReplace(int(parent.Fd()), leaf, int(parent.Fd()), data.Name)
		}
	case "/api/mkdir":
		if !validName(data.Name) {
			jsonError(w, 400, "invalid_name", errors.New("invalid directory name"))
			return
		}
		if _, _, err = virtualPath(path.Join(data.Path, data.Name)); err != nil {
			fileError(w, err)
			return
		}
		var dir *os.File
		dir, err = s.open(data.Path)
		if err == nil {
			err = unix.Mkdirat(int(dir.Fd()), data.Name, 0755)
			if err == nil {
				err = dir.Sync()
			}
			dir.Close()
		}
	}
	if err == nil {
		err = parent.Sync()
	}
	if err != nil {
		fileError(w, err)
		return
	}
	if r.URL.Path == "/api/delete" {
		if err = s.removeMediaIndexPath(r.Context(), data.Path, indexedDir); err != nil {
			// The file is already deleted. A locked index is recoverable via the
			// stale-index cleanup, so report it as a warning, not a failure.
			if errors.Is(err, errMediaIndexLocked) {
				jsonResponse(w, 200, map[string]any{"ok": true, "warning": err.Error()})
				return
			}
			jsonError(w, 500, "media_index_sync", err)
			return
		}
	}
	jsonResponse(w, 200, map[string]bool{"ok": true})
}
func (s *fileStore) usage(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("path")
	if name == "" {
		name = "/"
	}
	f, err := s.open(name)
	if err != nil {
		fileError(w, err)
		return
	}
	defer f.Close()
	var st unix.Statfs_t
	if err = unix.Fstatfs(int(f.Fd()), &st); err != nil {
		fileError(w, err)
		return
	}
	total := uint64(st.Blocks) * uint64(st.Bsize)
	free := uint64(st.Bavail) * uint64(st.Bsize)
	used := (uint64(st.Blocks) - uint64(st.Bfree)) * uint64(st.Bsize)
	pct := 0.0
	if total > 0 {
		pct = float64(used) / float64(total) * 100
	}
	jsonResponse(w, 200, map[string]any{"path": name, "total": total, "used": used, "free": free, "pct": pct})
}
func RegisterFiles(mux *http.ServeMux, a *App) (func() error, error) {
	s := &fileStore{app: a, thumbs: make(chan struct{}, a.Config.Read().ThumbConcurrent)}
	if err := os.MkdirAll(filepath.Join(a.RunDir, "thumbnails"), 0700); err != nil {
		return nil, err
	}
	mux.HandleFunc("GET /api/list", s.list)
	mux.HandleFunc("GET /api/download", s.download)
	mux.HandleFunc("GET /api/video_stream", s.download)
	mux.HandleFunc("POST /api/upload", s.upload)
	mux.HandleFunc("DELETE /api/delete", s.change)
	mux.HandleFunc("POST /api/rename", s.change)
	mux.HandleFunc("POST /api/mkdir", s.change)
	mux.HandleFunc("GET /api/disk_usage", s.usage)
	mux.HandleFunc("GET /api/media_list", s.mediaList)
	mux.HandleFunc("GET /api/media_info", s.mediaInfo)
	mux.HandleFunc("GET /api/thumbnail", s.thumbnail)
	mux.HandleFunc("GET /api/media_index_status", func(w http.ResponseWriter, r *http.Request) {
		storage := r.URL.Query().Get("storage")
		status, _, err := s.scanMediaIndex(r.Context(), storage)
		if err != nil {
			statusCode, code, message := mediaIndexErrorCode(err)
			if _, ok := mediaIndexLocationFor(storage); !ok {
				statusCode, code, message = 400, "invalid_storage", err
			}
			jsonError(w, statusCode, code, message)
			return
		}
		jsonResponse(w, 200, status)
	})
	mux.HandleFunc("POST /api/clean_media_index", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Storage string `json:"storage"`
			Confirm bool   `json:"confirm"`
		}
		if !readJSON(w, r, &request) {
			return
		}
		if !request.Confirm {
			jsonError(w, 400, "confirmation_required", errors.New("explicit confirmation required"))
			return
		}
		if _, ok := mediaIndexLocationFor(request.Storage); !ok {
			jsonError(w, 400, "invalid_storage", errors.New("unknown storage"))
			return
		}
		if err := a.RequireManaged(); err != nil {
			jsonError(w, 503, "device_unavailable", err)
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		result, err := s.cleanMediaIndex(r.Context(), request.Storage)
		if err != nil {
			statusCode, code, message := mediaIndexErrorCode(err)
			jsonError(w, statusCode, code, message)
			return
		}
		jsonResponse(w, 200, result)
	})
	return nil, nil
}
