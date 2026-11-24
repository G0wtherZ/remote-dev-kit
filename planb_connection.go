package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
)

type bConnection struct {
	Protocol    int    `json:"protocol"`
	ServerID    string `json:"server_id"`
	SSH         string `json:"ssh"`
	RemoteRoot  string `json:"remote_root"`
	Container   string `json:"container"`
	Image       string `json:"image"`
	WebPort     int    `json:"web_port"`
	WebToken    string `json:"web_token"`
	Certificate string `json:"device_certificate"`
}

func (c bConnection) validate() error {
	if c.Protocol != bProtocol || !bIDPattern.MatchString(c.ServerID) || c.Container != "rdev-b-"+c.ServerID || !strings.HasPrefix(c.RemoteRoot, "/") || !strings.HasSuffix(c.RemoteRoot, "/.rdev-b/"+c.ServerID) || strings.Contains(c.RemoteRoot, "/../") || strings.ContainsAny(c.RemoteRoot, "\r\n\x00") || !validDestination(c.SSH) || c.WebPort < 1024 || c.WebPort > 65535 || len(c.WebToken) != 64 || !strings.HasPrefix(c.Image, "rdev-native-b:") {
		return errors.New("连接文件无效或 B 协议不兼容")
	}
	return nil
}
func (a *App) bExport(c bProfile, args []string) error {
	f := flag.NewFlagSet("connection", flag.ContinueOnError)
	destination := f.String("export", "", "私有连接文件，交给自己的其他设备")
	if e := f.Parse(args); e != nil {
		return e
	}
	if *destination == "" {
		return errors.New("connection --export <私有文件>")
	}
	p, e := expandHome(*destination)
	if e != nil {
		return e
	}
	link := bConnection{Protocol: bProtocol, ServerID: c.ServerID, SSH: c.SSH, RemoteRoot: c.RemoteRoot, Container: c.Container, Image: c.Image, WebPort: c.WebPort, WebToken: c.WebToken, Certificate: c.Certificate}
	b, e := json.MarshalIndent(link, "", "  ")
	if e != nil {
		return e
	}
	file, e := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	_, e = file.Write(b)
	ce := file.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	fmt.Println("私有连接文件已导出；含 B 服务密钥，不含 SSH 私钥或 Claude 账号/历史。仅交给自己的受信任设备。")
	return nil
}

type bExitStatus struct {
	Code  int
	Cause error
}

func (e *bExitStatus) Error() string { return e.Cause.Error() }
func (e *bExitStatus) Unwrap() error { return e.Cause }
func bNativeExit(e error) error {
	if e == nil {
		return nil
	}
	var exit interface{ ExitCode() int }
	if errors.As(e, &exit) {
		code := exit.ExitCode()
		if code < 1 || code > 255 {
			code = 1
		}
		return &bExitStatus{Code: code, Cause: e}
	}
	return e
}
