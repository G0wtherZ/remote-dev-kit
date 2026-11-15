package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestArchivePathsAndSymlinksCannotEscape(t *testing.T) {
	for _, name := range []string{"top/../escape", "top/../../escape", "top//absolute", "top/a\\b"} {
		if _, e := archivePath(name); e == nil {
			t.Fatal("accepted", name)
		}
	}
	root := t.TempDir()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "top/escape", Typeflag: tar.TypeSymlink, Linkname: "../../outside"})
	tw.Close()
	gz.Close()
	if extractTarGzip(out.Bytes(), root) == nil {
		t.Fatal("tar symlink escaped")
	}
	var zipped bytes.Buffer
	zw := zip.NewWriter(&zipped)
	file, _ := zw.Create("top/../../outside")
	file.Write([]byte("bad"))
	zw.Close()
	if extractZip(zipped.Bytes(), root) == nil {
		t.Fatal("zip path escaped")
	}
}
func TestTarPreservesSafeExecutableAndRelativeLink(t *testing.T) {
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "node/bin/node", Mode: 0755, Size: 4})
	tw.Write([]byte("node"))
	tw.WriteHeader(&tar.Header{Name: "node/bin/npm", Typeflag: tar.TypeSymlink, Linkname: "../lib/npm"})
	tw.Close()
	gz.Close()
	root := t.TempDir()
	if e := extractTarGzip(out.Bytes(), root); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(filepath.Join(root, "bin", "node"))
	if e != nil || string(b) != "node" {
		t.Fatal(e)
	}
}
