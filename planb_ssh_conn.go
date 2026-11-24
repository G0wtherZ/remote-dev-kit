package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os/exec"
	"sync"
	"time"
)

type bSSHConn struct {
	command *exec.Cmd
	input   io.WriteCloser
	output  io.ReadCloser
	once    sync.Once
	cleanup func()
}

func (a *App) bDialSSH(ctx context.Context, c bProfile) (net.Conn, error) {
	cmd := exec.CommandContext(ctx, "ssh", sshOptions(c.sshBase(false), []string{"-W", fmt.Sprintf("127.0.0.1:%d", c.WebPort)})...)
	privateProcess(cmd)
	bHideWindow(cmd)
	input, e := cmd.StdinPipe()
	if e != nil {
		return nil, e
	}
	output, e := cmd.StdoutPipe()
	if e != nil {
		input.Close()
		return nil, e
	}
	if e = cmd.Start(); e != nil {
		input.Close()
		output.Close()
		return nil, e
	}
	cleanup, e := bTrackProcess(cmd)
	if e != nil {
		cmd.Process.Kill()
		cmd.Wait()
		return nil, e
	}
	return &bSSHConn{command: cmd, input: input, output: output, cleanup: cleanup}, nil
}
func (c *bSSHConn) Read(b []byte) (int, error)  { return c.output.Read(b) }
func (c *bSSHConn) Write(b []byte) (int, error) { return c.input.Write(b) }
func (c *bSSHConn) Close() error {
	c.once.Do(func() {
		c.input.Close()
		c.output.Close()
		c.cleanup()
		stopProcess(c.command)
		killProcessTree(c.command)
		c.command.Wait()
	})
	return nil
}
func (*bSSHConn) LocalAddr() net.Addr  { return bSSHAddr("local-private-ssh") }
func (*bSSHConn) RemoteAddr() net.Addr { return bSSHAddr("remote-b-loopback") }

// HTTP/WebSocket contexts own cancellation and close the SSH process on timeout.
func (*bSSHConn) SetDeadline(time.Time) error      { return nil }
func (*bSSHConn) SetReadDeadline(time.Time) error  { return nil }
func (*bSSHConn) SetWriteDeadline(time.Time) error { return nil }

type bSSHAddr string

func (bSSHAddr) Network() string  { return "private-ssh" }
func (a bSSHAddr) String() string { return string(a) }
