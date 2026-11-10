# Android 出口在线状态与延迟检测修复

## 问题与修复

- 原出口卡片忽略服务端 `online`，统一展示“P2P 支持 / QUIC 直连”；延迟使用随机数，“全部测速”只等待 900 ms 就提示完成。
- 卡片现在明确区分在线、离线和控制连接断开时的“状态未知”。离线节点置灰、禁止切换和检测；批量检测只包含在线且已授权的出口。分组页和主页面使用相同在线状态规则，缺少 `online` 时不再默认在线。
- 单节点和批量检测均调用 Go 核心，使用现有 `speed_test_v1` 上传握手、零数据载荷、半关闭和完成确认测量真实出口响应。耗时包含建立流与握手，不是带宽测速或纯传输 RTT。
- 检测结果绑定具体出口 ID，显示实际经过的路径；仅在路径元数据确认 `network=lan` 时标记“局域网直连”。首页出口延迟也改用该出口的测量结果，不再使用 Relay 心跳延迟。
- 核心执行前后检查连接、运行授权及出口在线状态。检测有 5 秒总超时、并发限制，连接换代或服务端列表版本变化时丢弃结果。界面在状态失效时清除测量缓存并停止批次，离开页面后不再发布旧结果。
- Android 声明 `speed_test_v1` 能力，并在已授权的中继出口、已绑定策略的直连出口分发测速流，支持 Android 作为检测目标。

## 验证

- `go test -race ./agent/client ./mobile/androidcore ./server/gateway -timeout 120s`
- `go vet ./agent/client ./mobile/androidcore`
- Android `:app:testDebugUnitTest :app:assembleDebug`；复用现有 HEV native 库，重建 ARM64/ARMv7 Go AAR，APK 原生库通过 16 KB 对齐校验。
- 新增回归覆盖：真实 TCP 半关闭与零载荷、指定出口与响应 ID、失败响应、阻塞握手取消、离线/移除/未授权/停止/重连/列表更新、Android 能力声明及出口流分发、Kotlin 在线状态和测量结果解析。

当前无连接的 Android 设备，未做真机点击及跨网络联调。Debug APK 位于 `android/app/build/outputs/apk/debug/app-debug.apk`。
