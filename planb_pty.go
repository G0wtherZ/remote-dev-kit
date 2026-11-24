package main

import (
	"os"
	"os/exec"
)

type bPTY struct {
	File   *os.File
	Cmd    *exec.Cmd
	Resize func(uint16, uint16) error
}

func (p *bPTY) Close() { p.File.Close(); stopProcess(p.Cmd); killProcessTree(p.Cmd); p.Cmd.Wait() }
