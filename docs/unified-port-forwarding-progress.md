# 通用端口转发开发进度

- 分支：feat/unified-port-forwarding-20261113
- 设计文档：docs/unified-port-forwarding-design.md
- 当前阶段：P0，本地 Agent 端口映射核心与现有 Relay 数据面的适配

## 已实现

- [x] 新建 agent/portforward 包，定义 Mapping、Transport、DatagramSession 和 Path。
- [x] TCP Listener 生命周期、并发限制、双向复制与半关闭。
- [x] UDP Listener，按本地来源 IP/端口隔离 Datagram Association。
- [x] UDP 发送队列、Association 数量限制、空闲回收和流量/丢包计数。
- [x] TCP + UDP 可以绑定同一个本地端口。
- [x] 默认仅允许 loopback 监听；禁止 wildcard，非 loopback 必须显式授权。
- [x] 单元测试覆盖 TCP/UDP 回环转发、启停删除和监听安全。
- [x] agent/client 提供 PortForwardTransport，复用现有 TunnelDialer 的 P2P/Relay 路径选择。
- [x] TCP 适配现有可靠 Stream；UDP 强制原生 Datagram，禁止可靠 Stream 降级。
- [x] 保留 Public Direct QUIC、P2P QUIC、Relay QUIC、Relay TLS 的独立路径状态。

## 尚未实现

- [ ] 专用 PortForward Server 授权、Lease、撤销和审计协议。
- [ ] Relay UDP 的 E2EE 原生 Datagram AEAD。当前现有 E2EE Relay UDP 使用可靠 Stream 封装；本适配器强制原生 Datagram，因此在 E2EE Relay 路径上会失败关闭，不能算作 Relay UDP 已验收。
- [ ] 目标 Agent 专用 Mapping Handler、目标范围 ACL 和 DNS Rebinding 防护。
- [ ] 将专用端口转发映射与现有 Proxy Exit 权限解耦。当前客户端适配器临时复用已授权 Proxy Exit，不能视为最终授权模型。
- [ ] Agent 配置持久化、运行时管理 API 和 UI。
- [ ] RDP 迁移和旧协议适配。
- [ ] 真实三端集成、P2P/Relay 网络矩阵和 Windows RDP 验收。

当前适配器只是复用现有 Proxy Exit 数据面，尚未完成独立的 PortForward 权限边界和远程目标端 Handler。不要在权限与目标 ACL 接入前向用户界面暴露为完整功能。
