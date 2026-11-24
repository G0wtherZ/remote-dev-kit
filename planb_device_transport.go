package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"
	"net"
	"net/http"
	"os"
	"path"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

func (w *bWeb) device(out http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+w.config.Token)) != 1 {
		http.Error(out, "unauthorized", 401)
		return
	}
	conn, e := websocket.Accept(out, r, nil)
	if e != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(32768)
	ctx, cancel := context.WithTimeout(w.closing, 15*time.Second)
	_, hello, e := conn.Read(ctx)
	cancel()
	if e != nil {
		return
	}
	var d bDevice
	if json.Unmarshal(hello, &d) != nil || d.Protocol != bProtocol || !bIDPattern.MatchString(d.ID) {
		conn.Close(websocket.StatusPolicyViolation, "device protocol mismatch")
		return
	}
	w.deviceMu.Lock()
	if w.devices == nil {
		w.devices = map[string]*bMux{}
	}
	if w.devices[d.ID] != nil || len(w.devices) >= 32 {
		w.deviceMu.Unlock()
		conn.Close(websocket.StatusTryAgainLater, "device already connected or limit reached")
		return
	}
	peer := bNewMux(w.closing, conn, nil)
	w.devices[d.ID] = peer
	w.deviceMu.Unlock()
	defer func() {
		peer.Close()
		peer.workers.Done()
		peer.workers.Wait()
		w.deviceMu.Lock()
		delete(w.devices, d.ID)
		w.deviceMu.Unlock()
	}()
	if e = bRegisterDevice(d); e != nil {
		conn.Close(websocket.StatusPolicyViolation, "device registration refused")
		return
	}
	socket := "/data/devices/" + d.ID + "/stream.sock"
	if old, err := os.Lstat(socket); err == nil {
		if old.Mode()&os.ModeSocket == 0 {
			return
		}
		_ = os.Remove(socket)
	}
	listener, e := net.Listen("unix", socket)
	if e != nil {
		return
	}
	defer listener.Close()
	ready, _ := json.Marshal(map[string]int{"protocol": bProtocol})
	if e = conn.Write(w.closing, websocket.MessageText, ready); e != nil {
		return
	}
	go func() {
		for {
			incoming, e := listener.Accept()
			if e != nil {
				return
			}
			if e = peer.Open(incoming); e != nil {
				incoming.Close()
			}
		}
	}()
	// The constructor holds one worker slot; Run owns it from here.
	peer.workers.Add(1)
	if err := peer.Run(); err != nil && w.closing.Err() == nil {
		fmt.Fprintln(os.Stderr, "B device stream ended:", err)
	}
}
func (a *App) bBridgeOnce(ctx context.Context, c bProfile, port int, host ssh.Signer, clientKey []byte, connected *atomic.Bool) error {
	transport := &http.Transport{Proxy: nil, DialContext: func(dialCtx context.Context, network, address string) (net.Conn, error) {
		if e := dialCtx.Err(); e != nil {
			return nil, e
		}
		return a.bDialSSH(ctx, c)
	}}
	defer transport.CloseIdleConnections()
	dialCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	headers := http.Header{"Authorization": []string{"Bearer " + c.WebToken}}
	conn, _, e := websocket.Dial(dialCtx, "ws://rdev-b/api/device", &websocket.DialOptions{HTTPClient: &http.Client{Transport: transport}, HTTPHeader: headers})
	if e != nil {
		return e
	}
	defer conn.CloseNow()
	d := bDevice{Protocol: bProtocol, ID: c.DeviceID, HostKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(host.PublicKey()))), ClientKey: string(clientKey), Platform: runtime.GOOS, Name: c.Name}
	hello, _ := json.Marshal(d)
	if e = conn.Write(dialCtx, websocket.MessageText, hello); e != nil {
		return e
	}
	_, ready, e := conn.Read(dialCtx)
	if e != nil {
		return e
	}
	var ack struct {
		Protocol int `json:"protocol"`
	}
	if json.Unmarshal(ready, &ack) != nil || ack.Protocol != bProtocol {
		return errors.New("B device handshake mismatch")
	}
	peer := bNewMux(ctx, conn, func() (net.Conn, error) {
		return net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 5*time.Second)
	})
	connected.Store(true)
	defer connected.Store(false)
	e = peer.Run()
	peer.workers.Wait()
	return e
}
func bDeviceSocketPath(value string) bool {
	return path.Base(value) == "stream.sock" && bIDPattern.MatchString(path.Base(path.Dir(value))) && path.Dir(path.Dir(value)) == "/data/devices"
}
