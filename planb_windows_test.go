package main

import (
	"bytes"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBWindowsNativeShellAndBinaryFiles(t *testing.T) {
	_, host, e := bNewKey()
	if e != nil {
		t.Fatal(e)
	}
	_, key, e := bNewKey()
	if e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	executor, e := bStartExecutor(host, key, root)
	if e != nil {
		t.Fatal(e)
	}
	defer executor.close()
	client, e := ssh.Dial("tcp", executor.listener.Addr().String(), &ssh.ClientConfig{User: "rdev-device", Auth: []ssh.AuthMethod{ssh.PublicKeys(key)}, HostKeyCallback: ssh.FixedHostKey(host.PublicKey()), Timeout: time.Second})
	if e != nil {
		t.Fatal(e)
	}
	defer client.Close()
	session, e := client.NewSession()
	if e != nil {
		t.Fatal(e)
	}
	var out, err bytes.Buffer
	session.Stdin = strings.NewReader("输入数据")
	session.Stdout = &out
	session.Stderr = &err
	e = session.Run(`[Console]::Write([Console]::In.ReadToEnd()); [Console]::Error.Write('错误流'); exit 7`)
	session.Close()
	status, ok := e.(*ssh.ExitError)
	if !ok || status.ExitStatus() != 7 || out.String() != "输入数据" || err.String() != "错误流" {
		t.Fatalf("PowerShell UTF8/exit: %v %q %q", e, out.String(), err.String())
	}
	session, e = client.NewSession()
	if e != nil {
		t.Fatal(e)
	}
	if e = session.Run("exit 0"); e != nil {
		t.Fatal("portable probe failed", e)
	}
	session.Close()
	files, e := sftp.NewClient(client)
	if e != nil {
		t.Fatal(e)
	}
	defer files.Close()
	name := filepath.Join(root, "文件 binary.bin")
	data := bytes.Repeat([]byte{0, 255, 8, 9}, 300000)
	f, e := files.Create(bSFTPPath(name, "windows"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.Write(data); e != nil {
		t.Fatal(e)
	}
	f.Close()
	f, e = files.Open(bSFTPPath(name, "windows"))
	if e != nil {
		t.Fatal(e)
	}
	got, e := io.ReadAll(f)
	f.Close()
	if e != nil || !bytes.Equal(got, data) {
		t.Fatal("SFTP binary mismatch", e)
	}
	if disk, e := os.ReadFile(name); e != nil || !bytes.Equal(disk, data) {
		t.Fatal("wrong Windows file path", e)
	}
}
