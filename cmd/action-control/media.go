package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

type MediaItem struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime"`
	Type  string `json:"type"`
	Ext   string `json:"ext"`
}

func mediaKind(ext string) string {
	switch strings.ToLower(ext) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".svg":
		return "image"
	case ".mp4", ".webm", ".avi", ".mov", ".mkv", ".mpg", ".mpeg", ".3gp":
		return "video"
	}
	return ""
}
func inputFDPath() string {
	if runtime.GOOS == "linux" {
		return "/proc/self/fd/3"
	}
	return "/dev/fd/3"
}

const allowedMediaFormats = "mov,matroska,webm,avi,mpegts,mpeg,mpegvideo,image2,image2pipe,jpeg_pipe,png_pipe,gif,bmp_pipe,webp_pipe,hevc,h264"

func (s *fileStore) mediaOutput(ctx context.Context, name string, input *os.File, args ...string) ([]byte, error) {
	program, err := s.app.Tool(name)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := ownedCommand(ctx, program, args...)
	cmd.ExtraFiles = []*os.File{input}
	stdout, stderr := &boundedBuffer{limit: 4 << 20}, &boundedBuffer{limit: 64 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err = cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s: %w: %s", name, err, stderr.Text())
	}
	if stdout.truncated {
		return nil, errors.New("media tool output exceeds limit")
	}
	return []byte(stdout.Text()), nil
}
func (s *fileStore) mediaInfo(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("file")
	f, err := s.open(name)
	if err != nil {
		fileError(w, err)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		jsonError(w, 400, "not_file", errors.New("expected a media file"))
		return
	}
	native, _ := s.nativeMediaMeta(r.Context(), name)
	if cfg, format, e := image.DecodeConfig(io.LimitReader(f, 2<<20)); e == nil {
		result := map[string]any{"width": cfg.Width, "height": cfg.Height, "codec": strings.ToUpper(format), "size": st.Size()}
		if native != nil {
			result["native"] = native
		}
		jsonResponse(w, 200, result)
		return
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		fileError(w, err)
		return
	}
	select {
	case s.thumbs <- struct{}{}:
		defer func() { <-s.thumbs }()
	case <-r.Context().Done():
		return
	}
	b, err := s.mediaOutput(r.Context(), "ffprobe", f, "-v", "error", "-protocol_whitelist", "file,pipe", "-format_whitelist", allowedMediaFormats, "-print_format", "json", "-show_format", "-show_streams", inputFDPath())
	if err != nil {
		jsonError(w, 422, "media_probe", err)
		return
	}
	var probe struct {
		Streams []struct {
			Type    string `json:"codec_type"`
			Codec   string `json:"codec_name"`
			Width   int    `json:"width"`
			Height  int    `json:"height"`
			FPS     string `json:"r_frame_rate"`
			Bitrate string `json:"bit_rate"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
			Bitrate  string `json:"bit_rate"`
		} `json:"format"`
	}
	if err = json.Unmarshal(b, &probe); err != nil {
		jsonError(w, 502, "media_probe", err)
		return
	}
	for _, stream := range probe.Streams {
		if stream.Type != "video" {
			continue
		}
		result := map[string]any{"width": stream.Width, "height": stream.Height, "codec": stream.Codec, "size": st.Size()}
		if duration, e := strconv.ParseFloat(probe.Format.Duration, 64); e == nil && duration >= 0 && !math.IsInf(duration, 0) && !math.IsNaN(duration) {
			result["duration_sec"] = duration
		}
		rate := strings.Split(stream.FPS, "/")
		if len(rate) == 2 {
			num, e1 := strconv.ParseFloat(rate[0], 64)
			den, e2 := strconv.ParseFloat(rate[1], 64)
			if e1 == nil && e2 == nil && den > 0 && num >= 0 {
				result["fps"] = num / den
			}
		}
		bitrate := stream.Bitrate
		if bitrate == "" {
			bitrate = probe.Format.Bitrate
		}
		if v, e := strconv.ParseInt(bitrate, 10, 64); e == nil {
			result["bitrate_bps"] = v
		}
		if native != nil {
			result["native"] = native
		}
		jsonResponse(w, 200, result)
		return
	}
	jsonError(w, 422, "no_video", errors.New("no image or video stream found"))
}
func (s *fileStore) thumbnail(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("file")
	input, err := s.open(name)
	if err != nil {
		fileError(w, err)
		return
	}
	defer input.Close()
	st, err := input.Stat()
	if err != nil || !st.Mode().IsRegular() {
		jsonError(w, 400, "not_file", errors.New("expected a media file"))
		return
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%d", name, st.Size(), st.ModTime().UnixNano())))
	cacheDir := filepath.Join(s.app.RunDir, "thumbnails")
	cache := filepath.Join(cacheDir, hex.EncodeToString(sum[:])+".jpg")
	serveCached := func() bool {
		f, e := os.Open(cache)
		if e != nil {
			return false
		}
		defer f.Close()
		info, e := f.Stat()
		if e != nil || info.Size() == 0 {
			return false
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "private, max-age=3600")
		http.ServeContent(w, r, "thumbnail.jpg", info.ModTime(), f)
		return true
	}
	if serveCached() {
		return
	}
	select {
	case s.thumbs <- struct{}{}:
		defer func() { <-s.thumbs }()
	case <-r.Context().Done():
		return
	}
	if serveCached() {
		return
	}
	if err = os.MkdirAll(cacheDir, 0700); err != nil {
		fileError(w, err)
		return
	}
	tmp, err := os.CreateTemp(cacheDir, ".thumb-*")
	if err != nil {
		fileError(w, err)
		return
	}
	defer tmp.Close()
	defer os.Remove(tmp.Name())
	cfg, _, decodeErr := image.DecodeConfig(io.LimitReader(input, 2<<20))
	if _, err = input.Seek(0, io.SeekStart); err != nil {
		fileError(w, err)
		return
	}
	if decodeErr == nil {
		if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 40_000_000 {
			jsonError(w, 413, "image_dimensions", errors.New("thumbnail image exceeds 40 megapixels"))
			return
		}
		img, _, e := image.Decode(input)
		if e != nil {
			jsonError(w, 422, "image_decode", e)
			return
		}
		ratio := math.Min(1, 512/float64(max(cfg.Width, cfg.Height)))
		width, height := max(1, int(float64(cfg.Width)*ratio)), max(1, int(float64(cfg.Height)*ratio))
		scaled := image.NewRGBA(image.Rect(0, 0, width, height))
		bounds := img.Bounds()
		// ponytail: nearest-neighbor preview capped at 512px; use a resampler only if thumbnail quality requires it.
		for y := 0; y < height; y++ {
			if r.Context().Err() != nil {
				return
			}
			for x := 0; x < width; x++ {
				source := img.At(bounds.Min.X+x*cfg.Width/width, bounds.Min.Y+y*cfg.Height/height)
				rr, gg, bb, aa := source.RGBA()
				if aa == 0 {
					source = color.White
				} else {
					source = color.RGBA{uint8(rr >> 8), uint8(gg >> 8), uint8(bb >> 8), 255}
				}
				scaled.Set(x, y, source)
			}
		}
		err = jpeg.Encode(tmp, scaled, &jpeg.Options{Quality: 75})
	} else {
		var b []byte
		b, err = s.mediaOutput(r.Context(), "ffmpeg", input, "-v", "error", "-threads", "2", "-protocol_whitelist", "file,pipe", "-format_whitelist", allowedMediaFormats, "-i", inputFDPath(), "-frames:v", "1", "-vf", "scale=512:512:force_original_aspect_ratio=decrease", "-an", "-sn", "-dn", "-threads", "2", "-c:v", "mjpeg", "-q:v", "8", "-f", "image2pipe", "pipe:1")
		if err == nil {
			_, err = tmp.Write(b)
		}
	}
	if err == nil {
		err = tmp.Sync()
	}
	if err == nil {
		err = tmp.Close()
	}
	if err == nil {
		err = os.Rename(tmp.Name(), cache)
	}
	if err != nil {
		jsonError(w, 422, "thumbnail_failed", err)
		return
	}
	s.trimThumbnails(cacheDir)
	if !serveCached() {
		jsonError(w, 500, "thumbnail_missing", errors.New("thumbnail cache was removed; retry request"))
	}
}
func (s *fileStore) trimThumbnails(dir string) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type entry struct {
		name  string
		size  int64
		mtime time.Time
	}
	files := []entry{}
	var total int64
	for _, f := range entries {
		if !strings.HasSuffix(f.Name(), ".jpg") {
			continue
		}
		st, e := f.Info()
		if e == nil && st.Mode().IsRegular() {
			files = append(files, entry{f.Name(), st.Size(), st.ModTime()})
			total += st.Size()
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mtime.Before(files[j].mtime) })
	for i, f := range files {
		if total <= 128<<20 && len(files)-i <= 2048 {
			break
		}
		if os.Remove(filepath.Join(dir, f.name)) == nil {
			total -= f.size
		}
	}
}
