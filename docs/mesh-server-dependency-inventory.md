# RelayProxy Mesh P0-A：Server 依赖盘点与首轮源码审查

> 审查状态：**进行中 / 第一轮静态证据已形成（不是 P0 验收通过）**  
> 审查日期：2026-10-17  
> 源码基线：`main@e4254c81ed678198c948272c22981fd16c19be9a`（固定 commit，防止审查中漂移）  
> 设计基线：`docs/relayproxy-decentralized-peer-mesh-design.md` v1.2  
> 范围：Server/Agent 核心启动装配、设备认证、出口 ACL、P2P、Public Direct、文件共享、RDP、浏览器同步与基础传输接口。  
> 证据分级：**源码确认**＝已读取目标 commit 的相应实现；**待验证**＝尚无完整调用图、运行时追踪或实网/自动化测试证据。  
> 本文件记录已核实的函数/行号，不代表已经跑过任何测试或完成全仓图谱复算。

## 0. 评审结论（阶段性）

**架构可以渐进式重构，但还不能关闭 P0。**

1. 中心依赖主要在**设备身份/授权数据库、会话控制、票据签发与当前版本校验**，而不是 `TunnelSession` 本身。
2. `internal/tunnel/session.go` 的 `TunnelSession` 仅负责打开/接受流、传输类型、地址与生命周期。建议在其上方引入经 Peer 双向认证、携带 Scope 的 `AuthenticatedConn` 适配器，暂不修改底层 QUIC/TLS 帧语义。
3. 当前公网直连与 P2P 都有服务端动态重校验，不能用“持有过期 Server 签名的旧票据”代替目标 Peer 当前 ACL；V1 应由资源 Owner 验证签名 Grant + 撤销状态 + policy revision + clock state。
4. **不可把中心在线设备目录直接改成全空间 Gossip 可见目录**。当前文件共享设备展示含目标主机的可见性询问与二次认证检查。
5. 当前 Server 既能提供中心式 Relay，也能作为本机 Exit；迁移时要把 `ServerExitDeviceID` 显式映射为一个受授权的 Peer Exit 服务，而非隐式公开。
6. 因尚无全仓函数调用图、动态调用频率、旧版测试基线和迁移原型，**不能给出可信的 P1–P6 人天或完整 fan-in 排名**。

## 1. 代码事实与范围

- 已从 GitHub 获取完整仓库树：1,031 树条目、908 文件对象（`truncated=false`）。这些不是代码图谱节点或函数调用边。
- 已读取代表性的 36 个关键生产 Go 文件（通过固定 SHA 的 GitHub 文件资源）；覆盖 `cmd/`、`server/`、`agent/` 与 `internal/` 多个核心包。未进行全量源码 AST 分析。
- 评审方提供的 `5986 nodes / 31380 edges`、`tunnel 127 fan-in`、`p2p 64 fan-in` **未独立复算**，不作为本报告统计结论。
- 本轮无本地 Go 测试执行、无 Windows/macOS/Android 实测、无故障注入；测试名称仅是拟复用的仓库已有测试文件。

## 2. 中心依赖热点清单（以风险和替换顺序排序，不是运行时频率）

