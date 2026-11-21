package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type App struct {
	State       string
	helperCache *helperSnapshot
}
type Config struct {
	Version              int    `json:"version"`
	SetupStage           string `json:"setup_stage,omitempty"`
	UserProvisionPending bool   `json:"user_provision_pending,omitempty"`
	Name                 string `json:"name"`
	AdminSSH             string `json:"admin_ssh"`
	AdminPort            int    `json:"admin_port,omitempty"`
	LocalRoot            string `json:"local_root"`
	LocalVolume          string `json:"local_volume,omitempty"`
	RemoteRoot           string `json:"remote_root"`
	RemoteBinary         string `json:"remote_binary"`
	BootstrapBinary      string `json:"bootstrap_binary"`
	RemoteOS             string `json:"remote_os"`
	RemoteArch           string `json:"remote_arch"`
	RemoteIP             string `json:"remote_ip"`
	RemoteUser           string `json:"remote_user,omitempty"`
	Identity             string `json:"identity,omitempty"`
	NetworkMode          string `json:"network_mode"`
	Tailscale            string `json:"tailscale"`
	Tailscaled           string `json:"tailscaled"`
	Socket               string `json:"socket"`
	SOCKS                string `json:"socks"`
	Readonly             bool   `json:"readonly"`
	AutoShare            bool   `json:"auto_share"`
}

func (a *App) load() (Config, error) {
	var c Config
	if b, e := os.ReadFile(filepath.Join(a.State, ".rdev-owner")); e != nil || string(b) != "remote-dev-kit\n" {
		return c, errors.New("私有目录没有本工具的管理标记；不会导入其他程序的身份数据")
	}
	b, e := os.ReadFile(filepath.Join(a.State, "config.json"))
	if e != nil {
		return c, fmt.Errorf("尚未完成 setup：%w", e)
	}
	e = json.Unmarshal(b, &c)
	if e != nil {
		return c, e
	}
	if c.Version != 1 {
		return c, errors.New("不支持的配置版本")
	}
	return c, nil
}
func (a *App) save(c Config) error {
	if err := a.prepareState(); err != nil {
		return err
	}
	if err := os.Chmod(a.State, 0700); err != nil {
		return err
	}
	b, e := json.MarshalIndent(c, "", "  ")
	if e != nil {
		return e
	}
	return atomicWrite(filepath.Join(a.State, "config.json"), append(b, '\n'), 0600)
}
func (a *App) prepareState() error {
	home, _ := os.UserHomeDir()
	p, e := resolveCreationPath(a.State)
	if e != nil {
		return e
	}
	a.State = p
	for _, forbidden := range []string{home, string(os.PathSeparator), "/tmp", "/private/tmp", "/var/tmp", "/etc", "/usr", "/Users", "/home"} {
		if samePath(p, forbidden) {
			return errors.New("私有配置必须使用独立子目录")
		}
	}
	if entries, e := os.ReadDir(p); e == nil && len(entries) > 0 {
		b, e := os.ReadFile(filepath.Join(p, ".rdev-owner"))
		if e != nil || string(b) != "remote-dev-kit\n" {
			return errors.New("私有配置路径已有其他文件，不会覆盖或修改其权限")
		}
	}
	if e = os.MkdirAll(p, 0700); e != nil {
		return e
	}
	if e = secureDirectory(p); e != nil {
		return e
	}
	return atomicWrite(filepath.Join(p, ".rdev-owner"), []byte("remote-dev-kit\n"), 0600)
}
func resolveCreationPath(value string) (string, error) {
	abs, e := expandHome(value)
	if e != nil {
		return "", e
	}
	candidate := abs
	var tail []string
	for {
		resolved, err := filepath.EvalSymlinks(candidate)
		if err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, tail[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			return "", err
		}
		tail = append(tail, filepath.Base(candidate))
		candidate = parent
	}
}
func atomicWrite(name string, data []byte, mode os.FileMode) error {
	f, e := os.CreateTemp(filepath.Dir(name), ".rdev-*")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if e = f.Chmod(mode); e == nil {
		_, e = f.Write(data)
	}
	if ce := f.Close(); e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	return os.Rename(tmp, name)
}
func expandHome(s string) (string, error) {
	h, e := os.UserHomeDir()
	if e != nil {
		return "", e
	}
	if s == "~" {
		s = h
	} else if strings.HasPrefix(s, "~/") || strings.HasPrefix(s, `~\`) {
		s = filepath.Join(h, s[2:])
	}
	return filepath.Abs(s)
}
func validateRoot(root, home, state string) error {
	if strings.ContainsAny(root+state, "\r\n\x00") {
		return errors.New("路径不能包含换行或 NUL")
	}
	r, e := resolveCreationPath(root)
	if e != nil {
		return e
	}
	h, err := resolveCreationPath(home)
	if err != nil {
		return err
	}
	s, err := resolveCreationPath(state)
	if err != nil {
		return err
	}
	if r == filepath.VolumeName(r)+string(os.PathSeparator) || samePath(r, h) {
		return errors.New("请选择主目录下的专用子目录，不共享整个 Home 或系统根目录")
	}
	if overlaps(r, s) {
		return errors.New("共享目录不能包含或位于本工具的私有配置目录")
	}
	for _, p := range []string{".ssh", ".aws", ".gnupg", ".azure"} {
		if overlaps(r, filepath.Join(h, p)) {
			return errors.New("共享路径与个人凭据目录重叠")
		}
	}
	return nil
}
func samePath(a, b string) bool { return strings.EqualFold(filepath.Clean(a), filepath.Clean(b)) }
func overlaps(a, b string) bool {
	r, e := filepath.Rel(a, b)
	return e == nil && (r == "." || r != ".." && !strings.HasPrefix(r, ".."+string(os.PathSeparator))) || func() bool {
		r, e := filepath.Rel(b, a)
		return e == nil && (r == "." || r != ".." && !strings.HasPrefix(r, ".."+string(os.PathSeparator)))
	}()
}

var nameRE = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,31}$`)

func validDestination(s string) bool {
	return s != "" && !strings.HasPrefix(s, "-") && !strings.ContainsAny(s, "\r\n\x00 \t")
}
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func shellJoin(ss []string) string {
	r := make([]string, len(ss))
	for i, s := range ss {
		r[i] = shellQuote(s)
	}
	return strings.Join(r, " ")
}
func sshQuote(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}
func freePort() (int, error) {
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		return 0, e
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

type Prompter struct{ Reader *bufio.Reader }

func (p *Prompter) ask(label, def string) (string, error) {
	fmt.Printf("%s [%s]: ", label, def)
	s, e := p.Reader.ReadString('\n')
	if e != nil && s == "" {
		return "", errors.New("交互输入已结束；可用 setup 参数指定配置")
	}
	s = strings.TrimSpace(s)
	if s == "" {
		s = def
	}
	return s, nil
}
func (p *Prompter) yes(label string, def bool) (bool, error) {
	d := "n"
	if def {
		d = "y"
	}
	s, e := p.ask(label+" (y/n)", d)
	if e != nil {
		return false, e
	}
	return strings.EqualFold(s, "y") || strings.EqualFold(s, "yes"), nil
}
