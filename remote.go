package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type RemoteInfo struct {
	HelperInfo
	OS            string            `json:"os"`
	Arch          string            `json:"arch"`
	Home          string            `json:"home"`
	UID           int               `json:"uid"`
	Root          string            `json:"root"`
	Mount         string            `json:"mount"`
	Mounted       bool              `json:"mounted"`
	MountReadable bool              `json:"mount_readable"`
	Tools         map[string]string `json:"tools"`
	IPs           []string          `json:"ips"`
	NetworkState  string            `json:"network_state"`
	HostKey       string            `json:"host_key"`
	TailscaleSSH  bool              `json:"tailscale_ssh"`
}

func remoteRoot(value string) (string, error) {
	h, _ := os.UserHomeDir()
	if value == "" {
		value = filepath.Join(h, "RemoteDev")
	}
	r, e := resolveCreationPath(value)
	if e != nil {
		return "", e
	}
	if strings.ContainsAny(r, "\r\n\x00") {
		return "", errors.New("目录名包含控制字符")
	}
	if e = validateRoot(r, h, filepath.Join(h, ".rdev")); e != nil {
		return "", e
	}
	return r, nil
}
func probe(root string) RemoteInfo {
	h, _ := os.UserHomeDir()
	info := RemoteInfo{HelperInfo: localHelperInfo(), OS: runtime.GOOS, Arch: runtime.GOARCH, Home: h, UID: os.Getuid(), Root: root, Mount: filepath.Join(root, "local"), Tools: map[string]string{}}
	for _, name := range []string{"git", "tmux", "sshfs", "tailscale", "claude", "node", "npm", "npx", "chromium", "google-chrome", "brew"} {
		if p, e := exec.LookPath(name); e == nil {
			info.Tools[name] = p
		}
	}
	for _, candidate := range []string{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "/opt/homebrew/bin/tailscale", "/usr/local/bin/tailscale"} {
		if _, e := os.Stat(candidate); e == nil {
			if strings.Contains(candidate, "Chrome") {
				info.Tools["chrome"] = candidate
			} else if info.Tools["tailscale"] == "" {
				info.Tools["tailscale"] = candidate
			}
		}
	}
	for _, file := range []string{"/etc/ssh/ssh_host_ed25519_key.pub", "/etc/ssh/ssh_host_ecdsa_key.pub", "/etc/ssh/ssh_host_rsa_key.pub"} {
		if b, e := os.ReadFile(file); e == nil && len(b) > 0 {
			info.HostKey = strings.TrimSpace(string(b))
			break
		}
	}
	if cli := info.Tools["tailscale"]; cli != "" {
		b, e := capture(cli, []string{"status", "--json"}, nil)
		if e == nil {
			var status struct {
				BackendState string
				Self         struct{ TailscaleIPs []string }
			}
			if json.Unmarshal(b, &status) == nil {
				info.IPs = status.Self.TailscaleIPs
				info.NetworkState = status.BackendState
			}
		}
		if b, e := capture(cli, []string{"debug", "prefs"}, nil); e == nil {
			var prefs struct{ RunSSH bool }
			if json.Unmarshal(b, &prefs) == nil {
				info.TailscaleSSH = prefs.RunSSH
			}
		}
	}
	info.Mounted = isMounted(info.Mount)
	if info.Mounted {
		ready := make(chan bool, 1)
		go func() {
			f, e := os.Open(info.Mount)
			if e != nil {
				ready <- false
				return
			}
			defer f.Close()
			_, e = f.ReadDir(1)
			ready <- e == nil || errors.Is(e, io.EOF)
		}()
		select {
		case info.MountReadable = <-ready:
		case <-time.After(2 * time.Second):
		}
	}
	return info
}
func isMounted(target string) bool {
	if runtime.GOOS == "linux" {
		cmd := exec.Command("findmnt", "-n", "-o", "FSTYPE", "--mountpoint", target)
		b, e := cmd.Output()
		return e == nil && strings.Contains(string(b), "fuse")
	}
	b, e := exec.Command("mount").Output()
	if e != nil {
		return false
	}
	for _, p := range []string{target, strings.ReplaceAll(target, " ", `\040`)} {
		if strings.Contains(string(b), " on "+p+" (") {
			return true
		}
	}
	return false
}
func initRemote(root string) error {
	if e := os.MkdirAll(root, 0700); e != nil {
		return e
	}
	marker := filepath.Join(root, ".rdev-root.json")
	if b, e := os.ReadFile(marker); e == nil {
		var own map[string]string
		if json.Unmarshal(b, &own) != nil || own["owner"] != "remote-dev-kit" {
			return errors.New("现有根目录标记不属于 rdev")
		}
	}
	mount := filepath.Join(root, "local")
	if !isMounted(mount) {
		if entries, e := os.ReadDir(mount); e == nil && len(entries) > 0 {
			return errors.New("挂载位置已有文件，请换一个远端根目录")
		}
		if e := os.MkdirAll(mount, 0700); e != nil {
			return e
		}
		if e := os.Chmod(mount, 0000); e != nil {
			return e
		}
	}
	if e := os.MkdirAll(filepath.Join(root, "work"), 0700); e != nil {
		return e
	}
	b, _ := json.Marshal(map[string]string{"owner": "remote-dev-kit", "version": version})
	if e := atomicWrite(marker, b, 0600); e != nil {
		return e
	}
	return output(probe(root))
}
func remoteCommand(args []string) error {
	if len(args) > 0 && strings.HasPrefix(args[0], "b-") {
		return bRemote(args)
	}
	addRuntimePath()
	if len(args) == 0 {
		return errors.New("missing remote operation")
	}
	op := args[0]
	root := ""
	if len(args) > 1 {
		root = args[1]
	}
	if op != "handshake" && op != "platform" && op != "create-user" && op != "dependencies" && op != "install-node" && op != "install-browser" && op != "install-agent" {
		var e error
		root, e = remoteRoot(root)
		if e != nil {
			return e
		}
	}
	switch op {
	case "handshake":
		return output(localHelperInfo())
	case "platform":
		h, _ := os.UserHomeDir()
		return output(map[string]string{"os": runtime.GOOS, "arch": runtime.GOARCH, "home": h})
	case "create-user":
		return createDevUser()
	case "init":
		return initRemote(root)
	case "probe":
		return output(probe(root))
	case "mount-health":
		if !isMounted(filepath.Join(root, "local")) {
			return errors.New("共享未挂载")
		}
		_, e := os.ReadDir(filepath.Join(root, "local"))
		return e
	case "mount":
		return remoteMount(root)
	case "unmount":
		return remoteUnmount(root)
	case "port-prepare":
		return preparePort(root)
	case "port-relay":
		if len(args) != 3 {
			return errors.New("缺少端口租约")
		}
		return relayPort(root, args[2])
	case "port-remove":
		if len(args) != 3 {
			return errors.New("缺少端口租约")
		}
		return removePort(root, args[2])
	case "terminal":
		if len(args) < 3 {
			return errors.New("missing terminal kind")
		}
		kind := args[2]
		extra := args[3:]
		if _, e := os.Stat(filepath.Join(root, ".rdev-root.json")); e != nil {
			return errors.New("远端尚未初始化")
		}
		if kind == "claude" && os.Getuid() == 0 {
			return errors.New("当前是 root；请用普通开发账户重新 setup 后运行 Claude")
		}
		program := ""
		switch kind {
		case "claude":
			program = "claude"
			if isMounted(filepath.Join(root, "local")) {
				extra = append([]string{"--add-dir", filepath.Join(root, "local")}, extra...)
			}
		case "shell":
			program = os.Getenv("SHELL")
			if program == "" {
				program = "/bin/sh"
			}
		default:
			return errors.New("unknown terminal kind")
		}
		if _, e := exec.LookPath(program); e != nil {
			return fmt.Errorf("远端没有 %s，可先运行 rdev agent install 或 rdev tools install base", program)
		}
		if _, e := exec.LookPath("tmux"); e != nil {
			return errors.New("远端未安装 tmux；运行 rdev tools install base")
		}
		name := sessionPrefix(root) + strconv.FormatInt(time.Now().UnixNano(), 36)
		return interactive("tmux", []string{"new-session", "-s", name, "-c", filepath.Join(root, "work"), "exec " + shellJoin(append([]string{program}, extra...))})
	case "reconnect":
		b, e := capture("tmux", []string{"list-sessions", "-F", "#{session_name}"}, nil)
		if e != nil {
			return e
		}
		var names []string
		for _, name := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if strings.HasPrefix(name, sessionPrefix(root)) {
				names = append(names, name)
			}
		}
		if len(names) == 0 {
			return errors.New("这个研发目录没有仍在运行的终端")
		}
		target := names[len(names)-1]
		if len(names) > 1 {
			for i, name := range names {
				fmt.Printf("%d. %s\n", i+1, name)
			}
			p := Prompter{bufio.NewReader(os.Stdin)}
			value, e := p.ask("接回哪个终端", strconv.Itoa(len(names)))
			if e != nil {
				return e
			}
			n, e := strconv.Atoi(value)
			if e != nil || n < 1 || n > len(names) {
				return errors.New("无效选择")
			}
			target = names[n-1]
		}
		return interactive("tmux", []string{"attach-session", "-t", "=" + target})
	case "dependencies":
		if len(args) < 2 {
			return errors.New("missing dependency group")
		}
		return installDependencies(args[1])
	case "install-node":
		return installNode()
	case "install-browser":
		return installBrowser()
	case "install-agent":
		return installAgent()
	case "browser":
		return serveBrowser(root)
	case "browser-mcp":
		return browserMCP(root)
	default:
		return errors.New("unknown remote operation")
	}
}
func sessionPrefix(root string) string {
	h := sha256.Sum256([]byte(root))
	return "rdev-" + hex.EncodeToString(h[:4]) + "-"
}
func remoteMount(root string) error {
	if _, e := os.Stat(filepath.Join(root, ".rdev-root.json")); e != nil {
		return errors.New("目录没有 rdev 管理标记")
	}
	target := filepath.Join(root, "local")
	if isMounted(target) {
		return errors.New("已有挂载，请先 share off")
	}
	exe, e := exec.LookPath("sshfs")
	if e != nil {
		return errors.New("没有 sshfs；先安装 FUSE/SSHFS，rdev tools install base")
	}
	help := exec.Command(exe, "-h")
	b, _ := help.CombinedOutput()
	mode := "slave"
	if strings.Contains(string(b), "passive") {
		mode = "passive"
	}
	if e = os.Chmod(target, 0700); e != nil {
		return e
	}
	underlying, e := os.Open(target)
	if e != nil {
		return e
	}
	defer underlying.Close()
	options := fmt.Sprintf("%s,cache=no,nodev,nosuid,uid=%d,gid=%d", mode, os.Getuid(), os.Getgid())
	if runtime.GOOS == "darwin" {
		options += ",backend=fskit"
	}
	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals()...)
	defer stop()
	defer underlying.Chmod(0000)
	cmd := exec.CommandContext(ctx, exe, "-f", ":/", target, "-o", options)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if e = cmd.Start(); e != nil {
		return e
	}
	// FUSE may leave a stale mount behind after its SSH stream dies. Tear down
	// this mount when its owning helper exits, without killing development tasks.
	defer func() {
		if e := remoteUnmount(root); e != nil {
			fmt.Fprintln(os.Stderr, "共享退出后未能安全卸载：", e)
		}
	}()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	guarded := false
	for {
		select {
		case e := <-done:
			underlying.Chmod(0000)
			return e
		case <-ticker.C:
			if !guarded && isMounted(target) {
				if e := underlying.Chmod(0000); e != nil {
					cmd.Process.Kill()
					return e
				}
				guarded = true
			}
		}
	}
}
func remoteUnmount(root string) error {
	if _, e := os.Stat(filepath.Join(root, ".rdev-root.json")); e != nil {
		return errors.New("拒绝卸载非 rdev 管理路径")
	}
	target := filepath.Join(root, "local")
	if !isMounted(target) {
		return nil
	}
	if runtime.GOOS == "linux" {
		p, e := exec.LookPath("fusermount3")
		if e != nil {
			p = "fusermount"
		}
		_, e = capture(p, []string{"-u", target}, nil)
		return e
	}
	_, e := capture("umount", []string{target}, nil)
	return e
}
func readInput[T any]() (T, error) {
	var t T
	b, e := io.ReadAll(io.LimitReader(os.Stdin, 1024*1024))
	if e != nil {
		return t, e
	}
	e = json.Unmarshal(b, &t)
	return t, e
}

