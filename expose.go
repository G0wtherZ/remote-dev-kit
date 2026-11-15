package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type portLease struct {
	Socket string `json:"socket"`
	Port   int    `json:"port"`
}

func preparePort(root string) error {
	if _, e := os.Stat(filepath.Join(root, ".rdev-root.json")); e != nil {
		return errors.New("研发目录尚未初始化")
	}
	dir, e := os.MkdirTemp("", "rdev-port-")
	if e != nil {
		return e
	}
	socket := filepath.Join(dir, "port.sock")
	if len(socket) > 95 {
		os.Remove(dir)
		return errors.New("远端临时目录太长，无法建立私有 socket")
	}
	b, _ := json.Marshal(map[string]string{"owner": "remote-dev-kit", "root": root})
	if e = atomicWrite(filepath.Join(dir, "owner.json"), b, 0600); e != nil {
		os.Remove(dir)
		return e
	}
	return output(portLease{Socket: socket})
}
func validatePortLease(root, socket string) error {
	if filepath.Base(socket) != "port.sock" || !strings.HasPrefix(filepath.Base(filepath.Dir(socket)), "rdev-port-") {
		return errors.New("非本工具端口租约")
	}
	if info, e := os.Lstat(filepath.Dir(socket)); e != nil || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("端口租约目录无效")
	}
	b, e := os.ReadFile(filepath.Join(filepath.Dir(socket), "owner.json"))
	if e != nil {
		return e
	}
	var marker map[string]string
	if json.Unmarshal(b, &marker) != nil || marker["owner"] != "remote-dev-kit" || marker["root"] != root {
		return errors.New("端口租约不属于此研发目录")
	}
	return nil
}
func removePort(root, socket string) error {
	if e := validatePortLease(root, socket); e != nil {
		return e
	}
	_ = os.Remove(socket)
	_ = os.Remove(filepath.Join(filepath.Dir(socket), "owner.json"))
	return os.Remove(filepath.Dir(socket))
}
func relayPort(root, socket string) error {
	if e := validatePortLease(root, socket); e != nil {
		return e
	}
	ready := false
	for i := 0; i < 100; i++ {
		if info, e := os.Stat(socket); e == nil && info.Mode()&os.ModeSocket != 0 {
			ready = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		return errors.New("私有端口通道没有就绪")
	}
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		return e
	}
	defer listener.Close()
	if e = output(portLease{Socket: socket, Port: listener.Addr().(*net.TCPAddr).Port}); e != nil {
		return e
	}
	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals()...)
	defer stop()
	go func() { io.Copy(io.Discard, os.Stdin); stop() }()
	go func() { <-ctx.Done(); listener.Close() }()
	for {
		incoming, e := listener.Accept()
		if e != nil {
			if ctx.Err() != nil {
				return nil
			}
			return e
		}
		go func() {
			defer incoming.Close()
			out, e := net.DialTimeout("unix", socket, 3*time.Second)
			if e != nil {
				return
			}
			defer out.Close()
			go func() {
				io.Copy(out, incoming)
				if u, ok := out.(*net.UnixConn); ok {
					u.CloseWrite()
				}
			}()
			io.Copy(incoming, out)
		}()
	}
}
func (a *App) expose(args []string) error {
	f := flag.NewFlagSet("expose", flag.ContinueOnError)
	minutes := f.Int("minutes", 60, "授权持续分钟数")
	yes := f.Bool("yes", false, "接受指定端口授权")
	if e := f.Parse(args); e != nil {
		return e
	}
	rest := f.Args()
	if len(rest) != 1 || *minutes < 1 || *minutes > 1440 {
		return errors.New("用法：rdev expose [--minutes 60] [--yes] <本机 TCP 端口>")
	}
	port, e := strconv.Atoi(rest[0])
	if e != nil || port < 1 || port > 65535 {
		return errors.New("无效端口")
	}
	c, e := a.load()
	if e != nil {
		return e
	}
	if !*yes {
		p := Prompter{bufio.NewReader(os.Stdin)}
		ok, e := p.yes(fmt.Sprintf("允许远端主机的本地进程访问本机 127.0.0.1:%d，持续 %d 分钟", port, *minutes), false)
		if e != nil {
			return e
		}
		if !ok {
			return errors.New("已取消")
		}
	}
	b, e := a.remote(c, []string{"port-prepare", c.RemoteRoot}, nil)
	if e != nil {
		return e
	}
	lease, e := decode[portLease](b)
	if e != nil {
		return e
	}
	defer a.remote(c, []string{"port-remove", c.RemoteRoot, lease.Socket}, nil)
	opts := []string{"-N", "-o", "ExitOnForwardFailure=yes", "-R", lease.Socket + ":127.0.0.1:" + strconv.Itoa(port)}
	tunnel := exec.Command("ssh", sshOptions(a.sshArgs(c, false), opts)...)
	tunnel.Stderr = os.Stderr
	if e = tunnel.Start(); e != nil {
		return e
	}
	defer func() { tunnel.Process.Kill(); tunnel.Wait() }()
	remote := exec.Command("ssh", append(a.sshArgs(c, false), shellJoin([]string{c.RemoteBinary, "remote", "port-relay", c.RemoteRoot, lease.Socket}))...)
	keepalive, e := remote.StdinPipe()
	if e != nil {
		return e
	}
	out, e := remote.StdoutPipe()
	if e != nil {
		return e
	}
	remote.Stderr = os.Stderr
	if e = remote.Start(); e != nil {
		return e
	}
	defer func() { keepalive.Close(); remote.Process.Kill(); remote.Wait() }()
	scan := bufio.NewScanner(out)
	var text strings.Builder
	for scan.Scan() {
		text.WriteString(scan.Text())
		text.WriteByte('\n')
		if json.Unmarshal([]byte(text.String()), &lease) == nil && lease.Port > 0 {
			break
		}
	}
	if lease.Port < 1 {
		return errors.New("端口转发没有就绪")
	}
	fmt.Printf("远端地址：http://127.0.0.1:%d → 本机 127.0.0.1:%d；%d 分钟后或 Ctrl+C 撤销。\n", lease.Port, port, *minutes)
	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals()...)
	defer stop()
	select {
	case <-ctx.Done():
	case <-time.After(time.Duration(*minutes) * time.Minute):
	}
	return nil
}
