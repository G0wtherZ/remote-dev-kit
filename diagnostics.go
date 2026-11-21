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
	Code   string `json:"code,omitempty"`
	Name   string `json:"name"`
	State  string `json:"state"`
	Detail string `json:"detail"`
	Action string `json:"action,omitempty"`
}
type healthReport struct {
	LocalHelper      HelperInfo    `json:"local_helper"`
	RemoteHelper     *HelperInfo   `json:"remote_helper,omitempty"`
	HelperCompatible bool          `json:"helper_compatible"`
	Compatibility    string        `json:"compatibility"`
	Stage            string        `json:"setup_stage,omitempty"`
	LocalRoot        string        `json:"local_root"`
	RemoteRoot       string        `json:"remote_root"`
	ConnectionReady  bool          `json:"connection_ready"`
	MountReady       bool          `json:"mount_ready"`
	Checks           []healthCheck `json:"checks"`
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
	r := healthReport{LocalHelper: localHelperInfo(), Compatibility: "unknown", Stage: c.SetupStage, LocalRoot: c.LocalRoot, RemoteRoot: c.RemoteRoot}
	add := func(name, state, detail, action string) {
		r.Checks = append(r.Checks, healthCheck{Name: name, State: state, Detail: detail, Action: action})
	}
	addIssue := func(name string, err error) {
		code, action := errorDetails(err)
		detail := err.Error()
		var issue *operationError
		if errors.As(err, &issue) {
			detail = issue.Detail
		}
		add(name, "error", detail, action)
		r.Checks[len(r.Checks)-1].Code = code
	}
	if e = checkLocalShare(c); e != nil {
		add("本机共享目录", "warning", e.Error(), "连接原磁盘，或 "+a.hint("setup", "--edit")+" 调整路径")
	} else {
		add("本机共享目录", "ok", c.LocalRoot, "")
	}
	if b, err := a.ts(c, "status", "--json"); err == nil {
		status, err := decode[NetStatus](b)
		if err == nil && status.BackendState == "Running" {
			add("本机私网", "ok", "已连接", "")
		} else {
			add("本机私网", "error", "尚未登录或未运行", a.hint("network", "login"))
		}
	} else {
		add("本机私网", "error", err.Error(), a.hint("repair")+"；尚未完成设置时用 "+a.hint("setup", "--resume"))
	}
	helper, err := a.preflight(c)
	if err != nil {
		code, _ := errorDetails(err)
		r.Compatibility = code
		var issue *operationError
		if errors.As(err, &issue) && (issue.Stage == "helper" || issue.Stage == "compatibility" || issue.Stage == "capability") {
			r.ConnectionReady = true
		}
		if helper.HelperVersion != "" {
			r.RemoteHelper = &helper
		}
		addIssue("远端助手预检", err)
	} else {
		r.RemoteHelper = &helper
		r.ConnectionReady = true
		r.HelperCompatible = true
		r.Compatibility = "compatible"
		add("助手兼容性", "ok", fmt.Sprintf("本机 %s / 远端 %s · 协议 %d 兼容", version, helper.HelperVersion, helper.ProtocolVersion), "")
		b, probeErr := a.remote(c, []string{"probe", c.RemoteRoot}, nil)
		if probeErr != nil {
			addIssue("远端运行环境", probeErr)
		} else {
			info, parseErr := decode[RemoteInfo](b)
			if parseErr != nil {
				addIssue("远端运行环境", &operationError{Code: "helper_metadata_invalid", Stage: "helper", Detail: "助手已通过握手，但运行环境响应不是有效 JSON。", Action: "核对助手路径与版本。"})
			} else if compatibilityErr := validateHelper(info.HelperInfo); compatibilityErr != nil {
				r.HelperCompatible = false
				r.Compatibility = "helper_metadata_inconsistent"
				addIssue("远端运行环境", compatibilityErr)
			} else {
				add("私网 SSH / 远端助手", "ok", fmt.Sprintf("%s/%s", info.OS, info.Arch), "")
				if info.UID == 0 {
					add("研发账户", "warning", "当前是 root，Claude 启动会被拒绝", "使用普通开发用户的新连接配置")
				} else {
					add("研发账户", "ok", "普通用户", "")
				}
				if capabilityErr := validateHelper(helper, capTerminal); capabilityErr != nil {
					addIssue("持久终端", capabilityErr)
				} else if info.Tools["tmux"] == "" {
					addIssue("持久终端", &operationError{Code: "dependency_missing", Stage: "runtime", Detail: "助手支持终端，但远端缺少 tmux。", Action: a.hint("tools", "install", "base")})
				} else {
					add("持久终端", "ok", "可用", "")
				}
				if !c.AutoShare {
					add("文件共享", "off", "用户已关闭，不会自动开启", "")
				} else if capabilityErr := validateHelper(helper, capShare, capMountHealth); capabilityErr != nil {
					addIssue("文件共享", capabilityErr)
				} else if info.Tools["sshfs"] == "" {
					addIssue("文件共享", &operationError{Code: "dependency_missing", Stage: "runtime", Detail: "助手支持共享，但远端缺少 SSHFS。", Action: a.hint("tools", "install", "base")})
				} else if !info.Mounted {
					addIssue("文件共享", &operationError{Code: "share_not_mounted", Stage: "runtime", Detail: "共享能力兼容，但当前没有挂载。", Action: a.hint("share", "on")})
				} else if !info.MountReadable {
					addIssue("文件共享", &operationError{Code: "share_unreadable", Stage: "runtime", Detail: "挂载存在但实际读取失败或超时。", Action: a.hint("repair")})
				} else {
					r.MountReady = true
					permission := "可读写"
					if c.Readonly {
						permission = "只读"
					}
					add("文件共享", "ok", "真实目录读取通过 · "+permission, "")
				}
				if info.Tools["claude"] == "" {
					add("Claude（可选）", "optional", "未安装，不影响 shell 和文件协作", "需要时 "+a.hint("agent", "install"))
				} else {
					add("Claude（可选）", "ok", "已发现程序路径；没有执行 Claude 或查询其账号", "")
				}
			}
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
		fmt.Printf("本机 rdev：%s · 协议 %d\n", r.LocalHelper.HelperVersion, r.LocalHelper.ProtocolVersion)
		if r.RemoteHelper != nil {
			protocol := fmt.Sprint(r.RemoteHelper.ProtocolVersion)
			if r.RemoteHelper.ProtocolVersion == 0 {
				protocol = "未提供"
			}
			fmt.Printf("远端助手：%s · 协议 %s\n", r.RemoteHelper.HelperVersion, protocol)
		}
		fmt.Printf("本机：%s\n远端：%s\n", r.LocalRoot, r.RemoteRoot)
		if r.Stage != "" && r.Stage != "ready" {
			fmt.Println("设置进度：", r.Stage, "（可执行 setup --resume）")
		}
		for _, check := range r.Checks {
			label := check.State
			if check.Code != "" {
				label += "/" + check.Code
			}
			fmt.Printf("[%s] %s：%s\n", label, check.Name, check.Detail)
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
	const prefix = "/Volumes/"
	if runtime.GOOS != "darwin" || len(root) < len(prefix) || !strings.EqualFold(root[:len(prefix)], prefix) {
		return "", nil
	}
	parts := strings.Split(root[len(prefix):], string(filepath.Separator))
	volume := "/Volumes/" + parts[0]
	b, e := capture("diskutil", []string{"info", "-plist", volume}, nil)
	if e != nil {
		return "", errors.New("原外接磁盘未挂载，不会创建同名空目录")
	}
	mount, e := capture("plutil", []string{"-extract", "MountPoint", "raw", "-o", "-", "-"}, b)
	if e != nil || !strings.EqualFold(strings.TrimSpace(string(mount)), volume) {
		return "", errors.New("共享路径不是已挂载的原磁盘")
	}
	uuid, e := capture("plutil", []string{"-extract", "VolumeUUID", "raw", "-o", "-", "-"}, b)
	if e != nil || strings.TrimSpace(string(uuid)) == "" {
		return "", errors.New("无法确认外接磁盘 UUID，未启用自动重连")
	}
	return strings.TrimSpace(string(uuid)), nil
}
