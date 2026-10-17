# RelayProxy Mesh P0-A 第二轮：授权生命周期与撤销一致性评审

> 状态：**源码核对完成一组关键调用链；发现风险需测试验证；P0-A 仍未关闭**  
> 日期：2026-10-17  
> 代码基线：`main@e4254c81ed678198c948272c22981fd16c19be9a`（所有行号均使用固定版本）  
> 上游：`docs/mesh-server-dependency-inventory.md`、Mesh 主设计 v1.2  
> 范围：设备准入、Grant CRUD 与过期、会话失效、Public Direct、P2P、文件共享。  
> 方法：仓库树完整读取（truncated=false）；按固定 SHA 读取对应实现和已有单测名称/关键断言。**没有执行 Go 测试、运行时 tracing、真实 NAT/移动设备测试或全量 AST 图谱统计。**

## 1. 执行结论

**No-Go：不能凭签名 Grant 或独立 P2P 数据连接替代旧 Server 权限逻辑，就宣布控制面可下线。**

当前系统采用多层失效模型：Server DB 判断授权、Server 关闭设备或身份 Tunnel、撤销 P2P session/lease、失效 Public Direct 端点/票据当前状态、刷新受权出口目录，并要求 Exit 再验目标 ACL。它们共同组成现有安全约束。

**新增 P0 风险**：跨身份 Grant 的 DB 提交、API 读回、会话失效回调不是一项原子操作；不同于单设备撤销走 `authorizationMu` 保护的注册/更改路径。此处存在可观察的跨操作时序窗口；**尚不能据源码直接证明可利用的越权持续时间**，但设计必须采用“本地权限提交屏障 + 持久安全事件/outbox + fail-closed 入口校验”，并补回归/故障注入测试。

## 2. 证据索引：实际关键调用链

| 链路 | 真实函数 / 位置 | 直接观察 |
|---|---|---|
| 设备连接准入 | `server/gateway/listener.go:401-...` → `GatewayConfig.ResolveIdentity/AuthorizeIdentityDevice/RecheckIdentityDevice` → `server/repository/identity.go:527,721,1022` | 设备接受条件通过 Server DB 解释，Agent 使用 Server 返回的 `DeviceAccepted` 状态 |
| 设备审批/撤销 | `server/api/router.go:1376-1404,1433-1465` → `session.Manager.ChangeDeviceAuthorization` → `DB.RevokeDevice/UpdateDeviceCapabilities` | 设备本身的 DB 变更与会话注册被 `authorizationMu` 串行化 |
| 授权 Grant 更新 | `server/api/identity_grant.go:98-160` → `DB.UpdateDeviceIdentityGrant` → `notifyDeviceIdentityGrantChanged` | 更新 DB 返回后执行回调，不经设备撤销的授权注册锁 |
| 授权 Grant 删除 | `server/api/identity_grant.go:163-194` → `DB.DeleteDeviceIdentityGrant` → 回调 | 删除提交完成后触发身份失效；回调由进程内指针实现，没有持久性 outbox |
| Grant 变更清会话 | `cmd/relay-server/main.go:619-630` → `invalidateIdentitySessions` → `sessionMgr.InvalidateIdentity`，后续 P2P/RDP/Direct 清理 | 为安全起见采用**整身份 Tunnel 失效**，不是只关闭被撤销的某个资源流 |
| 过期处理 | `server/repository/grant_expiry.go:14-139` → `cmd/relay-server/main.go:229-255` 的 2 秒 ticker | SQL 到期查询本身拒绝新授权；已有会话靠定时 sweep 返回受影响身份并关闭 |
| Public Direct 票据 | `server/direct/ticket.go:64` → `agent/direct/ticket_auth.go:84-167` → `agent/app/public_direct.go:186-188` | 验 Ed25519、5 分钟票据/30 秒 skew/replay，并通过 Relay 回查实时授权与策略 |
| Direct 版本验证 | `server/direct/control.go:92-139` → `cmd/relay-server/main.go:191-212` → `DB.PublicDirectAuthorization` | 验 `policyRevision/authorizationRevision`，回传 Relay ACL；不能用旧票据单独替代实时判断 |
| P2P 会话 | `agent/p2p/manager.go:339-385` → `server/p2p/coordinator.go:309-401,620-640,650-699` | 协调者先检查当前授权，再签发会话 token/lease；权限变更有 `RevokeDevice` 路径 |
| 出口 ACL | `server/gateway/router.go:997-1008` → `agent/exit/handler.go:162,457` → `agent/exit/target_acl.go:19` | Server Relay policy 与 Exit local ACL 叠加；目标地址/解析 IP 均受校验 |
| 文件列表保密 | `server/gateway/fileshare.go:44-97` → `DB.AuthorizeFileShare` + `hostHasVisibleFileShare` | 只展示同身份且双能力获批设备，Host 还要确认有可见共享并二次重验 |
| RDP 授权 | `server/rdp/coordinator.go:206-...,339-...` → `DB.AuthorizeRDP` | 新建/续租时重验控制器访问权限，目标 RDP 服务启停也在 DB 规则内 |

