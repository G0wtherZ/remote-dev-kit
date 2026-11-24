package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"
	"time"
)

func bDeviceClient(ctx context.Context, id string) (*ssh.Client, error) {
	if !bIDPattern.MatchString(id) {
		return nil, errors.New("设备标识不合法")
	}
	dir := "/data/devices/" + id
	var d bDevice
	b, e := os.ReadFile(dir + "/device.json")
	if e != nil {
		return nil, e
	}
	if e = json.Unmarshal(b, &d); e != nil {
		return nil, e
	}
	pub, _, _, _, e := ssh.ParseAuthorizedKey([]byte(d.HostKey))
	if e != nil {
		return nil, e
	}
	key, e := os.ReadFile(dir + "/client-key")
	if e != nil {
		return nil, e
	}
	signer, e := ssh.ParsePrivateKey(key)
	if e != nil {
		return nil, e
	}
	raw, e := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", "/data/devices/"+id+"/stream.sock")
	if e != nil {
		return nil, errors.New("本机设备离线；请在对应电脑运行 plan-b on")
	}
	raw.SetDeadline(time.Now().Add(15 * time.Second))
	conn, channels, requests, e := ssh.NewClientConn(&bIdleConn{Conn: raw}, "rdev-device", &ssh.ClientConfig{User: "rdev-device", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: ssh.FixedHostKey(pub), Timeout: 10 * time.Second})
	if e != nil {
		raw.Close()
		return nil, e
	}
	raw.SetDeadline(time.Time{})
	client := ssh.NewClient(conn, channels, requests)
	context.AfterFunc(ctx, func() { client.Close() })
	return client, nil
}

type bFileInfo struct {
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	Directory bool      `json:"directory"`
	Modified  time.Time `json:"modified"`
}

func (w *bWeb) files(out http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("device")
	directory := bSFTPPath(r.URL.Query().Get("path"), runtimeDevicePlatform(id))
	if directory == "" || strings.ContainsRune(directory, 0) {
		bWebError(out, errors.New("请填写本机文件路径"))
		return
	}

	client, e := bDeviceClient(r.Context(), id)
	if e != nil {
		bWebError(out, e)
		return
	}
	defer client.Close()
	files, e := sftp.NewClient(client)
	if e != nil {
		bWebError(out, e)
		return
	}
	defer files.Close()
	switch r.Method {
	case "GET":
		if r.URL.Query().Get("download") == "1" {
			f, e := files.Open(directory)
			if e != nil {
				bWebError(out, e)
				return
			}
			defer f.Close()
			info, e := f.Stat()
			if e != nil || !info.Mode().IsRegular() {
				bWebError(out, errors.New("只能下载普通文件"))
				return
			}
			if info.Size() > 256*1024*1024 {
				bWebError(out, errors.New("浏览器单文件限 256 MiB；更大的文件请使用 SFTP/scp"))
				return
			}
			out.Header().Set("Content-Type", "application/octet-stream")
			out.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(directory)}))
			io.Copy(out, f)
			return
		}
		entries, e := files.ReadDir(directory)
		if e != nil {
			bWebError(out, e)
			return
		}
		if len(entries) > 2000 {
			entries = entries[:2000]
		}
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].IsDir() != entries[j].IsDir() {
				return entries[i].IsDir()
			}
			return entries[i].Name() < entries[j].Name()
		})
		result := []bFileInfo{}
		for _, entry := range entries {
			result = append(result, bFileInfo{Name: entry.Name(), Size: entry.Size(), Directory: entry.IsDir(), Modified: entry.ModTime()})
		}
		bJSON(out, result)
	case "POST":
		r.Body = http.MaxBytesReader(out, r.Body, 256*1024*1024)
		if e := r.ParseMultipartForm(8 * 1024 * 1024); e != nil {
			bWebError(out, e)
			return
		}
		if r.MultipartForm != nil {
			defer r.MultipartForm.RemoveAll()
		}
		input, header, e := r.FormFile("file")
		if e != nil {
			bWebError(out, e)
			return
		}
		defer input.Close()
		name := path.Base(strings.ReplaceAll(header.Filename, "\\", "/"))
		if name == "." || name == "/" || strings.ContainsRune(name, 0) {
			bWebError(out, errors.New("文件名不合法"))
			return
		}
		destination := strings.TrimRight(directory, "/") + "/" + name
		if _, e = files.Stat(destination); e == nil {
			bWebError(out, errors.New("同名文件已存在；请先重命名，避免覆盖"))
			return
		}
		f, e := files.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY)
		if e != nil {
			bWebError(out, e)
			return
		}
		_, e = io.Copy(f, input)
		closeErr := f.Close()
		if e != nil {
			bWebError(out, e)
			return
		}
		if closeErr != nil {
			bWebError(out, closeErr)
			return
		}
		bJSON(out, map[string]string{"uploaded": destination})
	default:
		http.Error(out, "method", 405)
	}
}

// An idle network fails; an actively progressing large transfer is not cut by an absolute deadline.
type bIdleConn struct{ net.Conn }

func (c *bIdleConn) Read(b []byte) (int, error) {
	c.SetReadDeadline(time.Now().Add(30 * time.Second))
	return c.Conn.Read(b)
}
func (c *bIdleConn) Write(b []byte) (int, error) {
	c.SetWriteDeadline(time.Now().Add(30 * time.Second))
	return c.Conn.Write(b)
}

func bSFTPPath(value, platform string) string {
	if platform != "windows" {
		return value
	}
	value = strings.ReplaceAll(value, "\\", "/")
	if len(value) >= 2 && value[1] == ':' {
		if len(value) == 2 {
			value += "/"
		}
		return "/" + value
	}
	return value
}
