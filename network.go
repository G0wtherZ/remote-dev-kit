package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type NetStatus struct {
	BackendState string
	Health       []string
	Self         struct {
		TailscaleIPs []string
		Online       bool
	}
}

func (a *App) ts(c Config, args ...string) ([]byte, error) {
	if runtime.GOOS == "windows" && c.NetworkMode == "managed" {
		if e := verifyUserPipe(c.Socket); e != nil {
			return nil, e
		}
	}
	x := []string{}
	if c.Socket != "" {
		x = append(x, "--socket="+c.Socket)
	}
	return capture(c.Tailscale, append(x, args...), nil)
}
func (a *App) configureNetwork(c *Config, p *Prompter, yes bool) error {
	if c.NetworkMode == "existing" {
		if c.Tailscale == "" {
			c.Tailscale = platformDefaultCLI()
		}
		b, e := a.ts(*c, "status", "--json")
		if e != nil {
			return e
		}
		s, e := decode[NetStatus](b)
		if e != nil {
			return e
		}
		if s.BackendState != "Running" {
			return errors.New("要复用的 Tailscale 没有运行；先登录或选择 managed 模式")
		}
		return a.save(*c)
	}
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	var e error
	c.Tailscale, e = asset("tailscale", runtime.GOOS+"-"+runtime.GOARCH, "tailscale"+ext)
	if e != nil {
		return e
	}
	c.Tailscaled, e = asset("tailscale", runtime.GOOS+"-"+runtime.GOARCH, "tailscaled"+ext)
	if e != nil {
		return e
	}
	if c.Socket == "" {
		if runtime.GOOS == "windows" {
			h := sha256.Sum256([]byte(a.State))
			c.Socket = windowsPipe(hex.EncodeToString(h[:6]))
		} else {
			c.Socket = filepath.Join(a.State, "network.sock")
			if len(c.Socket) > 95 {
				return errors.New("私有配置路径过长，请使用用户主目录下较短的 --state-dir")
			}
		}
	}
	if c.SOCKS == "" {
		port, e := freePort()
		if e != nil {
			return e
		}
		c.SOCKS = "127.0.0.1:" + strconv.Itoa(port)
	}
	if e = a.save(*c); e != nil {
		return e
	}
	if e = a.startNetwork(*c); e != nil {
		return e
	}
	return a.loginNetwork(*c, p, yes)
}
func (a *App) network(args []string) error {
	c, e := a.load()
	if e != nil {
		return e
	}
	if len(args) == 0 {
		return errors.New("network login|status|stop")
	}
	switch args[0] {
	case "serve":
		return a.serveNetwork(c)
	case "login":
		if c.NetworkMode == "managed" {
			if e = a.startNetwork(c); e != nil {
				return e
			}
		}
		return a.loginNetwork(c, &Prompter{Reader: bufio.NewReader(os.Stdin)}, false)
	case "status":
		b, e := a.ts(c, "status", "--json")
		if e != nil {
			return e
		}
		s, e := decode[NetStatus](b)
		if e != nil {
			return e
		}
		return output(s)
	case "stop":
		return stopManaged(a, "network")
	default:
		return errors.New("network login|status|stop")
	}
}
func (a *App) startNetwork(c Config) error {
	if _, e := a.ts(c, "status", "--json"); e == nil {
		return nil
	}
	if e := startManaged(a, "network"); e != nil {
		return e
	}
	for i := 0; i < 50; i++ {
		if _, e := a.ts(c, "status", "--json"); e == nil {
			return nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	return errors.New("私网进程未就绪；查看私有目录下 logs/network.log")
}
func (a *App) serveNetwork(c Config) error {
	dir := filepath.Join(a.State, "network")
	if e := os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	cmd := exec.Command(c.Tailscaled, "--tun=userspace-networking", "--socket="+c.Socket, "--statedir="+dir, "--state="+filepath.Join(dir, "tailscaled.state"), "--socks5-server="+c.SOCKS, "--port=0")
	if runtime.GOOS == "windows" {
		cmd.Args = append(cmd.Args, "--windows-mode=dev")
	}
	privateProcess(cmd)
	cmd.Env = append(os.Environ(), "TS_BIND_TO_INTERFACE_BY_ROUTE=true", "GODEBUG=netdns=go")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals()...)
	defer stop()
	if e := cmd.Start(); e != nil {
		return e
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case e := <-done:
		return e
	case <-ctx.Done():
		stopProcess(cmd)
		select {
		case <-done:
			return nil
		case <-time.After(5 * time.Second):
			cmd.Process.Kill()
			<-done
			return nil
		}
	}
}

var authURL = regexp.MustCompile(`https://login\.tailscale\.com/a/[A-Za-z0-9]+`)

func (a *App) loginNetwork(c Config, p *Prompter, yes bool) error {
	b, e := a.ts(c, "status", "--json")
	if e == nil {
		if s, err := decode[NetStatus](b); err == nil && s.BackendState == "Running" {
			return nil
		}
	}
	args := []string{}
	if c.Socket != "" {
		args = append(args, "--socket="+c.Socket)
	}
	args = append(args, "up", "--accept-dns=false", "--accept-routes=false", "--shields-up", "--hostname=rdev-"+c.Name, "--timeout=10s")
	cmd := exec.Command(c.Tailscale, args...)
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	_ = cmd.Run()
	link := authURL.FindString(combined.String())
	if link != "" {
		fmt.Println("请本人在浏览器授权此设备：", link)
		_ = openBrowser(link)
		if yes {
			return errors.New("等待本人授权；完成后运行 rdev setup --resume")
		}
		if _, e = p.ask("授权完成后按 Enter", ""); e != nil {
			return e
		}
	}
	for i := 0; i < 20; i++ {
		b, e = a.ts(c, "status", "--json")
		if e == nil {
			if s, err := decode[NetStatus](b); err == nil && s.BackendState == "Running" {
				return nil
			}
		}
		time.Sleep(time.Second)
	}
	return errors.New("设备尚未在线；授权后运行 network login 或 setup --resume")
}
func openBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}
func serviceID(a *App, kind string) string {
	h := sha256.Sum256([]byte(a.State))
	return "io.rdev." + kind + "." + hex.EncodeToString(h[:5])
}
func managedPlist(a *App, kind string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", serviceID(a, kind)+".plist")
}
func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}
func startManaged(a *App, kind string) error {
	exe, _ := os.Executable()
	logs := filepath.Join(a.State, "logs")
	if e := os.MkdirAll(logs, 0700); e != nil {
		return e
	}
	for _, n := range []string{kind + ".log", kind + ".out"} {
		f, e := os.OpenFile(filepath.Join(logs, n), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		f.Close()
	}
	args := []string{exe, "--state-dir", a.State, kind, "serve"}
	if runtime.GOOS == "darwin" {
		id := serviceID(a, kind)
		domain := "gui/" + strconv.Itoa(os.Getuid())
		if exec.Command("launchctl", "print", domain+"/"+id).Run() == nil {
			return nil
		}
		var list strings.Builder
		for _, arg := range args {
			list.WriteString("<string>" + xmlEscape(arg) + "</string>")
		}
		plist := `<?xml version="1.0"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict><key>Label</key><string>` + id + `</string><key>ProgramArguments</key><array>` + list.String() + `</array><key>RunAtLoad</key><true/><key>KeepAlive</key><true/><key>ThrottleInterval</key><integer>10</integer><key>StandardOutPath</key><string>` + xmlEscape(filepath.Join(logs, kind+".out")) + `</string><key>StandardErrorPath</key><string>` + xmlEscape(filepath.Join(logs, kind+".log")) + `</string></dict></plist>`
		file := managedPlist(a, kind)
		if e := os.MkdirAll(filepath.Dir(file), 0700); e != nil {
			return e
		}
		if e := atomicWrite(file, []byte(plist), 0600); e != nil {
			return e
		}
		_, e := capture("launchctl", []string{"bootstrap", domain, file}, nil)
		return e
	}
	if runtime.GOOS == "windows" {
		_, e := capture("schtasks", []string{"/Create", "/TN", serviceID(a, kind), "/SC", "ONLOGON", "/RL", "LIMITED", "/TR", windowsCommand(args), "/F"}, nil)
		if e != nil {
			return e
		}
		_, e = capture("schtasks", []string{"/Run", "/TN", serviceID(a, kind)}, nil)
		return e
	}
	cmd := exec.Command(args[0], args[1:]...)
	log, _ := os.OpenFile(filepath.Join(logs, kind+".log"), os.O_APPEND|os.O_WRONLY, 0600)
	defer log.Close()
	cmd.Stdout = log
	cmd.Stderr = log
	return cmd.Start()
}
func stopManaged(a *App, kind string) error {
	if runtime.GOOS == "darwin" {
		_ = exec.Command("launchctl", "bootout", "gui/"+strconv.Itoa(os.Getuid())+"/"+serviceID(a, kind)).Run()
		file := managedPlist(a, kind)
		if b, e := os.ReadFile(file); e == nil && strings.Contains(string(b), xmlEscape(a.State)) {
			if e = os.Remove(file); e != nil {
				return e
			}
		}
		return nil
	}
	if runtime.GOOS == "windows" {
		_ = exec.Command("schtasks", "/End", "/TN", serviceID(a, kind)).Run()
		_ = exec.Command("schtasks", "/Delete", "/TN", serviceID(a, kind), "/F").Run()
		return nil
	}
	return errors.New("此本机平台暂无后台服务停止适配")
}
func windowsCommand(args []string) string {
	out := make([]string, len(args))
	for i, s := range args {
		out[i] = windowsQuote(s)
	}
	return strings.Join(out, " ")
}
func windowsPipe(id string) string {
	slash := string(rune(92))
	return slash + slash + "." + slash + "pipe" + slash + "rdev-" + id
}
func windowsQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n\v\"") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	slashes := 0
	for _, r := range s {
		if r == 92 {
			slashes++
			continue
		}
		if r == '"' {
			b.WriteString(strings.Repeat(string(rune(92)), slashes*2+1))
			b.WriteRune(r)
		} else {
			b.WriteString(strings.Repeat(string(rune(92)), slashes))
			b.WriteRune(r)
		}
		slashes = 0
	}
	b.WriteString(strings.Repeat(string(rune(92)), slashes*2))
	b.WriteByte('"')
	return b.String()
}
