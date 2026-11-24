# Plan B 使用指南

Plan B 是 Remote Dev Kit 独立实现的「远端官方 Claude + 本机执行」。参考项目仅用于
设计研究；本版本不运行、下载或依赖 cc-desk-tunnel，不安装它的 App，不使用其协议。

## 安装

本机 macOS/Windows 需要 OpenSSH 客户端。远端是可运行 Linux 容器的 Docker Engine
或 macOS Docker Desktop，SSH 用户需要 Docker 权限。不会自动授予 Docker 组权限、
安装宿主系统服务、重启网站或更改 VPN/DNS。推荐使用既有 Tailscale 私网 SSH 别名。

已有 rdev alpha.5：

```sh
rdev plan-b setup --ssh my-private-vps --name '我的 Mac'
rdev plan-b on --trust-local-user
rdev plan-b account
rdev plan-b claude
```

也可直接从完整安装包执行 `./rdev plan-b setup` 或 `rdev.exe plan-b setup`。
新安装时选择 B，可在 macOS 一次下载并进入 B 向导：

```sh
bash -c "$(curl -fsSL https://raw.githubusercontent.com/G0wtherZ/remote-dev-kit/v0.1.0-alpha.5/install.sh)" -- --plan-b --ssh my-private-vps
```

Windows PowerShell：

```powershell
& ([scriptblock]::Create((irm https://raw.githubusercontent.com/G0wtherZ/remote-dev-kit/v0.1.0-alpha.5/install.ps1))) --plan-b --ssh my-private-vps
```

下载会核对 SHA-256。旧版本安装器遇到不同版本不会覆盖；可以解压新包，直接使用
其中的新 rdev 和完整 assets，继续使用原私有配置。无需卸载或删除 Claude 数据。
默认仍安装/设置 A；只有 `--plan-b` 安装参数或 `rdev plan-b` 才进入 B。

### Claude 可独立安装

默认 B 镜像安装一份固定版本的官方 Claude。若希望自行前置/后置安装：

```sh
rdev plan-b setup --ssh my-private-vps --without-claude
rdev plan-b install-claude
rdev plan-b account
```

用户自己安装的 CLI 可放在 B 数据卷 `/data/runtime/claude/bin/claude`，其 PATH 优先于
镜像内版本。不会使用/覆盖宿主机原有 Claude，也不会读取、复制它的账号或历史。

## 日常体验

在本机项目目录里，用 iTerm2、Terminal、Windows Terminal 或 PowerShell 执行：

```sh
rdev plan-b claude                 # 新建原生 Claude 终端
rdev plan-b claude                 # 再执行一次，新建另一会话
rdev plan-b claude --resume        # 官方历史选择界面
rdev plan-b claude --model sonnet  # 参数原样传给官方 CLI
rdev plan-b sessions               # 查看本设备的活动终端
rdev plan-b reconnect              # 接回仍在运行的终端
rdev plan-b agent '检查当前项目' --output-format stream-json --verbose
rdev plan-b web                    # 浏览器接回同一批终端、查看文件与任务
```

原生终端提供 Claude 的计划模式、审批、追问、模型设置、多任务和历史恢复。
`Ctrl+B` 然后 `D` 可按 tmux 方式暂时离开，之后 reconnect 接回。浏览器关闭不会清空
原生对话。网页是本项目的轻量工具页，不是新的桌面 App 或自建聊天替代品。

开发代码、工具链、USB 驱动和硬件命令在**本机**；Harness、官方 CLI、账号与对话
在**远端 B 容器**。远端工作目录主要存放会话资料和明确暂存的文件。模型通过普通
SSH/SFTP 使用本机代码，执行位置不会因设备离线而被本工具切回服务器。

原生 CLI 的 `@` 补全仍是远端 CLI 所见目录，不能把本机路径伪装成已挂载目录。
可在对话中给出本机绝对路径，或用网页文件面板浏览、上传和下载；模型依照本机
执行上下文读取这些路径。B 不需要配置 MCP 才能执行/传文件；原有 MCP 可由用户
在 B 的官方 Claude 环境另行配置。

