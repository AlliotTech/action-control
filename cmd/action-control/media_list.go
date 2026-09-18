package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const mediaScanLimit = 100000
const mediaSnapshotLifetime = 5 * time.Minute

type mediaCatalog struct {
	id        string
	dir       string
	created   time.Time
	items     []MediaItem
	warnings  []string
	truncated bool
}

type mediaCursor struct {
	ID     string `json:"id"`
	Offset int    `json:"offset"`
	Query  string `json:"query"`
}

func (s *fileStore) invalidateMedia() {
	s.mediaMu.Lock()
	s.catalogs = nil
	s.mediaMu.Unlock()
}

func (s *fileStore) scanMedia(ctx context.Context, dir string) (*mediaCatalog, error) {
	root, err := s.open(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	catalog := &mediaCatalog{id: rand.Text(), dir: dir, created: time.Now(), items: []MediaItem{}, warnings: []string{}}
	limitReached := errors.New("media scan limit")
	warn := func(name string, err error) {
		if len(catalog.warnings) < 100 {
			catalog.warnings = append(catalog.warnings, fmt.Sprintf("%s: %v", name, err))
		}
	}
	var walk func(*os.File, string, int) error
	walk = func(f *os.File, base string, depth int) error {
		if depth > 32 {
			catalog.truncated = true
			return nil
		}
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			entries, readErr := f.Readdirnames(128)
			for _, entry := range entries {
				if len(catalog.items) >= mediaScanLimit {
					catalog.truncated = true
					return limitReached
				}
				if strings.HasPrefix(entry, ".action-control-") {
					continue
				}
				virtual := path.Join(base, entry)
				if _, _, err := virtualPath(virtual); err != nil {
					continue
				}
				var info unix.Stat_t
				if err := unix.Fstatat(int(f.Fd()), entry, &info, unix.AT_SYMLINK_NOFOLLOW); err != nil {
					if !errors.Is(err, os.ErrNotExist) {
						warn(virtual, err)
					}
					continue
				}
				if info.Mode&unix.S_IFMT == unix.S_IFDIR {
					child, err := s.open(virtual)
					if err != nil {
						warn(virtual, err)
						continue
					}
					err = walk(child, virtual, depth+1)
					child.Close()
					if errors.Is(err, limitReached) {
						return err
					}
					if err := ctx.Err(); err != nil {
						return err
					}
					if err != nil {
						warn(virtual, err)
					}
					continue
				}
				kind := mediaKind(path.Ext(entry))
				if kind != "" && info.Mode&unix.S_IFMT == unix.S_IFREG {
					catalog.items = append(catalog.items, MediaItem{entry, virtual, info.Size, info.Mtim.Sec, kind, strings.ToLower(path.Ext(entry))})
				}
			}
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			if readErr != nil {
				return readErr
			}
		}
	}
	if err := walk(root, dir, 0); err != nil && !errors.Is(err, limitReached) {
		return nil, err
	}
	return catalog, nil
}

func (s *fileStore) mediaList(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	dir := query.Get("dir")
	if dir == "" {
		dir = "/emulated"
	}
	// Recheck the directory even when serving a cached snapshot.
	f, err := s.open(dir)
	if err != nil {
		fileError(w, err)
		return
	}
	info, err := f.Stat()
	f.Close()
	if err != nil {
		fileError(w, err)
		return
	}
	if !info.IsDir() {
		fileError(w, unix.ENOTDIR)
		return
	}
	limit := 80
	if value := query.Get("limit"); value != "" {
		limit, err = strconv.Atoi(value)
		if err != nil || limit < 1 || limit > 200 {
			jsonError(w, 400, "invalid_page", errors.New("每页数量必须在 1–200 之间"))
			return
		}
	}
	kind, search, order := query.Get("type"), strings.ToLower(query.Get("search")), query.Get("sort")
	if kind == "" {
		kind = "all"
	}
	if order == "" {
		order = "newest"
	}
	if !slices.Contains([]string{"all", "image", "video"}, kind) || !slices.Contains([]string{"newest", "oldest", "name", "largest"}, order) || len(search) > 1024 {
		jsonError(w, 400, "invalid_filter", errors.New("无效的相册筛选或排序"))
		return
	}
	hash := sha256.Sum256([]byte(dir + "\x00" + kind + "\x00" + search + "\x00" + order))
	fingerprint := hex.EncodeToString(hash[:16])
	var cursor mediaCursor
	if value := query.Get("cursor"); value != "" {
		data, decodeErr := base64.RawURLEncoding.DecodeString(value)
		if len(value) > 1024 || decodeErr != nil || json.Unmarshal(data, &cursor) != nil || cursor.ID == "" || cursor.Offset < 0 || cursor.Query != fingerprint || query.Get("refresh") == "true" {
			jsonError(w, 400, "invalid_cursor", errors.New("分页位置与当前筛选不匹配，请刷新相册"))
			return
		}
	}

	s.mediaMu.Lock()
	var catalog *mediaCatalog
	now := time.Now()
	s.catalogs = slices.DeleteFunc(s.catalogs, func(item *mediaCatalog) bool { return now.Sub(item.created) > mediaSnapshotLifetime })
	for _, item := range s.catalogs {
		if cursor.ID != "" {
			if item.id == cursor.ID && item.dir == dir {
				catalog = item
				break
			}
		} else if query.Get("refresh") != "true" && item.dir == dir && now.Sub(item.created) < 30*time.Second {
			catalog = item
		}
	}
	if catalog == nil && cursor.ID != "" {
		s.mediaMu.Unlock()
		jsonError(w, 410, "media_snapshot_expired", errors.New("媒体列表已更新或过期，请刷新后继续浏览"))
		return
	}
	if catalog == nil {
		catalog, err = s.scanMedia(r.Context(), dir)
		if err == nil {
			s.catalogs = append(s.catalogs, catalog)
			count := 0
			for _, item := range s.catalogs {
				count += len(item.items)
			}
			for len(s.catalogs) > 1 && (len(s.catalogs) > 4 || count > mediaScanLimit) {
				count -= len(s.catalogs[0].items)
				s.catalogs = s.catalogs[1:]
			}
		}
	}
	s.mediaMu.Unlock()
	if err != nil {
		fileError(w, err)
		return
	}
	items := make([]MediaItem, 0)
	for _, item := range catalog.items {
		if (kind == "all" || item.Type == kind) && strings.Contains(strings.ToLower(item.Name), search) {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i], items[j]
		switch order {
		case "name":
			if a.Name != b.Name {
				return a.Name < b.Name
			}
		case "largest":
			if a.Size != b.Size {
				return a.Size > b.Size
			}
		case "oldest":
			if a.Mtime != b.Mtime {
				return a.Mtime < b.Mtime
			}
		default:
			if a.Mtime != b.Mtime {
				return a.Mtime > b.Mtime
			}
		}
		return a.Path < b.Path
	})
	if cursor.Offset > len(items) {
		jsonError(w, 400, "invalid_cursor", errors.New("无效的分页位置"))
		return
	}
	end := min(len(items), cursor.Offset+limit)
	next := ""
	if end < len(items) {
		data, _ := json.Marshal(mediaCursor{ID: catalog.id, Offset: end, Query: fingerprint})
		next = base64.RawURLEncoding.EncodeToString(data)
	}
	jsonResponse(w, 200, map[string]any{"items": items[cursor.Offset:end], "count": len(items), "next_cursor": next, "truncated": catalog.truncated, "warnings": catalog.warnings})
}
