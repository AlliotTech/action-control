package main

import "errors"

func signalProcess(_ int, _ string, _ int, _ *App) error {
	return errors.New("process signaling is only supported on the camera")
}
