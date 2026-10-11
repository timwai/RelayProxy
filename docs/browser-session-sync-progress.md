# RelayProxy Browser Session Sync — 开发进度与发布准入清单

> **持续维护的进度文档（Living Tracker）**  
> 首次核对：**2026-10-10** ｜ 跟踪分支：`feat/browser-session-sync` ｜ [Draft PR #194](https://github.com/timwai/RelayProxy/pull/194)  
> 首次代码核对基准：[`2050d08e`](https://github.com/timwai/RelayProxy/commit/2050d08e912a8defbbd4cd767dd101d8f4118b98)  
> 基准自动化结果：[Go CI](https://github.com/timwai/RelayProxy/actions/runs/38062048992)、[UI CI](https://github.com/timwai/RelayProxy/actions/runs/38062049049)、[Android Client](https://github.com/timwai/RelayProxy/actions/runs/38062049009) —— 三项均为 **success**。  
> **发布状态：开发预览 / 不可视为生产就绪；Browser Sync 服务端默认关闭；PR 尚未合并 main。**
>
> 2026-10-10 基准之后的新增提交，必须查看**相应 SHA** 的 CI 检查；不能沿用基准成功结论。

## 最新状态快照（2026-10-11，按已进入分支的代码核对）

| 核对项 | 结果 |
|---|---|
| 开发分支 / 当前已验代码提交 | `feat/browser-session-sync` / [`b504837`](https://github.com/timwai/RelayProxy/commit/b504837a113a8e312c44335b9077fe3f2e80691e)（后续文档提交不改业务代码） |
| PR 状态 | [#194](https://github.com/timwai/RelayProxy/pull/194)：**Open / Draft / 未合并 main** |
| 当前新范围自动化 | [Go CI #38108843827](https://github.com/timwai/RelayProxy/actions/runs/38108843827) **success**、[UI CI #38108843828](https://github.com/timwai/RelayProxy/actions/runs/38108843828) **success**、[Network Acceptance #38108843833](https://github.com/timwai/RelayProxy/actions/runs/38108843833) **success**、[Android #38108843829](https://github.com/timwai/RelayProxy/actions/runs/38108843829) **success**；网络 CI 测试通用代理/FakeIP/DNS，不等于真实 Chrome ↔ Agent 的双端验收 |
| Cookie 同步 P0 | P0-01–07：**3 项已实现·待验收（02/03/04），2 项部分完成（05/06），2 项未开始正式验收（01/07）**。没有一项可视为“已验收” |
| Cookie 同步 P1 / P2 | P1：**4 项部分完成、2 项未开始**；P2：**4 项未开始** |
| A Agent → B Chrome 网络绑定 | NB-01–04 已实现·待真机/安全验收；NB-05 审计与正式准入未开始 |
| B 本机 Agent（当前已入分支） | NB-06/07/09/10：无鉴权、无 Agent 域名白名单的回环入口；新增普通 HTTP Forward、任意合法 TCP CONNECT 目标/端口及 IPv4/IPv6/内网/单标签域名；复用 Agent **DIRECT/PROXY/REJECT**，Chrome 侧增加与 Cookie 配对独立的全 Profile 代理开关；代码/Go/UI 自动化已通过，真实 Chrome/Agent 尚未验收 |
| 最新“完整 Chrome 本机 Agent 代理”需求 | **已集成·待真机/安全验收**：原草稿 [`b7546cf`](https://github.com/timwai/RelayProxy/commit/b7546cf4bb85de249763672d5d48a5a17cfaba57) 的业务改动已迁入功能分支 [`eaf13e8`](https://github.com/timwai/RelayProxy/commit/eaf13e83029eb0272eda81f99f727e1b07eeef02)，经格式与协议测试修复形成 [`b504837`](https://github.com/timwai/RelayProxy/commit/b504837a113a8e312c44335b9077fe3f2e80691e)；Go/UI/Network CI success，不等于全协议浏览器网络接管或 P0 验收 |
| 上线结论 | **不放行**。仍需草稿代码整合修复、Chrome 实际联机测试、站点兼容、安全审计、跨平台安装/更新与性能验证 |

### 核心能力边界（不可混淆）

**普通 Chrome 网络代理**的最新目标是无需 Cookie 配对即可使用 B 本机 Agent，代理目标不额外限定 HTTPS 443、公共 DNS 域名、IP 类型、端口或 Agent 的 DIRECT 动作；由 Agent 现有的分流/出口权限及用户配置决定。**Cookie 会话同步**是另一项敏感能力，仍需要设备授权、双端加密、规则同意和安全作用域校验。取消“浏览器网络代理的人为限制”**不代表自动取消 Cookie 读取权限或站点授权**。A 远端共享出口仍须沿用独立身份授权和临时认证，不能变成匿名开放代理。

此外，`chrome.proxy`/PAC **不是系统 VPN**；UDP/QUIC/WebRTC、系统 DNS、部分内建地址绕过和其他扩展的代理覆盖需要实机核对。**“支持任意有效 TCP CONNECT”≠“能够透明接管 Chrome 的所有协议”**。本机无鉴权回环监听可被同机其他进程使用，放宽到内网地址后风险更高，必须经过独立安全审查。

### 最近交付与下一步顺序

1. **把 `b7546cf` 草稿规范整合到功能分支**，消除明显测试夹具/编译风险，验证 HTTP GET/POST、CONNECT 非 443、IPv4/IPv6、单标签域名、Agent DIRECT/REJECT/PROXY；开发前/后均须更新本进度表。草稿尚无 CI，不得直接宣称可用。
2. **在 Chrome 中实际验收独立全浏览器代理**：Profile 启停、PAC 站点优先级、浏览器默认代理与其他代理扩展冲突、Chrome 的 loopback bypass、端口变更与 Agent 异常恢复；HTTP/WebSocket、HTTPS、DNS/WebRTC/QUIC 的最终行为须按协议分别验证。
3. **补上零鉴权回环端口的安全威胁建模和资源/ACL 检查**：含同机其他程序可使用代理、内网 SSRF、DNS 重绑定、连接限额与 Agent Exit 原有访问控制，确认哪些行为是明确用户选择而非意外扩权。
4. **完成真实 A/B Chrome Cookie 登录与网络出口的双端验收**、故障恢复和独立密码学审查；P0 通过之后再补齐 P1 规则/统计/签名发布，P2 保持延后。

## 0. 范围、进度口径与维护规则

**目标**：用户本人管理或明确授权的两台桌面 Chrome A/B，在网站允许会话迁移的情况下，以双端审批、指定 HTTPS Origin 和 Cookie 名称白名单，经 Server WSS 中继端到端加密会话。**不依赖本机 Agent/Native Messaging；不绕过 MFA、Passkey、DBSC 或其他网站设备绑定。**

本表的 P0/P1/P2 是**发布工作优先级**，与[原设计的 P0–P4 开发阶段](./browser-session-sync-design-development.md#10-开发任务与优先级)是两套维度。避免将“设计阶段 P3 已实现”误解为“发布优先级 P0 已验收”。

状态定义：

- **已验收**：代码完成，覆盖相关自动化测试，按条目所列真机/安全/性能验收证据齐备；每项均须附记录。
- **已实现·待验收**：代码和部分自动化测试已存在，但尚缺真机、破坏性/恢复、安全或性能验收，**不计已验收**。
- **部分完成**：已有部分代码或测试，但仍有明确未实现的关键子项。
- **未开始**：仓库中未见对应完整实现，或仅存在设计/占位。不能把预期能力写成已完成。
- **阻塞**：存在妨碍当前发布的已证实问题；须记录复现、负责人和修复 PR。
- 如某条任务无现场证据，按“待验证”处理，而不是推测通过。不要使用主观百分比代替验收。

**每次开发提交/PR 的维护要求**：

1. 改动 Browser Sync 的协议、Server、扩展、管理页、测试、构建或发布流程时，**在同一个 PR 中**复核本文件，对受影响项更新状态、剩余工作、路径/测试证据和变更记录；状态不变时也须在记录中说明原因。
2. 每次更新刷新“核对日期/commit SHA/CI 链接”，仅在对应 SHA 的工作流完成且成功后填写“自动化通过”；对失败、取消、未运行、排队要原样记录。
3. **已实现 ≠ 已验收**。把“已验收”写入本文前，须具备可复核的用例、版本、平台、结果、记录链接（可脱敏）。浏览器网站兼容必须有授权测试记录。
4. 新增范围时先在相应 P 级新增 ID 与验收条件；完成后可升为“已验收”，**不得删除历史任务以提高完成率**。发现发布阻塞应增加“阻塞”说明。
5. 维护敏感信息：文档、CI 和问题记录**禁止包含 Cookie/Token 明文、完整浏览器私钥、会话快照和用户账户数据**；只记录匿名测试站点、用例 ID、错误码和脱敏诊断。
6. CI 只检查 PR 是否同时修改进度文档，**无法代替内容审查或未来自动改写文档**。此清单在每次开发里持续人工核对，并以 Git 历史作为审计轨迹。

## 一、上线前必须完成（P0）

> **准入规则：** 本节全部达到“已验收”，且无阻塞项，才允许从开发预览进入首批正式可用评审。目前 **P0 未完成**。

| ID | 交付项 | 当前状态（2026-10-11） | 已有实现/证据 | 剩余工作与验收条件 |
|---|---|---|---|---|
| P0-01 | **真实 Chrome A→B 端到端验收** | **未开始（人工验收）** | [扩展测试](../browser/chrome-extension/tests/session.test.mjs)使用模拟 Chrome API；[WSS 集成测试](../server/browser_sync/wss_integration_test.go)使用 Go 测试客户端 | Windows/macOS 各至少 1 个真实双端组合，测试注册→配对→登录→刷新→登出→撤销→重启→断网；授权测试站点至少包含可迁移与不可迁移场景；保留匿名记录；`APPLIED` 只代表 Cookie API 成功，不代表网站登录成功 |
| P0-02 | **最终同步状态一致性 / 丢 ACK 恢复** | **已实现·待验收** | [Server 投递结果](../server/browser_sync/delivery.go)、[WSS 路由](../server/browser_sync/relay.go)、[扩展恢复](../browser/chrome-extension/src/session-sync.js)；有终态短期记录、`DELIVERY_STATUS` 查询、`UNKNOWN` 状态 | 真机断网/Worker 强退/重连演练；测 A 离线时 B 的 `APPLIED`、过期结果、重复请求，确认不误报失败、不将旧 ACK 覆盖新 ACK；检查存储清理与边界 |
| P0-03 | **多 Cookie 失败补偿 / 崩溃保护** | **已实现·待验收** | [Cookie 预检查和逆序补偿](../browser/chrome-extension/src/cookies.js)、仅元数据的恢复意图、`PARTIAL` 暂停/人工恢复；[故障注入测试](../browser/chrome-extension/tests/session.test.mjs) | 在真实 Chrome 注入中途失败/退出/网站并发刷新；确保无静默账号串换、可判别部分恢复、不会自动恢复有风险的写入；记录无法完全回滚时的手工处置流程 |
| P0-04 | **防重放序号恢复 / 重装语义** | **已实现·待验收** | [持久递增序号](../server/browser_sync/session.go)、来源侧 `SEQUENCE_CURSOR`、[序号与授权测试](../server/browser_sync/session_test.go)、[浏览器工具测试](../browser/chrome-extension/tests/lifecycle.test.mjs) | 真机重启、扩展本地数据清除、密钥丢失、规则撤销后的重新配对；证明新 Profile/设备不会擅自复用原设备身份；长时间运行、并发发送与序号上限测试 |
| P0-05 | **MV3 生命周期 / 异常网络恢复** | **部分完成** | [服务 Worker](../browser/chrome-extension/src/service-worker.js)含启动/Alarm；[WSS 连接单飞](../browser/chrome-extension/src/server-api.js)、[五分钟对账限频](../browser/chrome-extension/src/lifecycle.js)及对应模拟测试 | 真机测试 Worker 休眠/系统睡眠/网络切换/TLS 续连、离线期间 Cookie 改动最终一致性；增加失败退避与网络状态提示；验证不会无限重连或漏同步 |
| P0-06 | **限流、资源配额、DoS 防护** | **部分完成** | [Server Handler](../server/browser_sync/handler.go)已有注册 IP 限频、64 连接槽及单帧限制；新增 [分层限流](../server/browser_sync/limits.go)（直接 IP 握手、设备重连、设备帧速率、单规则传输/突发频率、邀请及查询）、[规则数量上限](../server/browser_sync/rules.go)（每设备最多 32 条非撤销规则）、[待确认投递上限](../server/browser_sync/delivery.go)（每规则 16、每来源 128），有对应单测 | 仍需按身份的总量配额、持久终态记录数量上限、跨实例协调、压力测试、多设备实际断网重连与撤销验证；窗口限流仅在单 Server 进程内生效 |
| P0-07 | **独立安全审查 / 密码学复核** | **未开始（正式评审）** | [加密信封](../browser/chrome-extension/src/session-envelope.js)、[邀请加密](../browser/chrome-extension/src/envelope.js)和 Server 验签已有开发实现与单测 | 独立审查现有 P-256 ECDH+HKDF+AES-GCM 组合，优先评估标准 HPKE (RFC 9180)；核对签名/密钥绑定、来源 pin、重放、时序、恶意网站、扩展权限、日志、认证降级与密钥轮换；漏洞关闭后复测 |
| P0-08 | **Chrome 本机 Agent 完整网络代理能力与兼容性验收（新增）** | **已实现·待真机/安全验收** | [Agent 本机入口](../agent/app/browser_local_proxy.go)已支持 HTTP 正向代理、任意 TCP CONNECT Host/IP/端口；[RoutingDialer](../agent/routing/dialer.go)委托现有 DIRECT/PROXY/REJECT；[Chrome PAC](../browser/chrome-extension/src/network-binding.js)支持与 Cookie 配对无关的全 Profile 本机模式；[协议单测](../agent/app/browser_local_proxy_test.go)、[Chrome PAC 测试](../browser/chrome-extension/tests/network-binding.test.mjs)纳入 Go/UI CI | 仍需真实 Windows/macOS Chrome 的 HTTP/HTTPS/WebSocket、IPv4/IPv6、Agent 异常、回环绕过、DNS/WebRTC/QUIC 行为验收，以及无鉴权本机代理的安全/资源审计。Chrome proxy API 不等于透明 VPN，不得声称全协议覆盖 |

### P0 真机验收矩阵（每格都需要测试记录）

| 场景 | Windows Chrome A → B | macOS Chrome A → B | 不兼容网站/边界 |
|---|---|---|---|
| 注册、管理员审批、双端指纹配对 | 待验证 | 待验证 | 未授权设备必须拒绝 |
| 允许迁移的测试站点：登录、刷新后保持登录 | 待验证 | 待验证 | 网站服务端验证，不只看 `APPLIED` |
| 登出传播与目标独立账号保护 | 待验证 | 待验证 | 不自动删除 B 独立 Cookie |
| 两枚以上 Cookie：部分失败、回滚、人工恢复 | 待验证 | 待验证 | 部分回滚必须暂停 |
| Worker 休眠、浏览器重启、网络中断和 ACK 丢失 | 待验证 | 待验证 | 重放与旧 ACK 必须拒绝 |
| 撤销规则、撤销设备、禁用 Server 功能 | 待验证 | 待验证 | 停止 RelayProxy 同步；网站令牌需网站自身撤销 |
| DBSC/Passkey/MFA/设备绑定网站 | 待验证 | 待验证 | 明确提示不兼容，不尝试绕过 |

## 二、正式版本应补齐（P1）

> 本节是正式版产品化需求，部分可以在 P0 验收期间并行推进，但不得以 UI 完成为由放行 P0。

| ID | 交付项 | 当前状态 | 现有能力 / 代码证据 | 下一步与验收条件 |
|---|---|---|---|---|
| P1-01 | **完整规则管理** | **部分完成** | [Chrome 配对和撤销 UI](../browser/chrome-extension/src/popup.js)；[Server Web](../server/web/frontend/src/BrowserSync.jsx)目前以设备审批/撤销为主 | Server Web 加规则列表、站点范围（注意 Server 只能看到密文规则标识，不应解密 Origin）、启停/撤销、双端授权时间和筛选；避免让管理员借管理页取得 Cookie |
| P1-02 | **设备在线/最后同步/失败统计** | **部分完成** | [在线连接映射](../server/browser_sync/relay.go)、[扩展本地状态](../browser/chrome-extension/src/session-sync.js) | 仅用非敏感元数据提供在线/离线、最近接收/应用、失败计数和短期诊断；配合 Server Web 自动刷新，禁止记录 Token 与完整站点策略 |
| P1-03 | **冲突诊断与一次性确认** | **部分完成** | 已有 `CONFLICT`、持久覆盖开关、`PARTIAL` 手工恢复 | 增加按规则的只读错误码/风险提示、单次覆盖确认（优先替代长期全局许可）、账号切换警示、权限变化时取消等待中的恢复 |
| P1-04 | **Cookie 名称选择器** | **未开始** | [弹窗](../browser/chrome-extension/src/popup.js)目前手动输入名称；[Cookie 适配器](../browser/chrome-extension/src/cookies.js)已有作用域检查 | 仅在用户授予网站权限后枚举**Cookie 名称与安全属性**供勾选，默认不展示值，不预选所有 Cookie；支持检测作用域不兼容 |
| P1-05 | **扩展安装、固定 ID、升级与发布** | **未开始（发布流程）** | [MV3 manifest](../browser/chrome-extension/manifest.json)和[开发者加载说明](../browser/chrome-extension/README.md)存在 | 正式扩展签名/固定 ID（Server Origin 白名单所依赖）、版本兼容策略、密钥和存储迁移、权限变更提示、打包/回滚与升级验证 |
| P1-06 | **专用审计、规则指标与排错页** | **部分完成** | [Server ACK 元数据](../server/browser_sync/delivery.go)和双端设备审批可查询，但尚不是完整的安全审计系统 | 新增规则生命周期、设备授权、投递结果、错误码的结构化审计和保留期；支持分页、按身份筛选、脱敏导出及清理；排除 Cookie 值、站点凭据和密文快照 |

## 三、后续增强功能（P2）

> 不作为当前 P0 发布的强制依赖；每项都需单独威胁建模和显式授权。

| ID | 交付项 | 当前状态 | 下一步 / 范围限制 |
|---|---|---|---|
| P2-01 | 复杂 Cookie Scope（父域、不同 Path、CHIPS/分区） | **未开始** | 当前实现故意只允许精确 HTTPS 主机、`Secure`、hostOnly、`path=/`、非分区 Cookie。未来按每种作用域单独验签、白名单、冲突与回滚测试，禁止自动放宽权限 |
| P2-02 | 指定 LocalStorage Key 同步 | **未开始** | 单站点、按 Key 逐项授权、敏感值 E2EE、明确冲突语义；不支持一键复制整个 LocalStorage/IndexedDB |
| P2-03 | 离线密文邮箱 | **未开始** | 默认关闭；自愿启用、E2EE、短 TTL、容量配额、撤销删除、离线设备审批；当前 Server 不保存完整密文会话，A 离线时 B 不一定可获取最新状态 |
| P2-04 | 多 Server 实例间在线设备路由 | **未开始** | 当前 [单进程在线连接表](../server/browser_sync/relay.go)；未来需跨实例在线状态/订阅、投递唯一性、ACK 对账、跨节点顺序与故障转移 |

## 四、当前已有的核心开发成果（不等于发布准入）

| 能力 | 证据 |
|---|---|
| 无 Agent 模式，Server 功能默认关闭、必须 HTTPS+扩展白名单 | [扩展入口](../browser/chrome-extension/manifest.json)、[配置测试](../internal/config/browser_sync_test.go) |
| 浏览器独立设备密钥、管理员审批与发送/接收权限 | [设备认证](../server/browser_sync/auth.go)、[审批 API](../server/api/browser_sync.go)、[Server 管理 UI](../server/web/frontend/src/BrowserSync.jsx) |
| A/B 双端确认、加密站点邀请、签名和 E2EE 密文通道 | [规则存储](../server/browser_sync/rules.go)、[WebCrypto](../browser/chrome-extension/src/session-envelope.js)、[中继](../server/browser_sync/relay.go) |
| 指定 Secure Host-only Cookie 快照、登出、冲突与保护性恢复 | [Cookie 适配器](../browser/chrome-extension/src/cookies.js)、[故障测试](../browser/chrome-extension/tests/session.test.mjs) |
| 发送序号恢复、ACK 去重、断线后结果对账与 WSS 集成测试 | [Replay Cursor](../server/browser_sync/session.go)、[投递结果](../server/browser_sync/delivery.go)、[WSS 测试](../server/browser_sync/wss_integration_test.go) |

**上线决策：不放行。** 本表 P0-01/05/06/07 仍缺真机、可靠性、限流与正式安全审查；已实现的其他 P0 条目也尚未被真实 Chrome 验收。首个发布版本必须保留 Server Browser Sync 的默认关闭策略。

## 五、变更记录（每次开发持续追加）

| 日期（本地） | 代码基准/PR | 变化与证据 | 进度影响 | 维护结果 |
|---|---|---|---|---|
| 2026-10-10 | [PR #194 · `2050d08e`](https://github.com/timwai/RelayProxy/pull/194) | 按代码、开发说明、Go/UI/Android 基准成功 CI 核对。覆盖 E2EE、双端配对、Cookie 同步、登出、ACK、防重放、序号及故障恢复；无真实 Chrome E2E/外部安全验收记录 | P0-02/03/04 标为已实现待验收；P0-05/06 为部分完成；P0-01/07 未开始；P1 为部分完成/未开始；P2 尚未开始 | 建立发布准入、测试矩阵和进度更新规范 |
| 2026-10-10 | [PR #194 · 文档/CI `28f6fd59`](https://github.com/timwai/RelayProxy/commit/28f6fd59d3e9b9a8b78dcdc1d0a4c5f32bfa21c2) | 建立本持续进度表，设计文档和扩展 README 均链接本表；UI CI 增加 Git 历史检查，要求 Browser Sync 源码变更后在同 PR 更新本表 | P0/P1/P2 各项状态 **保持不变**，未新增真机或独立安全验收 | 维护机制已提交；该文档/CI 改动对应的检查仍需以实际运行结果为准 |
| 2026-10-10 | [PR #194 · 配额提交 `20cbcd1`](https://github.com/timwai/RelayProxy/commit/20cbcd1a906f39eefb08ae52250cf63c8d711d55) | 新增设备/规则双窗口限流、直接 IP 握手及重连限频、SQLite 规则/待确认投递容量；增加并发、上限及配额释放单测。[Go CI #38063453648](https://github.com/timwai/RelayProxy/actions/runs/38063453648) **失败：gofmt 格式检查**，本次格式修复后须以新 SHA 重跑 | P0-06 仍为**部分完成**；其余 P0/P1/P2 状态不变；正式 Chrome、安全及压力验收仍缺 | 已修复已知格式阻塞，待新提交 CI 核对 |

| 2026-10-10 | [格式修复 `5bf4dac`](https://github.com/timwai/RelayProxy/commit/5bf4dac8f8901b9f141587da8d34f8ddd4bec7f3) | [Go CI #38063641237](https://github.com/timwai/RelayProxy/actions/runs/38063641237) **success**（gofmt、tidy、vet、完整 go test、race、benchmark smoke）；[UI CI #38063641210](https://github.com/timwai/RelayProxy/actions/runs/38063641210) **success**（React/Chrome 扩展/完整回归/Windows 与 macOS 桌面）；[Android #38063641208](https://github.com/timwai/RelayProxy/actions/runs/38063641208) **success**。本条记录绑定该 SHA，不代表后续文档提交 CI 已通过 | P0-06 状态仍为**部分完成**；P0 真机验收与独立安全审查缺失，继续不放行 | 格式阻塞已解除，代码提交的三项工作流均通过；后续新 SHA 仍须单独核对 |

后续的记录格式：**日期 ｜ 实际代码 SHA/PR ｜ 本次变动及测试链接 ｜ 哪些 ID 状态改变或未改变 ｜ 新风险/遗留项**。保留历史记录，不覆盖旧条目。  
检查入口：[PR #194](https://github.com/timwai/RelayProxy/pull/194) · [Go CI](https://github.com/timwai/RelayProxy/actions/workflows/go-ci.yml) · [UI CI](https://github.com/timwai/RelayProxy/actions/workflows/ui-ci.yml)。

## 六、会话网络绑定（A Agent → B Chrome 扩展）

> 2026-10-11，开发中的可选功能；需要在 Server 显式设置 `browser_sync.network_proxy.listen/advertise/allowed_exit_ids`。默认不开启，现有纯 Cookie 模式不受影响。新的网络绑定不能代替真实 Chrome+Agent 双机验收。

| 任务 | 状态 | 当前实现与剩余工作 |
|---|---|---|
| NB-01 单站点双端网络出口授权 | **已实现·待真机/安全验收** | 来源 Chrome 选同身份已在线且服务端白名单允许的 Agent，并明确指定域名；目标 Chrome 必须单独核对原 Cookie 策略站点后接受；状态与 Cookie 内容隔离 |
| NB-02 HTTPS CONNECT → Relay → Agent TCP 出口 | **已实现·待真机/安全验收** | 专用 TLS listener、短期只在内存保存的 Browser 凭据、每个 CONNECT 复核活动双端授权、同身份在线 Agent、精确 hostname:443 和并发上限；只使用 Agent 原有的 OpenTCP ACL 与 DNS，不提供任意开放代理 |
| NB-03 B Chrome PAC 与认证 | **已实现·待真机/安全验收** | 使用 MV3 proxy + webRequestAuthProvider，绑定站点无 DIRECT fallback，按需要重新获取租约；必须实测浏览器认证缓存、PAC 作用域和异常恢复 |
| NB-04 会话应用与网络安全联动 | **已实现·待真机/安全验收** | 接收网络绑定会话前检查代理已可用，源 Agent 离线保持阻断；真机验证刷新、撤销、扩展冲突及 WebRTC/第三方资源行为 |
| NB-05 审计与正式准入 | **未开始** | 管理界面、Agent 出口明确启用提示、泄漏面审查、Cookie+IP 与真实网站成功率验收，以及跨实例端点与租约管理 |

| 2026-10-11 | Browser Sync 网络绑定初版（[Server 2b65a1e](https://github.com/timwai/RelayProxy/commit/2b65a1ef7f102df0fd3a67765f346fa3c08ecec7)、[Extension 95addc2](https://github.com/timwai/RelayProxy/commit/95addc259951dbe3c0b38863fd88826dc758146f)） | 提供 Server HTTPS CONNECT/Agent OpenTCP 出口与严格的双端绑定授权协议；CI 结果以提交 SHA 为准，不预先标记成功 | 新增 NB-01 至 NB-05，P0-01/05/06/07 仍未验收，正式发布不放行 | 持续更新 |

Browser Sync 网络绑定扩展实现：[PAC/代理认证](../browser/chrome-extension/src/network-binding.js)、[站点规则 UI](../browser/chrome-extension/src/popup.js)、[Cookie 接收前检查](../browser/chrome-extension/src/session-sync.js) 和 [PAC 单元测试](../browser/chrome-extension/tests/network-binding.test.mjs)。[初次 Go CI #38101561277](https://github.com/timwai/RelayProxy/actions/runs/38101561277) 失败于 `gofmt`；本次使用 CI 差异修正格式并修复数据库单连接循环查询与传递 Relay ACL。完整 CI 状态必须以本次代码提交 SHA 为准。

| 2026-10-11 | 网络绑定完整 CI 复核（本次格式修复） | [Go CI #38101778970](https://github.com/timwai/RelayProxy/actions/runs/38101778970) 因 gofmt 失败；针对 `network_proxy.go` 修复剩余格式差异，后续 CI 必须按此提交 SHA 判断，不沿用既有通过结果 | NB-01–04 仍为已实现、**待完整 CI 与真实 Chrome/Agent 验收**，P0 不放行 | 修复格式阻塞，继续跑完整 Go/UI/Android CI |

| 2026-10-11 | [Chrome PAC 测试修复](https://github.com/timwai/RelayProxy/pull/194) | [UI CI #38101778955](https://github.com/timwai/RelayProxy/actions/runs/38101778955) 在 `network-binding.test.mjs` 检测出 `127.0.0.1` 未被客户端拒绝；补数字型 IPv4 禁止，并使 Go 租约测试使用真实的 Basic 编码以覆盖正常和过期令牌 | NB-03 仍已实现、待完整 CI/真机验收；P0 继续不放行 | 记录回归与修复，不预宣告新提交 CI 成功 |

| 2026-10-11 | Cookie-only 向后兼容与强制代理验证 | Server 在未启用可选 HTTPS CONNECT listener 时仍允许已配对浏览器查询网络绑定，并保持 `status=none`；若数据库中存在活动网络绑定则继续 fail closed；B 检查当前 Chrome 代理实际由本扩展控制，避免其他扩展覆盖后继续应用 Cookie；补充 WSS 兼容性测试。[此前 Go CI #38101878382](https://github.com/timwai/RelayProxy/actions/runs/38101878382) 的 vet 错误（测试文件未使用 import）同时修复 | NB-03/04 增强，P0/P1/P2 状态不变、未获真机验收 | 与本次提交一起运行新的 CI，不能沿用旧 SHA |

| 2026-10-11 | [Go CI #38101967308](https://github.com/timwai/RelayProxy/actions/runs/38101967308) 格式复核 | 新增 WSS Cookie-only 兼容测试的 gofmt 格式检查失败，已按 CI diff 精确修复；继续以新提交的完整编译与 Go 测试为准 | NB 状态保持，未新增真实 Chrome/Agent 验收 | 不将格式检查失败视为业务测试通过 |

| 2026-10-11 | [UI CI #38101967271](https://github.com/timwai/RelayProxy/actions/runs/38101967271) PAC IPv4 回归 | 修复扩展数字 IPv4 地址判断中过度转义的点号：采用数字标签逐项检查，拒绝 127.0.0.1 类输入；保留此前扩展单元测试 | NB-03 仍待 CI / 真机验收；P0 状态不变 | 新提交 SHA 单独检查 |

| 2026-10-11 | 代理认证兼容修复 | `webRequest.onAuthRequired` 仅应响应已知 RelayProxy 代理的 Basic 407；网站自身 HTTP 401 与第三方代理认证不可被本扩展取消。增加 [代理认证隔离用例](../browser/chrome-extension/tests/network-binding.test.mjs) | NB-03 已实现待验收，避免普通网站登录回归；P0 状态不变 | 本次新提交须独立通过 CI，真实 Chrome 仍待验证 |

| 2026-10-11 | HTTPS CONNECT→Agent 协议集成用例 | 新增 [HTTPS CONNECT 模拟端到端测试](../server/browser_sync/network_proxy_integration_test.go)：使用真实 TLS 测试代理、短期 Browser Basic 凭据、经 Session Manager 的 Agent TunnelStream OpenTCP 模拟器，验证目标域名:443 与双向字节中继。不等于真实 Chrome 或物理 A Agent 验收 | NB-02 仍为已实现·待真机验收；P0 不放行 | 新 SHA 的 Go CI 尚待核验 |

| 2026-10-11 | [Go CI #38102301709](https://github.com/timwai/RelayProxy/actions/runs/38102301709) 格式修复 | HTTPS CONNECT 集成测试新增后 gofmt 对连续接口方法声明的对齐作了调整；已按 CI 输出修正，不视为业务测试结果 | NB-02 继续待真机/新 SHA CI 验证 | 重新触发 Go/UI/Android CI |

**网络绑定自动化结果（以代码 SHA `d83ebcae273678db00b021f529db28995d5892a3` 为准）：** [Go CI #38102344018](https://github.com/timwai/RelayProxy/actions/runs/38102344018) **success**（格式、tidy、vet、全量 Go 单测、竞态和 benchmark smoke，包含模拟 HTTPS CONNECT→Agent 双向流）；[UI CI #38102344036](https://github.com/timwai/RelayProxy/actions/runs/38102344036) **success**（扩展认证/PAC 单测、Server/Wails、Windows 和 macOS CI）；[Android Client #38102344009](https://github.com/timwai/RelayProxy/actions/runs/38102344009) **success**（debug APK 构建）。**仍缺真实 A Agent + B Chrome 端到端网络测试，P0 和 NB-05 不放行。** 后续文档专用提交不应复用该 SHA 的 CI 结论。

| 2026-10-11 | CONNECT 存量会话即时撤销保护 | Gateway 对每个活动 CONNECT 每 2 秒重新核验配对/身份/Agent 在线状态/已签名本机共享同意，并在任意失败或隧道退出时关闭双方连接；新增 Revocation EOF 的 TLS 集成测试与 Agent 独立帧 ACL/拒绝单测 | NB-01/02 可靠性加强，仍待真实 Agent/Chrome 和全面安全审计 | 以本次代码 SHA 的 CI 实际结果为准 |

| 2026-10-11 | [Go CI #38103066917](https://github.com/timwai/RelayProxy/actions/runs/38103066917) 格式复核 | Agent/Browser Network 新增 Go 字段及测试文件触发 gofmt 格式检查失败，已按照 CI diff 逐项修正；需在新 SHA 下重新运行 Go、UI 与网络回归 | NB-01/02 已实现但继续待真实浏览器验收，P0 不放行 | 记录格式修复，未提前宣告 CI 成功 |

| 2026-10-11 | [Go CI #38103119414](https://github.com/timwai/RelayProxy/actions/runs/38103119414) 拒绝未授权 Agent 共享回归 | 测试 `TestNetworkProxyCONNECTRelaysBytesThroughApprovedAgent` 揭示复合 Tree 覆盖导致 Server `AllowedExit` 缺失已签名 `browser.network.exit.v1` 强制条件、CONNECT 仍使用普通 TCP 帧；已修复为双重强制门禁与 `FrameTypeBrowserNetworkTCP`，保留正在运行的撤销监控 | NB-01/02 回归得到可复现安全缺陷并修复；尚待新 SHA 验证，正式发布不放行 | 以本次提交测试结果为准 |

| 2026-10-11 | Server 关闭时释放被 Hijack 的网络流 | `net/http.Server.Shutdown` 本身不会关闭已 Hijack 的 CONNECT 套接字；新增独立关闭广播 channel，立即唤醒存量连接 watchdog 并关闭双向数据流；新增幂等 Shutdown 单测；同时修复 CI 报出的 gofmt 空行问题 | NB-02/P0-06 资源生命周期增强，仍未完成真机压力/安全验收 | 以新提交的 CI 判断 |

| 2026-10-11 | [Go CI #38103289370](https://github.com/timwai/RelayProxy/actions/runs/38103289370) 测试夹具纠正 | 正向 HTTPS CONNECT 集成测试未声明已签名 Agent 本机许可，导致新权限门禁正确拒绝；现将正向路径夹具加入 `CapabilityBrowserNetworkExit`，并保留随后移除 capability 必须拒绝的负向断言 | NB-01/02 自检提升，尚待新的 CI 与真机验收 | 以新 SHA 的 Go/UI/网络回归判断 |

**2026-10-11 安全加固代码基准：[`5c933cc7`](https://github.com/timwai/RelayProxy/commit/5c933cc7e5a9a36f122937d52c23876af7c3b131)。** [Go CI #38103377888](https://github.com/timwai/RelayProxy/actions/runs/38103377888) **success**：gofmt、module 检查、vet、完整 Go 测试（含 Agent 本机同意及撤销 CONNECT 集成）、race 与 benchmark smoke；[UI CI #38103377883](https://github.com/timwai/RelayProxy/actions/runs/38103377883) **success**：Chrome 扩展、Web UI 与 Windows/macOS 桌面回归。[Network Acceptance #38103377885](https://github.com/timwai/RelayProxy/actions/runs/38103377885) **success**（独立 SOCKS5/HTTP、FakeIP/DNS、防泄漏网络回归）；[Android CI #38103377822](https://github.com/timwai/RelayProxy/actions/runs/38103377822) **success**（debug APK 构建）；这些自动化**不是**真实 A Agent/B Chrome 双机验收，NB-05/P0-01/P0-07 仍不能放行。

## 七、B 端本机 Agent 原生代理（新增范围）

| 任务 | 当前状态 | 实现与剩余验收 |
|---|---|---|
| NB-06 B Agent 独立本机 Chrome 出口 | **已实现·待真机/安全验收** | `browser_sync.local_proxy` 默认关闭、仅监听 127.0.0.1、无需本机 HTTP 鉴权或域名白名单。支持 HTTP 转发与非 443 CONNECT、IPv4/IPv6/私网/单标签目标；保留 64 并发槽与现有 Agent 规则/Exit ACL；其他本机程序也可访问，需安全验收 |
| NB-07 B 扩展按站点选本机/来源 Agent | **已实现·待真机/安全验收** | 已配对 Cookie 规则可选择本机 B 或来源 A Agent；额外提供独立于 Cookie 配对的全 Chrome Profile 本机代理开关；PAC 对明确授权的来源 A 规则保留优先权。实际 Chrome 策略覆盖/回环绕过仍待验收 |
| NB-08 本地 Agent 发现、权限 UI、真实跨端验证 | **未完成** | 需 Agent GUI 设置和本机入口健康检查、无鉴权回环服务的同机进程访问及内网路由风险审查、端口占用/退出/断网、代理规则变化、跨平台 Chrome，以及 DNS/WebRTC/第三方资源审计（来源 A 远端 407 缓存继续测试） |
| NB-09 **独立全 Chrome Profile 的本机代理** | **已实现·待真机验收** | 扩展弹窗提供独立开关及端口配置，Service Worker 设置 `chrome.proxy` PAC；不要求 Server 登录或 Cookie 配对。PAC 保持来源 A 网络绑定优先，其余走本机 Agent，停用后清理全局覆盖。单元测试通过；真实 Chrome 浏览器重启/代理冲突仍未验收 |
| NB-10 **任意 TCP 地址/端口、HTTP 正向代理与正常路由动作** | **已实现·待实网/安全验收** | Agent browser bridge 接收绝对 URI HTTP 与任意合法 TCP CONNECT；协议测试覆盖非 443、IPv4/IPv6、内网主机与 HTTP GET，且真实路由调用 `RoutingDialer.DialTCP` 遵循 PROXY/DIRECT/REJECT。WebSocket Upgrade、POST/大文件、Chrome 实体兼容需补真实验收 |
| NB-11 **Chrome 平台边界与实网验收** | **未开始** | Windows/macOS 实体 Chrome HTTP/HTTPS/WebSocket、IPv4/IPv6/非 443、本机/来源 A 优先级、浏览器隐式回环绕过、DNS/QUIC/WebRTC/UDP、Profile 切换、扩展冲突、异常停机和同机访问安全验收；记录不能由代理 API 控制的流量种类 |

**本轮新增范围不应误算为已验收。** B Agent 本机出口不保证与来源 A 公网 IP 一致；P0-01/05/06/07 与 NB-05 的生产准入仍然不放行。所有构建和集成测试结果必须按功能提交 SHA 单独填写。

| 2026-10-11 | [Go CI #38104275166](https://github.com/timwai/RelayProxy/actions/runs/38104275166) 格式修复 | B 本机 Agent 原生桥接的新增文件需要 gofmt；按 CI 差异修正后重新执行 Go/UI/Android/网络回归。真实 Chrome/Agent 仍未验收 | NB-06/07 待 CI 与真机测试，P0 仍不放行 | 修复格式阻塞 |

**B 本机 Agent 功能代码基准：** [`9f7ca3c`](https://github.com/timwai/RelayProxy/commit/9f7ca3c7f09dbaa299a2b50f0b3a9e9c11fde148)。[Go CI #38104619225](https://github.com/timwai/RelayProxy/actions/runs/38104619225) **success**（格式、vet、完整 Go 测试、race 与 benchmark，新增独立本机 CONNECT/配置回归）；[Network Acceptance #38104619231](https://github.com/timwai/RelayProxy/actions/runs/38104619231) **success**；[Android #38104619136](https://github.com/timwai/RelayProxy/actions/runs/38104619136) **success**。[UI CI #38104619145](https://github.com/timwai/RelayProxy/actions/runs/38104619145) **success**（Chrome 扩展单测、Server UI 与 Windows/macOS 桌面构建）。**当前没有真实 Chrome + B Agent 双机验收结果，不等于 NB-06/07 已验收。**

| 2026-10-11 | B Agent 本机代理进一步简化：零鉴权、零域名白名单 | 本机 `browser_sync.local_proxy` 只保留 `enabled`、`listen`；移除本机 Basic 407、Token 生成/传输/存储、`allowed_hosts` 配置；Agent 接收合规 HTTPS CONNECT DNS:443 交由现有 PROXY 路由与 Exit ACL，Chrome 端仍依据已确认 Cookie 规则生成精确站点 PAC。新增无凭据连接、任意合规域名、非回环监听拒绝和无 Cookie 权限回归。**风险**：同机其他进程可使用该入口（无独立身份/域名限制），需要安全审查 | NB-06/07 已编码·待 CI/真机/安全验收，P0 不放行 | 本轮提交 CI 另行核验 |

| 2026-10-11 | [`b8e4747`](https://github.com/timwai/RelayProxy/commit/b8e47473f13714ad8d682662eddc4c9f562bf6c7) / [`48ab31a`](https://github.com/timwai/RelayProxy/commit/48ab31aec20ff763f32981aed5067ac26233c849) | 按需求彻底取消 B 本机 Agent 代理 Token/Basic 407 和 `allowed_hosts` 配置，只需 opt-in 的 `enabled` + 回环 `listen`；Chrome PAC 仍按已配对 Cookie 规则的 HTTPS hostname 精确匹配，内网后缀/IP 字面量/非 TCP:443 拒绝，原生 RoutingDialer PROXY/REJECT/DIRECT 逻辑不变。远端 A 代理仍按原设备租约鉴权。清除旧本机凭据并修复遗留的本机 407 测试。**重要残余风险**：任何同机程序都可以使用此回环 CONNECT 中转，需额外安全验收 | NB-06/07 已实现、仍待真机/安全验收；NB-08 仍未完成，P0 状态不变 | [Go CI #38106663749](https://github.com/timwai/RelayProxy/actions/runs/38106663749) success；[UI CI #38106740184](https://github.com/timwai/RelayProxy/actions/runs/38106740184) 因本次最终测试补丁晚于进度文档更新，未通过“进度文档跟进”检查；本提交记录并触发新 CI，不能复用前一 SHA 成功结论 |

**本机 Agent 无鉴权、无域名白名单版本最终自动化核对（代码/进度基准 [`9b8483a`](https://github.com/timwai/RelayProxy/commit/9b8483afaba662a3f679b81186ccaa0fc4d52e40)）：**
[Go CI #38106813214](https://github.com/timwai/RelayProxy/actions/runs/38106813214) **success**（gofmt、模块检查、vet、全量 Go/本机 CONNECT 测试、race 与 benchmark）；
[UI CI #38106813210](https://github.com/timwai/RelayProxy/actions/runs/38106813210) **success**（Chrome 扩展完整回归以及 Windows/macOS 构建）；
[Network Acceptance #38106813211](https://github.com/timwai/RelayProxy/actions/runs/38106813211) **success**；
[Android CI #38106813202](https://github.com/timwai/RelayProxy/actions/runs/38106813202) **success**。
本地模式不提供 Agent 端鉴权/域名白名单；该回环代理可被同机其他进程使用。**CI 成功不代表已进行真实 B Chrome ↔ B Agent 登录与出口 IP 验收，更不代表已通过 P0 安全审查；PR #194 保持 Draft，不合并 main。**

## 八、2026-10-11 最新审计记录（进度文档同步）

| 日期 | 代码/证据 | 开发结论 | 进度与发布影响 |
|---|---|---|---|
| 2026-10-11 | 当前 [`031baf9`](https://github.com/timwai/RelayProxy/commit/031baf98d9623a9459d5a47d29e45e6266591325)；[Go](https://github.com/timwai/RelayProxy/actions/runs/38107014122)、[UI](https://github.com/timwai/RelayProxy/actions/runs/38107014106)、[Network](https://github.com/timwai/RelayProxy/actions/runs/38107014103)、[Android](https://github.com/timwai/RelayProxy/actions/runs/38107014237) | 四项 **success**，仅确认当前旧范围；无真实物理 Chrome / 安全验收 | P0-01–07、P1/P2 状态不变；NB-06/07 标明旧范围限制 |
| 2026-10-11 | 未入分支 [`b7546cf`](https://github.com/timwai/RelayProxy/commit/b7546cf4bb85de249763672d5d48a5a17cfaba57)（以 `031baf9` 为父提交、GitHub Commit API 可查；无 Actions run） | 包括普通 HTTP 正向代理、通用 CONNECT、Chrome 全局本机 Agent 模式等草稿，**没有在 `feat/browser-session-sync` 中，不能计为当前已交付** | 新增 P0-08、NB-09/10/11 跟踪项；下一步必须规范整合、补测试并跑新的 SHA CI |
| 2026-10-11 | [Draft PR #194](https://github.com/timwai/RelayProxy/pull/194) | 仍 Open/Draft，目标 main，未合并；Cookie 同步仍有真机/密码学审查发布门槛 | **正式发布不放行**；本次只同步进度文档，不把草稿合入主线 |


## 九、P0-08 / NB-09–11 集成实施（2026-10-11）

原草稿 [`b7546cf`](https://github.com/timwai/RelayProxy/commit/b7546cf4bb85de249763672d5d48a5a17cfaba57) 已通过 [`eaf13e8`](https://github.com/timwai/RelayProxy/commit/eaf13e83029eb0272eda81f99f727e1b07eeef02) 整合进入本开发分支，并在 [`b504837`](https://github.com/timwai/RelayProxy/commit/b504837a113a8e312c44335b9077fe3f2e80691e) 修正 Go 格式及 HTTP/CONNECT 测试夹具。现已实现 HTTP 正向代理、合法 TCP CONNECT 任意目标地址/端口、现有 Agent DIRECT/PROXY/REJECT 和独立 Chrome Profile 开关；**Go/UI/Network 自动化通过但不视为真实 Chrome 或安全验收**。Chrome Proxy API 对 QUIC/UDP/WebRTC/DNS 的协议边界仍然存在。Cookie 同步双端审批与加密逻辑未改变；远端 A 网络共享认证继续保留。P0-08、NB-09/10 现为 **已实现·待真机/安全验收**，NB-11 仍未开始实体平台验收；PR 保持 Draft。

| 2026-10-11 | [`eaf13e8`](https://github.com/timwai/RelayProxy/commit/eaf13e83029eb0272eda81f99f727e1b07eeef02) → [`b504837`](https://github.com/timwai/RelayProxy/commit/b504837a113a8e312c44335b9077fe3f2e80691e) | 本机 Agent HTTP 正向代理、任意合法 TCP CONNECT/地址类型、原生 DIRECT/PROXY/REJECT 与 Chrome Profile 全局开关已进入开发分支；修复 Go 格式和 CONNECT/HTTP 请求测试。[Go CI #38108843827](https://github.com/timwai/RelayProxy/actions/runs/38108843827) **success**；[UI CI #38108843828](https://github.com/timwai/RelayProxy/actions/runs/38108843828) **success**（含 Windows/macOS 桌面构建）；[Network Acceptance #38108843833](https://github.com/timwai/RelayProxy/actions/runs/38108843833) **success**；[Android #38108843829](https://github.com/timwai/RelayProxy/actions/runs/38108843829) **success** | P0-08、NB-06/07/09/10 **已实现·待真机/安全验收**；NB-11 未开始，P0-01/05/06/07 仍有原有验收阻塞，P1/P2 状态不变；PR Draft，不合并 main |

| 2026-10-11 | Chrome 原生路由：进程规则上下文补全 | 专用 `browser_sync.local_proxy` 的 `http.Server.ConnContext` 现在向 `RoutingDialer` 传递真实 Chrome→Agent 入站 TCP socket 的 `Source/Local` 元数据及 `browser_chrome` 入口标签，供 Agent 现有进程归属查找和命中进程级分流规则；覆盖 HTTP Forward 与 CONNECT，新增实 TCP 回归测试 `TestBrowserLocalProxyPreservesChromeSourceTuple`，不伪造进程名或突破原有 ACL。**按新 SHA 重新跑 CI，不能复用先前绿色结果。** | P0-08 / NB-10 继续已实现·待真实 Windows/macOS 进程名规则验收；NB-11 未开始，原 Cookie 同步门槛不变 | 新提交 CI 待核对 |
