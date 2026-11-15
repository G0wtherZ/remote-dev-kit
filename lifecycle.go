package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// Only our marked helper is replaced. No directory tree, user or agent is deleted.
func uploadRemoteHelper(c Config) error {
	dest := c.BootstrapBinary
	if dest == "" {
		dest = c.RemoteBinary
	}
	if !strings.HasPrefix(dest, "/") || filepath.Base(dest) != "rdev" || strings.ContainsAny(dest, "\r\n\x00") {
		return errors.New("助手路径无效，拒绝覆盖非 rdev 程序")
	}
	bin, e := asset("rdev", c.RemoteOS+"-"+c.RemoteArch, "rdev")
	if e != nil {
		return e
	}
	payload, e := os.ReadFile(bin)
	if e != nil {
		return e
	}
	var compressed bytes.Buffer
	z := gzip.NewWriter(&compressed)
	if _, e = z.Write(payload); e != nil {
		return e
	}
	if e = z.Close(); e != nil {
		return e
	}
	dir := filepath.ToSlash(filepath.Dir(dest))
	marker := dir + "/.rdev-install"
	owner := "remote-dev-kit\n" + c.Name + "\n" + dest + "\n"
	legacy := legacyHelperDigests[c.RemoteOS+"-"+c.RemoteArch]
	if legacy == "" {
		legacy = "no-known-legacy-helper"
	}
	script := "set -eu\numask 077\nmkdir -p " + shellQuote(dir) + "\n" +
		"test ! -L " + shellQuote(dest) + " && test ! -L " + shellQuote(marker) + "\n" +
		"if test -e " + shellQuote(dest) + " || test -e " + shellQuote(marker) + "; then\n" +
		"if test -f " + shellQuote(marker) + "; then\n" +
		"printf %s " + shellQuote(owner) + " | cmp -s - " + shellQuote(marker) + " || { echo '助手安装标记不匹配，未覆盖' >&2; exit 1; }\n" +
		"else\nif command -v sha256sum >/dev/null; then old=$(sha256sum " + shellQuote(dest) + "); else old=$(shasum -a 256 " + shellQuote(dest) + "); fi\n" +
		"test \"${old%% *}\" = " + shellQuote(legacy) + " || { echo '助手缺少匹配标记且不是已验证的旧版，未覆盖；保留所有数据' >&2; exit 1; }\nfi\nfi\n" +
		"tmp=$(mktemp " + shellQuote(dir+"/.rdev-upload.XXXXXXXX") + ")\nmeta=$(mktemp " + shellQuote(dir+"/.rdev-meta.XXXXXXXX") + ")\n" +
		"trap 'rm -f \"$tmp\" \"$meta\"' 0 1 2 15\ngzip -dc > \"$tmp\"\nchmod 700 \"$tmp\"\n" +
		"printf %s " + shellQuote(owner) + " > \"$meta\"\nmv -f \"$meta\" " + shellQuote(marker) + "\nmv -f \"$tmp\" " + shellQuote(dest) + "\n"
	fmt.Println("部署远端助手（仅替换有匹配归属标记的 rdev 文件）…")
	_, e = admin(c, []string{"sh", "-c", script}, compressed.Bytes())
	return e
}

func (a *App) bootstrapProbe(c Config) (RemoteInfo, error) {
	helper := c.BootstrapBinary
	if helper == "" {
		helper = c.RemoteBinary
	}
	b, e := admin(c, []string{helper, "remote", "probe", c.RemoteRoot}, nil)
	if e != nil {
		return RemoteInfo{}, e
	}
	return decode[RemoteInfo](b)
}

