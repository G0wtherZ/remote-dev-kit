package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

type healthCheck struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Detail string `json:"detail"`
	Action string `json:"action,omitempty"`
}
type healthReport struct {
	Stage           string        `json:"setup_stage,omitempty"`
	LocalRoot       string        `json:"local_root"`
	RemoteRoot      string        `json:"remote_root"`
	ConnectionReady bool          `json:"connection_ready"`
	MountReady      bool          `json:"mount_ready"`
	Checks          []healthCheck `json:"checks"`
}

func (a *App) doctor(args []string) error {
	f := flag.NewFlagSet("doctor", flag.ContinueOnError)
	asJSON := f.Bool("json", false, "机器可读诊断")
	network := f.Bool("network", false, "额外测试私网连接路径")
	if e := f.Parse(args); e != nil {
		return e
	}
	if f.NArg() != 0 {
		return errors.New("用法：rdev doctor [--json] [--network]")
	}
	c, e := a.load()
	if e != nil {
		return e
	}
	r := healthReport{Stage: c.SetupStage, LocalRoot: c.LocalRoot, RemoteRoot: c.RemoteRoot}
	add := func(name, state, detail, action string) {
		r.Checks = append(r.Checks, healthCheck{name, state, detail, action})
	}
	if e = checkLocalShare(c); e != nil {
		add("本机共享目录", "warning", e.Error(), "连接原磁盘，或 rdev setup --edit 调整路径")
	} else {
		add("本机共享目录", "ok", c.LocalRoot, "")
	}
	if b, err := a.ts(c, "status", "--json"); err == nil {
		status, err := decode[NetStatus](b)
		if err == nil && status.BackendState == "Running" {
			add("本机私网", "ok", "已连接", "")
		} else {
			add("本机私网", "error", "尚未登录或未运行", "rdev network login")
		}
	} else {
		add("本机私网", "error", err.Error(), "rdev repair；尚未完成设置时用 setup --resume")
	}
	b, e := a.remote(c, []string{"probe", c.RemoteRoot}, nil)
	if e != nil {
		add("私网 SSH / 远端助手", "error", e.Error(), "先 rdev repair；助手损坏时 rdev reset")
	} else {
		info, err := decode[RemoteInfo](b)
		if err != nil {
			return err
		}
		r.ConnectionReady = true
		add("私网 SSH / 远端助手", "ok", fmt.Sprintf("%s/%s", info.OS, info.Arch), "")
		if info.UID == 0 {
			add("研发账户", "warning", "当前是 root，Claude 启动会被拒绝", "使用普通开发用户的新连接配置")
		} else {
			add("研发账户", "ok", "普通用户", "")
		}
		if info.Tools["tmux"] == "" {
			add("持久终端", "error", "缺少 tmux", "rdev tools install base")
		} else {
			add("持久终端", "ok", "可用", "")
		}
		if !c.AutoShare {
			add("文件共享", "off", "用户已关闭，不会自动开启", "")
		} else if !info.Mounted {
			add("文件共享", "error", "未挂载", "rdev share on；缺少依赖时 tools install base")
		} else if !info.MountReadable {
			add("文件共享", "error", "挂载存在但实际读取失败或超时", "rdev repair；仍未恢复时关闭占用文件后 rdev reset")
		} else {
			r.MountReady = true
			permission := "可读写"
			if c.Readonly {
				permission = "只读"
			}
			add("文件共享", "ok", "真实目录读取通过 · "+permission, "")
		}
		if info.Tools["claude"] == "" {
			add("Claude（可选）", "optional", "未安装，不影响 shell 和文件协作", "需要时 rdev agent install")
		} else {
			add("Claude（可选）", "ok", "已安装；未自动登录或调用模型", "")
		}
	}
	if *network {
		if b, err := a.ts(c, "ping", "--c=1", "--timeout=3s", c.RemoteIP); err != nil {
			add("私网路径", "warning", err.Error(), "通过 Tailscale netcheck 检查底层网络")
		} else {
			add("私网路径", "ok", strings.TrimSpace(string(b)), "")
		}
	}
	if *asJSON {
		if e = output(r); e != nil {
			return e
		}
	} else {
		fmt.Printf("本机：%s\n远端：%s\n", r.LocalRoot, r.RemoteRoot)
		if r.Stage != "" && r.Stage != "ready" {
			fmt.Println("设置进度：", r.Stage, "（可执行 setup --resume）")
		}
		for _, check := range r.Checks {
			fmt.Printf("[%s] %s：%s\n", check.State, check.Name, check.Detail)
			if check.Action != "" {
				fmt.Println("  下一步：", check.Action)
			}
		}
	}
	for _, check := range r.Checks {
		if check.State == "error" {
			return errors.New("存在未就绪项，按上方建议处理；未修改网络或权限")
		}
	}
	return nil
}

func managedRunning(a *App, kind string) bool {
	if runtime.GOOS == "darwin" {
		return exec.Command("launchctl", "print", fmt.Sprintf("gui/%d/%s", os.Getuid(), serviceID(a, kind))).Run() == nil
	}
	if runtime.GOOS == "windows" {
		command := "$t=Get-ScheduledTask -TaskName '" + serviceID(a, kind) + "' -ErrorAction Stop; if([int]$t.State -eq 4){exit 0}else{exit 1}"
		return exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", command).Run() == nil
	}
	return false
}

func checkLocalShare(c Config) error {
	if c.LocalVolume != "" {
		identity, e := localVolumeIdentity(c.LocalRoot)
		if e != nil {
			return e
		}
		if identity != c.LocalVolume {
			return errors.New("共享磁盘身份与授权时不同，不会访问同名替代磁盘")
		}
	}
	info, e := os.Stat(c.LocalRoot)
	if e != nil {
		return fmt.Errorf("本机共享目录不可用：%w", e)
	}
	if !info.IsDir() {
		return errors.New("共享路径不是目录")
	}
	return nil
}

// For macOS removable mounts, pin the volume UUID rather than the mutable name.
// Other platforms retain their normal directory grant; no USB pass-through is implied.
func localVolumeIdentity(root string) (string, error) {
	if runtime.GOOS != "darwin" || !strings.HasPrefix(root, "/Volumes/") {
		return "", nil
	}
	parts := strings.Split(strings.TrimPrefix(root, "/Volumes/"), string(filepath.Separator))
	volume := "/Volumes/" + parts[0]
	b, e := capture("diskutil", []string{"info", "-plist", volume}, nil)
	if e != nil {
		return "", errors.New("原外接磁盘未挂载，不会创建同名空目录")
	}
	mount, e := capture("plutil", []string{"-extract", "MountPoint", "raw", "-o", "-", "-"}, b)
	if e != nil || strings.TrimSpace(string(mount)) != volume {
		return "", errors.New("共享路径不是已挂载的原磁盘")
	}
	uuid, e := capture("plutil", []string{"-extract", "VolumeUUID", "raw", "-o", "-", "-"}, b)
	if e != nil || strings.TrimSpace(string(uuid)) == "" {
		return "", errors.New("无法确认外接磁盘 UUID，未启用自动重连")
	}
	return strings.TrimSpace(string(uuid)), nil
}
