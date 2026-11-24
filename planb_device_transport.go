package main

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/crypto/ssh"
	"io"
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

type bBufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bBufferedConn) Read(data []byte) (int, error) { return c.reader.Read(data) }
func (w *bWeb) device(out http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" || r.Header.Get("Upgrade") != "rdev-device-v2" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+w.config.Token)) != 1 {
		http.Error(out, "unauthorized", 401)
		return
	}
	var d bDevice
	if bDecode(out, r, &d) != nil || d.Protocol != bProtocol || !bIDPattern.MatchString(d.ID) {
		http.Error(out, "device protocol mismatch", 400)
		return
	}
	w.deviceMu.Lock()
	if w.devices == nil {
		w.devices = map[string]*bMux{}
	}
	if _, exists := w.devices[d.ID]; exists || len(w.devices) >= 32 {
		w.deviceMu.Unlock()
		http.Error(out, "device already connected or limit reached", 409)
		return
	}
	w.devices[d.ID] = nil
	w.deviceMu.Unlock()
	defer func() { w.deviceMu.Lock(); delete(w.devices, d.ID); w.deviceMu.Unlock() }()
	if e := bRegisterDevice(d); e != nil {
		http.Error(out, "device registration refused", 400)
		return
	}
	socket := "/data/devices/" + d.ID + "/stream.sock"
	if old, e := os.Lstat(socket); e == nil {
		if old.Mode()&os.ModeSocket == 0 {
			http.Error(out, "invalid socket", 400)
			return
		}
		os.Remove(socket)
	}
	listener, e := net.Listen("unix", socket)
	if e != nil {
		http.Error(out, "device socket unavailable", 500)
		return
	}
	defer listener.Close()
	raw, rw, e := out.(http.Hijacker).Hijack()
	if e != nil {
		return
	}
	defer raw.Close()
	_, e = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: rdev-device-v2\r\nX-Rdev-Protocol: 1\r\n\r\n")
	if e != nil {
		return
	}
	if e = rw.Flush(); e != nil {
		return
	}
	peer := bNewMux(w.closing, &bBufferedConn{Conn: raw, reader: rw.Reader}, nil)
	w.deviceMu.Lock()
	w.devices[d.ID] = peer
	w.deviceMu.Unlock()
	defer func() { peer.Close(); peer.workers.Done(); peer.workers.Wait() }()
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
	peer.workers.Add(1)
	if e = peer.Run(); e != nil && w.closing.Err() == nil {
		fmt.Fprintln(os.Stderr, "B device stream ended:", e)
	}
}
func (a *App) bBridgeOnce(ctx context.Context, c bProfile, port int, host ssh.Signer, clientKey []byte, connected *atomic.Bool) error {
	raw, e := a.bDialSSH(ctx, c)
	if e != nil {
		return e
	}
	defer raw.Close()
	d := bDevice{Protocol: bProtocol, ID: c.DeviceID, HostKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(host.PublicKey()))), ClientKey: string(clientKey), Platform: runtime.GOOS, Name: c.Name}
	hello, _ := json.Marshal(d)
	request, e := http.NewRequestWithContext(ctx, "POST", "http://rdev-b/api/device", strings.NewReader(string(hello)))
	if e != nil {
		return e
	}
	request.Header.Set("Authorization", "Bearer "+c.WebToken)
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "rdev-device-v2")
	request.Header.Set("Content-Type", "application/json")
	if e = request.Write(raw); e != nil {
		return e
	}
	reader := bufio.NewReader(raw)
	response, e := http.ReadResponse(reader, request)
	if e != nil {
		return e
	}
	if response.StatusCode != 101 {
		b, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		response.Body.Close()
		return fmt.Errorf("B device handshake %d: %s", response.StatusCode, strings.TrimSpace(string(b)))
	}
	if response.Header.Get("X-Rdev-Protocol") != "1" {
		return errors.New("B device protocol mismatch")
	}
	peer := bNewMux(ctx, &bBufferedConn{Conn: raw, reader: reader}, func() (net.Conn, error) {
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
