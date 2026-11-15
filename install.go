package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

func addRuntimePath() {
	home, _ := os.UserHomeDir()
	paths := []string{filepath.Join(home, ".rdev", "runtime", "node", "bin"), filepath.Join(home, ".local", "bin"), "/opt/homebrew/bin", "/usr/local/bin", os.Getenv("PATH")}
	os.Setenv("PATH", strings.Join(paths, string(os.PathListSeparator)))
}
func elevated(program string, args []string) error {
	if os.Getuid() == 0 {
		return interactive(program, args)
	}
	return interactive("sudo", append([]string{program}, args...))
}
func installDependencies(group string) error {
	if group == "tailscale" {
		if runtime.GOOS == "darwin" {
			return errors.New("远端 Mac 请安装官方 Tailscale 并登录后 setup --resume：https://tailscale.com/download/mac；不会自动替换系统 VPN 扩展")
		}
		data, e := fetch("https://tailscale.com/install.sh", 2*1024*1024)
		if e != nil {
			return e
		}
		file, e := os.CreateTemp("", "rdev-tailscale-*.sh")
		if e != nil {
			return e
		}
		name := file.Name()
		defer os.Remove(name)
		file.Write(data)
		file.Close()
		return elevated("sh", []string{name})
	}
	if group != "base" && group != "browser" {
		return errors.New("依赖组仅支持 base、browser、tailscale")
	}
	if runtime.GOOS == "darwin" {
		if group == "browser" {
			return errors.New("请安装自己的 Chrome/Chromium；rdev 使用独立浏览器配置，不接管个人配置")
		}
		if _, e := exec.LookPath("brew"); e != nil {
			return errors.New("远端 Mac 需要先安装 Homebrew，或自行提供 git、tmux、sshfs")
		}
		if e := interactive("brew", []string{"install", "git", "tmux"}); e != nil {
			return e
		}
		fmt.Println("SSHFS/macFUSE：请安装兼容 FSKit 的版本 https://macfuse.github.io/ ，无需降低系统安全设置。")
		return nil
	}
	if _, e := exec.LookPath("apt-get"); e == nil {
		if e = elevated("apt-get", []string{"update"}); e != nil {
			return e
		}
		packages := []string{"git", "tmux", "sshfs", "ca-certificates", "curl", "python3", "python3-venv"}
		if group == "browser" {
			packages = []string{"chromium"}
		}
		// List restart recommendations instead of automatically restarting services.
		os.Setenv("NEEDRESTART_MODE", "l")
		return elevated("env", append([]string{"NEEDRESTART_MODE=l", "apt-get", "install", "-y", "--no-upgrade"}, packages...))
	}
	for _, manager := range []string{"dnf", "yum", "pacman", "apk"} {
		if _, e := exec.LookPath(manager); e != nil {
			continue
		}
		packages := []string{"git", "tmux", "fuse-sshfs", "curl", "python3"}
		args := []string{"install", "-y"}
		if manager == "pacman" {
			args = []string{"-S", "--needed", "--noconfirm"}
			packages = []string{"git", "tmux", "sshfs", "curl", "python"}
		}
		if manager == "apk" {
			args = []string{"add"}
			packages = []string{"git", "tmux", "sshfs", "curl", "python3"}
		}
		if group == "browser" {
			packages = []string{"chromium"}
		}
		return elevated(manager, append(args, packages...))
	}
	return errors.New("未识别此 Linux 的包管理器；请自行安装 git、tmux、sshfs 或 chromium 后运行 doctor")
}
func fetch(url string, limit int64) ([]byte, error) {
	client := &http.Client{Timeout: 90 * time.Second}
	response, e := client.Get(url)
	if e != nil {
		return nil, e
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("下载失败 HTTP %d", response.StatusCode)
	}
	b, e := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if int64(len(b)) > limit {
		return nil, errors.New("下载超过预期大小")
	}
	return b, e
}
func installNode() error {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return errors.New("远端 Node 安装支持 Linux/macOS")
	}
	index, e := fetch("https://nodejs.org/dist/index.json", 4*1024*1024)
	if e != nil {
		return e
	}
	var releases []struct {
		Version string `json:"version"`
		LTS     any    `json:"lts"`
	}
	if e = json.Unmarshal(index, &releases); e != nil {
		return e
	}
	v := ""
	for _, r := range releases {
		if s, ok := r.LTS.(string); ok && s != "" && regexp.MustCompile(`^v\d+\.\d+\.\d+$`).MatchString(r.Version) {
			v = r.Version
			break
		}
	}
	if v == "" {
		return errors.New("未找到官方 Node LTS 发行版")
	}
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x64"
	}
	filename := "node-" + v + "-" + runtime.GOOS + "-" + arch + ".tar.gz"
	prefix := "https://nodejs.org/dist/" + v + "/"
	manifest, e := fetch(prefix+"SHASUMS256.txt", 1024*1024)
	if e != nil {
		return e
	}
	want := ""
	for _, line := range strings.Split(string(manifest), "\n") {
		parts := strings.Fields(line)
		if len(parts) == 2 && parts[1] == filename {
			want = parts[0]
		}
	}
	if want == "" {
		return errors.New("官方校验清单中没有匹配平台的 Node 包")
	}
	data, e := fetch(prefix+filename, 100*1024*1024)
	if e != nil {
		return e
	}
	hash := sha256.Sum256(data)
	if hex.EncodeToString(hash[:]) != want {
		return errors.New("Node SHA-256 校验不匹配")
	}
	home, _ := os.UserHomeDir()
	parent := filepath.Join(home, ".rdev", "runtime")
	if e = os.MkdirAll(parent, 0700); e != nil {
		return e
	}
	tmp, e := os.MkdirTemp(parent, "node-new-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(tmp)
	if e = extractTarGzip(data, tmp); e != nil {
		return e
	}
	dest := filepath.Join(parent, "node")
	if _, e = os.Stat(dest); e == nil {
		return errors.New("已有受管理 Node；先确认升级策略，未覆盖原版本")
	}
	if e = os.Rename(tmp, dest); e != nil {
		return e
	}
	fmt.Println("已安装 Node", v, "到", dest)
	return nil
}
func installAgent() error {
	if os.Getuid() == 0 {
		return errors.New("请先使用普通开发账户，不在 root 下安装 Claude")
	}
	data, e := fetch("https://claude.ai/install.sh", 4*1024*1024)
	if e != nil {
		return e
	}
	file, e := os.CreateTemp("", "rdev-claude-*.sh")
	if e != nil {
		return e
	}
	name := file.Name()
	defer os.Remove(name)
	file.Write(data)
	file.Close()
	return interactive("bash", []string{name})
}
