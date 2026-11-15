# Remote Dev Kit

本机作为入口，远端运行研发环境。首版提供 macOS/Windows 命令行入口、Tailscale
私网引导、专用目录的真实 SFTP/SSHFS 挂载、原生 Claude 终端，以及独立远端浏览器。
Claude Code 可预装、后装或不装；本工具不修改它的二进制、账号或对话管理界面。

这是 **0.1.0-alpha.3**：适合试用，尚不是全平台稳定版。平台构建不代表实机验收，
具体范围见 [验证记录](docs/VALIDATION.md)。首版未签名、未公证，不会自动关闭系统保护。

## 一键下载、安装并启动向导

**在本机执行**，不需要先在 VPS 上安装本项目，也不需要 Go、Python 或 Docker。
脚本自动识别 CPU 架构，下载固定版本安装包，检查 SHA-256，再启动 setup。
以下命令固定到 `v0.1.0-alpha.3`，不会静默安装后续版本。

macOS（iTerm2、Terminal 等均可）：

```sh
bash -c "$(curl -fsSL https://raw.githubusercontent.com/G0wtherZ/remote-dev-kit/v0.1.0-alpha.3/install.sh)"
```

Windows PowerShell（5.1 或 7）：

```powershell
irm https://raw.githubusercontent.com/G0wtherZ/remote-dev-kit/v0.1.0-alpha.3/install.ps1 | iex
```