本机 Windows 优先使用 PowerShell 7；没有时使用系统 PowerShell，并设置命令通道
UTF-8。SFTP 以二进制传输，Windows 磁盘路径映射为 `/C:/...`。USB 能力来自本机原生
驱动/工具，例如串口、esptool、JTAG 工具；不是将整台 USB 控制器透传至 VPS。

## 显式控制本机权限

```sh
rdev plan-b on --trust-local-user
rdev plan-b status
rdev plan-b repair                  # 只检查/修复 B 私有连接配置
rdev plan-b off
```

setup/repair 对已知回环 `nc -X 5 -x` SOCKS 配置会在 B 私有 SSH Include 中改用本项目代理，保留原 SSH 配置与主机验证；不会改系统 VPN/DNS。

setup 不启用本机执行；on 是明确的当前 OS 用户通用执行授权。没有系统登录自启。
on 后连接进程会持续到 off，并在私网断开时尝试恢复连接。断线会结束活动的本机
命令，本工具不重放结果不确定的写操作；重新连上后可继续使用远端对话。

B 的项目目录不是沙箱。SSH 身份校验和通道加密保护连接，不限制这个 OS 用户可以
访问的文件、网络、软件或硬件。A 默认仍只开放其授权的目录/端口，不会自行启用 B。
执行器关闭不会撤销已执行命令产生的外部副作用。

## 多设备

在第一台电脑导出自己的 B 连接信息，通过私有方式交给自己的第二台电脑：

```sh
rdev plan-b connection --export ~/my-b-private.json
# 第二台电脑，须有其自己的 SSH 密钥/已验证的 SSH 配置
rdev plan-b setup --join ~/my-b-private.json --ssh my-private-vps --name '我的 Windows'
rdev plan-b on --trust-local-user
```

每台设备生成独立的执行器密钥和设备标识；导出文件不含 SSH 私钥、Claude 账号或
对话文件，但含 B 服务密钥，应视为敏感文件。两台设备共用远端 Harness/账号/会话，
这是完全信任的个人设备组，不是多租户系统。网页能看到不同设备的会话，执行位置
由该会话关联的设备决定。不同服务器用不同 `--state-dir`。

## 定时任务

在网页选择设备和项目后，可设置一次执行时间或重复间隔，时区按浏览器本机时间
输入、服务器按 UTC 保存。到点新建原生 Claude 会话，继续使用官方审批；不是静默
跳过审批的后台机器人。离线或超过计划时间 10 分钟的任务不会盲目补跑。
调度在启动前持久记录一次派发；重启后的未知结果不会自动重放。

## 数据与恢复

本机：`<state-dir>/plan-b/`（默认 `~/.rdev/plan-b/`），含敏感服务密钥、设备密钥。
远端：SSH 用户主目录下 `.rdev-b/<随机服务ID>/`。其中 `data/home` 是 B 自己的 Claude
用户目录，另有 workspace、devices、会话索引和调度配置。全部通过独立数据卷保存。

```sh
rdev plan-b off
rdev plan-b reset
```

reset 检查容器归属，重建运行容器而保留整个数据卷。活动终端会阻止重建；需要
明确中断时才使用 `reset --force-runtime`。共享服务的重建会影响其他设备的 B 连接。
新助手缺少能力/协议不兼容时会提示更新，不会为了“恢复”删除账号或使用 A 自动回退。

远端仅在 `127.0.0.1:<web-port>` 发布容器端口（默认 18788，可在首次 setup 用
`--web-port` 指定）。设备流量和网页访问均经已验证的 SSH 端口转发，内部 TLS 只信任从该 SSH 验证并保存的后端证书，容器内不挂 Docker socket，
不启用 privileged/host network/FUSE。长驻容器以普通用户运行并移除全部 capabilities。
初始化辅助进程只处理本工具标记的数据目录。macOS 的 Docker Desktop 也使用普通
TCP 端口映射，不依赖跨宿主机/VM 共享 Unix socket。

## 能力与验证口径

本版覆盖原生 CLI/网页终端、多会话/恢复、原生审批和计划、双向文件、设备连接、
headless 流式调用与定时任务。它没有复刻参考项目的 Electron 聊天界面、专有协议或
ACP 客户端；需要 agent 接入时可用官方 CLI 的流式输入/输出格式。

构建/模拟/实机/真实模型是不同证据，完整记录见 [验收记录](PLAN_B_VALIDATION.md)。