| 优先级 | 现有调用链与具体代码证据 | 集中式状态 / 耦合 | Mesh 目标替代 | 风险 |
|---|---|---|---|---|
| P0-1 | `cmd/relay-server/main.go:365-418` 注入 `GatewayConfig.ResolveIdentity/AuthorizeIdentityDevice/RecheckIdentityDevice`，并调用 `db.ResolveIdentity`、`db.ObserveIdentityDevice` | Server SQLite 审批、身份/策略版本、重连时鉴权 | `PeerTrustStore` + `MembershipVerifier` + 签名授权链与撤销集合 | **A 极高** |
| P0-2 | `server/repository/identity_grant.go:498-544` 的 `authorizeIdentityDeviceFeature`；`server/repository/db.go:1645-1673` 的 `AuthorizeClientExit` | 身份有效、设备批准、能力勾选、同空间/跨空间授权均以 DB 决定 | `ResourcePolicy.Evaluate(subject, target, scope, proof)`，Owner 最终判定 | **A 极高** |
| P0-3 | `server/p2p/coordinator.go:309-401` 的 `connect`、`:620-640` 的 `authorizedPair` | Server 会话目录、授权回调、会话 ID、32 字节随机 token、lease/撤销 | `Rendezvous` 只交换候选，`PeerAdmission` 本地鉴权，短期会话密钥双方协商/验证 | **A 高** |
| P0-4 | `cmd/relay-server/main.go:428-457` 发行 Public Direct Ticket；`server/direct/ticket.go:64` 的 `Issue`；`server/direct/control.go:92-139` 的即时票据重验 | Server-issued ticket、PolicyRevision / AuthorizationRevision、当前 ACL 回查 | Owner 签发短时 ServiceGrant/SessionCapability；目标本地验证新鲜度、撤销和 ACL | **A 极高** |
| P0-5 | `server/gateway/router.go:110-134` 的 `authorizeExit`、`:997-1008` 的 `targetPolicy`；`agent/exit/handler.go:162,457` 的二次检查 | Server 的身份授权 + Relay ACL，Exit 再做本地 ACL | `EffectiveAllow = Membership ∩ Capability ∩ Grant ∩ OwnerACL ∩ migrationLegacyACL` | **A 极高** |
| P0-6 | `server/repository/fileshare.go:15-48` 的 `AuthorizeFileShare`；`server/gateway/fileshare.go:44-97` 查询设备可见性并二次检查 | 中心按同身份/双设备能力预筛；目标设备确认可见性 | Peer 文件目录可见性仅由 Host Owner 允许，未授权者不得出现在文件浏览器 | **A 高** |
| P0-7 | `server/repository/rdp.go:251-269` 的 `AuthorizeRDP`，`server/rdp/coordinator.go:206-...、:339-... ` 重验 | 中心控制器/主机权限、服务启停与租约 | Target Owner 在创建/续租时本地鉴权；入口服务独立执行 IP/期限/限速 | **A 高** |
| P0-8 | `server/browser_sync/handler.go:281-...` challenge/verified-connect；`server/browser_sync/relay.go:81-104` 的 `relaySnapshot` | 中央扩展设备审批、规则与签名会话序列校验 | 浏览器 Device Principal 映射 Peer/Space；E2EE 不依赖中继信任，重放/登出冲突仍需保护 | **B 中高** |
| P0-9 | `server/session/manager.go:135-319` 的 Register/Invalidation；`cmd/relay-server/main.go:428-457` 的出口列表 | Server 单点的在线目录及跨设备清单 | `PeerDirectory` + 逐 Space 发现和签名能力声明；在线可达性独立于授权 | **B 中高** |
| P0-10 | `server/repository/db.go:1692-1699` 的 InsertConnectionAudit、`cmd/relay-server/main.go:303-315` 的有界审计队列 | 中心 SQLite 审计；高负载队列可能丢审计样本（有日志告警） | 每个实际入口/资源 Owner 本地审计，可选异步镜像；定义完整性/SLO 与隐私界限 | **B 中** |
| P0-11 | `internal/tunnel/session.go:17-39` 定义传输会话，不携带身份、Space、Grant | 现有语义是“已连通隧道”，鉴权在外围 | 新增上层 `AuthenticatedConn{PeerID,SpaceID,Principal,ProofState,Session}` 包装；禁止绕过 Scope 校验 | **B 中高** |

### 2.1 本轮已核实的重要细节

**认证握手**：`agent/app/agent.go:688-750` 发送包含身份 ID、安装公钥和 nonce 的 `DeviceHello`；使用 `DeviceAuthPayload` 响应 Server challenge，随后消费 `DeviceAccepted` 中的设备 ID、能力、出口目录和策略版本。V1 Mesh 必须重新定义双向 Peer 信任，不应依赖中心 `accepted.Success`。

**出口授权**：`server/repository/identity_grant.go:498-544` 首先检查双方设备及身份状态、业务能力；同一身份的 `proxy.use` 为隐式允许，跨身份依赖显式 `HasActiveDeviceIdentityGrant`。V1 资源授权要明确“同 Space 默认共享”是否延续，建议高风险出口/私网/RDP 使用显式 Owner ACL，迁移中遵守旧新权限交集。

**Public Direct 新鲜度**：`agent/direct/ticket_auth.go:84-167` 验 Ed25519 签名、限制 5 分钟票据、30 秒时钟偏差、nonce replay，并通过 `currentValidator` 获取当前策略；`agent/direct/runtime.go:51-80` 要求校验器存在。**迁移时保留重验语义**，只是将权威从 Server 转为资源 Owner。不能认为短时签名票据本身已经涵盖即时撤销。

**P2P 建联**：`agent/p2p/manager.go:339-385` 的 `StartClient` 依赖控制消息返回的 `P2PControlLeaseAck`；`server/p2p/coordinator.go:309-401` 创建会话并将 token、候选、RelayPolicy 下发两端。Mesh 应抽象 SignalingProvider，但无需直接重写 UDP 穿透和 QUIC I/O。

**文件列表保密**：`server/gateway/fileshare.go:44-97` 只考虑同身份、双能力被批准设备，设置 4 秒查询窗口，向 Host 询问是否允许 Client 看见其共享，并在展示前再次检查在线会话与授权。Mesh Gossip 发现不等于列表可见；应对每个 Host 本地执行查询权限。

**Server Exit 特殊身份**：`server/gateway/router.go:139-157` 明确把 `protocol.ServerExitDeviceID` 作为本地 Exit 特殊对象处理；在 Mesh 中不得把常量伪装成标准 Peer ID，必须有显式的服务 Owner/签名声明/授权范围。

## 3. 建议的解耦边界（不是代码已实现）

