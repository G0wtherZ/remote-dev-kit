package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

func asset(parts ...string) (string, error) {
	base := os.Getenv("RDEV_ASSETS")
	if base == "" {
		exe, _ := os.Executable()
		base = filepath.Join(filepath.Dir(exe), "assets")
	}
	p := filepath.Join(append([]string{base}, parts...)...)
	if _, e := os.Stat(p); e != nil {
		return "", fmt.Errorf("缺少平台资源 %s；请使用发布安装包或先运行 scripts/build.py", strings.Join(parts, "/"))
	}
	return p, nil
}
func (a *App) setup(args []string) error {
	f := flag.NewFlagSet("setup", flag.ContinueOnError)
	name := f.String("name", "default", "连接名称")
	dest := f.String("ssh", "", "已有 SSH 别名或 user@host")
	port := f.Int("ssh-port", 0, "SSH 端口；0 保留已有 SSH 配置/默认值")
	local := f.String("local-root", "~/RemoteDev", "本机专用共享目录，可自定义")
	remoteDir := f.String("remote-root", "~/RemoteDev", "远端研发根目录，可自定义")
	mode := f.String("network", "managed", "managed 用户态私网 / existing 复用已运行网络")
	cli := f.String("tailscale-bin", "", "复用的 Tailscale CLI 路径")
	socket := f.String("tailscale-socket", "", "复用网络的 socket，可选")
	socks := f.String("socks", "", "复用的本地 SOCKS，例如 127.0.0.1:11055")
	ro := f.Bool("read-only", false, "只读共享")
	noShare := f.Bool("no-share", false, "只设置连接，暂不启动目录挂载")
	plan := f.Bool("plan", false, "仅显示设置方案，不修改两端")
	yes := f.Bool("yes", false, "接受已指定配置；账户授权仍由本人完成")
	allowAdmin := f.Bool("allow-admin", false, "保留管理账号，仅用于测试；Claude 拒绝 root 运行")
	bootstrap := f.String("remote-bootstrap-dir", "", "可选远端程序目录，默认放在用户目录")
	resume := f.Bool("resume", false, "继续已经保存但尚未完成的设置")
	if e := f.Parse(args); e != nil {
		return e
	}
	prompt := &Prompter{bufio.NewReader(os.Stdin)}
	if *resume {
		return a.resumeSetup(prompt, *yes)
	}
	if !*yes {
		var e error
		if *dest, e = prompt.ask("远端 SSH 别名或 user@host", *dest); e != nil {
			return e
		}
		if strings.Contains(*dest, "@") && *port == 0 {
			value, err := prompt.ask("SSH 端口", "22")
			if err != nil {
				return err
			}
			*port, err = strconv.Atoi(value)
			if err != nil {
				return err
			}
		}
		if *local, e = prompt.ask("本机专用共享目录（远端可读写此目录）", *local); e != nil {
			return e
		}
		if *remoteDir, e = prompt.ask("远端研发根目录", *remoteDir); e != nil {
			return e
		}
		explicit := false
		f.Visit(func(value *flag.Flag) {
			if value.Name == "network" {
				explicit = true
			}
		})
		if !explicit && *cli == "" {
			candidate := platformDefaultCLI()
			if b, err := capture(candidate, []string{"status", "--json"}, nil); err == nil {
				if status, err := decode[NetStatus](b); err == nil && status.BackendState == "Running" {
					reuse, err := prompt.yes("检测到现有 Tailscale 连接，复用它", true)
					if err != nil {
						return err
					}
					if reuse {
						*mode = "existing"
						*cli = candidate
					}
				}
			}
		}
	}
	if !validDestination(*dest) || !nameRE.MatchString(*name) {
		return errors.New("请提供有效 SSH 地址与名称")
	}
	if *port < 0 || *port > 65535 {
		return errors.New("无效 SSH 端口")
	}
	if *mode != "managed" && *mode != "existing" {
		return errors.New("network 必须是 managed 或 existing")
	}
	home, _ := os.UserHomeDir()
	root, e := resolveCreationPath(*local)
	if e != nil {
		return e
	}
	state, e := filepath.Abs(a.State)
	if e != nil {
		return e
	}
	a.State = state
	if existing, e := filepath.EvalSymlinks(root); e == nil {
		root = existing
	}
	if e = validateRoot(root, home, state); e != nil {
		return e
	}
	c := Config{Version: 1, Name: *name, AdminSSH: *dest, AdminPort: *port, LocalRoot: root, RemoteRoot: *remoteDir, NetworkMode: *mode, Tailscale: *cli, Socket: *socket, SOCKS: *socks, Readonly: *ro, AutoShare: !*noShare}
	if *socks != "" {
		h, _, err := net.SplitHostPort(*socks)
		if err != nil || h != "localhost" && !net.ParseIP(h).IsLoopback() {
			return errors.New("仅允许本机回环 SOCKS 地址")
		}
	}
	if *plan {
		return output(map[string]any{"profile": c, "changes": []string{"建立专用目录", "本机独立私网连接，不接管系统 DNS/出口", "安装远端助手并生成独立 SSH 配置", "可选挂载和 Claude 安装"}})
	}
	if !*yes {
		ok, e := prompt.yes("按上述路径设置（不会自动安装 Claude 或开放个人浏览器）", true)
		if e != nil {
			return e
		}
		if !ok {
			return errors.New("已取消，未修改配置")
		}
	}
	if _, e = os.Stat(filepath.Join(a.State, "config.json")); e == nil {
		return errors.New("该私有目录已有配置；请用 doctor/repair，或选择另一个 --state-dir，避免覆盖现有身份")
	}
	if e = a.prepareState(); e != nil {
		return e
	}
	if e = os.MkdirAll(root, 0700); e != nil {
		return e
	}
	fmt.Println("检查 SSH（首次连接请本人核对主机指纹）…")
	initial := append([]string{"-o", "ForwardAgent=no", "-o", "StrictHostKeyChecking=ask", "-o", "AddKeysToAgent=yes"}, adminPort(c)...)
	if e = interactive("ssh", append(initial, c.AdminSSH, "true")); e != nil {
		return e
	}
	b, e := admin(c, []string{"sh", "-c", `printf '%s\n' "$(uname -s)" "$(uname -m)" "$HOME"`}, nil)
	if e != nil {
		return e
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 3 {
		return errors.New("无法识别远端平台")
	}
	platform := strings.ToLower(lines[0])
	arch := lines[1]
	switch arch {
	case "x86_64":
		arch = "amd64"
	case "aarch64", "arm64":
		arch = "arm64"
	default:
		return errors.New("远端架构暂不支持：" + arch)
	}
	if platform != "linux" && platform != "darwin" {
		return errors.New("远端仅支持 Linux/macOS")
	}
	c.RemoteOS = platform
	c.RemoteArch = arch
	bin, e := asset("rdev", platform+"-"+arch, "rdev")
	if e != nil {
		return e
	}
	dir := *bootstrap
	if dir == "" {
		dir = filepath.ToSlash(lines[2]) + "/.local/share/rdev/" + c.Name
	}
	if !strings.HasPrefix(dir, "/") || strings.ContainsAny(dir, "\r\n\x00") {
		return errors.New("远端程序目录必须是绝对路径")
	}
	c.RemoteBinary = strings.TrimRight(dir, "/") + "/rdev"
	c.BootstrapBinary = c.RemoteBinary
	if _, e = admin(c, []string{"mkdir", "-p", dir}, nil); e != nil {
		return e
	}
	fmt.Println("上传远端助手（压缩传输，完成前不替换现有文件）…")
	payload, e := os.ReadFile(bin)
	if e != nil {
		return e
	}
	var compressed bytes.Buffer
	zipper := gzip.NewWriter(&compressed)
	if _, e = zipper.Write(payload); e != nil {
		return e
	}
	if e = zipper.Close(); e != nil {
		return e
	}
	installScript := "set -eu\numask 077\ntmp=$(mktemp " + shellQuote(dir+"/.rdev-upload.XXXXXXXX") + ")\ntrap 'rm -f \"$tmp\"' 0 1 2 15\ngzip -dc > \"$tmp\"\nchmod 700 \"$tmp\"\nmv -f \"$tmp\" " + shellQuote(c.RemoteBinary) + "\n"
	if _, e = admin(c, []string{"sh", "-c", installScript}, compressed.Bytes()); e != nil {
		return e
	}
	b, e = admin(c, []string{c.RemoteBinary, "remote", "probe", *remoteDir}, nil)
	if e != nil {
		return e
	}
	info, e := decode[RemoteInfo](b)
	if e != nil {
		return e
	}
	if info.HostKey == "" {
		return errors.New("无法读取远端公有主机密钥；未跳过主机验证")
	}
	if info.UID == 0 && !*allowAdmin {
		if *yes {
			return errors.New("远端是 root；请使用普通开发账户，或交互 setup 选择新建 Linux 开发账户")
		}
		ok, e := prompt.yes("远端为 root，创建不带 sudo 的 rdev 开发账户", true)
		if e != nil {
			return e
		}
		if !ok {
			return errors.New("请改用普通开发账户后重试")
		}
		if platform != "linux" {
			return errors.New("远端 macOS 请先提供普通用户的 SSH 连接；不会自动创建 macOS 管理账户")
		}
		c.Identity = filepath.Join(a.State, "identity_ed25519")
		if e = interactive("ssh-keygen", []string{"-q", "-t", "ed25519", "-N", "", "-f", c.Identity, "-C", "remote-dev-kit-" + c.Name}); e != nil {
			return e
		}
		pub, e := os.ReadFile(c.Identity + ".pub")
		if e != nil {
			return e
		}
		request := map[string]string{"user": "rdev", "public_key": strings.TrimSpace(string(pub)), "source_binary": c.RemoteBinary}
		data, _ := jsonMarshal(request)
		b, e = admin(c, []string{c.RemoteBinary, "remote", "create-user"}, data)
		if e != nil {
			return e
		}
		created, e := decode[map[string]string](b)
		if e != nil {
			return e
		}
		c.RemoteUser = "rdev"
		c.RemoteBinary = created["binary"]
	}
	if e = a.save(c); e != nil {
		return e
	}
	if e = a.configureNetwork(&c, prompt, *yes); e != nil {
		return e
	}
	if info.NetworkState != "Running" {
		fresh := info.Tools["tailscale"] == ""
		if info.Tools["tailscale"] == "" {
			if *yes {
				return errors.New("远端未安装 Tailscale；运行 rdev tools install tailscale 后继续 setup，或交互确认安装")
			}
			ok, e := prompt.yes("远端缺少 Tailscale，安装官方客户端（需管理员权限）", true)
			if e != nil {
				return e
			}
			if !ok {
				return errors.New("需要可用的远端私网连接")
			}
			if e = adminInteractive(c, shellJoin([]string{c.BootstrapBinary, "remote", "dependencies", "tailscale"})); e != nil {
				return e
			}
		}
		fmt.Println("授权远端设备：请用与本机相同的 Tailscale 账户。")
		command := `tailscale up`
		if fresh {
			command += ` --accept-dns=false --accept-routes=false --ssh=false`
		}
		if info.UID != 0 {
			command = "sudo " + command
		}
		if e = adminInteractive(c, command); e != nil {
			return e
		}
		b, e = admin(c, []string{c.BootstrapBinary, "remote", "probe", c.RemoteRoot}, nil)
		if e != nil {
			return e
		}
		info, e = decode[RemoteInfo](b)
		if e != nil {
			return e
		}
	}
	if info.TailscaleSSH {
		fmt.Println("注意：远端已有 Tailscale SSH 设置；本工具未关闭它。若它接管 22 端口，请使用普通 OpenSSH 的独立端口后继续。")
	}
	for _, ip := range info.IPs {
		if v := net.ParseIP(ip); v != nil && v.To4() != nil {
			c.RemoteIP = ip
			break
		}
	}
	if c.RemoteIP == "" {
		return errors.New("远端还没有 Tailscale IPv4 地址；配置已保存，可重新登录后运行 setup --resume")
	}
	if e = a.writeSSH(c, info.HostKey); e != nil {
		return e
	}
	if e = a.save(c); e != nil {
		return e
	}
	b, e = a.remote(c, []string{"init", c.RemoteRoot}, nil)
	if e != nil {
		return e
	}
	ready, e := decode[RemoteInfo](b)
	if e != nil {
		return e
	}
	c.RemoteRoot = ready.Root
	if e = a.save(c); e != nil {
		return e
	}
	fmt.Printf("设置完成。共享目录：%s\n远端工作目录：%s/work\n远端挂载位置：%s/local\n", c.LocalRoot, c.RemoteRoot, c.RemoteRoot)
	if ready.Tools["sshfs"] == "" {
		fmt.Println("挂载依赖尚未安装：运行 rdev tools install base；远端 macOS 按 doctor 的 FSKit 指引操作。")
		if !*yes && c.AutoShare {
			ok, err := prompt.yes("现在安装远端基础工具", true)
			if err != nil {
				return err
			}
			if ok {
				if err = a.tools([]string{"install", "base"}); err != nil {
					return err
				}
			}
		}
	}
	if c.AutoShare {
		if e = a.share([]string{"on"}); e != nil {
			return e
		}
	}
	if ready.Tools["claude"] == "" {
		fmt.Println("Claude 未安装也不影响连接；需要时运行 rdev agent install。")
	}
	return nil
}
func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

func (a *App) resumeSetup(p *Prompter, yes bool) error {
	c, e := a.load()
	if e != nil {
		return e
	}
	if e = a.configureNetwork(&c, p, yes); e != nil {
		return e
	}
	b, e := admin(c, []string{c.RemoteBinary, "remote", "probe", c.RemoteRoot}, nil)
	if e != nil {
		return e
	}
	info, e := decode[RemoteInfo](b)
	if e != nil {
		return e
	}
	if info.NetworkState != "Running" {
		return errors.New("远端 Tailscale 尚未登录；通过原 SSH 登录执行 tailscale up，完成后再 setup --resume")
	}
	for _, ip := range info.IPs {
		if v := net.ParseIP(ip); v != nil && v.To4() != nil {
			c.RemoteIP = ip
			break
		}
	}
	if c.RemoteIP == "" {
		return errors.New("远端尚无私网地址")
	}
	if e = a.writeSSH(c, info.HostKey); e != nil {
		return e
	}
	b, e = a.remote(c, []string{"init", c.RemoteRoot}, nil)
	if e != nil {
		return e
	}
	ready, e := decode[RemoteInfo](b)
	if e != nil {
		return e
	}
	c.RemoteRoot = ready.Root
	if e = a.save(c); e != nil {
		return e
	}
	fmt.Println("设置已完成，可运行 rdev doctor。 ")
	return nil
}

// platformDefaultCLI never imports login state or private keys from another app.
func platformDefaultCLI() string {
	if p, e := exec.LookPath("tailscale"); e == nil {
		return p
	}
	if runtime.GOOS == "darwin" {
		return "/Applications/Tailscale.app/Contents/MacOS/Tailscale"
	}
	if runtime.GOOS == "windows" {
		return filepath.Join(os.Getenv("ProgramFiles"), "Tailscale", "tailscale.exe")
	}
	return "tailscale"
}
