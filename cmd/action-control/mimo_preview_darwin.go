package main

import "os/exec"

// Device observers are unavailable in local filesystem mode.
func protectPreviewChild(cmd *exec.Cmd) {}
