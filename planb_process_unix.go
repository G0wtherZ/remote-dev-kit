//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

func killProcessTree(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

func bHideWindow(*exec.Cmd)                   {}
func bTrackProcess(*exec.Cmd) (func(), error) { return func() {}, nil }
