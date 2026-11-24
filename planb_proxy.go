package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var bLegacySOCKS = regexp.MustCompile(`^(?:/usr/bin/)?nc -X 5 -x ((?:127\.0\.0\.1|localhost):[0-9]+) %h %p$`)

// Adapt only the known loopback nc SOCKS form. Keep all user host/key/auth settings
// in an Include, and never edit the user's global SSH or VPN configuration.
func (a *App) bNormalizeProxy(c *bProfile) error {
	args := []string{"-G"}
	if c.SSHConfig != "" {
		args = append(args, "-F", c.SSHConfig)
	}
	args = append(args, c.SSH)
	output, e := capture("ssh", args, nil)
	if e != nil {
		return e
	}
	var proxy string
	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(line, "proxycommand ") {
			proxy = strings.TrimPrefix(line, "proxycommand ")
			break
		}
	}
	match := bLegacySOCKS.FindStringSubmatch(proxy)
	if match == nil {
		return nil
	}
	host, _, e := net.SplitHostPort(match[1])
	if e != nil || host != "localhost" && !net.ParseIP(host).IsLoopback() {
		return errors.New("B SOCKS 代理必须为回环地址")
	}
	executable, e := os.Executable()
	if e != nil {
		return e
	}
	include := c.SSHConfig
	if include == "" {
		home, _ := os.UserHomeDir()
		include = filepath.Join(home, ".ssh", "config")
	}
	destination := filepath.Join(a.bDir(), "ssh_config")
	if samePath(include, destination) {
		return errors.New("B SSH Include 不能递归包含自身")
	}
	for _, value := range []string{executable, include, c.SSH} {
		if strings.ContainsAny(value, "\r\n\x00") {
			return errors.New("B SSH 路径不合法")
		}
	}
	content := fmt.Sprintf("# Managed by Remote Dev Kit Plan B; original config is preserved.\nHost %s\n  ProxyCommand %s proxy %s %%h %%p\nInclude %s\n", c.SSH, shellQuote(executable), match[1], sshQuote(include))
	if e = atomicWrite(destination, []byte(content), 0600); e != nil {
		return e
	}
	c.SSHConfig = destination
	fmt.Println("B 使用自己的回环 SOCKS 代理；原 SSH/VPN 配置保持原样。")
	return nil
}
