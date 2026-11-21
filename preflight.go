package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const helperProtocol = 1

const (
	capWorkspace    = "workspace-v1"
	capTerminal     = "terminal-tmux-v1"
	capShare        = "file-share-v1"
	capMountHealth  = "mount-health-v1"
	capPortRelay    = "port-relay-v1"
	capBrowser      = "browser-viewer-v1"
	capBrowserMCP   = "browser-mcp-v1"
	capDependencies = "dependency-install-v1"
	capAgentInstall = "agent-install-v1"
)

// These describe rdev's wire operations, not installed dependencies or Claude's
// version, permissions or account. Merely reading them runs no Claude command.
type HelperInfo struct {
	HelperVersion   string   `json:"helper_version"`
	ProtocolVersion int      `json:"protocol_version"`
	Capabilities    []string `json:"capabilities"`
}

func localHelperInfo() HelperInfo {
	return HelperInfo{version, helperProtocol, []string{capWorkspace, capTerminal, capShare, capMountHealth, capPortRelay, capBrowser, capBrowserMCP, capDependencies, capAgentInstall}}
}

type helperSnapshot struct {
	key       string
	info      HelperInfo
	checkedAt time.Time
}

type operationError struct {
	Code   string
	Stage  string
	Detail string
	Action string
	Cause  error
}

func (e *operationError) Error() string {
	text := "[" + e.Code + "] " + e.Detail
	if e.Action != "" {
		text += "\n下一步：" + e.Action
	}
	return text
}
func (e *operationError) Unwrap() error { return e.Cause }

func errorDetails(err error) (string, string) {
	var issue *operationError
	if errors.As(err, &issue) {
		return issue.Code, issue.Action
	}
	return "operation_failed", "按错误信息检查本工具的连接和依赖"
}

func (a *App) hint(args ...string) string {
	home, _ := os.UserHomeDir()
	def, _ := resolveCreationPath(filepath.Join(home, ".rdev"))
	if samePath(a.State, def) {
		return "rdev " + strings.Join(args, " ")
	}
	state := shellQuote(a.State)
	if runtime.GOOS == "windows" {
		state = "'" + strings.ReplaceAll(a.State, "'", "''") + "'"
	}
	return "rdev --state-dir " + state + " " + strings.Join(args, " ")
}

var helperVersionRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.+_-]{0,79}$`)
var capabilityRE = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)

func validateHelper(info HelperInfo, required ...string) error {
	local := localHelperInfo()
	if info.HelperVersion != "" && !helperVersionRE.MatchString(info.HelperVersion) {
		return &operationError{Code: "helper_metadata_invalid", Stage: "compatibility", Detail: "远端助手返回了无效的版本标识。", Action: "核对 rdev 助手路径和发布包。"}
	}
	if info.ProtocolVersion == 0 && info.Capabilities == nil {
		remote := info.HelperVersion
		if remote == "" {
			remote = "未报告版本"
		}
		return &operationError{Code: "helper_protocol_missing", Stage: "compatibility", Detail: fmt.Sprintf("本机 rdev %s；远端助手 %s 未提供协议与能力信息，无法确认协议兼容性，未启动请求的操作。", local.HelperVersion, remote), Action: "使用当前发布包重建 rdev 助手；先 reset --plan 核对范围，再 reset。保留 Claude 和全部用户数据。"}
	}
	if !helperVersionRE.MatchString(info.HelperVersion) || info.ProtocolVersion <= 0 || info.Capabilities == nil || len(info.Capabilities) > 128 {
		return &operationError{Code: "helper_metadata_invalid", Stage: "compatibility", Detail: "远端助手的版本/协议/能力响应缺失或格式无效，未启动所请求的操作。", Action: "核对助手路径及安装包；不要将其它程序当成 rdev 助手。"}
	}
	for _, capability := range info.Capabilities {
		if !capabilityRE.MatchString(capability) {
			return &operationError{Code: "helper_metadata_invalid", Stage: "compatibility", Detail: "远端助手返回了无效的能力标识。", Action: "核对 rdev 安装包和助手路径。"}
		}
	}
	if info.ProtocolVersion != local.ProtocolVersion {
		action := "更新 rdev 远端助手，不更改 Claude。"
		if info.ProtocolVersion > local.ProtocolVersion {
			action = "先更新本机 rdev 到兼容的发布包；不要自动降级较新的远端助手，也不需要更新 Claude。"
		}
		return &operationError{Code: "helper_protocol_mismatch", Stage: "compatibility", Detail: fmt.Sprintf("本机 rdev %s（协议 %d），远端助手 %s（协议 %d）：协议不兼容。", local.HelperVersion, local.ProtocolVersion, info.HelperVersion, info.ProtocolVersion), Action: action}
	}
	available := map[string]bool{}
	for _, c := range info.Capabilities {
		available[c] = true
	}
	var missing []string
	for _, c := range required {
		if !available[c] {
			missing = append(missing, c)
		}
	}
	if len(missing) > 0 {
		return &operationError{Code: "helper_capability_missing", Stage: "capability", Detail: fmt.Sprintf("远端 rdev %s 与本机协议兼容，但未提供本操作所需能力：%s。", info.HelperVersion, strings.Join(missing, ", ")), Action: "使用包含所需能力的 rdev 助手，或继续使用已有能力；无需修改 Claude 或扩大本机权限。"}
	}
	return nil
}

func requiredCapabilities(operation string) []string {
	switch operation {
	case "init":
		return []string{capWorkspace}
	case "terminal", "reconnect":
		return []string{capTerminal}
	case "mount":
		return []string{capShare, capMountHealth}
	case "port-prepare", "port-relay":
		return []string{capPortRelay}
	case "browser":
		return []string{capBrowser}
	case "browser-mcp":
		return []string{capBrowserMCP}
	case "dependencies", "install-node", "install-browser":
		return []string{capDependencies}
	case "install-agent":
		return []string{capAgentInstall}
	}
	return nil
}

func helperPathError(binary string) error {
	if !path.IsAbs(binary) || path.Base(binary) != "rdev" || strings.ContainsAny(binary, "\r\n\x00") {
		return &operationError{Code: "helper_path_invalid", Stage: "local_config", Detail: "远端助手必须是绝对路径的 rdev 程序；未执行该路径。", Action: "核对配置中的 rdev 助手路径。预检不执行 Claude 或其它程序。"}
	}
	return nil
}
func inspectHelperMetadata(call func([]string) ([]byte, error)) (HelperInfo, error) {
	b, e := call([]string{"remote", "handshake"})
	if e != nil {
		var issue *operationError
		if errors.As(e, &issue) && issue.Code == "remote_operation_unsupported" {
			v, err := call([]string{"--version"})
			if err != nil {
				return HelperInfo{}, err
			}
			parts := strings.Fields(string(v))
			if len(parts) != 2 || parts[0] != "rdev" || !helperVersionRE.MatchString(parts[1]) {
				return HelperInfo{}, &operationError{Code: "helper_metadata_invalid", Stage: "compatibility", Detail: "旧助手未提供可识别的 rdev 版本。", Action: "核对远端助手路径。"}
			}
			return HelperInfo{HelperVersion: parts[1]}, nil
		}
		return HelperInfo{}, e
	}
	var info HelperInfo
	if len(b) > 32768 || json.Unmarshal(b, &info) != nil {
		return HelperInfo{}, &operationError{Code: "helper_metadata_invalid", Stage: "compatibility", Detail: "远端助手的预检响应不是有效的版本/能力 JSON。", Action: "核对 rdev 助手路径和发布包；未执行请求的操作。"}
	}
	return info, nil
}
func (a *App) inspectHelper(c Config) (HelperInfo, error) {
	if e := helperPathError(c.RemoteBinary); e != nil {
		return HelperInfo{}, e
	}
	return inspectHelperMetadata(func(args []string) ([]byte, error) {
		if len(args) == 2 && args[0] == "remote" {
			return a.remoteRead(c, args[1:], nil)
		}
		return a.runRemoteCommand(c, append([]string{c.RemoteBinary}, args...), nil)
	})
}
func (a *App) adminPreflight(c Config, required ...string) error {
	if e := helperPathError(c.BootstrapBinary); e != nil {
		return e
	}
	info, e := inspectHelperMetadata(func(args []string) ([]byte, error) {
		b, e := admin(c, append([]string{c.BootstrapBinary}, args...), nil)
		if e != nil {
			return b, a.classifyRemoteError(e, e.Error(), false)
		}
		return b, nil
	})
	if e != nil {
		return e
	}
	return a.compatibilityIssue(info, required...)
}
func (a *App) compatibilityIssue(info HelperInfo, required ...string) error {
	e := validateHelper(info, required...)
	var issue *operationError
	if errors.As(e, &issue) && issue.Code == "helper_protocol_missing" {
		issue.Action = a.hint("reset", "--plan") + "；核对后 " + a.hint("reset") + "。只重建 rdev 组件，保留 Claude 和用户数据。"
	}
	return e
}

func (a *App) preflight(c Config, required ...string) (HelperInfo, error) {
	key := strings.Join([]string{a.State, c.Name, c.RemoteIP, c.RemoteUser, c.Identity, c.RemoteBinary, strconv.Itoa(c.AdminPort)}, "\x00")
	var info HelperInfo
	if a.helperCache != nil && a.helperCache.key == key && time.Since(a.helperCache.checkedAt) < 15*time.Second {
		info = a.helperCache.info
	} else {
		var e error
		info, e = a.inspectHelper(c)
		if e != nil {
			return info, e
		}
		// Cache only within this command, never on disk, and only after a valid
		// protocol handshake. Requested capabilities are checked on every use.
		if validateHelper(info) == nil {
			a.helperCache = &helperSnapshot{key: key, info: info, checkedAt: time.Now()}
		}
	}
	if e := a.compatibilityIssue(info, required...); e != nil {
		return info, e
	}
	return info, nil
}
