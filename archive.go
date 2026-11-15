package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path"
	"strings"
)

func archivePath(name string) (string, error) {
	parts := strings.SplitN(name, "/", 2)
	if len(parts) != 2 || parts[1] == "" {
		return "", nil
	}
	relative := parts[1]
	clean := path.Clean(relative)
	if path.IsAbs(relative) || clean == ".." || strings.HasPrefix(clean, "../") || strings.ContainsAny(relative, "\\\x00") {
		return "", errors.New("archive path escapes installation directory")
	}
	return relative, nil
}
func extractTarGzip(data []byte, directory string) error {
	reader, e := gzip.NewReader(bytes.NewReader(data))
	if e != nil {
		return e
	}
	defer reader.Close()
	root, e := os.OpenRoot(directory)
	if e != nil {
		return e
	}
	defer root.Close()
	tr := tar.NewReader(reader)
	var total int64
	for {
		header, e := tr.Next()
		if e == io.EOF {
			return nil
		}
		if e != nil {
			return e
		}
		p, e := archivePath(header.Name)
		if e != nil {
			return e
		}
		if p == "" {
			continue
		}
		total += header.Size
		if total > 1024*1024*1024 {
			return errors.New("archive exceeds extraction budget")
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if e = root.MkdirAll(p, 0755); e != nil {
				return e
			}
		case tar.TypeReg:
			if e = root.MkdirAll(path.Dir(p), 0755); e != nil {
				return e
			}
			file, e := root.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(header.Mode)&0777)
			if e != nil {
				return e
			}
			_, e = io.Copy(file, tr)
			file.Close()
			if e != nil {
				return e
			}
		case tar.TypeSymlink:
			resolved := path.Clean(path.Join(path.Dir(p), header.Linkname))
			if path.IsAbs(header.Linkname) || resolved == ".." || strings.HasPrefix(resolved, "../") {
				return errors.New("archive symlink escapes installation directory")
			}
			if e = root.MkdirAll(path.Dir(p), 0755); e != nil {
				return e
			}
			if e = root.Symlink(header.Linkname, p); e != nil {
				return e
			}
		default:
			return errors.New("unsupported archive entry")
		}
	}
}
