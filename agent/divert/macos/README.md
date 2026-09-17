# macOS Network Extension

正式实现位于仓库 `macos/`：

- `RelayProxyNetworkExtension`：`NETransparentProxyProvider`，处理 TCP/UDP、IPv4/IPv6、DIRECT/PROXY/REJECT；
- `RelayProxyMacHost`：安装/升级 System Extension、创建 App Group IPC token，并启用透明代理配置；
- Go `platform_darwin.go`：校验扩展状态和 Unix peer UID/token，经唯一的 `Server.ClassifyFlow` 决策；PROXY TCP 直接把 Unix stream 交给 `ForwardTCP`，UDP 使用有长度上限的二进制 datagram frame 交给 `ForwardUDP`。

IPC 位于 App Group `group.com.relayproxy.shared`。TCP 数据不加逐包 JSON framing；UDP 地址使用紧凑的 4/16 字节二进制编码，两端分段写入 frame，避免 header、endpoint 与 payload 的整包拼接复制。Provider 对 UDP 批次实施逐报文发送完成背压，防止慢链路产生无界发送缓存；认证控制连接保持常驻，不做周期性重连轮询。

## 构建与签名

macOS 的透明代理不能靠未签名命令行程序安装。需要 Apple Developer Team、包含 Network Extension/System Extension 权限的 provisioning profile，以及公证凭据：

```bash
brew install xcodegen
cd macos
DEVELOPMENT_TEAM=你的TeamID xcodegen generate
open RelayProxyMac.xcodeproj
```

在 Xcode 中为两个 target 选择对应 profile，Archive 后使用 Developer ID 分发并 notarize。先运行签名后的 `RelayProxyMacHost.app`，按系统提示批准扩展，再启动 `relay-agent`。Agent 会在启动透明模式时检查：

1. System Extension 显示为 `activated enabled`；
2. App Group token 存在且权限为 `0600`；
3. Provider 在 10 秒内完成 UID + token 双重认证的控制连接。

任一条件不满足时，`Preflight` 返回 `ErrPlatformNotReady`，不会把能力伪装为可用。`RELAYPROXY_NE_SOCKET`、`RELAYPROXY_NE_TOKEN_FILE` 仅用于签名测试环境；`RELAYPROXY_NE_SKIP_STATUS=1` 可跳过 `systemextensionsctl` 状态检查，不应在发行包中设置。

无原生 Agent 窗口时，浏览器打开 `http://127.0.0.1:9090/` 即可使用与 Windows GUI 相同的管理页面和配对流程。macOS Network Extension 的签名、安装与系统批准仍必须先由 `RelayProxyMacHost.app` 完成。
