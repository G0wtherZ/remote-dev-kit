package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/crypto/ssh"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"
)

type bDevice struct {
	Protocol  int    `json:"protocol"`
	ID        string `json:"id"`
	HostKey   string `json:"host_key"`
	ClientKey string `json:"client_key"`
	Platform  string `json:"platform"`
	Name      string `json:"name,omitempty"`
}

func (a *App) bServe(c bProfile) error {
	if !c.Authorized {
		return errors.New("B 本机执行未授权")
	}
	listener, e := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(c.ControlPort))
	if e != nil {
		return errors.New("B 本机控制端口已占用；不会替换已有进程")
	}
	defer listener.Close()
	hostKey, e := os.ReadFile(filepath.Join(a.bDir(), "host-key"))
	if e != nil {
		return e
	}
	host, e := ssh.ParsePrivateKey(hostKey)
	if e != nil {
		return e
	}
	clientKey, e := os.ReadFile(filepath.Join(a.bDir(), "client-key"))
	if e != nil {
		return e
	}
	client, e := ssh.ParsePrivateKey(clientKey)
	if e != nil {
		return e
	}
	home, _ := os.UserHomeDir()
	executor, e := bStartExecutor(host, client, home)
	if e != nil {
		return e
	}
	defer executor.close()
	ctx, cancel := signal.NotifyContext(context.Background(), shutdownSignals()...)
	defer cancel()
	var connected atomic.Bool
	server := &http.Server{ReadHeaderTimeout: 3 * time.Second, IdleTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+c.ControlToken)) != 1 {
			http.Error(w, "unauthorized", 401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		switch {
		case r.Method == "GET" && r.URL.Path == "/status":
			json.NewEncoder(w).Encode(map[string]any{"protocol": bProtocol, "device": c.DeviceID, "connected": connected.Load()})
		case r.Method == "POST" && r.URL.Path == "/stop":
			w.Write([]byte(`{"stopped":true}`))
			cancel()
		default:
			http.NotFound(w, r)
		}
	})}
	go server.Serve(listener)
	defer server.Close()
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				latest, e := a.bLoad()
				if e != nil || !latest.Authorized {
					cancel()
					return
				}
			}
		}
	}()
	port := executor.listener.Addr().(*net.TCPAddr).Port
	retry := time.Second
	for ctx.Err() == nil {
		err := a.bBridgeOnce(ctx, c, port, host, clientKey, &connected)
		connected.Store(false)
		if ctx.Err() != nil {
			return nil
		}
		fmt.Fprintln(os.Stderr, "B 私网通道断开：", err, "；不会重放命令，等待私网恢复。")
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(retry):
		}
		if retry < 15*time.Second {
			retry *= 2
		}
	}
	return nil
}
