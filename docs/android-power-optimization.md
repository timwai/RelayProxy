# Android 极致省电优化（默认生效）

保留代理连接、原生 VPN 数据面、服务端推送和自动重连，仅减少无意义的前台服务定时唤醒、跨 Kotlin/Go 的读取和后台状态统计。不要通过关闭 TUN、请求电池优化白名单或禁用心跳来伪造省电效果。

## 实现

- **消息事件驱动**：Go Core 收到服务端推送后，通过 `MessageListener.OnMessage` 通知 Android；Service 合并待执行的 drain，取消每秒 `PopMessagesJSON` 轮询。回调不在 Go 的消息互斥锁内执行；注册前已经排队的消息也会触发通知。
- **Relay 状态采样**：前台 1 秒；后台亮屏 CONNECTING 10 秒、连接活跃 15 秒、空闲或其他状态 60 秒；息屏 CONNECTING 15 秒，其余 120 秒。网络和电源状态事件会要求即时刷新。
- **VPN 原生状态采样**：前台 1 秒、后台亮屏 30 秒、息屏 120 秒。只改变 `TProxyIsRunning` 和 `TProxyGetStats` 检查频率，不关闭 Hev、TUN 或 SOCKS5。恢复到应用前台或屏幕状态切换会主动采样。
- **已存在的 P2P 低功耗模式**：息屏、Doze、系统省电或移动网络时继续触发 `setPowerConstrained`；不改变网络优先策略、出口分流或代理 DNS 逻辑。
- **系统兼容**：未新增 WakeLock、AlarmManager、定时 WorkManager、忽略系统电池优化等权限。

## 出口 / VPN / 本地代理生命周期（第二阶段）

“已配置 Server 地址和身份”不再表示自动后台连接。Android 只在**连接配置有效**且至少一种业务显式开启时启动 Relay Core：

| 出口共享 | VPN | 本地 SOCKS5/HTTP | Relay Core / 心跳 / 网络监听 |
| --- | --- | --- | --- |
| 关闭 | 关闭 | 全关 | 完全停止，不留后台状态轮询 |
| 开启 | 关闭 | 全关 | 运行（提供出口共享） |
| 关闭 | 开启 | 全关 | 运行（提供 VPN 代理） |
| 关闭 | 关闭 | 至少开启一个 | 运行（提供本地代理） |
| 任意组合 | 任意组合 | 任意组合 | 只要还有业务开启就运行 |

具体同步路径：
- **手动开关**：关闭最后一个功能时，关闭旧 Core 并释放 NetworkBinder 的 ConnectivityManager 回调、停止 Relay 前台通知和状态刷新；其他功能仍开启则按新配置重建 Core。
- **启动/返回主界面**：`ACTION_CONNECT` 不能仅凭合法身份信息启动 Core，已有 Core 也不会因返回界面被无谓重建。
- **开机及升级重启**：BootReceiver 不再把存在有效配置当成自动连接条件；会恢复用户明确启用的出口、VPN 或本地代理。
- **设置保存**：不再强制保持或重新激活已关闭的所有业务；关闭最后一个本地代理时向旧 Relay Service 发送一次 `ACTION_RECONFIGURE` 以完成资源释放。
- **关闭 VPN**：只取消 VPN 的需求；`ACTION_RECONFIGURE` 让 Relay Service 决策，不能通过 `ACTION_STOP` 误清除用户独立开启的出口共享。
- **Android Always-on VPN**：系统要求恢复的 Always-on VPN 仍按 Android 原机制恢复，不受手动关闭全部业务策略误阻断。

**范围**：这是停止 Android 应用自己的 Core、心跳、网络监听与诊断刷新，并非声称 Android 系统不会执行任何电池/包管理活动。通知、网络流量和真实连接仍受系统 Doze、运营商及设备厂商后台策略影响。

## 测试

1. Go：`go test ./mobile/androidcore`（包含消息回调、回调内 drain、先到消息后注册监听器测试）。
2. Android：执行项目构建脚本**重新生成** `mobilecore.aar`（`MessageListener` 是新增的 gomobile API），再执行 Android 单元测试 `PowerAwarePollPolicyTest` 和 `CoreDemandTest`；旧 AAR 无法通过新回调的 Kotlin 编译。
3. 启停组合：依次测试（出口、VPN、本地代理）的全部 8 种开关组合。关闭最后一个业务后，确认 Relay 通知消失、Core 已停止、没有定期状态刷新/重连；只开出口时从另一端访问出口；只开本地代理时验证 SOCKS5/HTTP；只开 VPN 时验证域名分流。快速连续开启/关闭不得保留僵尸连接。\n4. 真机回归：前台、息屏、Doze 和 Wi-Fi ↔ 移动网络切换中检查 TCP、UDP、DNS、分流、VPN 保活、Relay 自动恢复。
5. 消息回归：在应用前后台、亮屏/息屏接收服务端验证码和重要消息，确认即时触发而非定时取消息；展示时间受 Android 通知与息屏策略影响。
6. 同型号同网络/同出口 A/B：各做至少 30 分钟无流量息屏待机和 30 分钟负载。通过 Android Studio Energy Profiler、`adb shell dumpsys batterystats --reset` / `adb shell dumpsys batterystats` 比较 CPU 唤醒次数、网络无线电活动时间、耗电估算，同时记录重连时延和消息时延。

**代价**：息屏时 VPN native 停止的检测可能延后最多约两分钟（不含 Android Doze 延迟）；VPN 转发本身不会因此停止。真正能减少多少电量要根据上述真机数据判断，不能由轮询次数直接换算。