这会执行本项目公开的安装脚本，可先查看 [macOS 脚本](install.sh) 或
[Windows 脚本](install.ps1)。也可从 [Release 页面](https://github.com/G0wtherZ/remote-dev-kit/releases/tag/v0.1.0-alpha.3)
手动下载、核对 `SHA256SUMS` 后解压。在解压目录执行 `./setup.sh`（Mac）或
`.\setup.ps1`（Windows）。不需要以管理员身份运行本机安装器。

若 Windows 组织策略禁止脚本，请使用完整解压包中的 `.\rdev.exe setup`，不需要修改
执行策略；后续在该目录使用 `.\rdev.exe claude` 等命令，并保留整个解压目录。

### 开始前准备

| 项目 | 要求 |
| --- | --- |
| 本机 | macOS ARM64/Intel，或 Windows x64/ARM64；可用 OpenSSH 客户端 |
| 远端 | Linux 或 macOS，ARM64/x86-64；已启用普通 OpenSSH，可通过别名或 `user@host` 连接 |
| SSH 认证 | 需可用的密钥认证；带口令的密钥先通过本机 `ssh-agent` / `ssh-add` 解锁 |
| 管理权限 | 远端安装 Tailscale、git/tmux/SSHFS 等基础包时需要 root/sudo；研发使用普通用户 |
| 网络 | 本机和远端可访问 GitHub、Tailscale；两端设备加入同一 tailnet，访问规则允许目标 SSH 端口 |
| 文件挂载 | Linux 主机/容器须允许 FUSE；远端 macOS 需要单独安装兼容 FSKit 的 FUSE/SSHFS |
| Claude | 可预先安装，或之后可选安装；使用者自行准备可用账号并遵守服务条款 |

Windows 可通过系统「可选功能」安装 OpenSSH Client。建议先验证普通 SSH 的密钥连接：

```sh
ssh dev@example.com
# 自定义端口的例子：ssh -p 2222 dev@example.com
```

向导也能接受已有的 SSH 别名。不需要提供或粘贴 SSH 私钥；SSH 自行读取本机配置，
首次连接需要使用者核对主机指纹。后台连接使用密钥认证，不支持只依赖交互密码的连接。

### 向导会做什么

1. 询问 SSH 连接、本机共享目录、远端研发目录，以及是否复用已有 Tailscale。
2. 校验首次 SSH 连接，识别远端平台并上传对应助手。远端 Linux 若使用 root 连接，
   会提供创建独立、无 sudo 的 `rdev` 开发用户的选项。
3. 引导两端在 Tailscale 官方网页登录、注册或授权设备；**两端使用同一个账户/组织**。
   已在运行的远端 Tailscale 会复用，未安装时询问是否安装官方客户端。
4. 生成本工具自己的 SSH 配置，创建远端 `work` 和 `local`，需要时询问安装基础依赖并挂载。
5. Claude 安装与账号登录由使用者选择；不在 setup 中自动登录或调用模型。

一条命令负责启动引导，主机指纹、账户授权、sudo 和 macOS 系统扩展批准仍需要本人操作。
远端 macOS 的 Tailscale、Homebrew 和 FUSE 安装可能需要先手动完成，再继续设置。

向导询问 SSH 地址，以及两端各自的研发目录。**目录名、位置可自定义，建议选用户主目录
下的专用子目录**，默认 `~/RemoteDev`。这里的根目录指用户 Home 下的专用目录，
不是系统 `/`，也不是把整个 Home 暴露出去。

已有 Tailscale 账户可复用；新用户在官方网页注册。两端设备由用户本人在浏览器授权。
朋友分别使用自己的账户/组织，不随安装包加入作者的网络。
本机默认使用用户态私网，只给指定连接提供回环 SOCKS 入口，不接管系统 DNS、
互联网出口或现有 VPN。远端使用官方 Tailscale；如果已经配置好则复用，不重置其配置。

远端 SSH 是 root 时，交互向导提供创建不带 sudo 的 Linux 开发用户。已有同名非本工具
账户不会被覆盖。远端 macOS 请提供普通用户的 SSH 连接。

已有配置时再次运行 `rdev setup`，可选择继续设置或调整目录与共享；也可以直接：

```sh
rdev setup --edit
```

改变共享目录前会显示旧、新路径和权限，并询问是否应用。只修改连接配置，不搬迁文件；
更换远端主机或账户使用新的 `--state-dir`，不会把旧身份直接套用到另一台机器。

设置中断后：

```sh
rdev setup --resume
rdev tools install base    # 若上次中断在挂载依赖安装
rdev share on             # 开启共享并验证挂载
rdev doctor
```

若本机窗口尚未识别 `rdev`，重新打开终端。Mac 也可用
`~/.local/bin/rdev`，Windows 安装目录为 `%LOCALAPPDATA%\RemoteDevKit\app`。
`--resume` 会继续账户初始化、网络授权、基础依赖和挂载步骤，复用原设备身份。
远端 Tailscale 授权仍需本人完成；`--yes` 不会跳过授权。

需要手动指定配置时：

```sh
rdev setup --local-root "~/Research Desk" --remote-root "~/Cloud Workspace"
```

## 第一次使用

setup 完成后，先检查连接，再决定是否安装 Claude：

```sh
rdev doctor
rdev shell                 # 先体验纯远端 Shell，无需 Claude
rdev agent install         # 可选：在远端普通用户下安装官方 Claude Code
rdev agent login           # 可选：进入原生客户端，由本人完成 /login
rdev claude                # 启动远端 Claude
```

已有 Claude 可跳过安装；程序会查找远端 PATH 和 `~/.local/bin`。需要自定义 PATH 时先在
远端准备好环境。Mac 安装目录为 `~/.local/share/remote-dev-kit/app`；运行状态单独放在
`~/.rdev`，不会写入共享目录。

## 日常使用

```sh
rdev claude                         # 新启动原生远端 Claude
rdev claude --resume                # 原生历史对话界面
rdev claude --worktree feature-a    # 原生独立工作区
rdev reconnect                     # 接回仍在运行的远端终端
rdev shell                         # 不依赖 Claude 的远端 Shell
rdev doctor
rdev doctor --network               # 额外查看 Tailscale 路径
rdev doctor --json                  # 给脚本使用的结构化结果
```

每次启动新建远端持久终端；多任务、对话、权限交互交给 Claude 原生界面。
关闭本机终端通常只断开显示，远端 tmux 继续运行。远端重启不是 tmux 的进程恢复机制。

工作目录为 `<远端研发根目录>/work`。已授权本机路径挂载在
`<远端研发根目录>/local`；挂载就绪后，启动器把它加入 Claude 的附加目录范围。
真实 Claude 的 `@` 补全、账号登录和模型行为仍需要用户自己的客户端验收。

```sh
rdev share on
rdev share status
rdev share off
```

**这是实时挂载，不是同步镜像。远端修改、删除会影响本机专用目录中的原文件。**
只读方式可在 setup 使用 `--read-only`。本机只运行受限 SFTP 服务，不要求打开本机 SSH、
公网文件端口或整个磁盘。SFTP 虚拟根与专用目录绑定，禁止越界链接和凭据目录访问。
本机断线时共享不可用；重新连接会沿用原授权，关闭共享后不会自动重开。
就绪检查会实际读取目录，避免把残留挂载误报为可用；退出时清理本工具的挂载。
共享异常时，启动 Claude 会询问是否不等待共享恢复、继续远端工作，已有授权保持不变。
macOS `/Volumes/` 下的外接盘还会绑定卷 UUID：拔盘或同名路径换盘时拒绝自动共享，
不会在原挂载位置创建一个空目录冒充磁盘。重新格式化后需要通过 `setup --edit` 重新确认。

文件路径用法普通，但性能、锁、元数据和断线语义不等同本地磁盘。代码依赖、
数据库和频繁构建放在远端 `work`。共享目录中的 symlink 和 Unix 元数据操作有限制。

## 浏览器和端口

```sh
rdev preview 3000       # 显示远端开发服务的本机转发地址，Ctrl+C 结束
rdev expose --minutes 30 8080  # 临时授权远端访问本机 127.0.0.1:8080
rdev browser            # 独立远端 Chrome 配置；本机查看画面、导航、点击和输入
rdev browser mcp        # stdio 接入官方 Chrome DevTools MCP，连接同一开发浏览器
```

浏览器运行在远端，网页访问用远端出口。本机仅访问经过 SSH 转发的回环查看器。
查看器需要本次会话凭据并校验 Host/Origin；Chrome CDP 不公开到互联网。
不会复制个人 Chrome 配置，也不会用 `--no-sandbox` 运行 Chrome。
查看器目前是轻量调试入口，支持截图、导航、点击、文字、常用按键；不是完整远程桌面。
截图包含所浏览页面的内容，使用独立开发账号/资料。

将 `rdev browser mcp` 作为本机 MCP 客户端的 stdio 命令即可转到远端浏览器。
默认状态路径时示例配置：

```json
{"mcpServers":{"remote-dev-browser":{"command":"rdev","args":["browser","mcp"]}}}
```

浏览器自身先在另一个本机终端运行 `rdev browser`，保持该窗口打开。
上述 JSON 面向本机 MCP 客户端。远端 Claude 的 MCP 命令应在远端执行，例如
`<远端助手绝对路径>/rdev remote browser-mcp <远端研发根目录绝对路径>`；
助手路径可在本机私有 `~/.rdev/config.json` 的 `remote_binary` 中查看。
不要把本机的 stdio 配置原样复制给远端客户端。
官方 MCP 包独立安装，版本固定，不是本工具重新实现浏览器自动化协议。

`preview` 把远端端口带到本机；`expose` 方向相反。后者每次询问授权，默认 60 分钟，
Ctrl+C 可提前撤销。远端入口只绑定回环地址，通过专用 SSH Unix socket 转发，
不依赖把 VPS 的 `GatewayPorts` 改为公开监听。授权的是远端主机上的进程访问能力，
不是仅限 Claude；本机服务仍应有自己的认证。不要用它暴露任意命令执行服务。
USB 不是 TCP 端口：串口、烧录器等需要专用适配器，本版没有把 USB 设备自动透传。

浏览器调试前，按需准备远端运行时：

```sh
rdev tools install node
rdev tools install browser
rdev browser
```

Linux Chrome 需要系统库和正常沙箱环境；缺少库时按报错通过远端包管理器安装。
本版没有完整桌面、音视频通话、文件上传控件或个人浏览器映射。

## 可选工具

```sh
rdev tools install base       # git、tmux、SSHFS 等；使用管理员连接安装
rdev tools install node       # 普通用户目录中的官方 Node LTS，校验 SHA-256
rdev tools install browser
rdev agent install            # 普通用户下执行官方 Claude 安装流程
rdev agent login              # 打开原生 Claude，由用户本人完成 /login
```

基础包管理支持 apt、dnf/yum、pacman、apk；不会运行系统 upgrade 或修改网站配置。
包源未提供某依赖时会报告错误，不自动启用未知仓库。
远端 macOS 的 FUSE/SSHFS 需要兼容 FSKit 的安装；不会为了挂载默认降低系统安全设置。
远端 macOS 首次 Tailscale 安装需要按官方流程由设备所有者批准系统扩展。

## 检查、修复与安全重置

```sh
rdev doctor                # 检查就绪项，给出下一步；不安装、登录或改变权限
rdev repair                # 修复本工具连接，必要时安全卸载不可读的残留挂载
rdev reset --plan          # 查看重置范围，不修改环境
rdev reset                 # 确认后重新部署助手、连接及原先启用的共享
```

**reset 只重建 Remote Dev Kit 的组件，绝不格式化磁盘。** 不删除或重新安装 Claude，
不更改其配置、登录、对话历史，不结束远端 tmux/Claude 会话；保留代码、依赖、共享文件、
浏览器资料、账户与 SSH/Tailscale 身份。共享会短暂断开，正在使用共享的任务可能受影响。
无法安全卸载时报告错误，不强制结束开发任务。

助手缺失或损坏时，仅替换有匹配安装标记的 `rdev` 文件；对 alpha.1 的兼容只接受与已发布
助手 SHA-256 完全一致的旧文件。归属不明的程序不覆盖。主机密钥变化不会被 repair/reset
自动接受。设备和数据写入操作不自动重放；瞬时私网错误只对查询做一次有界重试，仍走原私网。

## 状态与卸载

```sh
rdev network status
rdev network login
rdev network stop
rdev uninstall
```

程序状态位于本机 `~/.rdev`，与共享路径分离。多主机可使用
`rdev --state-dir <另一个私有目录> setup`。macOS 用用户 LaunchAgent，Windows 用用户
登录任务管理后台进程。目录或二进制搬迁后应先停止本工具服务再重新安装。

卸载停止本工具的服务和挂载，保留共享数据、远端账号及私有身份状态。
设备注销由用户在自己的 Tailscale 管理页操作；不自动删除账号或网站服务。
同一安装包再次运行会进入已有配置向导。不同版本不会覆盖运行中的程序：升级时先
`rdev uninstall` 停止旧版服务，保留旧目录和 `~/.rdev` 作为备份，将完整新包放在长期路径，
用新包的 `rdev setup --resume` 继续，再把命令入口指向新包。运行服务期间不要移动或删除程序目录。
本版尚未提供自动升级和回退。

## 常见问题

- **现有 VPN/OpenVPN 会冲突吗？** 默认本机用户态网络不创建系统 VPN 接口、不接管系统 DNS
  或默认路由，只代理本工具的连接。它仍依赖底层网络能连通 Tailscale 控制/中继/对等节点，
  不能保证任意防火墙和 VPN 策略下都能连接。选择复用现有 Tailscale 时沿用其现行设置。
- **SSH 连得上，后台却认证失败？** 先确认 `ssh -o BatchMode=yes <别名或地址> true` 成功；
  加密私钥需解锁到 agent。私网 SSH 端口、tailnet 访问规则及远端防火墙也须允许连接。
- **提示 FUSE 不可用？** 受限容器可能没有 `/dev/fuse`，应换到允许 FUSE 的主机，或以
  `--no-share` 先使用远端研发环境。远端 macOS 的 FUSE/SSHFS 目前需要人工配置并实机验收。
- **启用了 Tailscale SSH？** 它可能接管 tailnet 的 22 端口。本工具需要支持转发的普通
  OpenSSH，可让其使用独立端口，并在 setup 的 `--ssh-port` 中指定。
- **升级或换电脑？** Alpha 不做覆盖升级、不复制旧设备身份。先 `rdev uninstall` 停止服务，
  保留数据并备份私有状态后手动处理安装目录。第二台电脑应使用已获授权的普通 SSH 用户；
  不会静默向已有 `rdev` 用户追加另一把设备密钥。
- **能保证匿名、固定公网出口或账号不受限制？** 不能。模型请求在远端执行并使用远端的
  实际出口；tailnet 地址不等于匿名。账号、地区与网络使用仍由使用者自行决定。

## 隐私与发布内容

公开仓库只包含源代码、通用示例、文档和安装脚本。Release 只包含程序、第三方许可和校验清单。
本机私钥、Tailscale 节点身份、SSH 配置、个人文件、浏览器资料与原始测试日志不随包发布。
维护者的本地 `evidence/` 和 `dist/` 均不进入 Git；公开的验证摘要见 [验证记录](docs/VALIDATION.md)。

安装后，连接地址、目录、主机公钥及可能生成的设备私钥存于**使用者本机** `~/.rdev`；
不要上传该目录。共享内容会进入自己的远端机器，若在对话中引用，还会按客户端行为发送给模型服务。
远端账户仍能操作其可访问的系统资源，本工具不是容器或完整安全沙箱。

## 从源码构建

开发需要 Go 1.25+、Python 3（只用于打包）：

```sh
go test -race ./...
go vet ./...
python3 scripts/build.py --target darwin-arm64 --target windows-amd64
```

安装包内含编译好的本机 CLI、远端助手与独立 Tailscale CLI，不要求朋友安装 Go。
构建固定 Tailscale v1.104.7，使用 Go module checksum 验证依赖，并用 `-trimpath`
去除编译机器路径。归档采用文件白名单，不包含研发目录、私钥、配置、节点状态或日志。

`scripts/build.py --app-only` 仅交叉构建助手。开发时可用 `RDEV_ASSETS=<dist/assets>`
指定资源目录；发布包无需此环境变量。

## 当前边界

- macOS/Windows 本机与 Linux/macOS 远端均有代码适配和平台构建。
- 已完成 macOS ARM64 到 Ubuntu VPS 的私网 SSH、真实 FUSE 文件读写、共享启停和回环端口验证；浏览器及官方 MCP 通过本机独立 Chrome 验证。
- 普通 Linux 用户创建、SSH 密钥登录和原生参数经隔离 Ubuntu 容器验证；未使用真实 Claude 账号或调用模型。
- Windows、远端 macOS、各 Linux 发行版需要各自实机验收；不能用交叉编译代替。
- 没有自动烧录、JTAG/SWD、Xcode 或本机个人浏览器控制。硬件适配在后续明确授权后增加。
- 没有承诺匿名访问、隐藏所有元数据或避免平台账号限制。
- 协作文件和模型请求仍按用户选择发送到远端/服务商；挂载不改变模型的上下文消耗。