func createDevUser() error {
	if runtime.GOOS != "linux" || os.Getuid() != 0 {
		return errors.New("创建开发账户仅支持 Linux 管理员")
	}
	r, e := readInput[map[string]string]()
	if e != nil {
		return e
	}
	name := r["user"]
	key := r["public_key"]
	if !nameRE.MatchString(name) || !strings.HasPrefix(key, "ssh-ed25519 ") || strings.ContainsAny(key, "\r\n") {
		return errors.New("无效开发账户或公钥")
	}
	u, e := user.Lookup(name)
	existing := e == nil
	if existing {
		r, err := os.OpenRoot(u.HomeDir)
		if err != nil {
			return err
		}
		b, _ := r.ReadFile(".ssh/authorized_keys")
		r.Close()
		if !strings.Contains(string(b), key) {
			return errors.New("该开发账户已存在且不属于本次设置，不会覆盖其密钥")
		}
	} else {
		if _, look := exec.LookPath("useradd"); look == nil {
			_, e = capture("useradd", []string{"--create-home", "--shell", "/bin/bash", name}, nil)
		} else {
			_, e = capture("adduser", []string{"-D", "-s", "/bin/sh", name}, nil)
		}
		if e != nil {
			return e
		}
		u, e = user.Lookup(name)
		if e != nil {
			return e
		}
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	home, err := os.OpenRoot(u.HomeDir)
	if err != nil {
		return err
	}
	defer home.Close()
	for _, dir := range []string{".ssh", ".local", ".local/share", ".local/share/rdev"} {
		if e = home.MkdirAll(dir, 0700); e != nil {
			return e
		}
		fd, e := home.Open(dir)
		if e != nil {
			return e
		}
		e = fd.Chown(uid, gid)
		fd.Close()
		if e != nil {
			return e
		}
	}
	if !existing {
		f, e := home.OpenFile(".ssh/authorized_keys", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		_, e = f.WriteString("no-agent-forwarding,no-X11-forwarding " + key + "\n")
		if e == nil {
			e = f.Chown(uid, gid)
		}
		f.Close()
		if e != nil {
			return e
		}
	}
	b, e := os.ReadFile(r["source_binary"])
	if e != nil {
		return e
	}
	temporary := ".local/share/rdev/.rdev-new-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	defer home.Remove(temporary)
	f, e := home.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0700)
	if e != nil {
		return e
	}
	_, e = f.Write(b)
	if e == nil {
		e = f.Chown(uid, gid)
	}
	f.Close()
	if e != nil {
		return e
	}
	if e = home.Rename(temporary, ".local/share/rdev/rdev"); e != nil {
		return e
	}
	return output(map[string]string{"user": name, "binary": filepath.Join(u.HomeDir, ".local/share/rdev/rdev")})
}
