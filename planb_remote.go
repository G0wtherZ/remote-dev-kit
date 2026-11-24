package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/crypto/ssh"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"
)

type bSessionRequest struct {
	ID       string   `json:"id"`
	DeviceID string   `json:"device_id"`
	Project  string   `json:"project"`
	Kind     string   `json:"kind"`
	Args     []string `json:"args"`
}

func bRemote(args []string) error {
	if os.Getenv("RDEV_NATIVE_B") != "1" {
		return errors.New("B 助手仅在专用 B 运行环境中提供操作；不会修改宿主目录")
	}
	switch args[0] {
	case "b-idle":
		return bWebServe()
	case "b-device-cert":
		b, e := os.ReadFile("/data/config/device.crt")
		if e != nil {
			return e
		}
		_, e = os.Stdout.Write(b)
		return e
	case "b-web-config":
		return bWriteWebConfig()
	case "b-handshake":
		return output(map[string]any{"protocol": bProtocol, "version": version, "capabilities": []string{"native-claude", "ssh-device", "sftp", "tmux", "headless", "web-terminal", "schedules", "files", "device-tls-forward-v1"}})
	case "b-proxy":
		if len(args) != 2 || !bDeviceSocketPath(args[1]) {
			return errors.New("非法 B 设备 socket")
		}
		conn, e := net.DialTimeout("unix", args[1], 5*time.Second)
		if e != nil {
			return errors.New("本机设备未在线；请在该设备运行 rdev plan-b on")
		}
		defer conn.Close()
		go func() {
			io.Copy(conn, os.Stdin)
			if u, ok := conn.(*net.UnixConn); ok {
				u.CloseWrite()
			}
		}()
		_, e = io.Copy(os.Stdout, conn)
		return e
	case "b-device":
		var d bDevice
		if e := json.NewDecoder(io.LimitReader(os.Stdin, 32768)).Decode(&d); e != nil {
			return e
		}
		if e := bRegisterDevice(d); e != nil {
			return e
		}
		return output(map[string]any{"protocol": bProtocol, "device": d.ID})
	case "b-session-prepare":
		var r bSessionRequest
		if e := json.NewDecoder(io.LimitReader(os.Stdin, 2*1024*1024)).Decode(&r); e != nil {
			return e
		}
		if !bIDPattern.MatchString(r.ID) || !bIDPattern.MatchString(r.DeviceID) || strings.ContainsAny(r.Project, "\r\n\x00") {
			return errors.New("非法 B 会话请求")
		}
		if e := os.MkdirAll("/data/launches", 0700); e != nil {
			return e
		}
		b, _ := json.Marshal(r)
		return atomicWrite("/data/launches/"+r.ID+".json", b, 0600)
	case "b-terminal":
		if len(args) < 3 || !bIDPattern.MatchString(args[1]) {
			return errors.New("非法 B 终端请求")
		}
		var request bSessionRequest
		b, e := os.ReadFile("/data/launches/" + args[1] + ".json")
		if e != nil {
			return e
		}
		if e = json.Unmarshal(b, &request); e != nil {
			return e
		}
		return bNativeExit(bTerminal(request, args[2], args[3:]))
	default:
		return errors.New("未知 B 助手操作")
	}
}
func bTerminal(r bSessionRequest, kind string, args []string) error {
	prefix := "b-" + r.DeviceID + "-"
	if kind == "sessions" {
		b, e := capture("tmux", []string{"list-sessions", "-F", "#{session_name}\t#{session_attached}"}, nil)
		if e != nil {
			fmt.Println("没有活动 B 终端；历史仍保存在官方 Claude 配置中。")
			return nil
		}
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, prefix) {
				fmt.Println(line)
			}
		}
		return nil
	}
	if kind == "reconnect" {
		b, e := capture("tmux", []string{"list-sessions", "-F", "#{session_name}"}, nil)
		if e != nil {
			return errors.New("没有活动终端；可用 plan-b claude --resume 找历史")
		}
		var names []string
		for _, name := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if strings.HasPrefix(name, prefix) {
				names = append(names, name)
			}
		}
		if len(names) == 0 {
			return errors.New("本机没有可接回的 B 终端")
		}
		target := names[len(names)-1]
		if len(args) > 0 {
			target = args[0]
			if !strings.HasPrefix(target, prefix) || strings.ContainsAny(target, "\r\n:") {
				return errors.New("终端不属于当前设备")
			}
		}
		return interactive("tmux", []string{"attach-session", "-t", target})
	}
	conf := "/data/devices/" + r.DeviceID + "/ssh_config"
	if _, e := os.Stat(conf); e != nil {
		return errors.New("设备尚未注册，先 plan-b on")
	}
	if _, e := capture("ssh", []string{"-F", conf, "device", "exit 0"}, nil); e != nil {
		return errors.New("本机设备已离线；先恢复 plan-b on，不会改在服务器执行本机项目")
	}
	if kind == "agent" {
		hash := sha256.Sum256([]byte(r.Project))
		work := "/data/workspace/" + r.DeviceID + "/" + hex.EncodeToString(hash[:6])
		if e := os.MkdirAll(work, 0700); e != nil {
			return e
		}
		program := append([]string{"claude", "--append-system-prompt", bLocalPrompt(r, conf), "--print"}, args...)
		cmd := exec.Command(program[0], program[1:]...)
		cmd.Dir = work
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
	r.Kind = kind
	r.Args = args
	info, e := bCreateSession(r)
	if e != nil {
		return e
	}
	return interactive("tmux", []string{"attach-session", "-t", "=" + info.ID})
}
func bLocalPrompt(r bSessionRequest, config string) string {
	return fmt.Sprintf(`Remote Dev Kit Plan B. The official Claude Code Harness and account are on this remote machine. The user's project and development execution are on their %s device, not in this Harness workspace.
Project path on the device: %s
Use native Bash to execute ordinary SSH commands: ssh -F %s device '<command>'. Begin each project command by changing to that device project directory using the device's shell quoting. Use scp or sftp with the same -F config for exact file transfers. The device SSH server has the current user's full OS permissions; the project directory is not a sandbox. Read local project CLAUDE.md/AGENTS.md through SSH before working. Never substitute execution on the remote Harness for a failed/offline device command. Report disconnects and uncertain command outcomes; do not blindly replay mutating commands. Native remote Read/Write tools refer to Harness files, not device project files. Use remote files only for conversation artifacts and explicit staging.
Keep normal native plan, approval and user-input controls. Other user devices are under /data/devices; only use one when the user requests it.`, runtimeDevicePlatform(r.DeviceID), r.Project, shellQuote(config))
}
func runtimeDevicePlatform(id string) string {
	var d bDevice
	b, e := os.ReadFile("/data/devices/" + id + "/device.json")
	if e == nil && json.Unmarshal(b, &d) == nil {
		return d.Platform
	}
	return "local"
}

