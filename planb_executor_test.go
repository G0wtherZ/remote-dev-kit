package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func bTestExecutor(t *testing.T) (*bExecutor, *ssh.Client) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell acceptance requires PowerShell 7")
	}
	_, host, e := bNewKey()
	if e != nil {
		t.Fatal(e)
	}
	_, key, e := bNewKey()
	if e != nil {
		t.Fatal(e)
	}
	x, e := bStartExecutor(host, key, t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(x.close)
	client, e := ssh.Dial("tcp", x.listener.Addr().String(), &ssh.ClientConfig{User: "rdev-device", Auth: []ssh.AuthMethod{ssh.PublicKeys(key)}, HostKeyCallback: ssh.FixedHostKey(host.PublicKey()), Timeout: time.Second})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { client.Close() })
	return x, client
}
func TestBExecutorStreamsAndExit(t *testing.T) {
	_, client := bTestExecutor(t)
	session, e := client.NewSession()
	if e != nil {
		t.Fatal(e)
	}
	defer session.Close()
	var out, err bytes.Buffer
	session.Stdout = &out
	session.Stderr = &err
	session.Stdin = strings.NewReader("中文 INPUT")
	e = session.Run("cat; printf 'ERROR' >&2; exit 7")
	exit, ok := e.(*ssh.ExitError)
	if !ok || exit.ExitStatus() != 7 || out.String() != "中文 INPUT" || err.String() != "ERROR" {
		t.Fatalf("%v out=%q err=%q", e, out.String(), err.String())
	}
}
func TestBExecutorLargeStreamsAndParallel(t *testing.T) {
	_, client := bTestExecutor(t)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, e := client.NewSession()
			if e != nil {
				t.Error(e)
				return
			}
			defer s.Close()
			b, e := s.Output("printf parallel")
			if e != nil || string(b) != "parallel" {
				t.Errorf("%s %v", b, e)
			}
		}()
	}
	wg.Wait()
	s, _ := client.NewSession()
	defer s.Close()
	var out, err bytes.Buffer
	s.Stdout = &out
	s.Stderr = &err
	e := s.Run("head -c 2097152 /dev/zero; head -c 2097152 /dev/zero >&2")
	want := sha256.Sum256(make([]byte, 2097152))
	if e != nil || sha256.Sum256(out.Bytes()) != want || sha256.Sum256(err.Bytes()) != want {
		t.Fatalf("large streams %d %d %v", out.Len(), err.Len(), e)
	}
}
func TestBExecutorSFTPAndDeniedChannels(t *testing.T) {
	x, client := bTestExecutor(t)
	files, e := sftp.NewClient(client)
	if e != nil {
		t.Fatal(e)
	}
	defer files.Close()
	name := filepath.Join(x.cwd, "文件 二进制.bin")
	f, e := files.Create(name)
	if e != nil {
		t.Fatal(e)
	}
	data := bytes.Repeat([]byte{0, 255, 8, 9}, 300000)
	if _, e = f.Write(data); e != nil {
		t.Fatal(e)
	}
	f.Close()
	f, e = files.Open(name)
	if e != nil {
		t.Fatal(e)
	}
	got, e := io.ReadAll(f)
	f.Close()
	if e != nil || !bytes.Equal(got, data) {
		t.Fatal("binary mismatch", e)
	}
	s, _ := client.NewSession()
	if e = s.RequestPty("xterm", 24, 80, ssh.TerminalModes{}); e == nil {
		t.Fatal("accepted PTY")
	}
	s.Close()
	if conn, e := client.Dial("tcp", "127.0.0.1:22"); e == nil {
		conn.Close()
		t.Fatal("accepted forwarding")
	}
}
func TestBExecutorRejectsWrongKey(t *testing.T) {
	x, _ := bTestExecutor(t)
	_, wrong, _ := bNewKey()
	c, e := ssh.Dial("tcp", x.listener.Addr().String(), &ssh.ClientConfig{User: "rdev-device", Auth: []ssh.AuthMethod{ssh.PublicKeys(wrong)}, HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: time.Second})
	if e == nil {
		c.Close()
		t.Fatal("accepted wrong signed key")
	}
}
func TestBExecutorDisconnectCancelsCommand(t *testing.T) {
	x, c := bTestExecutor(t)
	s, _ := c.NewSession()
	marker := filepath.Join(x.cwd, "started")
	if e := s.Start("echo ready > " + shellQuote(marker) + "; sleep 30; echo unsafe > " + shellQuote(filepath.Join(x.cwd, "after"))); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		if _, e := os.Stat(marker); e == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("not started")
		case <-time.After(10 * time.Millisecond):
		}
	}
	c.Close()
	done := make(chan struct{})
	go func() { x.close(); close(done) }()
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("executor cleanup hung")
	}
	if _, e := os.Stat(filepath.Join(x.cwd, "after")); e == nil {
		t.Fatal("command continued after disconnect")
	}
}
