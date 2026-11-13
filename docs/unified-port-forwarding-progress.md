# 通用端口转发开发进度

- 分支：feat/unified-port-forwarding-20261113
- 设计文档：docs/unified-port-forwarding-design.md
- 当前阶段：P0 第一阶段，本地 Agent 端口映射核心

## 已实现

- [x] 新建 agent/portforward 包，定义 Mapping、Transport、DatagramSession 和 Path。
- [x] TCP Listener 生命周期、并发限制、双向复制与半关闭。
- [x] UDP Listener，按本地来源 IP/端口隔离 Datagram Association。
- [x] UDP 发送队列、Association 数量限制、空闲回收和流量/丢包计数。
- [x] TCP + UDP 可以绑定同一个本地端口。
- [x] 默认仅允许 loopback 监听；禁止 wildcard，非 loopback 必须显式授权。
- [x] 单元测试覆盖 TCP/UDP 回环转发、启停删除和监听安全。

## 尚未实现

- [ ] Relay TCP Stream / 原生 QUIC Datagram 适配器。
- [ ] 认证 P2P Peer Session 适配器和 P2P 优先、Relay 竞速。
- [ ] Server 授权、Lease、撤销和审计协议。
- [ ] 目标 Agent ACL、DNS 解析和远程 Dial Handler。
- [ ] RDP 迁移和旧协议适配。
- [ ] Agent 配置持久化、UI 和真实三端集成验收。

当前代码是可独立测试的本地端口转发核心，不代表端到端远程端口转发已可用。Relay/P2P 与目标端授权接入完成前，不应向用户界面暴露为可用功能。