func bRegisterDevice(d bDevice) error {
	if d.Protocol != bProtocol || !bIDPattern.MatchString(d.ID) {
		return errors.New("B 设备协议/标识无效")
	}
	if _, e := ssh.ParsePrivateKey([]byte(d.ClientKey)); e != nil {
		return e
	}
	if _, _, _, _, e := ssh.ParseAuthorizedKey([]byte(d.HostKey)); e != nil {
		return e
	}
	dir := "/data/devices/" + d.ID
	if e := os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	if e := atomicWrite(dir+"/client-key", []byte(d.ClientKey), 0600); e != nil {
		return e
	}
	if e := atomicWrite(dir+"/known_hosts", []byte("rdev-device "+d.HostKey+"\n"), 0600); e != nil {
		return e
	}
	conf := "Host device device-" + d.ID + "\n  HostName rdev-device\n  HostKeyAlias rdev-device\n  User rdev-device\n  ProxyCommand /usr/local/bin/rdev remote b-proxy /data/devices/" + d.ID + "/stream.sock\n  IdentityFile " + dir + "/client-key\n  UserKnownHostsFile " + dir + "/known_hosts\n  IdentitiesOnly yes\n  StrictHostKeyChecking yes\n  BatchMode yes\n  ForwardAgent no\n  ClearAllForwardings yes\n  ConnectTimeout 10\n  ServerAliveInterval 10\n  ServerAliveCountMax 2\n  ControlMaster auto\n  ControlPersist 30\n  ControlPath " + dir + "/mux\n"
	if e := atomicWrite(dir+"/ssh_config", []byte(conf), 0600); e != nil {
		return e
	}
	d.ClientKey = ""
	b, _ := json.Marshal(d)
	if e := atomicWrite(dir+"/device.json", b, 0600); e != nil {
		return e
	}
	return nil
}
