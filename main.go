package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const version = "0.1.0-alpha.3"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "rdev:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	global := flag.NewFlagSet("rdev", flag.ContinueOnError)
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	state := global.String("state-dir", filepath.Join(home, ".rdev"), "私有配置目录（不会被共享）")
	showVersion := global.Bool("version", false, "显示版本")
	if err := global.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			usage()
			return nil
		}
		return err
	}
	if *showVersion {
		fmt.Println("rdev", version)
		return nil
	}
	args = global.Args()
	if strings.ContainsAny(*state, "\r\n\x00") {
		return errors.New("私有配置路径含控制字符")
	}
	app := &App{State: *state}
	if normalized, e := resolveCreationPath(app.State); e == nil {
		app.State = normalized
	} else {
		return e
	}
	if len(args) == 0 {
		usage()
		return nil
	}
	switch args[0] {
	case "version", "--version":
		fmt.Println("rdev", version)
		return nil
	case "setup":
		return app.setup(args[1:])
	case "status", "doctor":
		return app.doctor(args[1:])
	case "repair":
		return app.repair()
	case "reset":
		return app.reset(args[1:])
	case "shell", "claude":
		return app.terminal(args[0], args[1:])
	case "reconnect":
		return app.reconnect(args[1:])
	case "share":
		return app.share(args[1:])
	case "preview":
		return app.preview(args[1:])
	case "expose":
		return app.expose(args[1:])
	case "browser":
		return app.browser(args[1:])
	case "agent":
		return app.agent(args[1:])
	case "tools":
		return app.tools(args[1:])
	case "network":
		return app.network(args[1:])
	case "uninstall":
		return app.uninstall(args[1:])
	case "remote":
		return remoteCommand(args[1:])
	case "proxy":
		return proxy(args[1:])
	case "help", "-h", "--help":
		usage()
		return nil
	default:
		return errors.New("未知命令；运行 rdev help 查看用法")
	}
}

func usage() {
	fmt.Print(`Remote Dev Kit — 本机入口，远端研发环境

  rdev setup                 交互设置两端，路径可自定义
  rdev doctor                检查连接、挂载依赖和开发工具
  rdev repair                恢复本工具连接/挂载，不重置系统网络
  rdev reset [--plan]        重建本工具助手与连接，保留 Claude 和全部用户数据
  rdev claude [原生参数...]   新开远端原生 Claude；--resume 等参数直接透传
  rdev shell                 打开远端工作目录
  rdev reconnect             找回仍在运行的远端终端
  rdev share on|off|status    启动/停止专用目录的真实挂载
  rdev preview 3000          在本机浏览器预览远端开发端口
  rdev expose 8080           临时允许远端调用指定本机端口（有确认和到期）
  rdev browser              查看远端独立开发浏览器
  rdev browser mcp          运行该浏览器的官方 DevTools MCP 适配器
  rdev agent install|login   可选安装/登录 Claude；setup 不强制安装
  rdev tools install base|node|browser
  rdev network login|status|stop
  rdev uninstall            移除本工具的配置；保留共享数据

多个服务器：在命令前添加 --state-dir <独立私有配置目录>。
首次 setup 建议使用用户主目录下的专用共享文件夹，不使用系统根目录 /。
`)
}

func output(v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e == nil {
		fmt.Println(string(b))
	}
	return e
}
