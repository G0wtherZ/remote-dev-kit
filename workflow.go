package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"
)

func (a *App) terminal(kind string, args []string) error {
	c, e := a.load()
	if e != nil {
		return e
	}
	if kind == "claude" && c.AutoShare {
		if e = a.share([]string{"on"}); e != nil {
			return e
		}
	}
	return interactive("ssh", append(a.sshArgs(c, true), shellJoin(append([]string{c.RemoteBinary, "remote", "terminal", c.RemoteRoot, kind}, args...))))
}
func (a *App) reconnect(args []string) error {
	c, e := a.load()
	if e != nil {
		return e
	}
	return interactive("ssh", append(a.sshArgs(c, true), shellJoin([]string{c.RemoteBinary, "remote", "reconnect", c.RemoteRoot})))
}
func (a *App) repair() error {
	c, e := a.load()
	if e != nil {
		return e
	}
	if c.NetworkMode == "managed" {
		if e = a.startNetwork(c); e != nil {
			return e
		}
	}
	b, e := admin(c, []string{c.BootstrapBinary, "remote", "probe", c.RemoteRoot}, nil)
	if e != nil {
		return e
	}
	info, e := decode[RemoteInfo](b)
	if e != nil {
		return e
	}
	for _, ip := range info.IPs {
		if v := net.ParseIP(ip); v != nil && v.To4() != nil {
			c.RemoteIP = ip
			break
		}
	}
	if e = a.writeSSH(c, info.HostKey); e != nil {
		return e
	}
	if e = a.save(c); e != nil {
		return e
	}
	if c.AutoShare {
		if e = a.share([]string{"on"}); e != nil {
			return e
		}
	}
	return a.doctor(nil)
}
func (a *App) doctor(args []string) error {
	c, e := a.load()
	if e != nil {
		return e
	}
	result := map[string]any{"local_root": c.LocalRoot, "remote_root": c.RemoteRoot, "network_mode": c.NetworkMode, "connection_ready": false, "claude_optional": true}
	if b, e := a.ts(c, "status", "--json"); e == nil {
		if n, e := decode[NetStatus](b); e == nil {
			result["local_network"] = n.BackendState
			result["local_network_health"] = n.Health
		}
	}
	b, e := a.remote(c, []string{"probe", c.RemoteRoot}, nil)
	if e != nil {
		result["connection_error"] = e.Error()
		output(result)
		return errors.New("私网 SSH 尚未就绪")
	}
	info, e := decode[RemoteInfo](b)
	if e != nil {
		return e
	}
	result["remote"] = info
	result["connection_ready"] = true
	result["mount_ready"] = info.Tools["sshfs"] != ""
	if info.OS == "darwin" {
		result["macos_mount_note"] = "需要兼容 FSKit 的 macFUSE/SSHFS；本工具不会降低系统安全设置"
	}
	if info.UID == 0 {
		result["account_note"] = "当前远端是 root；Claude 启动将被拒绝，请使用普通开发账户"
	}
	return output(result)
}
func (a *App) share(args []string) error {
	c, e := a.load()
	if e != nil {
		return e
	}
	if len(args) == 0 {
		return errors.New("用法：share on|off|status")
	}
	switch args[0] {
	case "on":
		b, e := a.remote(c, []string{"probe", c.RemoteRoot}, nil)
		if e != nil {
			return e
		}
		r, e := decode[RemoteInfo](b)
		if e != nil {
			return e
		}
		if r.Tools["sshfs"] == "" {
			return errors.New("远端缺少 SSHFS；运行 rdev tools install base")
		}
		c.AutoShare = true
		if e = a.save(c); e != nil {
			return e
		}
		if e = startManaged(a, "share"); e != nil {
			return e
		}
		for i := 0; i < 20; i++ {
			b, err := a.remote(c, []string{"probe", c.RemoteRoot}, nil)
			if err == nil {
				if info, err := decode[RemoteInfo](b); err == nil && info.Mounted {
					fmt.Printf("已挂载 %s → %s/local\n", c.LocalRoot, c.RemoteRoot)
					return nil
				}
			}
			time.Sleep(300 * time.Millisecond)
		}
		return errors.New("挂载尚未就绪；查看 logs/share.log，未把空目录当作成功挂载")
	case "off":
		c.AutoShare = false
		if e = a.save(c); e != nil {
			return e
		}
		_ = stopManaged(a, "share")
		_, e = a.remote(c, []string{"unmount", c.RemoteRoot}, nil)
		return e
	case "status":
		b, e := a.remote(c, []string{"probe", c.RemoteRoot}, nil)
		if e != nil {
			return e
		}
		r, e := decode[RemoteInfo](b)
		if e != nil {
			return e
		}
		return output(map[string]any{"mounted": r.Mounted, "local_root": c.LocalRoot, "remote_mount": r.Mount, "readonly": c.Readonly})
	case "serve":
		if !c.AutoShare {
			return nil
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		for ctx.Err() == nil {
			cmd := exec.CommandContext(ctx, "ssh", append(a.sshArgs(c, false), shellJoin([]string{c.RemoteBinary, "remote", "mount", c.RemoteRoot}))...)
			in, e := cmd.StdinPipe()
			if e != nil {
				return e
			}
			out, e := cmd.StdoutPipe()
			if e != nil {
				return e
			}
			cmd.Stderr = os.Stderr
			if e = cmd.Start(); e != nil {
				return e
			}
			stream := &duplex{Reader: out, writer: in, closer: out}
			e = serveExport(c.LocalRoot, c.Readonly, stream)
			stream.Close()
			wait := cmd.Wait()
			if ctx.Err() != nil {
				return nil
			}
			fmt.Fprintf(os.Stderr, "共享连接已断开，将重试（%v / %v）\n", e, wait)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(3 * time.Second):
			}
		}
		return nil
	default:
		return errors.New("share on|off|status")
	}
}

