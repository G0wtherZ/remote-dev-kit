package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// This executor is deliberately separate from scheme A's scoped exportFS.
// B explicitly grants the current OS user's command/file permissions.
type bExecutor struct {
	listener    net.Listener
	config      *ssh.ServerConfig
	cwd         string
	ctx         context.Context
	cancel      context.CancelFunc
	connections sync.Map
	active      atomic.Int32
	wg          sync.WaitGroup
}

func bNewKey() ([]byte, ssh.Signer, error) {
	_, key, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		return nil, nil, e
	}
	block, e := ssh.MarshalPrivateKey(key, "remote-dev-kit-plan-b")
	if e != nil {
		return nil, nil, e
	}
	signer, e := ssh.NewSignerFromKey(key)
	return pem.EncodeToMemory(block), signer, e
}
func bStartExecutor(host, client ssh.Signer, cwd string) (*bExecutor, error) {
	info, e := os.Stat(cwd)
	if e != nil || !info.IsDir() {
		return nil, errors.New("本机项目目录不存在")
	}
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		return nil, e
	}
	ctx, cancel := context.WithCancel(context.Background())
	x := &bExecutor{listener: listener, cwd: cwd, ctx: ctx, cancel: cancel}
	x.config = &ssh.ServerConfig{MaxAuthTries: 3, PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if meta.User() != "rdev-device" || !bytes.Equal(key.Marshal(), client.PublicKey().Marshal()) {
			return nil, errors.New("device authentication refused")
		}
		return &ssh.Permissions{}, nil
	}}
	x.config.AddHostKey(host)
	x.wg.Add(1)
	go x.accept()
	return x, nil
}
func (x *bExecutor) accept() {
	defer x.wg.Done()
	for {
		conn, e := x.listener.Accept()
		if e != nil {
			return
		}
		if x.active.Add(1) > 32 {
			x.active.Add(-1)
			conn.Close()
			continue
		}
		x.connections.Store(conn, true)
		x.wg.Add(1)
		go func() {
			defer x.wg.Done()
			defer x.active.Add(-1)
			defer x.connections.Delete(conn)
			defer conn.Close()
			x.serve(conn)
		}()
	}
}
func (x *bExecutor) close() {
	x.cancel()
	x.listener.Close()
	x.connections.Range(func(key, value any) bool { key.(net.Conn).Close(); return true })
	x.wg.Wait()
}
func (x *bExecutor) serve(conn net.Conn) {
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	server, channels, requests, e := ssh.NewServerConn(conn, x.config)
	if e != nil {
		return
	}
	defer server.Close()
	conn.SetDeadline(time.Time{})
	ctx, cancel := context.WithCancel(x.ctx)
	defer cancel()
	go func() { server.Wait(); cancel() }()
	go ssh.DiscardRequests(requests)
	var channelsWG sync.WaitGroup
	defer channelsWG.Wait()
	slots := make(chan struct{}, 16)
	for offered := range channels {
		if offered.ChannelType() != "session" {
			offered.Reject(ssh.UnknownChannelType, "only session channels")
			continue
		}
		select {
		case slots <- struct{}{}:
		default:
			offered.Reject(ssh.ResourceShortage, "channel limit")
			continue
		}
		channel, requests, e := offered.Accept()
		if e != nil {
			<-slots
			continue
		}
		channelsWG.Add(1)
		go func() {
			defer channelsWG.Done()
			defer func() { <-slots }()
			defer channel.Close()
			x.session(ctx, channel, requests)
		}()
	}
}
func (x *bExecutor) session(parent context.Context, channel ssh.Channel, requests <-chan *ssh.Request) {
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	started := false
	defer func() {
		cancel()
		channel.Close()
		if started {
			<-done
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case request, ok := <-requests:
			if !ok {
				return
			}
			if started {
				request.Reply(false, nil)
				continue
			}
			switch request.Type {
			case "exec":
				var body struct{ Command string }
				if ssh.Unmarshal(request.Payload, &body) != nil || len(body.Command) > 1024*1024 || strings.ContainsRune(body.Command, 0) {
					request.Reply(false, nil)
					continue
				}
				started = true
				request.Reply(true, nil)
				go func() {
					defer close(done)
					code := x.execute(ctx, channel, body.Command)
					channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{code}))
					channel.CloseWrite()
				}()
			case "subsystem":
				var body struct{ Name string }
				if ssh.Unmarshal(request.Payload, &body) != nil || body.Name != "sftp" {
					request.Reply(false, nil)
					continue
				}
				opts := []sftp.ServerOption{sftp.WithServerWorkingDirectory(x.cwd)}
				if runtime.GOOS == "windows" {
					opts = append(opts, sftp.WindowsRootEnumeratesDrives())
				}
				server, e := sftp.NewServer(channel, opts...)
				if e != nil {
					request.Reply(false, nil)
					continue
				}
				started = true
				request.Reply(true, nil)
				go func() {
					defer close(done)
					defer server.Close()
					server.Serve()
					channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
					channel.CloseWrite()
				}()
			default:
				request.Reply(false, nil)
			}
		}
	}
}
func bShell(command string) (*exec.Cmd, error) {
	if runtime.GOOS == "windows" {
		shell, e := exec.LookPath("pwsh.exe")
		if e != nil {
			shell, e = exec.LookPath("powershell.exe")
		}
		if e != nil {
			return nil, errors.New("本机缺少 PowerShell；请安装 PowerShell 7 或启用系统 PowerShell")
		}
		encoding := `[Console]::InputEncoding = New-Object System.Text.UTF8Encoding($false); [Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false); $OutputEncoding=[Console]::OutputEncoding; `
		return exec.Command(shell, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", encoding+command), nil
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	if !filepath.IsAbs(shell) {
		return nil, errors.New("SHELL 必须是绝对路径")
	}
	// Inherit the launching terminal's environment without rerunning login scripts
	// for every tool call (which can print or repeat setup side effects).
	return exec.Command(shell, "-c", command), nil
}
func (x *bExecutor) execute(ctx context.Context, channel ssh.Channel, command string) uint32 {
	cmd, e := bShell(command)
	if e != nil {
		fmt.Fprintln(channel.Stderr(), e)
		return 127
	}
	cmd.WaitDelay = 1500 * time.Millisecond
	cmd.Dir = x.cwd
	cmd.Env = os.Environ()
	privateProcess(cmd)
	bHideWindow(cmd)
	// Pipes belong to the child; cancelling closes stdin too, so blocked input never holds cleanup.
	stdin, e := cmd.StdinPipe()
	if e != nil {
		return 127
	}
	cmd.Stdout = channel
	cmd.Stderr = channel.Stderr()
	if e = cmd.Start(); e != nil {
		stdin.Close()
		fmt.Fprintln(channel.Stderr(), e)
		return 127
	}
	cleanup, e := bTrackProcess(cmd)
	if e != nil {
		stdin.Close()
		cmd.Process.Kill()
		cmd.Wait()
		fmt.Fprintln(channel.Stderr(), "无法建立命令回收机制：", e)
		return 127
	}
	defer cleanup()
	go func() { io.Copy(stdin, channel); stdin.Close() }()
	finished := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			cleanup()
			stdin.Close()
			stopProcess(cmd)
			select {
			case <-finished:
			case <-time.After(time.Second):
				killProcessTree(cmd)
			}
		case <-finished:
		}
	}()
	e = cmd.Wait()
	close(finished)
	stdin.Close()
	// Reclaim descendants even when the foreground command exited normally.
	killProcessTree(cmd)
	if e == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(e, &exit) && exit.ExitCode() >= 0 {
		return uint32(exit.ExitCode())
	}
	return 255
}
