# Auto 模式恢复 QUIC

日期：2026-10-20。

`auto` 模式仍保留原来的快速连接行为：QUIC 先启动，250 ms 后启动 TLS 竞速，先成功的传输承载会话。

当选中 TLS 后，共享 TunnelManager 会在 15 秒后单独尝试 QUIC。每次尝试使用完整的连接超时预算，默认 10 秒，不再与 TLS 竞速。失败期间 TLS 继续服务；后续等待间隔依次为 30 秒、60 秒、120 秒，之后维持 120 秒，均从上次尝试结束后计时。

QUIC 握手成功后，管理器关闭旧 TLS 会话，通过已有的重连回调重新执行设备认证与能力协商。原会话的迟到清理不会关闭新会话；关闭 Agent、切网、离线会取消探测并释放候选连接。手动指定 `tcp_only`、明文 TCP、已连接 QUIC 时不会启动此探测。

这是传输替换，并不迁移旧 TCP/RDP 连接或 UDP association。切换时已有会话可能断开，需要应用重连。两端均恢复并完成 QUIC Datagram 协商后，新建 RDP UDP 中继即可使用；控制端先前已关闭的 RDP 监听也需要重新打开。

桌面 Agent 与 Android Core 共用此逻辑。更新需要重新构建、安装并重启相应客户端，无需修改 Server 协议。现有 `quic_only` 配置无需变更。

诊断快照的 `connection` 增加：

- `quicRecoveryAttempts`：后台 QUIC 尝试次数。
- `quicRecoveries`：成功替换 TLS 的次数，设备认证完成仍以连接状态为准。
- `quicRecoveryError`：最近一次探测错误，成功替换后清空。

## 验证

回归覆盖 UDP 黑洞后恢复、失败时已有 TLS 流继续传输、成功后的 QUIC 数据传输、重新认证后 RDP UDP 往返、关闭与切网取消探测，以及旧会话/旧网络候选不能覆盖当前会话。

```sh
go test -race ./internal/tunnel ./agent/app ./agent/rdp ./agent/client ./mobile/androidcore
```

自动化验证使用本地真实 TLS/QUIC 与模拟 RDP UDP 端点；Windows 远程桌面实机切换仍需更新客户端后验证。