func (a *App) provisionUser(c *Config) error {
	if c.Identity == "" || c.BootstrapBinary == "" {
		return errors.New("开发账户恢复信息不完整，未创建替换身份")
	}
	if _, e := os.Stat(c.Identity); os.IsNotExist(e) {
		if _, err := os.Stat(c.Identity + ".pub"); err == nil {
			return errors.New("已有公钥但私钥缺失，未生成替换身份")
		}
		if e = interactive("ssh-keygen", []string{"-q", "-t", "ed25519", "-N", "", "-f", c.Identity, "-C", "remote-dev-kit-" + c.Name}); e != nil {
			return e
		}
	} else if e != nil {
		return e
	}
	pub, e := os.ReadFile(c.Identity + ".pub")
	if os.IsNotExist(e) {
		pub, e = capture("ssh-keygen", []string{"-y", "-f", c.Identity}, nil)
		if e == nil {
			e = atomicWrite(c.Identity+".pub", pub, 0600)
		}
	}
	if e != nil {
		return e
	}
	derived, err := capture("ssh-keygen", []string{"-y", "-f", c.Identity}, nil)
	if err != nil {
		return err
	}
	actual, want := strings.Fields(string(pub)), strings.Fields(string(derived))
	if len(actual) < 2 || len(want) < 2 || actual[0] != want[0] || actual[1] != want[1] {
		return errors.New("私钥和公钥不匹配，未覆盖身份或远端密钥")
	}
	request, _ := jsonMarshal(map[string]string{"user": "rdev", "public_key": strings.TrimSpace(string(pub)), "source_binary": c.BootstrapBinary})
	b, e := admin(*c, []string{c.BootstrapBinary, "remote", "create-user"}, request)
	if e != nil {
		return e
	}
	created, e := decode[map[string]string](b)
	if e != nil {
		return e
	}
	if created["user"] != "rdev" || !strings.HasPrefix(created["binary"], "/") || filepath.Base(created["binary"]) != "rdev" {
		return errors.New("开发账户响应无效")
	}
	c.RemoteUser = "rdev"
	c.RemoteBinary = created["binary"]
	c.UserProvisionPending = false
	return a.save(*c)
}

func (a *App) completeSetup(c Config, p *Prompter, yes bool) error {
	if c.UserProvisionPending {
		if e := a.provisionUser(&c); e != nil {
			return e
		}
	}
	c.SetupStage = "network"
	if e := a.save(c); e != nil {
		return e
	}
	if e := a.configureNetwork(&c, p, yes); e != nil {
		return e
	}
	info, e := a.bootstrapProbe(c)
	if e != nil {
		return e
	}
	if info.NetworkState != "Running" {
		fresh := info.Tools["tailscale"] == ""
		if fresh {
			if yes {
				return errors.New("远端缺少 Tailscale；运行 tools install tailscale，完成授权后再次 setup --resume")
			}
			ok, err := p.yes("远端缺少 Tailscale，安装官方客户端（需要远端管理员权限）", true)
			if err != nil {
				return err
			}
			if !ok {
				return errors.New("等待远端私网连接，可稍后 setup --resume")
			}
			helper := c.BootstrapBinary
			if helper == "" {
				helper = c.RemoteBinary
			}
			if e = adminInteractive(c, shellJoin([]string{helper, "remote", "dependencies", "tailscale"})); e != nil {
				return e
			}
		}
		if yes {
			return errors.New("远端 Tailscale 尚未登录；通过原 SSH 运行 tailscale up（普通用户加 sudo），授权后 setup --resume")
		}
		fmt.Println("请用与本机相同的 Tailscale 账户授权远端设备。")
		command := "tailscale up"
		if fresh {
			command += " --accept-dns=false --accept-routes=false --ssh=false"
		}
		if info.UID != 0 {
			command = "sudo " + command
		}
		if e = adminInteractive(c, command); e != nil {
			return e
		}
		info, e = a.bootstrapProbe(c)
		if e != nil {
			return e
		}
	}
	if info.TailscaleSSH {
		fmt.Println("远端启用了 Tailscale SSH；请确认普通 OpenSSH 的端口支持连接与转发。本工具不会更改该设置。")
	}
	c.RemoteIP = ""
	for _, ip := range info.IPs {
		if v := net.ParseIP(ip); v != nil && v.To4() != nil {
			c.RemoteIP = ip
			break
		}
	}
	if c.RemoteIP == "" {
		return errors.New("远端没有私网 IPv4 地址；完成授权后 setup --resume")
	}
	if e = a.writeSSH(c, info.HostKey); e != nil {
		return e
	}
	c.SetupStage = "dependencies"
	if e = a.save(c); e != nil {
		return e
	}
	b, e := a.remote(c, []string{"init", c.RemoteRoot}, nil)
	if e != nil {
		return e
	}
	ready, e := decode[RemoteInfo](b)
	if e != nil {
		return e
	}
	c.RemoteRoot = ready.Root
	if e = a.save(c); e != nil {
		return e
	}
	missing := ready.Tools["tmux"] == "" || c.AutoShare && ready.Tools["sshfs"] == ""
	if missing {
		if yes {
			return errors.New("连接已保存，远端缺少基础依赖；运行 tools install base 后 setup --resume（无需重新授权）")
		}
		ok, err := p.yes("安装远端基础工具 git、tmux、SSHFS", true)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("连接已保存，可稍后 tools install base 并 setup --resume")
		}
		if e = a.tools([]string{"install", "base"}); e != nil {
			return e
		}
	}
	c.SetupStage = "sharing"
	if e = a.save(c); e != nil {
		return e
	}
	if c.AutoShare {
		if e = a.share([]string{"on"}); e != nil {
			return e
		}
	}
	c.SetupStage = "ready"
	if e = a.save(c); e != nil {
		return e
	}
	fmt.Printf("设置完成。\n本机共享：%s\n远端工作：%s/work\n远端共享：%s/local\n", c.LocalRoot, c.RemoteRoot, c.RemoteRoot)
	if ready.Tools["claude"] == "" {
		fmt.Println("Claude 是可选项：可先运行 rdev shell，或稍后 rdev agent install。")
	}
	return nil
}

