package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"sync"
)

type Config struct {
	Schema          int     `json:"schema"`
	ThemeMode       string  `json:"theme_mode"`
	AutoConnect     bool    `json:"auto_connect"`
	ThumbConcurrent int     `json:"thumb_concurrent"`
	CamW            int     `json:"cam_w"`
	CamH            int     `json:"cam_h"`
	CamFPS          int     `json:"cam_fps"`
	CamExtPort      int     `json:"cam_ext_port"`
	CamQuality      int     `json:"cam_quality"`
	CamBitrate      float64 `json:"cam_bitrate"`
}
type ConfigStore struct {
	mu    sync.RWMutex
	name  string
	value Config
}

func defaultConfig() Config {
	return Config{Schema: 1, ThemeMode: "auto", ThumbConcurrent: 3, CamW: 1280, CamH: 720, CamFPS: 30, CamExtPort: 8554, CamQuality: 1, CamBitrate: 4.4}
}

func validateConfig(c Config) error {
	if c.Schema != 1 {
		return errors.New("unsupported configuration schema")
	}
	if c.ThemeMode != "auto" && c.ThemeMode != "light" && c.ThemeMode != "dark" {
		return errors.New("invalid theme")
	}
	if c.ThumbConcurrent < 1 || c.ThumbConcurrent > 8 {
		return errors.New("thumbnail concurrency must be 1–8")
	}
	if c.CamW < 320 || c.CamW > 7680 || c.CamH < 240 || c.CamH > 4320 || c.CamW%2 != 0 || c.CamH%2 != 0 {
		return errors.New("invalid even camera dimensions")
	}
	if c.CamFPS < 1 || c.CamFPS > 240 || c.CamExtPort < 1024 || c.CamExtPort > 65535 || c.CamExtPort == 8080 {
		return errors.New("invalid frame rate or streaming port")
	}
	if c.CamQuality < 0 || c.CamQuality > 3 || math.IsNaN(c.CamBitrate) || math.IsInf(c.CamBitrate, 0) || c.CamBitrate < 0.5 || c.CamBitrate > 1000 {
		return errors.New("invalid quality or bitrate")
	}
	return nil
}
func loadConfig(name string) (*ConfigStore, error) {
	s := &ConfigStore{name: name, value: defaultConfig()}
	b, err := os.ReadFile(name)
	if errors.Is(err, os.ErrNotExist) {
		return s, s.Update(func(*Config) error { return nil })
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(b, &s.value); err != nil {
		return nil, fmt.Errorf("configuration: %w", err)
	}
	if err = validateConfig(s.value); err != nil {
		return nil, err
	}
	return s, nil
}
func (s *ConfigStore) Read() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.value
}
func (s *ConfigStore) Update(change func(*Config) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.value
	if err := change(&next); err != nil {
		return err
	}
	if err := validateConfig(next); err != nil {
		return err
	}
	b, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	if err = atomicWrite(s.name, append(b, '\n'), 0600); err != nil {
		return err
	}
	s.value = next
	return nil
}

type configPatch struct {
	ThemeMode       *string  `json:"theme_mode"`
	AutoConnect     *bool    `json:"auto_connect"`
	ThumbConcurrent *int     `json:"thumb_concurrent"`
	CamW            *int     `json:"cam_w"`
	CamH            *int     `json:"cam_h"`
	CamFPS          *int     `json:"cam_fps"`
	CamExtPort      *int     `json:"cam_ext_port"`
	CamQuality      *int     `json:"cam_quality"`
	CamBitrate      *float64 `json:"cam_bitrate"`
}

func (p configPatch) apply(c *Config) error {
	if p.ThemeMode != nil {
		c.ThemeMode = *p.ThemeMode
	}
	if p.AutoConnect != nil {
		c.AutoConnect = *p.AutoConnect
	}
	if p.ThumbConcurrent != nil {
		c.ThumbConcurrent = *p.ThumbConcurrent
	}
	if p.CamW != nil {
		c.CamW = *p.CamW
	}
	if p.CamH != nil {
		c.CamH = *p.CamH
	}
	if p.CamFPS != nil {
		c.CamFPS = *p.CamFPS
	}
	if p.CamExtPort != nil {
		c.CamExtPort = *p.CamExtPort
	}
	if p.CamQuality != nil {
		c.CamQuality = *p.CamQuality
	}
	if p.CamBitrate != nil {
		c.CamBitrate = *p.CamBitrate
	}
	return nil
}
func registerConfig(mux *http.ServeMux, a *App) {
	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, 200, struct {
			Config
			Version string `json:"version"`
		}{a.Config.Read(), Version})
	})
	mux.HandleFunc("POST /api/config", func(w http.ResponseWriter, r *http.Request) {
		var p configPatch
		if !readJSON(w, r, &p) {
			return
		}
		if err := a.Config.Update(p.apply); err != nil {
			jsonError(w, 400, "config_update", err)
			return
		}
		jsonResponse(w, 200, a.Config.Read())
	})
	mux.HandleFunc("GET /api/auto_connect", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, 200, map[string]bool{"enabled": a.Config.Read().AutoConnect})
	})
	mux.HandleFunc("POST /api/auto_connect", func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Enabled *bool `json:"enabled"`
		}
		if !readJSON(w, r, &p) {
			return
		}
		if p.Enabled == nil {
			jsonError(w, 400, "invalid_config", errors.New("enabled is required"))
			return
		}
		if err := a.Config.Update(func(c *Config) error { c.AutoConnect = *p.Enabled; return nil }); err != nil {
			jsonError(w, 500, "config_write", err)
			return
		}
		jsonResponse(w, 200, map[string]bool{"enabled": *p.Enabled})
	})
}
