package main

import (
	"bufio"
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
	"time"
)

func (a *App) terminal(kind string, args []string) error {
	c, e := a.load()
	if e != nil {
		return e
	}
	if _, e = a.preflight(c, capTerminal); e != nil {
		return e
	}
	if kind == "claude" && c.AutoShare {
		if e = a.share([]string{"on"}); e != nil {
			fmt.Fprintf(os.Stderr, "本机共享未就绪：%v\n", e)
			p := &Prompter{bufio.NewReader(os.Stdin)}
			ok, err := p.yes("本次不等待共享恢复，继续远端工作（已有授权不变）", false)
			if err != nil {
				return e
			}
			if !ok {
				return e
			}
		}
	}
	return interactive("ssh", append(a.sshArgs(c, true), shellJoin(append([]string{c.RemoteBinary, "remote", "terminal", c.RemoteRoot, kind}, args...))))
}
func (a *App) reconnect(args []string) error {
	c, e := a.load()
	if e != nil {
		return e
	}
	if _, e = a.preflight(c, capTerminal); e != nil {
		return e
	}
	return interactive("ssh", append(a.sshArgs(c, true), shellJoin([]string{c.RemoteBinary, "remote", "reconnect", c.RemoteRoot})))
}
func (a *App) refreshConnection(c *Config) error {
	info, e := a.bootstrapProbe(*c)
	if e != nil {
		return fmt.Errorf("管理连接或远端助手不可用；助手损坏可运行 rdev reset：%w", e)
	}
	if info.NetworkState != "Running" {
		return errors.New("远端 Tailscale 未就绪，请通过原 SSH 检查登录；未更改原网络设置")
	}
	address := ""
	for _, ip := range info.IPs {
		if v := net.ParseIP(ip); v != nil && v.To4() != nil {
			address = ip
			break
		}
	}
	if address == "" {
		return errors.New("远端没有私网地址")
	}
	next := *c
	next.RemoteIP = address
	if e = a.writeSSH(next, info.HostKey); e != nil {
		return e
	}
	if e = a.save(next); e != nil {
		return e
	}
	*c = next
	return nil
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
	if e = a.refreshConnection(&c); e != nil {
		return e
	}
	if c.AutoShare {
		b, err := a.remote(c, []string{"probe", c.RemoteRoot}, nil)
		if err != nil {
			return err
		}
		info, err := decode[RemoteInfo](b)
		if err != nil {
			return err
		}
		if info.Mounted && !info.MountReadable {
			if err = stopManaged(a, "share"); err != nil {
				return err
			}
			if _, err = a.remote(c, []string{"unmount", c.RemoteRoot}, nil); err != nil {
				return fmt.Errorf("残留挂载仍被占用，未结束任何开发任务：%w", err)
			}
		}
		if e = a.share([]string{"on"}); e != nil {
			return e
		}
	}
	return a.doctor(nil)
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
		if _, e = a.preflight(c, capShare, capMountHealth); e != nil {
			return e
		}
		b, e := a.remote(c, []string{"probe", c.RemoteRoot}, nil)
		if e != nil {
			return e
		}
		r, e := decode[RemoteInfo](b)
		if e != nil {
			return e
		}
		if e = checkLocalShare(c); e != nil {
			return e
		}
		if r.Mounted && r.MountReadable && managedRunning(a, "share") {
			c.AutoShare = true
			if e = a.save(c); e != nil {
				return e
			}
			fmt.Println("共享已连接。")
			return nil
		}
		if r.Mounted && !r.MountReadable {
			return errors.New("发现不可读的残留挂载；运行 rdev repair 安全重建连接")
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
				if info, err := decode[RemoteInfo](b); err == nil && info.Mounted && info.MountReadable {
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
		ctx, cancel := signal.NotifyContext(context.Background(), shutdownSignals()...)
		defer cancel()
		for ctx.Err() == nil {
			c, e = a.load()
			if e != nil {
				return e
			}
			if !c.AutoShare {
				return nil
			}
			if e = checkLocalShare(c); e != nil {
				fmt.Fprintln(os.Stderr, e)
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(3 * time.Second):
					continue
				}
			}
			a.helperCache = nil
			if _, e = a.preflight(c, capShare, capMountHealth); e != nil {
				return e
			}
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
		if _, e = a.preflight(c, capAgentInstall); e != nil {
			return e
		}
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
		if _, e = a.preflight(c, capDependencies); e != nil {
			return e
		}
		return interactive("ssh", append(a.sshArgs(c, true), shellJoin([]string{c.RemoteBinary, "remote", "install-browser"})))
	}
	if args[1] == "node" {
		if _, e = a.preflight(c, capDependencies); e != nil {
			return e
		}
		return interactive("ssh", append(a.sshArgs(c, true), shellJoin([]string{c.RemoteBinary, "remote", "install-node"})))
	}
	if e = a.adminPreflight(c, capDependencies); e != nil {
		return e
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
