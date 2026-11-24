package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const bProtocol = 1

var bIDPattern = regexp.MustCompile(`^[a-f0-9]{12}$`)

type bProfile struct {
	Protocol     int    `json:"protocol"`
	ServerID     string `json:"server_id"`
	DeviceID     string `json:"device_id"`
	SSH          string `json:"ssh"`
	SSHConfig    string `json:"ssh_config,omitempty"`
	RemoteRoot   string `json:"remote_root"`
	Container    string `json:"container"`
	Image        string `json:"image"`
	ControlPort  int    `json:"control_port"`
	WebPort      int    `json:"web_port"`
	ControlToken string `json:"control_token"`
	Authorized   bool   `json:"local_execution_authorized"`
	WebToken     string `json:"web_token"`
	SkipClaude   bool   `json:"skip_claude,omitempty"`
	Joined       bool   `json:"joined,omitempty"`
	Name         string `json:"device_name,omitempty"`
	setupMux     string
}

func bRandom(n int) string {
	b := make([]byte, n)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func (a *App) bDir() string { return filepath.Join(a.State, "plan-b") }
func (a *App) bSave(c bProfile) error {
	b, e := json.MarshalIndent(c, "", "  ")
	if e != nil {
		return e
	}
	return atomicWrite(filepath.Join(a.bDir(), "profile.json"), append(b, '\n'), 0600)
}
func (a *App) bLoad() (bProfile, error) {
	var c bProfile
	owner, err := os.ReadFile(filepath.Join(a.bDir(), ".native-owner"))
	if err != nil || string(owner) != "remote-dev-kit-native-b\n" {
		return c, errors.New("B 目录缺少本工具归属标记，不导入其他环境")
	}

	b, e := os.ReadFile(filepath.Join(a.bDir(), "profile.json"))
	if e != nil {
		return c, errors.New("尚未设置独立 Plan B；运行 rdev plan-b setup")
	}
	if e = json.Unmarshal(b, &c); e != nil {
		return c, e
	}
	if c.Protocol != bProtocol || !bIDPattern.MatchString(c.ServerID) || !bIDPattern.MatchString(c.DeviceID) || c.Container != "rdev-b-"+c.ServerID || !validDestination(c.SSH) || !strings.HasSuffix(c.RemoteRoot, "/.rdev-b/"+c.ServerID) || !strings.HasPrefix(c.RemoteRoot, "/") || strings.ContainsAny(c.RemoteRoot, "\r\n\x00") || c.ControlPort < 1024 || c.ControlPort > 65535 || len(c.ControlToken) != 64 {
		return c, errors.New("B 配置或协议无效，保留数据；请使用相同配置重新 setup")
	}
	return c, nil
}
func (a *App) planB(args []string) error {
	if len(args) == 0 || args[0] == "help" {
		fmt.Print(`Plan B — rdev 独立实现，官方 Claude 远端运行、本机执行
  rdev plan-b setup --ssh <私网SSH别名>   独立容器与账号，不修改原有 Claude
  rdev plan-b on --trust-local-user     允许远端以当前本机用户执行；无登录自启
  rdev plan-b off                       撤销本机执行并终止其活动命令
  rdev plan-b claude [原生参数...]       在当前本机项目新开远端 Claude 终端
  rdev plan-b sessions|reconnect        列出或接回远端终端
  rdev plan-b account                   登录 B 自己的官方 Claude
  rdev plan-b status|reset              检查或重建 B，保留账号和全部数据
`)
		return nil
	}
	if args[0] == "setup" {
		return a.bSetup(args[1:])
	}
	c, e := a.bLoad()
	if e != nil {
		return e
	}
	switch args[0] {
	case "serve":
		return a.bServe(c)
	case "on":
		f := flag.NewFlagSet("plan-b on", flag.ContinueOnError)
		trust := f.Bool("trust-local-user", false, "允许 B 使用当前本机用户的完整命令/文件权限")
		if e = f.Parse(args[1:]); e != nil {
			return e
		}
		if !*trust && !c.Authorized {
			return errors.New("本机通用执行须显式授权：rdev plan-b on --trust-local-user；项目路径不是沙箱")
		}
		c.Authorized = true
		if e = a.bSave(c); e != nil {
			return e
		}
		return a.bOn(c)
	case "off":
		c.Authorized = false
		if e = a.bSave(c); e != nil {
			return e
		}
		_, e = a.bControl(c, "POST", "/stop")
		if e != nil {
			fmt.Println("已撤销 B 授权；若后台进程仍在退出，将不再重连。")
			return nil
		}
		fmt.Println("本机执行已关闭，远端账号和历史保留。")
		return nil
	case "status":
		local, err := a.bControl(c, "GET", "/status")
		fmt.Printf("独立 rdev Plan B / 协议 %d\n本机授权：%t；执行器在线：%t\n", bProtocol, c.Authorized, err == nil)
		if err == nil {
			fmt.Println(strings.TrimSpace(string(local)))
		}
		out, err := a.bSSH(c, []string{"docker", "inspect", "-f", "{{.State.Status}} {{json .HostConfig.PortBindings}}", c.Container}, nil, false)
		fmt.Print(string(out))
		return err
	case "reset":
		if _, err := a.bControl(c, "GET", "/status"); err == nil {
			return errors.New("请先 plan-b off，结束 B 任务后再 reset；不会删除账号/历史")
		}
		force := false
		for _, flag := range args[1:] {
			if flag == "--force-runtime" {
				force = true
			} else {
				return errors.New("reset 仅支持 --force-runtime")
			}
		}
		script := "set -eu\nif docker inspect " + shellQuote(c.Container) + " >/dev/null 2>&1; then\n" + bOwnerGuard(c) + "\n"
		if !force {
			script += "if docker exec " + shellQuote(c.Container) + " tmux list-sessions >/dev/null 2>&1; then echo 'B 还有活动终端，请先结束，或显式 reset --force-runtime 中断运行进程（保留所有数据）' >&2; exit 1; fi\n"
		}
		script += "docker rm -f " + shellQuote(c.Container) + "\nfi\n"
		if _, e = a.bSSH(c, []string{"sh", "-c", script}, nil, false); e != nil {
			return e
		}
		return a.bRunContainer(c)
	case "connection":
		return a.bExport(c, args[1:])
	case "install-claude":
		_, e = a.bSSH(c, []string{"docker", "exec", "-i", c.Container, "sh", "-c", "npm install --global --prefix /data/runtime/claude @anthropic-ai/claude-code@2.1.321"}, nil, false)
		return e
	case "web":
		return a.bOpenWeb(c, args[1:])
	case "account":
		_, e = a.bSSH(c, []string{"docker", "exec", "-it", c.Container, "claude", "auth", "login"}, nil, true)
		return e
	case "claude", "sessions", "reconnect", "shell", "agent":
		if args[0] != "sessions" {
			if !c.Authorized {
				return errors.New("B 本机执行未授权；先运行 plan-b on --trust-local-user")
			}
			if e = a.bOn(c); e != nil {
				return e
			}
		}
		cwd, e := os.Getwd()
		if e != nil {
			return e
		}
		request := bSessionRequest{ID: bRandom(6), DeviceID: c.DeviceID, Project: cwd, Kind: args[0], Args: args[1:]}
		input, _ := json.Marshal(request)
		// Native terminal stdin must remain the user's terminal. Metadata travels in a private file, separately.
		if _, e = a.bSSH(c, []string{"docker", "exec", "-i", c.Container, "rdev", "remote", "b-session-prepare"}, input, false); e != nil {
			return e
		}
		command := []string{"docker", "exec", "-it", c.Container, "rdev", "remote", "b-terminal", request.ID, args[0]}
		command = append(command, args[1:]...)
		return a.bSSHStream(c, command, args[0] != "agent")
	default:
		return errors.New("未知 Plan B 命令；运行 rdev plan-b help")
	}
}
func (c bProfile) sshBase(tty bool) []string {
	x := []string{"-o", "StrictHostKeyChecking=yes", "-o", "ForwardAgent=no", "-o", "BatchMode=yes", "-o", "ConnectTimeout=20", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3"}
	if c.setupMux != "" {
		x = append(x, "-o", "ControlMaster=auto", "-o", "ControlPath="+c.setupMux, "-o", "ControlPersist=30")
	}
	if c.SSHConfig != "" {
		x = append(x, "-F", c.SSHConfig)
	}
	if tty {
		x = append(x, "-t")
	} else {
		x = append(x, "-T")
	}
	return append(x, c.SSH)
}
func (a *App) bSSH(c bProfile, command []string, input []byte, tty bool) ([]byte, error) {
	args := append(c.sshBase(tty), shellJoin(command))
	cmd := exec.Command("ssh", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if tty {
		cmd.Stdin = os.Stdin
		return nil, cmd.Run()
	}
	cmd.Stdin = bytes.NewReader(input)
	var out, err bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &err
	e := cmd.Run()
	if e != nil {
		return out.Bytes(), fmt.Errorf("B SSH/远端操作失败：%w：%s", e, strings.TrimSpace(err.String()))
	}
	return out.Bytes(), nil
}
func (a *App) bSetup(args []string) error {
	f := flag.NewFlagSet("plan-b setup", flag.ContinueOnError)
	dest := f.String("ssh", "", "已验证的私网 SSH 别名")
	joinFile := f.String("join", "", "加入自己的已有 B 服务（私有连接文件）")
	name := f.String("name", "", "本机设备名称")
	webPort := f.Int("web-port", 18788, "远端回环 Web 端口")
	without := f.Bool("without-claude", false, "仅安装桥接；用户可后置安装 B 自己的 Claude")
	if e := f.Parse(args); e != nil {
		return e
	}
	if e := a.prepareState(); e != nil {
		return e
	}
	dir := a.bDir()
	if entries, e := os.ReadDir(dir); e == nil && len(entries) > 0 {
		if b, e := os.ReadFile(filepath.Join(dir, ".native-owner")); e != nil || string(b) != "remote-dev-kit-native-b\n" {
			return errors.New("该目录已有其他 Plan B 数据；不导入旧集成版，请使用独立 --state-dir")
		}
	}
	if e := os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	if e := secureDirectory(dir); e != nil {
		return e
	}
	if e := atomicWrite(filepath.Join(dir, ".native-owner"), []byte("remote-dev-kit-native-b\n"), 0600); e != nil {
		return e
	}
	_, profileStat := os.Stat(filepath.Join(dir, "profile.json"))
	c, e := a.bLoad()
	if e != nil && profileStat == nil {
		return fmt.Errorf("现有 B 配置损坏，保留身份与数据，不会重建：%w", e)
	}
	if e != nil {
		c = bProfile{Protocol: bProtocol, ServerID: bRandom(6), DeviceID: bRandom(6), ControlToken: bRandom(32), WebPort: 18788, WebToken: bRandom(32)}
		if *joinFile != "" {
			data, err := os.ReadFile(*joinFile)
			if err != nil {
				return err
			}
			var link bConnection
			if err = json.Unmarshal(data, &link); err != nil {
				return err
			}
			if err = link.validate(); err != nil {
				return err
			}
			c.ServerID = link.ServerID
			c.Container = link.Container
			c.Image = link.Image
			c.RemoteRoot = link.RemoteRoot
			c.WebPort = link.WebPort
			c.WebToken = link.WebToken
			c.SSH = link.SSH
			c.Joined = true
		}

		if *dest == "" {
			if ac, err := a.load(); err == nil {
				c.SSH = "rdev-" + ac.Name
				c.SSHConfig = filepath.Join(a.State, "ssh_config")
			}
		}
		if *dest != "" {
			c.SSH = *dest
		}
		if c.SSH == "" {
			p := Prompter{bufio.NewReader(os.Stdin)}
			c.SSH, e = p.ask("已验证的私网 SSH 别名", "")
			if e != nil {
				return e
			}
		}
		if !validDestination(c.SSH) {
			return errors.New("SSH 别名不合法")
		}
		home, e := a.bSSH(c, []string{"sh", "-c", `printf '%s' "$HOME"`}, nil, false)
		if e != nil {
			return e
		}
		h := strings.TrimSpace(string(home))
		if !strings.HasPrefix(h, "/") || strings.ContainsAny(h, "\r\n\x00") {
			return errors.New("远端用户目录无效")
		}
		if !c.Joined {
			c.RemoteRoot = h + "/.rdev-b/" + c.ServerID
			c.Container = "rdev-b-" + c.ServerID
			c.WebPort = *webPort
		}
		c.ControlPort, e = freePort()
		if e != nil {
			return e
		}
	} else if *dest != "" && *dest != c.SSH {
		return errors.New("更换服务器请使用另一个 --state-dir")
	}
	if c.WebPort < 1024 || c.WebPort > 65535 {
		return errors.New("Web 端口必须是 1024..65535")
	}
	if *name != "" {
		c.Name = *name
	}
	if len(c.Name) > 80 || strings.ContainsAny(c.Name, "\r\n\x00") {
		return errors.New("设备名不合法")
	}
	if profileStat != nil {
		c.SkipClaude = *without
	} else {
		f.Visit(func(option *flag.Flag) {
			if option.Name == "without-claude" {
				c.SkipClaude = *without
			}
		})
	}
	for _, name := range []string{"host-key", "client-key"} {
		p := filepath.Join(dir, name)
		if _, e := os.Stat(p); os.IsNotExist(e) {
			if profileStat == nil {
				return errors.New("现有 B 设备密钥缺失；不自动替换身份，请从备份恢复或使用新 state-dir")
			}
			key, _, e := bNewKey()
			if e != nil {
				return e
			}
			if e = atomicWrite(p, key, 0600); e != nil {
				return e
			}
		}
	}
	if e = a.bSave(c); e != nil {
		return e
	}
	if c.WebToken == "" {
		c.WebToken = bRandom(32)
		if e = a.bSave(c); e != nil {
			return e
		}
	}
	if c.Joined {
		if e = a.bPreflight(c, "ssh-device", "sftp"); e != nil {
			return e
		}
		fmt.Println("已加入已有 B 服务；本机执行仍须显式 on。")
		return nil
	}
	return a.bProvision(c)
}
func (a *App) bControl(c bProfile, method, path string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	request, e := http.NewRequestWithContext(ctx, method, "http://127.0.0.1:"+strconv.Itoa(c.ControlPort)+path, nil)
	if e != nil {
		return nil, e
	}
	request.Header.Set("Authorization", "Bearer "+c.ControlToken)
	client := &http.Client{Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	response, e := client.Do(request)
	if e != nil {
		return nil, e
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, errors.New("本机控制端口不属于此 B 配置")
	}
	return io.ReadAll(io.LimitReader(response.Body, 8192))
}
func (a *App) bOn(c bProfile) error {
	if !c.Authorized {
		return errors.New("本机执行尚未授权")
	}
	existing, controlErr := a.bControl(c, "GET", "/status")
	if controlErr == nil && bytes.Contains(existing, []byte(`"connected":true`)) {
		return nil
	}
	executable, e := os.Executable()
	if e != nil {
		return e
	}
	logs := filepath.Join(a.bDir(), "bridge.log")
	log, e := os.OpenFile(logs, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer log.Close()
	cmd := exec.Command(executable, "--state-dir", a.State, "plan-b", "serve")
	privateProcess(cmd)
	cmd.Stdout = log
	cmd.Stderr = log
	if controlErr != nil {
		if e = a.bPreflight(c, "device-mux-v1"); e != nil {
			return e
		}
		if e = cmd.Start(); e != nil {
			return e
		}
		cmd.Process.Release()
	}
	for i := 0; i < 150; i++ {
		if b, e := a.bControl(c, "GET", "/status"); e == nil && bytes.Contains(b, []byte(`"connected":true`)) {
			fmt.Println("Plan B 本机执行已在线；plan-b off 可随时撤销，不随系统登录自动启动。")
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("B 执行器未就绪，请检查私网 SSH 和 %s；没有切换公网通道", logs)
}
func bOwnerGuard(c bProfile) string {
	return "set -eu\ntest \"$(docker inspect -f '{{index .Config.Labels \"io.rdev.native-b.owner\"}}' " + shellQuote(c.Container) + ")\" = " + shellQuote(c.ServerID) + " || { echo '拒绝操作其他容器' >&2; exit 1; }"
}
func (a *App) bRunContainer(c bProfile) error {
	script := `set -eu
if docker inspect ` + shellQuote(c.Container) + ` >/dev/null 2>&1; then
` + bOwnerGuard(c) + `
test "$(docker inspect -f '{{.Config.Image}}' ` + shellQuote(c.Container) + `)" = ` + shellQuote(c.Image) + ` || { echo 'B 镜像已更新；请先 off，再 reset 重建容器，全部账号/历史保留' >&2; exit 1; }
docker start ` + shellQuote(c.Container) + `
else
docker run -d --name ` + shellQuote(c.Container) + ` --label io.rdev.native-b.owner=` + shellQuote(c.ServerID) + ` --restart unless-stopped --security-opt no-new-privileges:true --cap-drop ALL -v ` + shellQuote(c.RemoteRoot+"/data:/data") + ` -p 127.0.0.1:` + strconv.Itoa(c.WebPort) + `:8788 --log-opt max-size=10m --log-opt max-file=3 ` + shellQuote(c.Image) + `
fi
`
	_, e := a.bSSH(c, []string{"sh", "-c", script}, nil, false)
	return e
}
func (a *App) bProvision(c bProfile) error {
	if runtime.GOOS != "windows" {
		dir, e := os.MkdirTemp("", "rdev-bsetup-")
		if e != nil {
			return e
		}
		defer os.RemoveAll(dir)
		socket := filepath.Join(dir, "mux")
		if len(socket) < 95 {
			c.setupMux = socket
			defer func() { _ = exec.Command("ssh", sshOptions(c.sshBase(false), []string{"-O", "exit"})...).Run() }()
		}
	}

	platform, e := a.bSSH(c, []string{"docker", "info", "--format", "{{.Architecture}}"}, nil, false)
	if e != nil {
		return fmt.Errorf("远端需要可用 Docker；不会安装或重启现有 Docker：%w", e)
	}
	arch := strings.TrimSpace(string(platform))
	if arch == "x86_64" {
		arch = "amd64"
	}
	if arch == "aarch64" {
		arch = "arm64"
	}
	if arch != "amd64" && arch != "arm64" {
		return errors.New("B 容器架构暂不支持")
	}
	bin, e := asset("rdev", "linux-"+arch, "rdev")
	if e != nil {
		return e
	}
	payload, e := os.ReadFile(bin)
	if e != nil {
		return e
	}
	sum := sha256.Sum256(append(append([]byte{}, payload...), []byte(bDockerfile+strconv.FormatBool(c.SkipClaude))...))
	c.Image = "rdev-native-b:" + hex.EncodeToString(sum[:8])
	if e = a.bSave(c); e != nil {
		return e
	}
	payloadHash := sha256.Sum256(payload)
	var packed bytes.Buffer
	zipper := gzip.NewWriter(&packed)
	if _, e = zipper.Write(payload); e != nil {
		return e
	}
	if e = zipper.Close(); e != nil {
		return e
	}
	fmt.Printf("上传校验后的 B 助手（压缩 %.1f MiB）…\n", float64(packed.Len())/1048576)
	script := `set -eu
umask 077
p=` + shellQuote(c.RemoteRoot) + `
if [ -e "$p" ]; then test "$(cat "$p/.owner" 2>/dev/null)" = ` + shellQuote(c.ServerID) + `; else mkdir -p "$p"; printf %s ` + shellQuote(c.ServerID) + ` > "$p/.owner"; fi
mkdir -p "$p/build" "$p/data/home" "$p/data/workspace" "$p/data/devices"
tmp="$p/build/.rdev-upload-$$"
trap 'rm -f "$tmp"' 0 1 2 15
gzip -dc > "$tmp"
if command -v sha256sum >/dev/null; then actual=$(sha256sum "$tmp"); else actual=$(shasum -a 256 "$tmp"); fi
test "${actual%% *}" = ` + shellQuote(hex.EncodeToString(payloadHash[:])) + ` || { echo 'B 助手上传校验失败，保留旧程序' >&2; exit 1; }
chmod 755 "$tmp"
mv "$tmp" "$p/build/rdev"
`
	if _, e = a.bSSH(c, []string{"sh", "-c", script}, packed.Bytes(), false); e != nil {
		return e
	}
	if _, e = a.bSSH(c, []string{"sh", "-c", "umask 077; cat > " + shellQuote(c.RemoteRoot+"/build/Dockerfile")}, []byte(bDockerfile), false); e != nil {
		return e
	}
	fmt.Println("构建 rdev 自己的 B 镜像和专用官方 Claude；不会读取宿主 Claude…")
	if _, e = a.bSSH(c, []string{"docker", "build", "-q", "--build-arg", "INSTALL_CLAUDE=" + strconv.FormatBool(!c.SkipClaude), "-t", c.Image, c.RemoteRoot + "/build"}, nil, false); e != nil {
		return e
	}
	// The only privileged helper acts on this owned data volume, then exits.
	check := []string{"docker", "run", "--rm", "--user", "1000", "--cap-drop", "ALL", "--entrypoint", "sh", "-v", c.RemoteRoot + "/data:/data", c.Image, "-c", "test -w /data/home && test -w /data/devices && test -w /data/workspace"}
	if _, err := a.bSSH(c, check, nil, false); err != nil {
		if _, e = a.bSSH(c, []string{"docker", "run", "--rm", "--user", "0", "--cap-drop", "ALL", "--cap-add", "CHOWN", "--cap-add", "DAC_OVERRIDE", "--entrypoint", "chown", "-v", c.RemoteRoot + "/data:/data", c.Image, "-R", "1000:1000", "/data"}, nil, false); e != nil {
			return e
		}
	}
	webConfig, _ := json.Marshal(bWebConfig{Token: c.WebToken, ServerID: c.ServerID})
	if _, e = a.bSSH(c, []string{"docker", "run", "--rm", "-i", "--entrypoint", "rdev", "-v", c.RemoteRoot + "/data:/data", c.Image, "remote", "b-web-config"}, webConfig, false); e != nil {
		return e
	}
	if e = a.bRunContainer(c); e != nil {
		return e
	}
	fmt.Println("独立 Plan B 已设置。先 plan-b on --trust-local-user，再 plan-b claude。登录用 plan-b account。")
	return nil
}

const bDockerfile = `FROM node:24.22.2-bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates openssh-client git bash tmux python3 tini procps && rm -rf /var/lib/apt/lists/*
ARG INSTALL_CLAUDE=true
RUN if [ "$INSTALL_CLAUDE" = true ]; then npm install --global @anthropic-ai/claude-code@2.1.321 && npm cache clean --force; fi
COPY rdev /usr/local/bin/rdev
RUN mkdir -p /data/home /data/workspace /data/devices && chown -R node:node /data
ENV RDEV_NATIVE_B=1 HOME=/data/home PATH=/data/runtime/claude/bin:$PATH
USER node
WORKDIR /data/workspace
ENTRYPOINT ["/usr/bin/tini","-g","--"]
CMD ["rdev","remote","b-idle"]
`

func (a *App) bSSHStream(c bProfile, command []string, tty bool) error {
	cmd := exec.Command("ssh", append(c.sshBase(tty), shellJoin(command))...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return bNativeExit(cmd.Run())
}

func (a *App) bPreflight(c bProfile, required ...string) error {
	b, e := a.bSSH(c, []string{"docker", "exec", c.Container, "rdev", "remote", "b-handshake"}, nil, false)
	if e != nil {
		return e
	}
	var info struct {
		Protocol     int      `json:"protocol"`
		Capabilities []string `json:"capabilities"`
	}
	if json.Unmarshal(b, &info) != nil || info.Protocol != bProtocol {
		return errors.New("B 助手协议不兼容；off 后 setup/reset 更新，不修改宿主 Claude")
	}
	set := map[string]bool{}
	for _, capability := range info.Capabilities {
		set[capability] = true
	}
	for _, name := range required {
		if !set[name] {
			return fmt.Errorf("B 助手缺少能力 %s；off 后 setup/reset 更新，保留全部数据", name)
		}
	}
	return nil
}