func (a *App) existingSetup(f *flag.FlagSet, p *Prompter, yes, plan, edit, resume bool, local, remote string, ro, noShare bool) error {
	c, e := a.load()
	if e != nil {
		return e
	}
	visited := map[string]bool{}
	f.Visit(func(v *flag.Flag) { visited[v.Name] = true })
	allowed := map[string]bool{"yes": true, "plan": true, "edit": true, "resume": true, "local-root": true, "remote-root": true, "read-only": true, "no-share": true}
	for name := range visited {
		if !allowed[name] {
			return fmt.Errorf("已有配置的 %s 不会被覆盖；更换主机或身份请使用新的 --state-dir", name)
		}
	}
	changes := visited["local-root"] || visited["remote-root"] || visited["read-only"] || visited["no-share"]
	if resume && changes {
		return errors.New("--resume 保持现有授权；调整配置请使用 --edit")
	}
	edit = edit || changes
	if !yes && !edit && !resume && !plan {
		choice, err := p.ask("已有设置：1 检查并继续；2 调整目录与共享", "1")
		if err != nil {
			return err
		}
		if choice != "1" && choice != "2" {
			return errors.New("请选择 1 或 2")
		}
		edit = choice == "2"
	}
	next := c
	if edit {
		if !yes {
			if !visited["local-root"] {
				local, e = p.ask("本机共享目录", c.LocalRoot)
				if e != nil {
					return e
				}
			}
			if !visited["remote-root"] {
				remote, e = p.ask("远端研发目录（不搬迁原文件）", c.RemoteRoot)
				if e != nil {
					return e
				}
			}
			if !visited["read-only"] {
				ro, e = p.yes("共享只读", c.Readonly)
				if e != nil {
					return e
				}
			}
			if !visited["no-share"] {
				on, err := p.yes("启用文件共享", c.AutoShare)
				if err != nil {
					return err
				}
				noShare = !on
			}
		} else {
			if !visited["local-root"] {
				local = c.LocalRoot
			}
			if !visited["remote-root"] {
				remote = c.RemoteRoot
			}
			if !visited["read-only"] {
				ro = c.Readonly
			}
			if !visited["no-share"] {
				noShare = !c.AutoShare
			}
		}
		next.LocalRoot, e = resolveCreationPath(local)
		if e != nil {
			return e
		}
		home, _ := os.UserHomeDir()
		if e = validateRoot(next.LocalRoot, home, a.State); e != nil {
			return e
		}
		next.LocalVolume, e = localVolumeIdentity(next.LocalRoot)
		if e != nil {
			return e
		}
		next.RemoteRoot = remote
		next.Readonly = ro
		next.AutoShare = !noShare
	}
	if plan {
		return output(map[string]any{"current": c, "proposed": next, "action": "继续或调整本工具配置；保留 Claude、身份与全部用户文件"})
	}
	if edit {
		if _, e = a.remote(c, []string{"probe", next.RemoteRoot}, nil); e != nil {
			return e
		}
		fmt.Printf("本机：%s → %s\n远端：%s → %s\n只读：%t；共享：%t。仅改变连接配置，不移动文件。\n", c.LocalRoot, next.LocalRoot, c.RemoteRoot, next.RemoteRoot, next.Readonly, next.AutoShare)
		if !yes {
			ok, err := p.yes("应用配置（短暂断开旧共享，活动任务可能受影响）", false)
			if err != nil {
				return err
			}
			if !ok {
				return errors.New("已取消")
			}
		}
		if e = a.share([]string{"off"}); e != nil {
			return e
		}
		if e = os.MkdirAll(next.LocalRoot, 0700); e != nil {
			return e
		}
		next.SetupStage = "network"
		if e = a.save(next); e != nil {
			return e
		}
	}
	return a.completeSetup(next, p, yes)
}

