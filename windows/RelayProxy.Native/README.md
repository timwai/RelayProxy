# RelayProxy Windows Native GUI

Windows 客户端新的原生 GUI 实现。目标是用 WinUI 3 / Fluent / Mica 替换当前 Wails + WebView2 界面，同时继续复用现有 Go Agent 的认证、QUIC、Public Direct、P2P、代理、路由、透明代理与 RDP 能力。

## 第一阶段已落地

- WinUI 3 原生窗口、Mica、NavigationView 和完整功能导航骨架
- 自动启动同目录 `relay-agent.exe --no-gui`，解析现有 `[Web] Management page:` 本地接口
- 支持 `RELAYPROXY_AGENT_PATH` 指定 Agent
- 支持 `RELAYPROXY_MANAGEMENT_URL` 连接已有 Agent
- 运行概览：`/api/status` + `/api/connections`
- 出口列表与切换：`/api/proxy/exits` + `/api/select-exit`
- 实时连接监控：`/api/connections`
- 消息历史、验证码复制、清空：`/api/messages`
- 其余模块已保留原生导航入口，后续按 `docs/prototypes/relayproxy-complete-ui-prototype.html` 逐页迁移

旧 Wails GUI 暂时保留，直到原生 GUI 达到完整功能对等。

## 开发环境

- Windows 10 1809+ / Windows 11
- .NET 10 SDK
- Visual Studio 2026 + WinUI application development workload
- Windows App SDK 2.5.1

```powershell
dotnet restore .\windows\RelayProxy.Native\RelayProxy.Native.csproj
dotnet build .\windows\RelayProxy.Native\RelayProxy.Native.csproj -c Debug -r win-x64
```
