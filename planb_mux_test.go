package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"github.com/coder/websocket"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func bTestMuxTransfer(t *testing.T, fragmented bool) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	target, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer target.Close()
	go func() {
		for {
			conn, e := target.Accept()
			if e != nil {
				return
			}
			go func() {
				defer conn.Close()
				data, e := io.ReadAll(conn)
				if e == nil {
					_, _ = io.Copy(conn, bytes.NewReader(data))
					_ = conn.(*net.TCPConn).CloseWrite()
				}
			}()
		}
	}()
	broker, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer broker.Close()
	remote := make(chan *bMux, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, e := websocket.Accept(w, r, nil)
		if e != nil {
			return
		}
		peer := bNewMux(ctx, websocket.NetConn(ctx, ws, websocket.MessageBinary), nil)
		remote <- peer
		_ = peer.Run()
		peer.workers.Wait()
	}))
	defer server.Close()
	ws, _, e := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if e != nil {
		t.Fatal(e)
	}
	local := bNewMux(ctx, websocket.NetConn(ctx, ws, websocket.MessageBinary), func() (net.Conn, error) {
		conn, e := net.Dial("tcp", target.Addr().String())
		if e != nil {
			return nil, e
		}
		if fragmented {
			return &bTestFragmentConn{Conn: conn, limit: 256}, nil
		}
		return conn, nil
	})
	defer local.Close()
	go local.Run()
	peer := <-remote
	defer peer.Close()
	go func() {
		for {
			conn, e := broker.Accept()
			if e != nil {
				return
			}
			if fragmented {
				conn = &bTestFragmentConn{Conn: conn, delay: time.Millisecond}
			}
			_ = peer.Open(conn)
		}
	}()
	complete := make(chan error, 1)
	go func() {
		conn, e := net.Dial("tcp", broker.Addr().String())
		if e != nil {
			complete <- e
			return
		}
		defer conn.Close()
		count := 600000
		if fragmented {
			count = 50000
		}
		data := bytes.Repeat([]byte{0, 255, 2, 9, 11}, count)
		writeDone := make(chan error, 1)
		go func() {
			_, e := io.Copy(conn, bytes.NewReader(data))
			if e == nil {
				e = conn.(*net.TCPConn).CloseWrite()
			}
			writeDone <- e
		}()
		got, e := io.ReadAll(conn)
		if e == nil {
			e = <-writeDone
		}
		if e == nil && sha256.Sum256(got) != sha256.Sum256(data) {
			e = io.ErrUnexpectedEOF
		}
		complete <- e
	}()
	select {
	case e := <-complete:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(12 * time.Second):
		t.Fatal("multiplexed transfer or half-close stalled")
	}
}
func TestBDeviceSocketIsContainerPrivate(t *testing.T) {
	for _, p := range []string{"/tmp/device.sock", "/data/devices/../stream.sock", "/data/devices/0123456789ab/private-key"} {
		if bDeviceSocketPath(p) {
			t.Fatal("accepted", p)
		}
	}
	if !bDeviceSocketPath("/data/devices/0123456789ab/stream.sock") {
		t.Fatal("rejected owned socket")
	}
}
func TestBWindowsSFTPPaths(t *testing.T) {
	for from, want := range map[string]string{`C:\Users\dev\数据`: `/C:/Users/dev/数据`, `D:`: `/D:/`, `/C:/Users/dev`: `/C:/Users/dev`, `\\server\share\dir`: `//server/share/dir`} {
		if got := bSFTPPath(from, "windows"); got != want {
			t.Fatalf("%q -> %q want %q", from, got, want)
		}
	}
	if got := bSFTPPath(`/tmp/back\slash`, "darwin"); got != `/tmp/back\slash` {
		t.Fatal("changed POSIX filename")
	}
}

func TestBIndependentMuxLargeBinaryAndHalfClose(t *testing.T) {
	t.Run("large", func(t *testing.T) { bTestMuxTransfer(t, false) })
	t.Run("fragmented-and-slow-consumer", func(t *testing.T) { bTestMuxTransfer(t, true) })
}

type bTestFragmentConn struct {
	net.Conn
	limit int
	delay time.Duration
}

func (c *bTestFragmentConn) Read(b []byte) (int, error) {
	if c.limit > 0 && len(b) > c.limit {
		b = b[:c.limit]
	}
	return c.Conn.Read(b)
}
func (c *bTestFragmentConn) Write(b []byte) (int, error) {
	if c.delay > 0 {
		time.Sleep(c.delay)
	}
	return c.Conn.Write(b)
}
func (c *bTestFragmentConn) CloseWrite() error { return c.Conn.(*net.TCPConn).CloseWrite() }
