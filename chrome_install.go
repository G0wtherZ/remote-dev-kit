package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

func extractZip(data []byte, directory string) error {
	z, e := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if e != nil {
		return e
	}
	root, e := os.OpenRoot(directory)
	if e != nil {
		return e
	}
	defer root.Close()
	var total uint64
	for _, entry := range z.File {
		parts := strings.SplitN(entry.Name, "/", 2)
		if len(parts) != 2 || parts[1] == "" {
			continue
		}
		p := parts[1]
		clean := path.Clean(p)
		if path.IsAbs(p) || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(p, `\`) {
			return errors.New("archive path escapes installation directory")
		}
		total += entry.UncompressedSize64
		if total > 1024*1024*1024 {
			return errors.New("browser archive exceeds extraction budget")
		}
		if entry.FileInfo().IsDir() {
			if e = root.MkdirAll(p, 0755); e != nil {
				return e
			}
			continue
		}
		if e = root.MkdirAll(path.Dir(p), 0755); e != nil {
			return e
		}
		reader, e := entry.Open()
		if e != nil {
			return e
		}
		if entry.Mode()&os.ModeSymlink != 0 {
			b, e := io.ReadAll(io.LimitReader(reader, 4096))
			reader.Close()
			if e != nil {
				return e
			}
			link := string(b)
			resolved := path.Clean(path.Join(path.Dir(p), link))
			if path.IsAbs(link) || resolved == ".." || strings.HasPrefix(resolved, "../") {
				return errors.New("browser archive symlink escapes installation directory")
			}
			if e = root.Symlink(link, p); e != nil {
				return e
			}
			continue
		}
		if !entry.Mode().IsRegular() {
			reader.Close()
			return errors.New("unsupported browser archive entry")
		}
		file, e := root.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, entry.Mode().Perm())
		if e != nil {
			reader.Close()
			return e
		}
		_, e = io.Copy(file, reader)
		reader.Close()
		file.Close()
		if e != nil {
			return e
		}
	}
	return nil
}
func installBrowser() error {
	if os.Getuid() == 0 {
		return errors.New("浏览器应安装到普通开发用户；不在 root 下运行或关闭沙箱")
	}
	platform := ""
	switch runtime.GOOS + "-" + runtime.GOARCH {
	case "linux-amd64":
		platform = "linux64"
	case "linux-arm64":
		platform = "linux-arm64"
	case "darwin-amd64":
		platform = "mac-x64"
	case "darwin-arm64":
		platform = "mac-arm64"
	default:
		return errors.New("此远端浏览器平台未支持")
	}
	b, e := fetch("https://googlechromelabs.github.io/chrome-for-testing/last-known-good-versions-with-downloads.json", 2*1024*1024)
	if e != nil {
		return e
	}
	var index struct {
		Channels map[string]struct {
			Version   string `json:"version"`
			Downloads struct {
				Chrome []struct {
					Platform string `json:"platform"`
					URL      string `json:"url"`
				} `json:"chrome"`
			} `json:"downloads"`
		} `json:"channels"`
	}
	if e = json.Unmarshal(b, &index); e != nil {
		return e
	}
	release := index.Channels["Stable"]
	address := ""
	for _, d := range release.Downloads.Chrome {
		if d.Platform == platform {
			address = d.URL
			break
		}
	}
	u, e := url.Parse(address)
	if e != nil || u.Scheme != "https" || u.Host != "storage.googleapis.com" || !strings.HasPrefix(u.Path, "/chrome-for-testing-public/") {
		return errors.New("未找到匹配平台的官方 Chrome for Testing 下载")
	}
	fmt.Println("下载独立开发浏览器 Chrome for Testing", release.Version)
	data, e := fetch(address, 300*1024*1024)
	if e != nil {
		return e
	}
	home, _ := os.UserHomeDir()
	parent := filepath.Join(home, ".rdev", "runtime")
	if e = os.MkdirAll(parent, 0700); e != nil {
		return e
	}
	tmp, e := os.MkdirTemp(parent, "chrome-new-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(tmp)
	if e = extractZip(data, tmp); e != nil {
		return e
	}
	dest := filepath.Join(parent, "chrome")
	if _, e = os.Stat(dest); e == nil {
		return errors.New("已存在受管理浏览器；未覆盖正在使用的版本")
	}
	if e = os.Rename(tmp, dest); e != nil {
		return e
	}
	binary := ""
	filepath.WalkDir(dest, func(p string, d fs.DirEntry, e error) error {
		if e == nil && !d.IsDir() && (filepath.Base(p) == "chrome" || strings.HasSuffix(filepath.ToSlash(p), ".app/Contents/MacOS/Google Chrome for Testing")) {
			binary = p
		}
		return nil
	})
	if binary == "" {
		return errors.New("浏览器包中没有找到执行文件")
	}
	state, _ := json.Marshal(map[string]string{"version": release.Version, "path": binary})
	if e = atomicWrite(filepath.Join(parent, "chrome.json"), state, 0600); e != nil {
		return e
	}
	fmt.Println("安装完成。浏览器使用独立配置；缺少系统库时请按浏览器日志安装发行版依赖。")
	return nil
}
