//go:build !windows

package main

import (
	"os"
	"os/exec"
	"syscall"
)

func safeOpenFlags() int           { return syscall.O_NONBLOCK | syscall.O_NOFOLLOW }
func shutdownSignals() []os.Signal { return []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP} }
func privateProcess(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
func stopProcess(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
}

func secureDirectory(path string) error { return os.Chmod(path, 0700) }
func verifyUserPipe(string) error       { return nil }
