package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

const cameraPresetFile = "camera-last-success.json"

type cameraPresetConfig struct {
	Width   int     `json:"cam_w"`
	Height  int     `json:"cam_h"`
	FPS     int     `json:"cam_fps"`
	Port    int     `json:"cam_ext_port"`
	Quality int     `json:"cam_quality"`
	Bitrate float64 `json:"cam_bitrate"`
}
type cameraPreset struct {
	Config        cameraPresetConfig `json:"config"`
	SavedAt       string             `json:"saved_at"`
	Firmware      string             `json:"firmware"`
	FirmwareMatch bool               `json:"firmware_match"`
}

func cameraFirmware(a *App) string {
	data, err := os.ReadFile(a.Path("/build.prop"))
	if err != nil {
		return ""
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func saveCameraPreset(a *App, cfg Config) error {
	if err := validateConfig(cfg); err != nil {
		return err
	}
	preset := cameraPreset{
		Config:   cameraPresetConfig{cfg.CamW, cfg.CamH, cfg.CamFPS, cfg.CamExtPort, cfg.CamQuality, cfg.CamBitrate},
		SavedAt:  time.Now().UTC().Format(time.RFC3339),
		Firmware: cameraFirmware(a),
	}
	data, err := json.Marshal(preset)
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(a.Dir, cameraPresetFile), data, 0600)
}

func readCameraPreset(a *App) (*cameraPreset, error) {
	data, err := os.ReadFile(filepath.Join(a.Dir, cameraPresetFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var preset cameraPreset
	if err := json.Unmarshal(data, &preset); err != nil {
		return nil, err
	}
	cfg := defaultConfig()
	cfg.CamW, cfg.CamH, cfg.CamFPS = preset.Config.Width, preset.Config.Height, preset.Config.FPS
	cfg.CamExtPort, cfg.CamQuality, cfg.CamBitrate = preset.Config.Port, preset.Config.Quality, preset.Config.Bitrate
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	if _, err := time.Parse(time.RFC3339, preset.SavedAt); err != nil {
		return nil, err
	}
	preset.FirmwareMatch = preset.Firmware != "" && preset.Firmware == cameraFirmware(a)
	return &preset, nil
}
