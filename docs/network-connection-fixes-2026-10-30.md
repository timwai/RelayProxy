# P2P 与局域网直连修复闭环

日期：2026-10-30。对应本轮审查的六项 P2 问题；测试以当前工作树为准。

| 问题 | 修复后的行为 | 回归证据 |
| --- | --- | --- |
| RDP 使用过期 UDP 端口 | 打洞循环持续读取已认证信令更新的候选快照；更换候选时同时清理旧地址的挑战和可达性证明 | `TestDialUDPUsesUpdatedTargetPort` |
| Rendezvous 阻塞 LAN | 控制端立即打洞，目标端立即启动读取与发布 LAN 候选；反射探测仅写 socket，响应由同一个读取流程分发 | `TestLANDoesNotWaitForDeadRendezvous`、`TestTargetPublishesLANBeforeRendezvousCompletes` |
| 冷却期间不检测切网 | 尚未到期的冷却使网络观察器保持运行，网络改变后清除旧冷却；冷却到期且无其他工作时停表 | `TestCooldownObservesNetworkChange`、`TestExpiredCooldownStopsNetworkPolling` |
| 仅 Client 设备填满 READY 队列 | 只有 Exit 侧会话投递入站流处理通知，Client 仍通过 `ReadyForExit` 获取直连 | `TestClientReadyQueueDoesNotAccumulateClosedSessions`、现有 LAN QUIC 集成测试 |
| 文件 P2P 遗漏关闭 UDP socket | 返回的文件流持有原始 socket，关闭时依次回收流、QUIC Transport 和 socket；重复关闭幂等 | `TestPunchedFileCloseReleasesUDPSocket` |
| 路径报告格式不兼容 | 本地诊断保留 UPnP 状态，线上 CandidateSummary 发送兼容服务端的计数格式 | `TestPathReportKeepsWireSummaryCountOnly`、现有服务端路径报告校验测试 |

自审补充：RDP 控制信令的取消现在会立即中断阻塞读写，避免 LAN 成功后仍等待后台发布的 deadline。`TestRDPControlCancellationInterruptsPendingIO` 覆盖两个方向。共享 socket 的探测继续校验响应来源与 nonce，并且不修改主读取流程的 deadline，见 `TestSharedSocketProbeValidatesResponsesWithoutChangingDeadline`。晚到 WAN 候选保留原有反射探测加打洞的 3.2 秒预算，Relay 仍在 300ms 后参加竞速。

验证通过：

```sh
go test -race -count=1 ./internal/p2p/... ./agent/p2p ./agent/rdp/p2p ./agent/client ./server/p2p ./server/rdp ./agent/app ./mobile/androidcore -timeout 120s
go test -race -count=10 ./agent/p2p ./agent/rdp/p2p ./internal/p2p/candidate ./agent/app -run 'Test(Cooldown|ExpiredCooldown|ClientReady|PathReport|DialUDP|LANDoes|TargetPublishes|PunchedFile|RDPControlCancellation|SharedSocketProbe)' -timeout 120s
go vet ./internal/p2p/... ./agent/p2p ./agent/rdp/p2p ./agent/client ./server/p2p ./server/rdp ./agent/app ./mobile/androidcore
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./agent/app ./agent/p2p ./agent/rdp/p2p ./internal/p2p/...
git diff --check
```

本轮自审未发现这六项修复中尚未处理的问题。运行验证为本机 socket、真实 QUIC/文件传输集成和虚拟时间测试；Windows 为交叉编译，未执行 Windows 实机或真实跨 NAT 网络验收。
