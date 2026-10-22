# Android 极致省电优化（默认生效）

保留代理连接、原生 VPN 数据面、服务端推送和自动重连，仅减少无意义的前台服务定时唤醒、跨 Kotlin/Go 的读取和后台状态统计。不要通过关闭 TUN、请求电池优化白名单或禁用心跳来伪造省电效果。

## 实现

- **消息事件驱动**：Go Core 收到服务端推送后，通过 `MessageListener.OnMessage` 通知 Android；Service 合并待执行的 drain，取消每秒 `PopMessagesJSON` 轮询。回调不在 Go 的消息互斥锁内执行；注册前已经排队的消息也会触发通知。
- **Relay 状态采样**：前台 1 秒；后台亮屏 CONNECTING 10 秒、连接活跃 15 秒、空闲或其他状态 60 秒；息屏 CONNECTING 15 秒，其余 120 秒。网络和电源状态事件会要求即时刷新。
- **VPN 原生状态采样**：前台 1 秒、后台亮屏 30 秒、息屏 120 秒。只改变 `TProxyIsRunning` 和 `TProxyGetStats` 检查频率，不关闭 Hev、TUN 或 SOCKS5。恢复到应用前台或屏幕状态切换会主动采样。
- **已存在的 P2P 低功耗模式**：息屏、Doze、系统省电或移动网络时继续触发 `setPowerConstrained`；不改变网络优先策略、出口分流或代理 DNS 逻辑。
- **系统兼容**：未新增 WakeLock、AlarmManager、定时 WorkManager、忽略系统电池优化等权限。

## 测试

1. Go：`go test ./mobile/androidcore`（包含消息回调、回调内 drain、先到消息后注册监听器测试）。
2. Android：执行项目构建脚本**重新生成** `mobilecore.aar`（`MessageListener` 是新增的 gomobile API），再执行 Android 单元测试 `PowerAwarePollPolicyTest`；旧 AAR 无法通过新回调的 Kotlin 编译。
3. 真机回归：前台、息屏、Doze 和 Wi-Fi ↔ 移动网络切换中检查 TCP、UDP、DNS、分流、VPN 保活、Relay 自动恢复。
4. 消息回归：在应用前后台、亮屏/息屏接收服务端验证码和重要消息，确认即时触发而非定时取消息；展示时间受 Android 通知与息屏策略影响。
5. 同型号同网络/同出口 A/B：各做至少 30 分钟无流量息屏待机和 30 分钟负载。通过 Android Studio Energy Profiler、`adb shell dumpsys batterystats --reset` / `adb shell dumpsys batterystats` 比较 CPU 唤醒次数、网络无线电活动时间、耗电估算，同时记录重连时延和消息时延。

**代价**：息屏时 VPN native 停止的检测可能延后最多约两分钟（不含 Android Doze 延迟）；VPN 转发本身不会因此停止。真正能减少多少电量要根据上述真机数据判断，不能由轮询次数直接换算。
