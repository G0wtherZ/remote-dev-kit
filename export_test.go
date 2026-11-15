package main

import (
	"bytes"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/pkg/sftp"
)

func exportClient(t *testing.T, root string, readonly bool) *sftp.Client {
	t.Helper()
	serverSide, clientSide := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- serveExport(root, readonly, serverSide) }()
	client, e := sftp.NewClientPipe(clientSide, clientSide)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { client.Close(); clientSide.Close(); <-done })
	return client
}
func TestSFTPReadWriteUnicodeRenameAndTruncate(t *testing.T) {
	root := t.TempDir()
	c := exportClient(t, root, false)
	if e := c.Mkdir("/资料 folder"); e != nil {
		t.Fatal(e)
	}
	f, e := c.Create("/资料 folder/数据.bin")
	if e != nil {
		t.Fatal(e)
	}
	data := bytes.Repeat([]byte{0, 1, 2, 255}, 65536)
	if _, e = f.Write(data); e != nil {
		t.Fatal(e)
	}
	f.Close()
	if e = c.PosixRename("/资料 folder/数据.bin", "/资料 folder/renamed.bin"); e != nil {
		t.Fatal(e)
	}
	f, e = c.Open("/资料 folder/renamed.bin")
	if e != nil {
		t.Fatal(e)
	}
	b, e := io.ReadAll(f)
	f.Close()
	if e != nil || !bytes.Equal(b, data) {
		t.Fatal("round trip mismatch", e)
	}
	if e = c.Truncate("/资料 folder/renamed.bin", 13); e != nil {
		t.Fatal(e)
	}
	info, e := c.Stat("/资料 folder/renamed.bin")
	if e != nil || info.Size() != 13 {
		t.Fatal(info, e)
	}
	if e = c.Remove("/资料 folder/renamed.bin"); e != nil {
		t.Fatal(e)
	}
	if e = c.RemoveDirectory("/资料 folder"); e != nil {
		t.Fatal(e)
	}
}
func TestSFTPRejectsTraversalAndSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret"), []byte("not-exported"), 0600)
	if e := os.Symlink(outside, filepath.Join(root, "escape")); e != nil {
		t.Skip("symlink privilege unavailable", e)
	}
	c := exportClient(t, root, false)
	for _, p := range []string{"../secret", "/../secret", "/escape/secret", "/C:\\Windows\\system.ini", "/.ssh/id_ed25519"} {
		if f, e := c.Open(p); e == nil {
			f.Close()
			t.Fatal("escaped", p)
		}
	}
	if f, e := c.Create("/escape/overwrite"); e == nil {
		f.Close()
		t.Fatal("write escaped")
	}
	if b, e := os.ReadFile(filepath.Join(outside, "secret")); e != nil || string(b) != "not-exported" {
		t.Fatal("outside sentinel modified")
	}
}
func TestSFTPReadonlyAndHandleLimit(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "input"), []byte("test"), 0600)
	c := exportClient(t, root, true)
	if _, e := c.Create("/output"); e == nil {
		t.Fatal("readonly create succeeded")
	}
	if e := c.Remove("/input"); e == nil {
		t.Fatal("readonly remove succeeded")
	}
	var handles []*sftp.File
	for i := 0; i < 64; i++ {
		f, e := c.Open("/input")
		if e != nil {
			t.Fatal(i, e)
		}
		handles = append(handles, f)
	}
	if f, e := c.Open("/input"); e == nil {
		f.Close()
		t.Fatal("handle budget not enforced")
	}
	handles[0].Close()
	f, e := c.Open("/input")
	if e != nil {
		t.Fatal("closed handle did not release budget", e)
	}
	f.Close()
	for _, f := range handles[1:] {
		f.Close()
	}
}
