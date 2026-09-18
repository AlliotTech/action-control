package main

import (
	"errors"
	"golang.org/x/sys/unix"
)

func signalProcess(pid int, start string, signal int, a *App) error {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	current, err := readProcess(a, pid)
	if err != nil {
		return err
	}
	if current.Start != start {
		return errors.New("process identity changed; refresh process list")
	}
	return unix.PidfdSendSignal(fd, unix.Signal(signal), nil, 0)
}
