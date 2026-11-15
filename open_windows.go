package main

import (
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"syscall"
)

func safeOpenFlags() int           { return 0 } // os.Root enforces Windows reparse-point containment.
func shutdownSignals() []os.Signal { return []os.Signal{os.Interrupt, syscall.SIGTERM} }
func privateProcess(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000200} }
func stopProcess(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = exec.Command("taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F").Run()
	}
}

func secureDirectory(path string) error {
	u, e := user.Current()
	if e != nil {
		return e
	}
	_, e = capture("icacls", []string{path, "/inheritance:r", "/grant:r", "*" + u.Uid + ":(OI)(CI)F", "*S-1-5-18:(OI)(CI)F", "/Q"}, nil)
	return e
}
