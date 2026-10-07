# RelayProxy Windows Native GUI

Windows 客户端的 WinUI 3 原生 GUI。正式 Agent 下载采用单 EXE：WinUI、.NET、Windows App SDK 运行时和对应架构的 Go Core 都打进同一个可执行文件；首次启动由 WinUI/.NET single-file 机制释放运行依赖后，GUI 再启动内嵌的 `relay-agent.exe --no-gui`。界面不依赖 Wails / WebView2。

## 当前功能

- WinUI 3 / Fluent / Mica 原生主窗口
- 原生系统托盘、关闭到托盘、浅色 / 深色 / 跟随系统；开机自启动使用 `--minimized` 参数进入托盘
- 自动启动同目录 `relay-agent.exe --no-gui`
- `RELAYPROXY_AGENT_PATH` 可指定 Agent Core
- `RELAYPROXY_MANAGEMENT_URL` 可连接已经运行的 Agent
- 运行概览：连接状态、当前出口、RTT、流量、Public Direct、P2P、Relay、SOCKS5/HTTP/Exit
- 身份与设备：当前 Device / Identity / 审批状态、授权出口和授权 RDP 设备
- 连接与路径：Server、QUIC/TLS、Transport、Public Direct、P2P、Relay fallback
- 出口选择：真实授权出口、在线状态与切换
- 本机代理：SOCKS5、HTTP 独立开关 / 地址 / 端口
- Windows 透明代理：WinDivert 模式和排除进程
- 本机出口共享：Internet / Private / Loopback、上游代理、Domain / CIDR ACL
- 分流规则：新增、编辑、删除、启停、排序、进程 / 目标 / 端口 / 协议 / 指定出口 / Datagram Required / Handle Direct
- RDP：只展示服务端授权设备，一键连接并启动 `mstsc.exe`，显示 TCP/UDP 路径
- 消息中心：验证码、普通消息、重要提醒、历史、复制验证码、清空历史
- 原生消息弹窗：WinUI 窗口、消息队列、无顶部色条、可配置自动关闭时间
- 实时连接：进程、目标、规则、出口、路径、上下行速率
- 诊断与日志：`/api/diagnostics`、`/api/logs`、清空日志
- 配置 revision 校验，避免 GUI 覆盖外部修改

Android 独有的 Wi-Fi / 移动数据优先、移动网络故障切换等“Android 网络调度”不会出现在 Windows GUI。

## 发行布局

面向普通 Windows 用户的主产物是单文件：

```text
RelayProxy-agent-windows-amd64.exe
RelayProxy-agent-windows-arm64.exe
```

这两个文件分别对应 x64 / ARM64，内部已经包含 WinUI 3、.NET、Windows App SDK 运行时、图标和对应架构的 Go Core。WinUI single-file 在首次启动时会把运行依赖释放到 Windows 临时提取目录；它是“单文件分发”，不是“零落盘运行”。

完整目录版仍保留给 CLI、诊断和迁移期回退：

```text
relay-agent-gui.exe      同一套 WinUI 单文件 GUI
relay-agent.exe          独立 Go Agent Core / CLI
relay-agent-wails.exe    旧 Wails GUI，仅迁移期回退
relay-server.exe         Windows Server
...
```

单文件版启动后会使用 bundle 中释放出来的 `relay-agent.exe`，为当前 GUI 会话创建一个独立的随机 loopback 管理端口与随机 token；这条私有通道不会修改或复用用户配置的 Web 管理监听地址、端口和 token。Network Service 安装 / 修复 / 卸载接口也只注册在这条原生私有通道上，不会暴露给普通浏览器管理页。旧 Wails GUI 不再是默认桌面入口，但暂时保留到原生 GUI 经实际部署验证完成。默认独立 `relay-agent.exe` 也不编译 Wails/WebView2 shell；只有 `relay-agent-wails.exe` 使用 `wailslegacy` build tag。

## 开发环境

- Windows 10 1809+ / Windows 11
- .NET 10 SDK
- Visual Studio 2026 + WinUI application development workload
- Windows App SDK 2.5.1
- Go（用于 Agent Core）

只编译 UI：

```powershell
dotnet restore .\windows\RelayProxy.Native\RelayProxy.Native.csproj -r win-x64
dotnet build .\windows\RelayProxy.Native\RelayProxy.Native.csproj -c Debug -r win-x64 -p:Platform=x64
```

生成可直接分发的单 EXE：

```powershell
.\scripts\build-windows-native.ps1 -Configuration Release -Runtime win-x64
```

完整项目发布：

```powershell
.\scripts\build.ps1 -Version 1.0.0
```

完整构建会额外生成根目录的 `RelayProxy-agent-windows-amd64.exe` / `RelayProxy-agent-windows-arm64.exe` 单文件下载产物；平台目录仍保留独立 Core、Server 和 `relay-agent-wails.exe` 供 CLI、调试与回退。
