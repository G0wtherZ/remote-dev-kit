package main

import (
	"bytes"
	"context"
	"github.com/coder/websocket"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestBIndependentMuxWithRealSSHAndSFTP(t *testing.T) {
	_, host, _ := bNewKey()
	_, key, _ := bNewKey()
	root := t.TempDir()
	executor, e := bStartExecutor(host, key, root)
	if e != nil {
		t.Fatal(e)
	}
	defer executor.close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan *bMux, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, e := websocket.Accept(w, r, nil)
		if e != nil {
			return
		}
		peer := bNewMux(ctx, websocket.NetConn(ctx, ws, websocket.MessageBinary), nil)
		ready <- peer
		_ = peer.Run()
		peer.workers.Wait()
	}))
	defer server.Close()
	ws, _, e := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if e != nil {
		t.Fatal(e)
	}
	local := bNewMux(ctx, websocket.NetConn(ctx, ws, websocket.MessageBinary), func() (net.Conn, error) { return net.Dial("tcp", executor.listener.Addr().String()) })
	defer local.Close()
	go local.Run()
	peer := <-ready
	defer peer.Close()
	broker, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer broker.Close()
	go func() {
		for {
			conn, e := broker.Accept()
			if e != nil {
				return
			}
			_ = peer.Open(conn)
		}
	}()
	client, e := ssh.Dial("tcp", broker.Addr().String(), &ssh.ClientConfig{User: "rdev-device", Auth: []ssh.AuthMethod{ssh.PublicKeys(key)}, HostKeyCallback: ssh.FixedHostKey(host.PublicKey()), Timeout: time.Second})
	if e != nil {
		t.Fatal(e)
	}
	defer client.Close()
	for i := 0; i < 5; i++ {
		extra, e := ssh.Dial("tcp", broker.Addr().String(), &ssh.ClientConfig{User: "rdev-device", Auth: []ssh.AuthMethod{ssh.PublicKeys(key)}, HostKeyCallback: ssh.FixedHostKey(host.PublicKey()), Timeout: time.Second})
		if e != nil {
			t.Fatal("second tunnel", e)
		}
		extra.Close()
	}
	files, e := sftp.NewClient(client)
	if e != nil {
		t.Fatal(e)
	}
	defer files.Close()
	data := bytes.Repeat([]byte{0, 255, 8, 9}, 512*1024)
	name := bSFTPPath(filepath.Join(root, "mux-SFTP.bin"), runtime.GOOS)
	done := make(chan error, 1)
	go func() {
		f, e := files.Create(name)
		if e != nil {
			done <- e
			return
		}
		_, e = f.Write(data)
		ce := f.Close()
		if e == nil {
			e = ce
		}
		if e != nil {
			done <- e
			return
		}
		f, e = files.Open(name)
		if e != nil {
			done <- e
			return
		}
		got, e := io.ReadAll(f)
		f.Close()
		if e == nil && !bytes.Equal(got, data) {
			e = io.ErrUnexpectedEOF
		}
		done <- e
	}()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("SSH/SFTP over device protocol stalled")
	}
}
