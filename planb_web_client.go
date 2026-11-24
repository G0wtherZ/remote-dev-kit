package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"time"
)

func (a *App) bOpenWeb(c bProfile, args []string) error {
	f := flag.NewFlagSet("plan-b web", flag.ContinueOnError)
	noOpen := f.Bool("no-open", false, "仅输出私有入口链接")
	if e := f.Parse(args); e != nil {
		return e
	}
	if len(c.WebToken) != 64 {
		return errors.New("B 缺少 Web 配置，请 setup 后 off/reset 更新助手")
	}
	if e := a.bPreflight(c, "device-tls-forward-v1"); e != nil {
		return e
	}
	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals()...)
	defer stop()
	forward, e := a.bStartForward(ctx, c)
	if e != nil {
		return e
	}
	defer forward.Close()
	c.forwardAddress = forward.address
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		return e
	}
	defer listener.Close()
	target, _ := url.Parse("https://rdev-device")
	proxy := httputil.NewSingleHostReverseProxy(target)
	transport := &http.Transport{Proxy: nil, ForceAttemptHTTP2: false, DialTLSContext: func(_ context.Context, network, address string) (net.Conn, error) { return a.bDialSSH(ctx, c) }}
	defer transport.CloseIdleConnections()
	proxy.Transport = transport
	server := &http.Server{Handler: proxy, ReadHeaderTimeout: 5 * time.Second}
	defer server.Close()
	go server.Serve(listener)
	go func() { <-ctx.Done(); server.Close() }()
	address := "http://" + listener.Addr().String()
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	ready := false
	for i := 0; i < 15; i++ {
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
		return errors.New("B 网页未就绪；检查 status 或 setup/reset，不会新增公网入口")
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
	return nil
}
