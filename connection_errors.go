package main

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

func (a *App) classifyRemoteError(cause error, stderr string, timedOut bool) error {
	detail := strings.TrimSpace(stderr)
	if len(detail) > 1800 {
		detail = detail[:1800] + "…"
	}
	message := strings.ToLower(detail)
	issue := &operationError{Code: "remote_operation_failed", Stage: "remote", Detail: fmt.Sprintf("远端操作失败：%v: %s", cause, detail), Action: a.hint("doctor"), Cause: cause}
	var executable *exec.Error
	var exit *exec.ExitError
	switch {
	case errors.As(cause, &executable) && errors.Is(executable.Err, exec.ErrNotFound):
		issue.Code = "ssh_client_missing"
		issue.Stage = "local"
		issue.Detail = "本机找不到 OpenSSH 客户端 ssh。"
		issue.Action = "安装或启用本机 OpenSSH Client 后重试；不会自动安装 SSH 服务端。"
	case timedOut:
		issue.Code = "connection_timeout"
		issue.Stage = "transport"
		issue.Detail = "远端请求在限定时间内没有完成；写入与执行操作不会自动重放。"
		issue.Action = a.hint("doctor", "--network")
	case strings.Contains(message, "host key verification failed") || strings.Contains(message, "remote host identification has changed"):
		issue.Code = "host_key_mismatch"
		issue.Stage = "trust"
		issue.Detail = "SSH 主机身份验证失败，未接受新密钥。"
		issue.Action = "通过可信渠道核对远端主机密钥；不要关闭 StrictHostKeyChecking 或直接清空信任记录。"
	case strings.Contains(message, "can't open user config file"):
		issue.Code = "connection_config_missing"
		issue.Stage = "local_config"
		issue.Detail = "本工具的 SSH 连接配置缺失，尚未尝试远端操作。"
		issue.Action = "未完成设置用 " + a.hint("setup", "--resume") + "；已完成过设置用 " + a.hint("repair")
	case errors.As(cause, &exit) && exit.ExitCode() == 255:
		issue.Stage = "transport"
		issue.Code = "connection_unavailable"
		issue.Action = a.hint("doctor", "--network")
		if strings.Contains(message, "permission denied") || strings.Contains(message, "authentication failed") {
			issue.Code = "ssh_authentication_failed"
			issue.Stage = "authentication"
			issue.Detail = "私网 SSH 已拒绝身份认证。"
			issue.Action = "检查原 SSH 账户和密钥；加密私钥先用 ssh-add 解锁。不会生成替换身份或放宽验证。"
		}
	case strings.Contains(message, "unknown remote operation"):
		issue.Code = "remote_operation_unsupported"
		issue.Stage = "helper"
		issue.Action = "核对 rdev 远端助手的协议与能力。"
	case errors.As(cause, &exit) && exit.ExitCode() == 127:
		issue.Code = "helper_missing"
		issue.Stage = "helper"
		issue.Detail = "SSH 已响应，但指定的 rdev 助手不存在或无法找到。"
		issue.Action = a.hint("reset", "--plan") + "；仅重建本工具助手，不需要重装 Claude。"
	case errors.As(cause, &exit) && exit.ExitCode() == 126:
		issue.Code = "helper_not_executable"
		issue.Stage = "helper"
		issue.Detail = "SSH 已响应，但 rdev 助手不能执行；可能是权限或平台包不匹配。"
		issue.Action = "核对远端平台与 rdev 发布包；用 reset --plan 核对重建范围，不更改 Claude。"
	}
	return issue
}