```go
// 目标接口草案。不得替代现有认证实现直接上线。
type PeerPrincipal struct {
    PeerID   string
    SpaceID  string
    Epoch    uint64
}
type AuthenticatedConn interface {
    tunnel.TunnelSession
    Principal() PeerPrincipal
    AuthState() AuthState
}
type PeerAdmission interface {
    Authenticate(ctx context.Context, session tunnel.TunnelSession) (AuthenticatedConn, error)
}
type ResourceAuthorizer interface {
    Authorize(ctx context.Context, subject PeerPrincipal, resourceID, action string, proof []byte) error
}
type RendezvousProvider interface {
    Propose(ctx context.Context, targetPeer string, candidates []Candidate) error
}
```

注意：禁止将 `AuthenticatedConn.Principal()` 当作所有 Stream 自动授权，多个 Space 共用底层连接时，每个服务 Stream 必须单独绑定并验证 `space_id, resource_id, action, policy_revision`。

### 3.1 PR 执行切分

- **PR-A1（纯审计）**：全量解析 Go AST/import，建立符号交叉引用图，输出机器可复现 CSV/JSON + commit SHA；补充 `AuthorizeClientExit/AuthorizeRDP/AuthorizeFileShare` 到全部调用方的路径，区分静态引用、接口动态分派与运行时频率。不可把评审的 127/64 直接标为已验证。
- **PR-A2（旧行为门禁）**：为设备重连/撤销、跨身份 ACL、Direct ticket 当前状态回查、P2P lease、文件不可见/RDP、浏览器同步和 TLS/QUIC fallback 建立复用测试及环境矩阵；成功证明现有行为。
- **PR-B（薄适配）**：增加 AuthenticatedConn/Principal/ResourceAuthorizer 接口与 legacy adapter；现有 Server 继续是唯一权威，不切换默认行为。
- **PR-C（无 Server LAN 最小闭环）**：引入 mTLS Peer Trust、授权链、Owner ACL、撤销快通道，完成双设备局域网文件访问；所有安全门禁就绪后再开发公网和 Relay。

## 4. 关键回归测试地图（仓库存在文件，不代表本轮执行通过）

| 场景 | 优先复用测试文件 |
|---|---|
| 身份、审批和授权 | `server/repository/enrollment_test.go`、`server/repository/identity_grant_test.go`、`server/repository/identity_admission_test.go` |
| Session 注册、撤销竞态 | `server/session/manager_test.go`、`server/session/authorization_test.go` |
| P2P 协调/续期 | `server/p2p/coordinator_test.go`、`agent/p2p/manager_test.go` |
| 公网直连验签/重验 | `server/direct/control_test.go`、`agent/direct/ticket_auth_test.go`、`agent/direct/public_integration_test.go` |
| 出口目标 ACL | `server/gateway/target_policy_test.go`、`agent/exit/target_acl_test.go` |
| 文件共享授权与可见性 | `server/repository/fileshare_test.go`、`agent/app/file_share_visibility_filter_test.go`、`agent/app/fileshare_relay_integration_test.go` |
| RDP | `server/rdp/coordinator_test.go`、`server/repository/rdp_security_query_test.go` |
| 浏览器同步 | `server/browser_sync/authorization_test.go`、`server/browser_sync/wss_integration_test.go` |
| 基础传输 | `internal/tunnel/lifecycle_test.go`、`agent/app/transport_integration_test.go` |

## 5. 尚未关闭的 P0 门禁

- [ ] 源码 AST 全量调用图（含所有 package、静态调用点、接口分派边与测试覆盖）和复算 127/64 口径；并记录结果可信边界。
- [ ] 动态采样：代表性负载下的调用量、尾延迟、连接/CPU/内存热点；静态 fan-in 不等于运行热度。
- [ ] 全量数据库写入路径、设备/身份迁移与日志备份/恢复分析。
- [ ] 老版本 Windows/macOS/Linux/Android、浏览器扩展实际联通与故障注入回归。
- [ ] P0-B 协议编码 ADR 及 Go/Kotlin/JS 签名字节一致性、安全负向向量；禁止提前宣称冻结。
- [ ] 从旧 Server 关闭前必须完成 Owner ACL、撤销传播、双栈交集、快照防回滚测试。

## 6. 下一轮评审重点

1. **高风险优先**：对 `db.AuthorizeClientExit`、`AuthorizeRDP`、`AuthorizeFileShare` 和 `PublicDirectAuthorization` 做“写点/读点/失效回流”的函数级生命周期审查。
2. **边界对齐**：在调用图中区分中心 Control Plane 与现有独立 P2P Data Plane，确定用 Adapter 而非重写的范围。
3. **旧协议底线**：测试 Server 控制通道中断后 Direct/P2P 失效的实际语义；设计 Mesh 新行为时不能意外放宽授权。
4. **P0 验收**：产出按固定 commit 可复现的图谱，并把代码热力图、测试结果、失败缺陷与 PR 计划关联。

## 7. 本轮结论

**Go（继续 P0）**：允许基于当前代码创建只增加接口、日志、测试的审计/适配 PR。  
**No-Go（切断 Server）**：在成员/授权/撤销/Owner ACL 尚未按目标协议实现并通过回归前，不得直接关闭中心控制面。  
**No-Go（宣称代码评审闭环）**：本报告仍是代表性静态审查，而不是全部依赖图/测试完成的正式 P0 签字。
