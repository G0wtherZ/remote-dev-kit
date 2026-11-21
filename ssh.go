package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func capture(command string, args []string, input []byte) ([]byte, error) {
	cmd := exec.Command(command, args...)
	cmd.Stdin = bytes.NewReader(input)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	b, e := cmd.Output()
	if e != nil {
		return b, fmt.Errorf("%s: %w: %s", command, e, strings.TrimSpace(stderr.String()))
	}
	return b, nil
}
func interactive(command string, args []string) error {
	cmd := exec.Command(command, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
func (a *App) sshArgs(c Config, tty bool) []string {
	x := []string{"-F", filepath.Join(a.State, "ssh_config"), "-o", "ForwardAgent=no", "-o", "ConnectTimeout=20"}
	if tty {
		x = append(x, "-t")
	} else {
		x = append(x, "-T", "-o", "BatchMode=yes")
	}
	return append(x, "rdev-"+c.Name)
}
func (a *App) remote(c Config, args []string, input []byte) ([]byte, error) {
	if len(args) > 0 {
		if required := requiredCapabilities(args[0]); len(required) > 0 {
			if _, e := a.preflight(c, required...); e != nil {
				return nil, e
			}
		}
	}
	return a.remoteRead(c, args, input)
}
func (a *App) remoteRead(c Config, args []string, input []byte) ([]byte, error) {
	b, e := a.remoteOnce(c, args, input)
	safe := len(args) > 0 && (args[0] == "probe" || args[0] == "handshake" || args[0] == "mount-health")
	var issue *operationError
	retryable := errors.As(e, &issue) && issue.Code == "connection_unavailable"
	if e != nil && safe && retryable && transientPrivateFailure(e.Error()) {
		_, _ = a.ts(c, "ping", "--c=1", "--timeout=2s", c.RemoteIP)
		return a.remoteOnce(c, args, input)
	}
	return b, e
}
func transientPrivateFailure(message string) bool {
	if !strings.Contains(message, "exit status 255") {
		return false
	}
	for _, text := range []string{"private-network destination is unavailable", "Connection closed", "Connection timed out", "Connection refused", "No route to host"} {
		if strings.Contains(message, text) {
			return true
		}
	}
	return false
}
func (a *App) remoteOnce(c Config, args []string, input []byte) ([]byte, error) {
	return a.runRemoteCommand(c, append([]string{c.RemoteBinary, "remote"}, args...), input)
}
func (a *App) runRemoteCommand(c Config, command []string, input []byte) ([]byte, error) {
	if len(command) == 0 {
		return nil, errors.New("缺少 rdev 助手操作")
	}
	if e := helperPathError(command[0]); e != nil {
		return nil, e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", append(a.sshArgs(c, false), shellJoin(command))...)
	cmd.Stdin = bytes.NewReader(input)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	b, e := cmd.Output()
	if e != nil {
		return b, a.classifyRemoteError(e, stderr.String(), ctx.Err() != nil)
	}
	return b, nil
}

func admin(c Config, command []string, input []byte) ([]byte, error) {
	if !validDestination(c.AdminSSH) {
		return nil, errors.New("无效 SSH 地址")
	}
	args := []string{"-T", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ForwardAgent=no", "-o", "ConnectTimeout=15"}
	args = append(args, adminPort(c)...)
	args = append(args, c.AdminSSH, shellJoin(command))
	return capture("ssh", args, input)
}
func adminPort(c Config) []string {
	if c.AdminPort > 0 {
		return []string{"-p", strconv.Itoa(c.AdminPort)}
	}
	return nil
}
func adminInteractive(c Config, command string) error {
	args := []string{"-t", "-o", "ForwardAgent=no"}
	args = append(args, adminPort(c)...)
	return interactive("ssh", append(args, c.AdminSSH, command))
}
func adminSFTP(c Config, input []byte) ([]byte, error) {
	args := []string{"-b", "-"}
	if c.AdminPort > 0 {
		args = append(args, "-P", strconv.Itoa(c.AdminPort))
	}
	return capture("sftp", append(args, c.AdminSSH), input)
}
func (a *App) writeSSH(c Config, publicKey string) error {
	if e := verifyPinnedKey(filepath.Join(a.State, "known_hosts"), publicKey); e != nil {
		return e
	}
	args := append([]string{"-G"}, adminPort(c)...)
	b, e := capture("ssh", append(args, c.AdminSSH), nil)
	if e != nil {
		return e
	}
	fields := map[string][]string{}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(line, " ")
		if ok {
			fields[k] = append(fields[k], v)
		}
	}
	for _, k := range []string{"user", "port"} {
		if len(fields[k]) == 0 {
			return errors.New("无法读取 SSH 配置 " + k)
		}
	}
	if c.RemoteUser != "" {
		fields["user"] = []string{c.RemoteUser}
	}
	if c.Identity != "" {
		fields["identityfile"] = []string{c.Identity}
		fields["identityagent"] = nil
	}
	known := filepath.Join(a.State, "known_hosts")
	if e = atomicWrite(known, []byte("rdev-"+c.Name+" "+strings.TrimSpace(publicKey)+"\n"), 0600); e != nil {
		return e
	}
	lines := []string{"Host rdev-" + c.Name, "  HostName " + c.RemoteIP, "  User " + fields["user"][0], "  Port " + fields["port"][0], "  HostKeyAlias rdev-" + c.Name, "  UserKnownHostsFile " + sshQuote(known), "  StrictHostKeyChecking yes", "  ForwardAgent no", "  ControlMaster no", "  ServerAliveInterval 15", "  ServerAliveCountMax 3"}
	if c.SOCKS != "" {
		exe, _ := os.Executable()
		lines = append(lines, "  ProxyCommand "+sshQuote(exe)+" proxy "+sshQuote(c.SOCKS)+" %h %p")
	}
	for _, identity := range fields["identityfile"] {
		expanded, _ := expandHome(identity)
		if _, e := os.Stat(expanded); e == nil || identity == "none" {
			lines = append(lines, "  IdentityFile "+sshQuote(identity))
		}
	}
	if agent := fields["identityagent"]; len(agent) > 0 && agent[0] != "none" {
		lines = append(lines, "  IdentityAgent "+sshQuote(agent[0]))
	}
	return atomicWrite(filepath.Join(a.State, "ssh_config"), []byte(strings.Join(lines, "\n")+"\n"), 0600)
}

func verifyPinnedKey(file, publicKey string) error {
	parts := strings.Fields(publicKey)
	if len(parts) < 2 || !strings.HasPrefix(parts[0], "ssh-") && !strings.HasPrefix(parts[0], "ecdsa-") {
		return errors.New("远端主机公钥无效，未修改信任记录")
	}
	b, e := os.ReadFile(file)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	old := strings.Fields(string(b))
	if len(old) < 3 || old[1] != parts[0] || old[2] != parts[1] {
		return errors.New("远端主机密钥发生变化，未自动接受；请先通过可信渠道核对主机身份")
	}
	return nil
}
func (a *App) upload(c Config, local, remote string) error {
	if strings.ContainsAny(local+remote, "\r\n\x00") {
		return errors.New("文件名包含不支持的控制字符")
	}
	args := []string{"-F", filepath.Join(a.State, "ssh_config"), "-b", "-", "rdev-" + c.Name}
	_, e := capture("sftp", args, []byte("put "+sshQuote(local)+" "+sshQuote(remote)+"\n"))
	return e
}
func (a *App) preview(args []string) error {
	open := true
	if len(args) == 2 && args[0] == "--no-open" {
		open = false
		args = args[1:]
	}
	if len(args) != 1 {
		return errors.New("用法：rdev preview <远端端口>")
	}
	p, e := strconv.Atoi(args[0])
	if e != nil || p < 1 || p > 65535 {
		return errors.New("端口必须为 1–65535")
	}
	c, e := a.load()
	if e != nil {
		return e
	}
	local, e := freePort()
	if e != nil {
		return e
	}
	cmd := exec.Command("ssh", sshOptions(a.sshArgs(c, false), []string{"-N", "-o", "ExitOnForwardFailure=yes", "-L", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", local, p)})...)
	cmd.Stderr = os.Stderr
	if e = cmd.Start(); e != nil {
		return e
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals()...)
	defer stop()
	ready := false
	for i := 0; i < 100; i++ {
		connection, e := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", local), 100*time.Millisecond)
		if e == nil {
			connection.Close()
			ready = true
			break
		}
		select {
		case e := <-done:
			return e
		default:
		}
		time.Sleep(150 * time.Millisecond)
	}
	if !ready {
		cmd.Process.Kill()
		<-done
		return errors.New("端口转发未就绪")
	}
	address := fmt.Sprintf("http://127.0.0.1:%d", local)
	fmt.Printf("预览 %s → 远端 127.0.0.1:%d；Ctrl+C 关闭。\n", address, p)
	if open {
		_ = openBrowser(address)
	}
	select {
	case e := <-done:
		return e
	case <-ctx.Done():
		cmd.Process.Kill()
		<-done
		return nil
	}
}
func sshOptions(base, options []string) []string {
	result := append([]string{}, base[:len(base)-1]...)
	result = append(result, options...)
	return append(result, base[len(base)-1])
}
func decode[T any](b []byte) (T, error) { var value T; e := json.Unmarshal(b, &value); return value, e }
