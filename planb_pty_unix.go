//go:build !windows

package main

import (
	"github.com/creack/pty"
	"os"
	"os/exec"
)

func bOpenPTY(command []string, rows, cols uint16) (*bPTY, error) {
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	file, e := pty.StartWithSize(cmd, &pty.Winsize{Rows: rows, Cols: cols})
	if e != nil {
		return nil, e
	}
	return &bPTY{File: file, Cmd: cmd, Resize: func(r, c uint16) error { return pty.Setsize(file, &pty.Winsize{Rows: r, Cols: c}) }}, nil
}
