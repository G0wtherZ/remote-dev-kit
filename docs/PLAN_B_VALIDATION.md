# Plan B 验收记录

版本：alpha.5。日期：2026-11-24。这里只记录 Remote Dev Kit 的独立实现；之前直接
集成参考项目的实验已撤下，其测试结果不算作本版证据。

## 已完成的开发检查

- Go 执行器：签名认证、错误密钥拒绝、输入/双输出/退出码、并发、2 MiB 双流、二进制
  SFTP、拒绝 PTY/额外转发、断线回收。
- 标准 Yamux 传输：3 MiB 往返、TCP 半关闭、碎片数据与慢速读取，race 检查通过。
- 网页边界：登录密钥、同源校验、HMAC CSRF 请求/WS 校验、HttpOnly/SameSite cookie、
  退出、非法设备/会话路径、CSP 与不依赖外部脚本的静态页面。
- 配置：归属标记、协议与身份保留、显式本机执行授权。
- TLS：由 SSH 获取的固定后端信任、拒绝未知回环端点、重复 setup 保留证书身份。
- 六个平台的助手交叉构建；macOS/Windows/Linux CI 执行 Go tests、vet、build。
  Windows CI 另外实际执行原生 PowerShell UTF-8、退出码与二进制 SFTP 检查。

## Mac—Linux VPS 最终链路

通过现有 Tailscale 私网 SSH，使用本版 TCP 端口转发、固定信任 TLS 和标准 Yamux。
同一条私网的旧 macOS `nc` SOCKS 代理在大文件链路中失败；B 私有 SSH 配置改用
本项目代理后复验通过，原 SSH 配置、VPN 路由及 DNS 未改动。

`scripts/test_plan_b_live.py --native-cli --client-binary <本版rdev>` 覆盖：

| 检查 | 结果 |
| --- | --- |
| 本机 headless CLI 入口，不申请 TTY、不登录模型 | 官方 CLI 版本输出成功 |
| 真实 VPS—Mac stdin/stdout/stderr/退出码 | 通过，含二进制与中文 |
| 四路并发原生命令 | 通过 |
| Mac → VPS，2 MiB 随机文件，中文及空格文件名 | 通过 |
| 文件 SHA-256 完整性 | 一致 |
| VPS → Mac 二进制回传 | 一致 |
| 官方远端 CLI → 脚本化 Messages API → Mac 工具 → 结果回传 | 通过 |

上述工具闭环使用真实官方 CLI 和真实跨机器链路，模型响应来自临时测试服务。
不会将这个结果算作真实 Claude 服务、账号或模型决策能力的验收。

## 浏览器与数据保护

- Chrome 实际打开本项目网页，经私网读取 Mac 测试目录；浏览器下载文件与源文件
  SHA-256 一致。
- 网页新建官方 Claude 原生终端，显示其首次启动界面；刷新/接回保持同一会话。
- 网页创建未来测试任务，保存后停用，未派发模型调用。调度重启/到期/重复派发边界
  另有本地自动测试，不宣称实测过全部长周期运行。
- B 容器重建后，自己数据卷中的保留标记哈希不变；宿主已有 Claude 资料未导入、
  未挂载、未重置。既有网站实测 HTTP 200。
- 实际运行长命令后执行 `off`：授权撤销、本机子进程结束；显式 `on` 后重新连接并
  成功执行新命令，没有自动重放之前的命令。
- USB 仅作设备枚举，硬件串口没有打开，固件没有改动。
- macOS 安装测试覆盖完整下载/校验/安装、A 参数透传、B 安装入口、重复安装与损坏
  包拒绝；包内只收录 allowlist 指定源码文档及二进制，不收录测试配置/节点身份。

## 验证边界

- `scripts/test_plan_b_live.py --native-cli` 使用临时 Claude home 和本地脚本化 Messages API，
  用于验证官方 CLI 到本机工具的协议闭环；它不是真实模型或真实账户验收。
- 没有导入、读取或修改宿主机已有 Claude 登录资料；用户选择先交付，B 的真实账号
  登录与真实模型对话留待后续验证。
- 实机为 macOS 本机与 Linux VPS。Windows 原生命令在 CI 实际执行，但没有用户
  Windows 实机、真实 Windows USB 或远端 macOS Docker Desktop 的完整部署证据。
  后者使用标准 TCP 端口发布，不将架构适配或交叉构建当成实机通过。
- 不宣称与参考项目 100% 相同：主入口为 CLI 和浏览器原生终端，没有 Electron 聊天界面
  或专有 ACP 客户端；官方流式接口用于自动化接入。
