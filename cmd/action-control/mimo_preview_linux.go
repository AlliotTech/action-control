package main

import (
	"os/exec"
	"syscall"
)

func protectPreviewChild(cmd *exec.Cmd) {
	// Context cancellation handles orderly exits. Also stop this observer if
	// the web service is killed before its cleanup runs.
	cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
}
