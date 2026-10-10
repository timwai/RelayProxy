# RelayProxy Browser Session Sync — 开发进度与发布准入清单

> **持续维护的进度文档（Living Tracker）**  
> 首次核对：**2026-10-10** ｜ 跟踪分支：`feat/browser-session-sync` ｜ [Draft PR #194](https://github.com/timwai/RelayProxy/pull/194)  
> 本次代码核对基准：[`2050d08e`](https://github.com/timwai/RelayProxy/commit/2050d08e912a8defbbd4cd767dd101d8f4118b98)  
> 基准自动化结果：[Go CI](https://github.com/timwai/RelayProxy/actions/runs/38062048992)、[UI CI](https://github.com/timwai/RelayProxy/actions/runs/38062049049)、[Android Client](https://github.com/timwai/RelayProxy/actions/runs/38062049009) —— 三项均为 **success**。  
> **发布状态：开发预览 / 不可视为生产就绪；Browser Sync 服务端默认关闭；PR 尚未合并 main。**
>
> 2026-10-10 基准之后的新增提交，必须查看**相应 SHA** 的 CI 检查；不能沿用基准成功结论。

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

| ID | 交付项 | 当前状态（2026-10-10） | 已有实现/证据 | 剩余工作与验收条件 |
|---|---|---|---|---|
| P0-01 | **真实 Chrome A→B 端到端验收** | **未开始（人工验收）** | [扩展测试](../browser/chrome-extension/tests/session.test.mjs)使用模拟 Chrome API；[WSS 集成测试](../server/browser_sync/wss_integration_test.go)使用 Go 测试客户端 | Windows/macOS 各至少 1 个真实双端组合，测试注册→配对→登录→刷新→登出→撤销→重启→断网；授权测试站点至少包含可迁移与不可迁移场景；保留匿名记录；`APPLIED` 只代表 Cookie API 成功，不代表网站登录成功 |
| P0-02 | **最终同步状态一致性 / 丢 ACK 恢复** | **已实现·待验收** | [Server 投递结果](../server/browser_sync/delivery.go)、[WSS 路由](../server/browser_sync/relay.go)、[扩展恢复](../browser/chrome-extension/src/session-sync.js)；有终态短期记录、`DELIVERY_STATUS` 查询、`UNKNOWN` 状态 | 真机断网/Worker 强退/重连演练；测 A 离线时 B 的 `APPLIED`、过期结果、重复请求，确认不误报失败、不将旧 ACK 覆盖新 ACK；检查存储清理与边界 |
| P0-03 | **多 Cookie 失败补偿 / 崩溃保护** | **已实现·待验收** | [Cookie 预检查和逆序补偿](../browser/chrome-extension/src/cookies.js)、仅元数据的恢复意图、`PARTIAL` 暂停/人工恢复；[故障注入测试](../browser/chrome-extension/tests/session.test.mjs) | 在真实 Chrome 注入中途失败/退出/网站并发刷新；确保无静默账号串换、可判别部分恢复、不会自动恢复有风险的写入；记录无法完全回滚时的手工处置流程 |
| P0-04 | **防重放序号恢复 / 重装语义** | **已实现·待验收** | [持久递增序号](../server/browser_sync/session.go)、来源侧 `SEQUENCE_CURSOR`、[序号与授权测试](../server/browser_sync/session_test.go)、[浏览器工具测试](../browser/chrome-extension/tests/lifecycle.test.mjs) | 真机重启、扩展本地数据清除、密钥丢失、规则撤销后的重新配对；证明新 Profile/设备不会擅自复用原设备身份；长时间运行、并发发送与序号上限测试 |
| P0-05 | **MV3 生命周期 / 异常网络恢复** | **部分完成** | [服务 Worker](../browser/chrome-extension/src/service-worker.js)含启动/Alarm；[WSS 连接单飞](../browser/chrome-extension/src/server-api.js)、[五分钟对账限频](../browser/chrome-extension/src/lifecycle.js)及对应模拟测试 | 真机测试 Worker 休眠/系统睡眠/网络切换/TLS 续连、离线期间 Cookie 改动最终一致性；增加失败退避与网络状态提示；验证不会无限重连或漏同步 |
| P0-06 | **限流、资源配额、DoS 防护** | **部分完成** | [Server Handler](../server/browser_sync/handler.go)已有注册 IP 限频、64 连接槽及单帧限制；新增 [分层限流](../server/browser_sync/limits.go)（直接 IP 握手、设备重连、设备帧速率、单规则传输/突发频率、邀请及查询）、[规则数量上限](../server/browser_sync/rules.go)（每设备最多 32 条非撤销规则）、[待确认投递上限](../server/browser_sync/delivery.go)（每规则 16、每来源 128），有对应单测 | 仍需按身份的总量配额、持久终态记录数量上限、跨实例协调、压力测试、多设备实际断网重连与撤销验证；窗口限流仅在单 Server 进程内生效 |
| P0-07 | **独立安全审查 / 密码学复核** | **未开始（正式评审）** | [加密信封](../browser/chrome-extension/src/session-envelope.js)、[邀请加密](../browser/chrome-extension/src/envelope.js)和 Server 验签已有开发实现与单测 | 独立审查现有 P-256 ECDH+HKDF+AES-GCM 组合，优先评估标准 HPKE (RFC 9180)；核对签名/密钥绑定、来源 pin、重放、时序、恶意网站、扩展权限、日志、认证降级与密钥轮换；漏洞关闭后复测 |

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

后续的记录格式：**日期 ｜ 实际代码 SHA/PR ｜ 本次变动及测试链接 ｜ 哪些 ID 状态改变或未改变 ｜ 新风险/遗留项**。保留历史记录，不覆盖旧条目。  
检查入口：[PR #194](https://github.com/timwai/RelayProxy/pull/194) · [Go CI](https://github.com/timwai/RelayProxy/actions/workflows/go-ci.yml) · [UI CI](https://github.com/timwai/RelayProxy/actions/workflows/ui-ci.yml)。
