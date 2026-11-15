package main

import (
	"errors"
	"io"
	"os"
	"path"
	"strings"
	"sync"

	"github.com/pkg/sftp"
)

type exportFS struct {
	root     *os.Root
	readonly bool
	init     sync.Once
	open     chan struct{}
}
type trackedFile struct {
	*os.File
	once    sync.Once
	release func()
}

func (f *trackedFile) Close() error {
	var e error
	f.once.Do(func() { e = f.File.Close(); f.release() })
	return e
}

func virtualPath(p string) (string, error) {
	if strings.ContainsAny(p, "\\:\x00") {
		return "", os.ErrPermission
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return "", os.ErrPermission
		}
		part = strings.ToLower(strings.TrimRight(part, " ."))
		if part == ".." || part == ".ssh" || part == ".rdev" || part == ".aws" || part == ".gnupg" {
			return "", os.ErrPermission
		}
	}
	p = strings.TrimPrefix(path.Clean("/"+p), "/")
	if p == "" {
		p = "."
	}
	return p, nil
}
func (f *exportFS) regular(p string, flags int) (*trackedFile, error) {
	p, e := virtualPath(p)
	if e != nil {
		return nil, e
	}
	if info, err := f.root.Lstat(p); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return nil, os.ErrPermission
	}
	f.init.Do(func() { f.open = make(chan struct{}, 64) })
	select {
	case f.open <- struct{}{}:
	default:
		return nil, errors.New("too many open file handles")
	}
	file, e := f.root.OpenFile(p, flags|safeOpenFlags(), 0600)
	if e != nil {
		<-f.open
		return nil, e
	}
	info, e := file.Stat()
	if e != nil || !info.Mode().IsRegular() {
		file.Close()
		<-f.open
		return nil, os.ErrPermission
	}
	return &trackedFile{File: file, release: func() { <-f.open }}, nil
}
func (f *exportFS) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	return f.regular(r.Filepath, os.O_RDONLY)
}
func (f *exportFS) Filewrite(r *sftp.Request) (io.WriterAt, error) { return f.OpenFile(r) }
func (f *exportFS) OpenFile(r *sftp.Request) (sftp.WriterAtReaderAt, error) {
	p := r.Pflags()
	if f.readonly && (p.Write || p.Creat || p.Trunc || p.Append) {
		return nil, os.ErrPermission
	}
	flags := os.O_RDONLY
	if p.Read && p.Write {
		flags = os.O_RDWR
	} else if p.Write {
		flags = os.O_WRONLY
	}
	if p.Creat {
		flags |= os.O_CREATE
	}
	if p.Excl {
		flags |= os.O_EXCL
	}
	if p.Trunc {
		flags |= os.O_TRUNC
	}
	return f.regular(r.Filepath, flags)
}
func (f *exportFS) Filecmd(r *sftp.Request) error {
	if f.readonly {
		return os.ErrPermission
	}
	p, e := virtualPath(r.Filepath)
	if e != nil {
		return e
	}
	if p == "." {
		return os.ErrPermission
	}
	switch r.Method {
	case "Mkdir":
		return f.root.Mkdir(p, 0700)
	case "Remove", "Rmdir":
		return f.root.Remove(p)
	case "Rename":
		target, e := virtualPath(r.Target)
		if e != nil || target == "." {
			return os.ErrPermission
		}
		return f.root.Rename(p, target)
	case "Setstat":
		flags := r.AttrFlags()
		attrs := r.Attributes()
		// Handle-based metadata writes avoid symlink races in path-based chmod/chown.
		file, e := f.regular(r.Filepath, os.O_RDWR)
		if e != nil {
			return e
		}
		defer file.Close()
		if flags.UidGid {
			return os.ErrPermission
		}
		if flags.Size {
			if e = file.Truncate(int64(attrs.Size)); e != nil {
				return e
			}
		}
		if flags.Permissions {
			return file.Chmod(os.FileMode(attrs.Mode) & 0777)
		}
		return nil
	default:
		return os.ErrPermission
	}
}
func (f *exportFS) PosixRename(r *sftp.Request) error {
	if f.readonly {
		return os.ErrPermission
	}
	p, e := virtualPath(r.Filepath)
	if e != nil || p == "." {
		return os.ErrPermission
	}
	target, e := virtualPath(r.Target)
	if e != nil || target == "." {
		return os.ErrPermission
	}
	return f.root.Rename(p, target)
}

type fileList []os.FileInfo

func (l fileList) ListAt(out []os.FileInfo, offset int64) (int, error) {
	if offset < 0 {
		return 0, os.ErrInvalid
	}
	if offset >= int64(len(l)) {
		return 0, io.EOF
	}
	n := copy(out, l[offset:])
	if n < len(out) {
		return n, io.EOF
	}
	return n, nil
}
func (f *exportFS) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	p, e := virtualPath(r.Filepath)
	if e != nil {
		return nil, e
	}
	switch r.Method {
	case "List":
		dir, e := f.root.Open(p)
		if e != nil {
			return nil, e
		}
		defer dir.Close()
		entries, e := dir.Readdir(-1)
		if e != nil {
			return nil, e
		}
		filtered := fileList{}
		for _, entry := range entries {
			if _, e := virtualPath(path.Join(p, entry.Name())); e == nil {
				filtered = append(filtered, entry)
			}
		}
		return filtered, nil
	case "Stat":
		info, e := f.root.Stat(p)
		if e != nil {
			return nil, e
		}
		return fileList{info}, nil
	case "Readlink":
		return nil, os.ErrPermission
	default:
		return nil, os.ErrInvalid
	}
}
func (f *exportFS) Lstat(r *sftp.Request) (sftp.ListerAt, error) {
	p, e := virtualPath(r.Filepath)
	if e != nil {
		return nil, e
	}
	info, e := f.root.Lstat(p)
	if e != nil {
		return nil, e
	}
	return fileList{info}, nil
}
func (f *exportFS) RealPath(p string) (string, error) {
	r, e := virtualPath(p)
	if e != nil {
		return "", e
	}
	if r == "." {
		return "/", nil
	}
	return "/" + r, nil
}
func (f *exportFS) Readlink(string) (string, error) { return "", os.ErrPermission }
func serveExport(root string, readonly bool, stream io.ReadWriteCloser) error {
	r, e := os.OpenRoot(root)
	if e != nil {
		return e
	}
	defer r.Close()
	f := &exportFS{root: r, readonly: readonly}
	s := sftp.NewRequestServer(stream, sftp.Handlers{FileGet: f, FilePut: f, FileCmd: f, FileList: f}, sftp.WithRSMaxTxPacket(64*1024))
	defer s.Close()
	e = s.Serve()
	if errors.Is(e, io.EOF) {
		return nil
	}
	return e
}
