# RelayProxy Mesh 第二轮设计评审处理表

> 评审来源：`design-review.md`（2026-10-17，评审原始 v1.0 设计）；与此前 `relayproxy-mesh-design-review.xlsx`（20 项）的 R-01～R-20 **不是同一份评审**。  
> 对照基线：上一次生成的 v1.1 完整修订稿及安全/网络/产品专题文档；本次产出 v1.2 完整设计补充。  
> 状态语义：“设计已补充”≠ 已实现、已验证或评审通过；阈值均是初始方案，必须经实测和安全评审。  
> 本轮设计完成时未修改 GitHub 分支或源代码；后续已随 v1.2 主文档提交至 `design/decentralized-peer-mesh`，**源代码仍未修改**。

## 逐项处理

| 评审项 | 优先级 | v1.1 状态 | v1.2 处理 / 设计决定 | 验收或待办 |
|---|---|---|---|---|
| 1 Server 真实依赖热力图 | 高 | 开放（此前 R-20） | 提升为 P0 **第一个**交付物，定义符号/调用方/数据/调用频率/Owner/测试字段；确认部分入口文件存在，但未独立复算代码图谱 | **未完成**：实际全仓函数图谱、静态/动态热路径、风险和排期；不得宣称 fan-in 数据已独立验证 |
| 2 签名事件 GC / tombstone 保留 | 高 | 有签名 checkpoint，无压缩规则；V1 已不再是通用 DAG | 定义单写线性链快照压缩、撤销承诺/证明、离线恢复与告警；不直接采纳 DAG GC | 安全快照/撤销恢复测试及长时 SQLite 基准；**设计已补充、实现未验证** |
| 3 时钟偏差与安全降级 | 高 | 有 monotonic、冷启动 stale，阈值未定义 | 参考 `≤120s 正常 / 120–300s 可疑 / >300s POLICY_STALE`，拒绝借宽限期延长短期高危凭证 | 时钟攻击、NTP 漂移、重启/恢复测试；阈值待校准 |
| 4 多空间端点信息侧信道 | 高 | 数据表已有 `space_id`，发现缓存/摘要缺隔离约束 | PeerRecord、endpoint_hint、Gossip、缓存、Relay 查询/错误码按 Space 分区 | 多空间信息泄漏/跨空间拓扑探测负向测试 |
| 5 protobuf 与 CBOR 选择 | 高 | 仍是开放 ADR | P0 候选协议 protobuf + 明确字节级 signing frame；给出字段、严格解析、上限与升级规则；CBOR 为备选 | **待 P0 冻结**：Go/Kotlin/JS 正负向向量/字节一致性；未声称方案已选定 |
| 6 服务声明 2m TTL | 中 | 原示例仍 2m | 能力声明独立按变更广播/5–10m 复核；端点状态短时验证，不能拿可达性当授权 | 50 Peer 规模控制平面负载测试，重签频率校准 |
| 7 撤销同步快速通道/流控 | 中 | 有紧急 push、有限 orphan buffer，无队列定额 | P0–P3 安全事件优先队列、256 KiB/页建议、每 Peer 并发限制、持久 P0 outbox | 拥塞/DoS/长队列下 P0 延迟与数据不丢失测试 |
| 8 邀请发起者离线 | 中 | 已支持与新公钥绑定的预签邀请 | 明确任一在线可信 Peer 可代收/转发，但只有现任 `membership.write` 可以正式入链 | 发起者离线、writer 离线、邀请码重复兑换与并发测试 |
| 9 DAG 因果父索引 | 中 | V1 单写线性链，不存在通用 DAG parents | **不采纳通用 `mesh_event_parent` 为 V1 必选**；增加 `(space,chain,epoch,seq/prev_hash)` 索引、pending dependency 依赖表 | 验证缺失依赖、分叉检测、checkpoint 快照与按链头回放 |
| 10 Relay 配额和耗尽语义 | 中 | per-space 配额原则，缺周期、硬限额/整形行为 | per-space + per-peer + relay_id 计费，限速整形、容量/字节限额拒绝新会话、持久用量和独立控制预算 | 限额重启/切换周期/并发公平/中继切换测试；不做多 Relay 全网强一致账本 |
| 11 顶部身份空间切换 | 中 | 仅设置页有空间管理 | UI 顶部固定空间选择器；页面/缓存/订阅/搜索结果按空间重建 | 快速切换时不渲染旧空间的敏感设备/文件/拓扑 |
| 12 QUIC 版本/通道/拥塞继承 | 中 | 复用现有 QUIC 与 Relay 禁 Brutal，缺明确 stream 方案 | 单 QUIC connection 复用、控制 stream 与业务 stream 分离、P0 队列/窗口保留；高负载下可选第二连接 | Fork API、stream/connection flow control、P0 撤销受重上传影响与兼容性基准 |

## 本轮明确的非采纳事项

- **不回退到通用多写者 DAG**：第二轮评审 §2/§9 针对 v1.0 的 DAG，而 v1.1 已改为单写者链 + 独立仅撤销集合；因此针对性设计生命周期与链索引，而不是重新引入 DAG 父表。
- **不把任意在线管理员视为成员写入者**：离线邀请码可以由代理代收，但入链仍需要 writer 权限。`revoke_only` 不可借邀请码扩权。
- **不把逻辑时钟作为凭证过期权威**：`seq/checkpoint` 管顺序和防回滚；物理时间仍决定 `expires_at`，时间不可信则收紧权限。
- **不宣称全网共享中继配额或无中心即无条件可达**：V1 Relay 分布式单点计数，中继与引导节点仍可能是连通性的必要条件。

## 下一步实施门禁

1. **P0-A**：静态真实符号图 + 调用链 + 运行时抽样 + 旧功能基线，产出 `mesh-server-dependency-inventory.md` 和风险热力图；以 repo SHA 固定证据。
2. **P0-B**：ADR-12/13/14/15 安全审查，定稿 wire/signing frame 与跨语言向量，不得在 P1 继续悬而未决。
3. **P1/P2**：单写链索引、可证明快照/GC、P0 撤销 outbox、时钟降级、多 Space 隔离按前后依赖实现和注入测试。
4. **P3**：服务声明负载、Relay 配额、QUIC 控制面优先级与公网连通性矩阵。
5. **发布前**：旧/新双栈权限取交集、恢复旧备份不能扩权、离线邀请码/多空间侧信道测试全部通过。

## 本次独立检视范围

独立读取当前 `main` 的以下路径开头内容：`cmd/relay-server/main.go`、`cmd/relay-agent/main.go`、`server/service/auth_service.go`、`internal/tunnel/session.go`。前者 import 可见 `server/{api,direct,p2p,rdp,repository,service,session}` 及 `agent/exit`，后者 import 可见 `agent/{app,bridge,divert,exit,fileshare,gui}`。这只能作为**存在性/浅层依赖证据**；评审文档中的节点数、边数与 fan-in 仍需复算。