## 3. 风险与整改门禁

### F-01（P0 / 需要并发和故障注入验证）：Grant 提交与在线权限失效之间的间隙

- **源码证据 A**：`server/repository/identity_grant.go:375-402` 更新授权后先 `tx.Commit()`，再调用 `db.GetDeviceIdentityGrant(id)` 读回。创建逻辑也在 `:208-211` 先提交再读回。读回如果错误，即使事务已提交，API 可能走错误路径而不触发失效回调。
- **源码证据 B**：`server/api/identity_grant.go:137-160` 在 DB 更新成功返回后才 `notifyDeviceIdentityGrantChanged`；删除在 `:182-194` 也是提交后通知。当前通知是进程内回调，不是持久/事务 outbox。
- **源码证据 C**：`cmd/relay-server/main.go:619-630` 回调使受权身份的 session 失效，但它与 DB 提交之间没有 `session.Manager.authorizationMu` 共享的单一事务边界。
- **对比**：`server/session/manager.go:217-259` 的设备撤销路径让 `RegisterAuthenticated` 与设备审批变更串行；`server/session/authorization_test.go:10-48` 已覆盖这条路径。

**结论分级**：**已确认时序分离**；“故障后永久保留越权隧道”及具体持续时间 **待测试验证**。不能据此宣称线上存在确定的可利用漏洞。

**方案**：Mesh 资源 Owner 本地更新授权时，必须在同一个数据库提交中保存 `policy_epoch++` 和不可丢失的签名撤销事件/outbox。新 `SessionOpen` 在 Owner 的准入路径核对最新 epoch，旧会话标记为 `REVOKING` 并在定义的时限内关闭，失败必须可重试。双栈迁移继续执行 `LegacyAllow AND MeshAllow`。旧 Server 的改善可单独立 PR，但不能在本次设计评审中直接上线更改。

**必须补充的测试**：
1. `TestGrantCommitThenReadbackFailureStillInvalidates`：注入 commit 成功后读取失败，旧会话不能保留授权。
2. `TestGrantRevokeRacesWithNewSessionOpen`：并发撤销/新建会话与已建立流，校验失效屏障；分别覆盖 Relay/P2P/Direct。
3. `TestAuthorizationOutboxCrashRecovery`：写入成功后模拟退出/重启，事件恢复发送，授权不回滚。
4. `TestGrantReassignInvalidatesOldAndNewGrantee`：改授权身份，两边的会话快照均刷新。
5. `TestGrantExpireLiveStreamWindow`：定时器延迟、存储繁忙下测已有流收敛；报告实测尾延迟，不以 2 秒周期充当上限保证。

### F-02（P0）：Public Direct 的“票据 + 当前授权”不可简化成“验签即可”

`agent/direct/ticket_auth.go:84-167` 维护短期签名与 replay 表，当前态由 `CurrentValidator` 提供；`agent/app/public_direct.go:186-188` 通过 Relay 控制通道执行查询；`server/direct/control.go:92-139` 要求当前 revision 和 Relay ACL 一致。

**设计门禁**：Owner 的本地 `Grant/Revocation/PolicyRevision` 验证接管该查询。短时票据只证明“某个授权曾存在”，不证明“现在还允许”。在 Owner 缺失可信撤销链头或时钟状态不可信时，按资源风险 fail closed，不应为了继续公网连接自行放宽 TTL。

**测试**：旧票据/新 revoke、复用 nonce、伪造发行者、公私钥轮换、Owner 恢复旧备份、Relay 断线期间新建 Direct 会话。

### F-03（P0）：P2P Signaling Provider 不等于 Admission Policy

`server/p2p/coordinator.go:309-401` 在交换候选前由 Server 验权并签发随机会话 token；`agent/p2p/manager.go:339-385` 依赖 `P2PControlLeaseAck`。Mesh 信令节点可以是授权 Peer/Relay，但不得因为信令到达就授予资源访问权。

**设计门禁**：目标 Peer 在建立 QUIC/mTLS 后独立验证 `PeerID/SpaceID/ResourceGrant/action`、当前撤销与 Owner ACL。保留现有 P2P 候选生成和 UDP/QUIC 组件；将 `SignalingProvider` 与 `ResourceAuthorizer` 作为不同接口。

### F-04（P1）：撤销传播不是单一函数调用

`cmd/relay-server/main.go:605-640` 中设备能力更改、身份更改、Grant 变更和设备撤销的 teardown 范围不同。Mesh 要定义 **身份事件、设备事件、资源事件** 的 `affected_sessions` 映射，至少区分：`device_revoke`、`identity_disable`、`grant_revoke`、`grant_expire`、`resource_policy_update`、`peer_key_rotate`。

**验收**：任意事件持久化之后，Owner 不得再允许旧 epoch 的新 SessionOpen；已存在流的关闭时延应记录可解释的 SLO。资源 Owner 离线与消息被丢弃情况下，靠本地状态/凭证新鲜度约束，而不是假设全网撤销推送必达。

### F-05（P1）：文件设备目录的授权防泄漏

