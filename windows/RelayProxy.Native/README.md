# RelayProxy Windows Native GUI

Windows 客户端的 WinUI 3 原生 GUI。默认发行文件名仍是 `relay-agent-gui.exe`，但界面已经不再依赖 Wails / WebView2。网络核心继续由同目录 `relay-agent.exe --no-gui` 提供，WinUI 只负责原生界面、托盘、弹窗和配置。

## 当前功能

- WinUI 3 / Fluent / Mica 原生主窗口
- 原生系统托盘、关闭到托盘、启动时最小化、浅色 / 深色 / 跟随系统
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

正式 Windows 目录现在使用：

```text
relay-agent-gui.exe      WinUI 3 原生 GUI（默认双击入口）
relay-agent.exe          Go Agent Core / CLI
relay-agent-wails.exe    旧 Wails GUI，仅迁移期回退
RelayProxy.Native.dll    原生 GUI 依赖
Microsoft.*              Windows App SDK 自包含依赖
...
```

`relay-agent-gui.exe` 启动后会寻找同目录 `relay-agent.exe`，并通过 Agent 现有 loopback management API 工作。旧 Wails GUI 不再是默认桌面入口，但暂时保留到原生 GUI 经实际部署验证完成。

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

生成可运行的原生客户端目录：

```powershell
.\scripts\build-windows-native.ps1 -Configuration Release -Runtime win-x64
```

完整项目发布：

```powershell
.\scripts\build.ps1 -Version 1.0.0
```

完整构建会让 `relay-agent-gui.exe` 成为 WinUI 3 原生客户端，并额外保留 `relay-agent-wails.exe` 作为回退。
