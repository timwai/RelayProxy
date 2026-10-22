# 网络连接优化

日期：2026-10-19。本轮接续 [传输与 Android 资源优化](network-performance-optimization.md)。

## 行为变化

| 项目 | 实现与默认行为 |
| --- | --- |
| QUIC 多地址竞速 | Relay 与 Public Direct 共用带 context 的 DNS 解析；IPv4/IPv6 地址交错，每 200 ms 启动下一个地址，快速失败时立即尝试下一地址。最多 16 个去重地址，成功后取消其余尝试，关闭迟到成功的连接及其 UDP socket；TLS 的 SNI 仍使用原始主机名 |
| 心跳容错 | 桌面与 Android 共用实现，读取窗口至少 5 秒并按最近心跳/QUIC RTT 放大；软超时最多延长两次，通常最多等 15 秒，硬上限不超过 30 秒且受心跳周期约束。保留半帧读取位置，只发送一条 Ping；写入错误、EOF、非法 JSON 与持续超时仍关闭会话 |
| Android 切网 | 由 capability 与 LinkProperties 回调共同确认可用性；健康备选网络回切首选网络前稳定等待 1.5 秒，真实失联立即切换。同一 Network 的地址、路由、DNS、MTU 变化合并 250 ms 后通知 Core；回切绑定失败时保留健康备选网络，并将再次回切间隔延长至 30 秒 |
| 保留应用状态 | 网络变化调用 Go Core 的 `NetworkChanged`，只取消旧通道及其连接尝试，保留本机 SOCKS/HTTP/VPN 代理监听、出口 Handler 和 TCP 恢复注册表。Core 创建期间的网络变化也会补送；停止服务与修改连接配置仍走完整停止流程 |
| P2P 分类冷却 | 临时信令/连接故障最多冷却 30 秒，出口离线/限流最多 1 分钟，UDP 打洞失败及策略拒绝继续逐步延长至 10 分钟；故障种类变化时重新计算阶梯。已认证清单中出口离线转在线、已认证候选变化可提前解除可恢复故障的冷却；相同清单不会反复解除冷却，策略拒绝和限流不会被这些事件绕过 |
| 网络恢复重连 | Android 网络事件取消旧拨号、离线暂停拨号、恢复唤醒退避；事件合并并保留最多 100 ms 随机延迟，重试间隔至少 500 ms。桌面每 5 秒检查本机地址签名，变化时只唤醒等待中的重连，保持健康 Relay 会话 |
| 连接阶段指标 | Android `StatusJSON.connectionMetrics` 与桌面 `Diagnostics.connection` 提供拨号次数、失败次数、重连次数、网络事件数、拨号耗时、恢复耗时、控制流首字节、认证完成、最终传输和回退原因；P2P 状态增加故障类别及冷却剩余毫秒 |

QUIC 的阶段指标包含 DNS、地址竞速及握手时间；TCP 的 `connectMs` 包含系统 DNS 与 TCP 建连，`handshakeMs` 包含 TLS/yamux 建立。所有耗时单位为毫秒，`firstByteMs` 从传输建立到首次读到控制流字节，`controlReadyMs` 到成功认证响应。`lastRecoveryMs` 衡量旧通道断开到新传输建立，认证耗时单独报告。拨号统计不代替认证失败统计；认证错误仍通过现有状态错误输出。

## 恢复边界

保留注册表允许已协商 `proxy_stream_resume_v1` 的 TCP 使用既有恢复协议；客户端恢复期限仍为 10 秒，出口保存目标连接的宽限期仍为 15 秒。超出期限、授权失效、对端不支持恢复的普通 TCP、UDP association 均不保证跨网络连续。这里通过重新建连及恢复实现切网，没有启用 QUIC 连接迁移。

Android 只在回调确认可用并完成进程网络绑定后报告恢复。`NetworkChanged(true)` 表示实际网络路径变化，会替换旧通道；桌面 `WakeReconnect()` 仅缩短已有重连的等待。

## 验证

新增回归覆盖：黑洞地址不阻塞后续地址、取消落选尝试、清理迟到成功的 UDP socket、原始域名 SNI、DNS 取消和 IPv6 zone、心跳长度头/负载分段超时后继续读取及下一帧对齐、持续超时/取消/EOF、离线停止拨号与恢复唤醒、取消旧网络拨号、连接指标快照隔离、故障分类与在线事件冷却、稳定桌面网络不重复触发恢复，以及 Android Core 切网期间本机 SOCKS 握手与监听对象保留、旧 Public Direct 凭据在新会话前撤销。

```sh
go test ./...
go test -race ./internal/tunnel ./agent/p2p ./agent/app ./mobile/androidcore
go vet ./internal/tunnel ./agent/p2p ./agent/app ./mobile/androidcore
gradle -p android -PrelayAbi=arm64-v8a :app:testDebugUnitTest :app:assembleDebug
```

Go 全仓测试、相关模块竞态与静态检查通过。重新生成 ARM64 gomobile AAR 后构建 Debug APK，11 项 Android JVM 测试通过，3 个原生库通过 16 KiB ELF LOAD 对齐检查。gomobile 工具依赖仍在临时模块内准备，仓库 `go.mod`/`go.sum` 保持原状。

Android 系统回调时序、Wi-Fi/蜂窝实际切换、Doze 与弱网需要真机验收；本轮没有公网成功率、提速或节电百分比结果。建议在真机覆盖：双栈单侧黑洞、切网时连续下载、短暂失联后恢复、首选 Wi-Fi 抖动、同一 Wi-Fi 的 DHCP/DNS 变化、后台省电和明确停止服务后的 socket 释放。