`server/gateway/fileshare.go:44-97` 的发现结果不是所有同空间 Peer 列表，而是“中心授权 + Host 查询确认 + 再次中心授权”的交集。迁移到 Gossip 时分离：网络发现（允许知道地址的范围）、服务能力公告（签名但不自动展示）、用户文件设备列表（Host Owner 明确允许）。未知或未授权设备不得因 Gossip 出现在共享文件页面。

## 4. 已有测试证据与缺口

| 已有测试（实际文件与测试名） | 确认覆盖 | 本轮未找到的针对性覆盖 |
|---|---|---|
| `server/session/authorization_test.go:10` `TestRevocationAndRegistrationAreSerialized` | 单设备撤销/注册串行化 | Grant 更新提交与会话 invalidation 的跨组件竞态 |
| `server/session/authorization_test.go:50` `TestInvalidateIdentityRemovesAllMatchingSessionsOnly` | 按身份失效会话的集合行为 | 与外部 SQL 提交和新流准入之间的时序边界 |
| `server/api/identity_grant_test.go:30` `TestDeviceIdentityGrantAdminAPILifecycle` | CRUD、revision 冲突、同步回调**次数** | 回调失败、进程中断、提交后读取失败 |
| `server/repository/grant_expiry_test.go:9` `TestExpireIdentityGrantsRemovesExpiredRowsAndDeduplicatesIdentities` | 到期清库与受影响身份去重 | 运行中流与清理延迟 |
| `server/direct/control_test.go:134` `TestControllerValidatesCurrentTicketRevision` | 当前验证器允许/拒绝分支 | 与真实 SQLite、并发撤销和实际 Public Direct Stream 的整链路竞态 |
| `agent/direct/ticket_auth_test.go:166` `TestTicketAuthenticatorRequiresCurrentAuthorizationRevision` | Direct Ticket 当前版本校验 | Relay 控制通道故障与 Owner 直接鉴权替换 |
| `server/p2p/coordinator_test.go:113` `TestCoordinatorRevokesLeaseWhenAuthorizationChanges` | P2P lease 重验/撤销 | 新 Mesh 无中心条件下的独立目标鉴权 |

本表仅说明**读取测试源码后确认有相应断言**，不表示当前 commit 的测试执行成功。

## 5. P0-A 下一步执行顺序与关闭标准

1. **PR-A1：全量依赖数据与复现脚本**。以此 commit 提取完整 Go package/symbol/import 图；说明 `fan-in=127/64` 的统计定义、可计算性和接口动态分派局限，提交 CSV/JSON 热点数据与可复现脚本。此项仍**未完成**。
2. **PR-A2：锁和回调故障注入**。先在旧 Server 建立上述五条失败用例、run go test -race，保存报告。不能用“已有测试文件名”代替测试结果。
3. **PR-A3：审查 API 与 SQL 写点**。为 `DeviceIdentityGrant/SystemGrant/DeviceCapability/Enrollment/RDP/Fileshare/Browser` 逐个建立写点、revision、会话失效、审计、回滚的矩阵；特别检查事务提交成功但通知丢失的通用问题。
4. **P0-B 协议门禁（独立）**。冻结签名框架的字节规范和跨 Go/Kotlin/JS 测试向量。不得因为数据库生命周期评审完成就把协议标记“已冻结”。

### P0 Go / No-Go

- **Go**：继续编写新旧授权接口的薄适配器、测试夹具、调用图脚本和非默认行为的 Peer Core 骨架。
- **No-Go**：切断现有 Server 实时鉴权/集中撤销路径；把 Public Direct 的 current-validator 改成可跳过；开放不经 Host Owner 审批的文件设备列表；宣称 P0-A 审查闭环。
- **不估工期**：当前缺动态热度、全量调用图和完整回归；此阶段可排任务顺序，不可据静态入边数承诺工期。

## 6. 代码证据网址

- [Server 主装配](https://github.com/timwai/RelayProxy/blob/e4254c81ed678198c948272c22981fd16c19be9a/cmd/relay-server/main.go)
- [Server API Grant](https://github.com/timwai/RelayProxy/blob/e4254c81ed678198c948272c22981fd16c19be9a/server/api/identity_grant.go)
- [Grant 事务](https://github.com/timwai/RelayProxy/blob/e4254c81ed678198c948272c22981fd16c19be9a/server/repository/identity_grant.go)
- [Session 准入与失效](https://github.com/timwai/RelayProxy/blob/e4254c81ed678198c948272c22981fd16c19be9a/server/session/manager.go)
- [Public Direct 验证](https://github.com/timwai/RelayProxy/blob/e4254c81ed678198c948272c22981fd16c19be9a/agent/direct/ticket_auth.go)
- [Proxy P2P 协调](https://github.com/timwai/RelayProxy/blob/e4254c81ed678198c948272c22981fd16c19be9a/server/p2p/coordinator.go)
- [文件共享设备过滤](https://github.com/timwai/RelayProxy/blob/e4254c81ed678198c948272c22981fd16c19be9a/server/gateway/fileshare.go)

> 本轮改动只包含审查文档，不代表代码漏洞已经修复，也不改变 `main`。