type duplex struct {
	io.Reader
	writer io.WriteCloser
	closer io.Closer
}

func (d *duplex) Write(p []byte) (int, error) { return d.writer.Write(p) }
func (d *duplex) Close() error                { d.writer.Close(); return d.closer.Close() }
func (a *App) agent(args []string) error {
	c, e := a.load()
	if e != nil {
		return e
	}
	if len(args) != 1 {
		return errors.New("agent install|login")
	}
	switch args[0] {
	case "install":
		return interactive("ssh", append(a.sshArgs(c, true), shellJoin([]string{c.RemoteBinary, "remote", "install-agent"})))
	case "login":
		return a.terminal("claude", nil)
	default:
		return errors.New("agent install|login")
	}
}
func (a *App) tools(args []string) error {
	if len(args) != 2 || args[0] != "install" {
		return errors.New("tools install base|node|browser|tailscale")
	}
	c, e := a.load()
	if e != nil {
		return e
	}
	if args[1] == "browser" {
		return interactive("ssh", append(a.sshArgs(c, true), shellJoin([]string{c.RemoteBinary, "remote", "install-browser"})))
	}
	if args[1] == "node" {
		return interactive("ssh", append(a.sshArgs(c, true), shellJoin([]string{c.RemoteBinary, "remote", "install-node"})))
	}
	return adminInteractive(c, shellJoin([]string{c.BootstrapBinary, "remote", "dependencies", args[1]}))
}
func (a *App) uninstall(args []string) error {
	c, e := a.load()
	if e != nil {
		return e
	}
	_ = stopManaged(a, "share")
	_, _ = a.remote(c, []string{"unmount", c.RemoteRoot}, nil)
	if c.NetworkMode == "managed" {
		_ = stopManaged(a, "network")
	}
	for _, kind := range []string{"share", "network"} {
		if runtime.GOOS == "darwin" {
			file := managedPlist(a, kind)
			if b, e := os.ReadFile(file); e == nil && strings.Contains(string(b), xmlEscape(a.State)) {
				_ = os.Remove(file)
			}
		} else if runtime.GOOS == "windows" {
			_ = exec.Command("schtasks", "/Delete", "/TN", serviceID(a, kind), "/F").Run()
		}
	}
	// Preserve identity, Tailscale state and all data. A forgotten profile must not
	// silently create a fresh node or delete another installation's configuration.
	return output(map[string]any{"services_removed": true, "data_preserved": []string{c.LocalRoot, c.RemoteRoot}, "private_state_preserved": a.State, "note": "如需注销设备，请在自己的 Tailscale 管理页操作；本命令不会删除共享文件或远端账户。"})
}
