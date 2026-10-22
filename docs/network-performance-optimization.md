# 传输与 Android 资源优化

日期：2026-10-19。

## 修改范围

| 方向 | 改动 | 作用与边界 |
| --- | --- | --- |
| 稳定性 | P2P 缓存淘汰跳过活跃 QUIC 流，后续生命周期检查继续收缩缓存；活跃期间更新最近使用时间 | 省电切换或打开另一出口时保留传输；断网与服务端撤权仍按现有规则关闭连接 |
| P2P 可达性 | 公网地址探测每 200 ms 重发未响应的请求，仍使用同一 UDP socket、nonce、响应来源校验与 2 秒总期限 | 请求或响应偶发丢包后仍有机会获得公网候选；保留双栈竞争及首个成功后的 250 ms 等待窗口 |
| 复制缓冲 | 每方向先使用池化 32 KiB；累计传输达到 128 KiB 且读满缓冲时升级到池化 128 KiB，并归还小缓冲 | 普通连接双向复制缓冲从 256 KiB 降到 64 KiB，减少 75%；大流量方向仍可使用原来的缓冲尺寸。这不是进程总内存降低比例 |
| DNS 缓存 | NXDOMAIN 复用最多 5 秒，与正向结果共用原有条目上限；临时失败、超时、取消不缓存 | 减少不存在域名的重复查询，避免短暂网络异常阻塞后续恢复；新建域名可能需要等待短缓存过期 |
| QUIC UDP 清理 | 无 endpoint association 时停止清理定时器；新建 association 时唤醒；每轮清空复用切片的对象引用 | 空闲或只做 Relay 转发的会话不再每 100 ms 扫描，已关闭连接不再被清理器切片持续引用 |
| 时钟 | 使用运行时单调时钟替代永不退出的 100 ms 采样 goroutine | 消除对应空闲唤醒，避免系统时钟调整影响刷新间隔与复制耗时 |
| Android 消息 | Go Core 提供消息到达回调；空队列变为非空时通知，Service 投递至主线程；停止时释放监听器和队列 | 取消每秒 JNI 轮询，保留即时通知；注册前到达的消息与 Core 发布前的消息均补充处理 |
| Android 后台 | 使用统一省电策略：亮屏后台活动连接每 15 秒采样、空闲每 60 秒采样，息屏时通常每 120 秒采样；前台可见时每秒刷新 | 降低后台状态序列化与通知检查频率，消息回调独立触发 |
| P2P 候选更新 | 候选变化触发上报，仅失败时启动重试 timer | 不再让每条空闲直连每 15 秒检查未变化的候选 |

## 本地验证

Go 1.27.1，macOS arm64，Apple M4。以下全仓回归、相关模块竞态与静态检查通过：

```sh
go test ./...
go test -race ./internal/tunnel ./internal/p2p/... ./agent/p2p ./agent/exit ./agent/client ./mobile/androidcore
go vet ./internal/tunnel ./internal/p2p/... ./agent/p2p ./agent/exit ./mobile/androidcore
```

新增覆盖：连续丢弃 1 或 3 个公网探测包后的恢复、探测取消与 socket 复用、省电及新会话建立期间活跃 QUIC 流传输、复制缓冲扩容后的数据与计数一致性、取消时双向退出、UDP 清理器从空闲恢复及重组预算释放、DNS 负缓存过期与错误分类、消息突发合并/并发消费/停止释放。

本地 `net.Pipe` 的 1 MiB 复制微基准，每项 3 次、每次 500 ms，吞吐中位数如下：

| 基准 | 修改前 MB/s | 修改后 MB/s |
| --- | ---: | ---: |
| `BenchmarkPipe1MiB` | 9983.90 | 9942.18 |
| `BenchmarkPipe1MiBIdleTimeout` | 9753.22 | 9649.60 |

这两项中位数分别降低约 0.4% 和 1.1%，用于检查缩小初始缓冲的本地复制代价，不能代表公网测速或宣称吞吐提升。`BenchmarkDNSCacheHit` 为 42.06 ns/op、0 B/op、0 allocs/op。

Android 验证使用重新生成的 ARM64 gomobile AAR、JDK 17、SDK 35、NDK 27.2.12479018 与 Gradle 8.9：

```sh
gradle -p android -PrelayAbi=arm64-v8a :app:testDebugUnitTest :app:assembleDebug
```

11 项 JVM 测试全部通过，ARM64 Debug APK 构建成功，打包原生库通过 16 KiB ELF 对齐检查。产物为 `android/app/build/outputs/apk/debug/app-debug.apk`。

JVM 测试通过 `testImplementation` 引入真实 `org.json`，使已有 JSON 配置测试能够执行；APK 继续使用系统 JSON。构建时的 gomobile 工具依赖放在临时模块目录，保留仓库原有 `go.mod`、`go.sum`。

## 真机与公网验收

1. Wi-Fi 与蜂窝分别测 Relay/P2P 的单连接、多连接吞吐、RTT、丢包及切换恢复时间，确保大流量稳定性。
2. 使用相同设备、路由器、网络与重复次数，对比直连成功次数；覆盖双栈、CGNAT、对称 NAT、UDP 受限及网络切换。协议重试不能保证穿透所有 NAT。
3. 在相同电量、屏幕和网络条件下，对比空闲 30 分钟、持续下载 30 分钟以及息屏消息到达的耗电、CPU 唤醒与通知延迟。
4. 长时间运行并反复建立/关闭 TCP、UDP 和不同出口连接，检查 RSS、Go heap、goroutine、UDP association 与重组预算是否稳定回落。

当前自动化与本地微基准没有给出真实网络的提速百分比、直连成功率百分比或真机节电百分比。Android 需要重新构建并安装 APK 才能获得这些改动。
