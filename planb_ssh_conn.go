package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

type bForward struct {
	address string
	cmd     *exec.Cmd
	done    chan error
	once    sync.Once
	cleanup func()
}

func (a *App) bStartForward(ctx context.Context, c bProfile) (*bForward, error) {
	port, e := freePort()
	if e != nil {
		return nil, e
	}
	address := "127.0.0.1:" + strconv.Itoa(port)
	opts := []string{"-N", "-o", "ExitOnForwardFailure=yes", "-L", address + fmt.Sprintf(":127.0.0.1:%d", c.WebPort)}
	cmd := exec.CommandContext(ctx, "ssh", sshOptions(c.sshBase(false), opts)...)
	privateProcess(cmd)
	bHideWindow(cmd)
	cmd.Stderr = os.Stderr
	if e = cmd.Start(); e != nil {
		return nil, e
	}
	cleanup, e := bTrackProcess(cmd)
	if e != nil {
		cmd.Process.Kill()
		cmd.Wait()
		return nil, e
	}
	forward := &bForward{address: address, cmd: cmd, done: make(chan error, 1), cleanup: cleanup}
	go func() { forward.done <- cmd.Wait() }()
	return forward, nil
}
func (f *bForward) Close() {
	f.once.Do(func() { f.cleanup(); stopProcess(f.cmd); killProcessTree(f.cmd) })
}

type bForwardConn struct {
	net.Conn
	forward *bForward
	once    sync.Once
}

func (c *bForwardConn) Close() error {
	e := c.Conn.Close()
	c.once.Do(func() {
		if c.forward != nil {
			c.forward.Close()
		}
	})
	return e
}
func (a *App) bDialSSH(ctx context.Context, c bProfile) (net.Conn, error) {
	config, e := bTLSConfig(c)
	if e != nil {
		return nil, e
	}
	var forward *bForward
	address := c.forwardAddress
	if address == "" {
		forward, e = a.bStartForward(ctx, c)
		if e != nil {
			return nil, e
		}
		address = forward.address
	}
	fail := func(e error) (net.Conn, error) {
		if forward != nil {
			forward.Close()
		}
		return nil, e
	}
	for i := 0; i < 300; i++ {
		if ctx.Err() != nil {
			return fail(ctx.Err())
		}
		if forward != nil {
			select {
			case err := <-forward.done:
				return fail(fmt.Errorf("私网 SSH 转发退出：%v", err))
			default:
			}
		}
		raw, err := net.DialTimeout("tcp", address, 200*time.Millisecond)
		if err != nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		secured := tls.Client(raw, config)
		handshake, cancel := context.WithTimeout(ctx, 15*time.Second)
		err = secured.HandshakeContext(handshake)
		cancel()
		if err != nil {
			raw.Close()
			return fail(fmt.Errorf("B 后端 TLS 身份验证失败：%w", err))
		}
		return &bForwardConn{Conn: secured, forward: forward}, nil
	}
	return fail(errors.New("SSH 本地转发未就绪"))
}