func (a *App) reset(args []string) error {
	f := flag.NewFlagSet("reset", flag.ContinueOnError)
	yes := f.Bool("yes", false, "确认重建本工具组件")
	plan := f.Bool("plan", false, "只显示计划")
	if e := f.Parse(args); e != nil {
		return e
	}
	if f.NArg() != 0 {
		return errors.New("用法：rdev reset [--plan] [--yes]")
	}
	c, e := a.load()
	if e != nil {
		return e
	}
	if c.UserProvisionPending {
		return errors.New("账户初始化尚未完成，请先 setup --resume；未重置任何身份")
	}
	fmt.Println("将重新部署远端 rdev 助手、重建本工具 SSH 配置和已授权挂载。\n保留 Claude 安装/配置/登录/历史、所有用户文件、远端终端、账户及 Tailscale 身份。\n共享会短暂断开，使用共享的活动任务可能受影响。")
	if *plan {
		return nil
	}
	if !*yes {
		p := &Prompter{bufio.NewReader(os.Stdin)}
		ok, err := p.yes("重建本工具组件", false)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("已取消")
		}
	}
	if c.NetworkMode == "managed" {
		if e = a.startNetwork(c); e != nil {
			return e
		}
	}
	if e = uploadRemoteHelper(c); e != nil {
		return e
	}
	if c.RemoteUser != "" {
		pub, err := os.ReadFile(c.Identity + ".pub")
		if err != nil {
			return err
		}
		helper := c.BootstrapBinary
		if helper == "" {
			return errors.New("缺少独立管理助手，不使用开发用户可写的助手执行管理员操作")
		}
		request, _ := jsonMarshal(map[string]string{"user": c.RemoteUser, "public_key": strings.TrimSpace(string(pub)), "source_binary": helper})
		if _, e = admin(c, []string{helper, "remote", "create-user"}, request); e != nil {
			return e
		}
	}
	info, e := a.bootstrapProbe(c)
	if e != nil {
		return e
	}
	if e = verifyPinnedKey(filepath.Join(a.State, "known_hosts"), info.HostKey); e != nil {
		return e
	}
	// A strict unmount reports busy mounts instead of killing Claude or discarding writes.
	if e = a.refreshConnection(&c); e != nil {
		return e
	}
	if e = stopManaged(a, "share"); e != nil {
		return e
	}
	if _, e = a.remote(c, []string{"unmount", c.RemoteRoot}, nil); e != nil {
		return fmt.Errorf("共享未能安全卸载，未结束任何 Claude 会话：%w", e)
	}
	if _, e = a.remote(c, []string{"init", c.RemoteRoot}, nil); e != nil {
		return e
	}
	if c.AutoShare {
		if e = a.share([]string{"on"}); e != nil {
			return e
		}
	}
	c.SetupStage = "ready"
	if e = a.save(c); e != nil {
		return e
	}
	fmt.Println("本工具环境已重建，Claude 和用户数据保持原样。")
	return a.doctor(nil)
}
