package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// Keep the listening socket owned from bind to shutdown. Each HTTP connection uses
// a verified OpenSSH stdio channel; there is no released-port/bind/auth-token race.
func (a *App) bOpenWeb(c bProfile, args []string) error {
	f := flag.NewFlagSet("plan-b web", flag.ContinueOnError)
	noOpen := f.Bool("no-open", false, "仅输出私有入口链接")
	if e := f.Parse(args); e != nil {
		return e
	}
	if len(c.WebToken) != 64 {
		return errors.New("B 缺少 Web 配置，请 setup 后 off/reset 更新助手；保留账号与历史")
	}
	if e := a.bPreflight(c, "web-terminal"); e != nil {
		return e
	}
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		return e
	}
	defer listener.Close()
	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals()...)
	defer stop()
	opts := []string{"-W", fmt.Sprintf("127.0.0.1:%d", c.WebPort)}
	// macOS/Linux share one verified SSH transport, including across asset requests.
	if runtime.GOOS != "windows" {
		dir, e := os.MkdirTemp("", "rdev-bweb-")
		if e != nil {
			return e
		}
		defer os.RemoveAll(dir)
		socket := filepath.Join(dir, "mux")
		if len(socket) < 95 {
			opts = append(opts, "-o", "ControlMaster=auto", "-o", "ControlPath="+socket, "-o", "ControlPersist=30")
			defer func() {
				_ = exec.Command("ssh", sshOptions(c.sshBase(false), []string{"-O", "exit", "-o", "ControlPath=" + socket})...).Run()
			}()
		}
	}
	var connections sync.Map
	var workers sync.WaitGroup
	slots := make(chan struct{}, 24)
	go func() {
		<-ctx.Done()
		listener.Close()
		connections.Range(func(key, value any) bool { key.(net.Conn).Close(); return true })
	}()
	go func() {
		for {
			local, err := listener.Accept()
			if err != nil {
				return
			}
			select {
			case slots <- struct{}{}:
			default:
				local.Close()
				continue
			}
			connections.Store(local, true)
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer func() { <-slots }()
				defer connections.Delete(local)
				defer local.Close()
				cmd := exec.CommandContext(ctx, "ssh", sshOptions(c.sshBase(false), opts)...)
				cmd.Stderr = os.Stderr
				input, e := cmd.StdinPipe()
				if e != nil {
					return
				}
				output, e := cmd.StdoutPipe()
				if e != nil {
					return
				}
				if e = cmd.Start(); e != nil {
					return
				}
				go func() { io.Copy(input, local); input.Close() }()
				io.Copy(local, output)
				local.Close()
				_ = cmd.Wait()
			}()
		}
	}()
	address := "http://" + listener.Addr().String()
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	ready := false
	for i := 0; i < 30; i++ {
		response, err := client.Get(address + "/health")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == 200 {
				ready = true
				break
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	if !ready {
		stop()
		workers.Wait()
		return errors.New("B 网页未就绪；检查 status，更新助手后 off/reset，不会新增公网入口")
	}
	cwd, _ := os.Getwd()
	link := address + "/#" + url.Values{"token": {c.WebToken}, "device": {c.DeviceID}, "project": {cwd}}.Encode()
	if *noOpen {
		fmt.Println(link)
	} else {
		fmt.Println("正在打开 B 网页：", address, "；关闭本终端可停止网页通道。")
		if e = openBrowser(link); e != nil {
			return e
		}
	}
	<-ctx.Done()
	workers.Wait()
	return nil
}
