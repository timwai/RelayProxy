> **2026-10-12 · 真实网站反馈“页面 IndexedDB 脚本异常，未确认读写完成”（修复候选、待最终 SHA CI）：** 检查扩展 `executeSiteIndexedDB` 后确认原实现把 `chrome.scripting.executeScript` 返回**空列表、缺少 `result`、或 `result:null`**统一报同一错误。这类返回可能发生于 Chrome 目标标签页刷新/导航、异步注入上下文终止或 Chrome 未能序列化执行结果；它**既不能证明失败，也不能证明成功**。修复不退回不安全的 MAIN world，继续限制 `ISOLATED` 且只向顶层 frame 注入。读取 `read` 与只读 `validate` 遇无结果先重新核对 `chrome.tabs.get(tabId)` 的 Origin/导航状态，安全地**至多重试一次**；`write` **绝不自动重复写入**，而是独立重新读取目标站点**完整快照**并按规范化结构比较，仅在精确匹配请求的写后目标时允许返回成功；否则保留 `PARTIAL_ROLLBACK` 保护交由原恢复事务/人工复核。浏览器权限/注入拒绝、跨站跳转、页面关闭或非顶层 Frame 改报明确原因。Node 回归增加空 read 安全重试、完整写后核验、不同写后内容拒绝重复写入、导航/Frame 拒绝及有界重试。**这是对已知空返回故障的保守恢复，不保证解释所有网站脚本异常；生产站点与实体 Chrome 仍需补录匿名阶段信息并验收。** 最新 SHA Go/UI CI 结果未出前不可称已彻底修复，PR #194 保持 Draft，不合并 main。

> **2026-10-12 · P0-07/09 IndexedDB MAIN-world 页面干扰面隔离（本 SHA CI 待验收）：** 复核 Chrome 扩展注入 API 后，改用 `chrome.scripting.executeScript({world:'ISOLATED'})` 实现 IndexedDB 全量读取、v3 回写及校验。ISOLATED 执行世界与站点 MAIN 世界隔离 JS 全局对象/原型，却仍访问该页面 **同 Origin 的真实 IndexedDB**（与同项目 Local/Session Storage 一致），减少网站自身脚本覆盖 `indexedDB.open/databases`、Promise、Blob、Date 等原语干扰扩展恢复逻辑的风险；不使用 MAIN world，也不新增网页消息桥接或宿主权限。Node 层对 `executeScript.world` 强断言 ISOLATED；真实 Chromium 双 Profile E2E 在来源及接收站点的 MAIN world **将 window.indexedDB 改成抛异常的 getter**，要求扩展仍可采集、恢复、验证该站点数据库（索引、记录、Date/Blob），并在恢复网页 API 后验证实际账号 Cookie/Storage/IndexedDB 和 E2EE 流程。**此修改仅隔离 JS 原型/全局篡改，不防止站点脚本并发合法修改同 Origin DB、服务端控制消息/密钥泄漏或真正恶意第三方扩展；属于有 CI 证据的安全面缩减，不可冒充完整独立安全审计。** 本轮最新 Go/UI CI（含生产 Handler 真实 WSS 直连/反向代理双轮）尚待核验，PR 保持 Draft，不合并 main。

> **2026-10-12 · P0-04 Chromium 不可提取私钥丢失/设备身份轮换验收（待最新 SHA CI）：** 将真实 TLS/WSS + 两 Profile 自动化再扩展一个关键负向用例：保持 Chrome Profile 的 `browserDeviceId` 配置、只删除扩展自身 IndexedDB `relayproxy-browser-sync` 的非提取 P-256 设备密钥库。调用正式 `getOrCreateIdentity` 必须生成**全新 Browser Device ID、全新签名及加密公钥**，不得复用已撤销旧设备 ID 或信任映射；随后使用正式 HTTP 注册，Server 必须响应 `pending`，未获新审批时真实 WSS AUTH 不得成功。该测试不会读取、打印或导出私钥内容，也不改变正常生产流程。配合 MV3 恢复 single-flight 的 Node 测试和完整 Chromium 直连/反代重启 CI，后续需在最新 SHA 验证。**仅覆盖删除扩展密钥库的受控场景，不等于已验收 Chrome 彻底卸载/签名升级/实体设备磁盘损坏或备份恢复**；P0-04/05 继续保留人工与安全复核门禁，Draft、不合并 main。

> **2026-10-12 · P0-05 MV3 恢复并发单飞（最新 SHA 待 CI）：** 检查发现 Service Worker 的 `initialize({force:true})` 和每分钟 `chrome.alarms` 都能并行调用 `restoreActiveSubscriptions`。原先只对底层 WSS CONNECT 做 single-flight；两个恢复任务仍可交叉查询规则/ACK、刷新相同 `lastReconcile` 并同时发出 `SYNC_REQUEST`，造成来源端重复会话、接收端恢复事务争用和短时限流。现添加纯逻辑 `singleFlight(task)` 包装整个恢复过程；启动、更新、Alarm 同时调用时共享同一 Promise，成功和失败都会释放执行状态，下一次 Alarm 可以重试；不保存凭据、Origin、Cookie 或 Snapshot，也不延长 ACK 保留期限。Node 回归覆盖并发合并、首个调用结果共享、异常后恢复；真实 Chromium 断线、完整 Profile 重启以及反向代理重复验收仍由 UI CI 覆盖，**不得用单测代替真实网络验收或断电/强杀压力验收**。此轮最新 HEAD 的 Go/UI CI 尚待核验，P0-05 仍不视为生产通过，PR 保持 Draft、不合并 main。

> **2026-10-12 · 真实 Profile 重启竞态验收强化（最新提交待 CI）：** [Go CI #38217847815](https://github.com/timwai/RelayProxy/actions/runs/38217847815) 成功， [UI CI #38217847806](https://github.com/timwai/RelayProxy/actions/runs/38217847806) 的直连及反向代理 Chromium 用例均已各自 success，但先前连续出现 `FAILED` 终态，暂不视为稳定通过。审查发现 E2E 重启 B 后将 Chrome onStartup **可能正用于恢复会话的网站标签页主动关闭**，这是测试自己制造的危险干扰：现改为复用现有站点 Tab，仅在没有站点 Tab 时创建，绝不主动关闭正在恢复的标签页。为排除偶然通过，UI CI 直连及 HTTPS 反向代理各**连续运行两轮**真实 Profile 重启、Cookie/Storage/IndexedDB v3、WSS/ACK/撤销 E2E；安全固定类别诊断保留。该改动仅消除测试脚本的人工干扰，仍需观察真实目标端在浏览器崩溃、关机及网络中断场景的竞态，保留 P0-05 未验收状态。不能借用前一个 SHA 的成功结果，PR Draft，不合并 main。

> **2026-10-12 · 重启后同步 FAILED 的安全类别诊断（待最新 CI）：** [UI CI #38217465800](https://github.com/timwai/RelayProxy/actions/runs/38217465800) 在**直连**测试也复现重启后来源端 `FAILED`、无 pending；这否定了“仅反向代理推送 ACK 丢失”的单一猜测。接收端可能在重复投递、标签页重建或 Cookie/IndexedDB 恢复时发生失败。临时 CI Fixture 新增只能在 loopback 随机 Bearer 下读取最多 20 条审计结果的接口，严格只输出 Store 白名单的 `outcome/errorCode` 枚举（`INDEXEDDB_FAILURE` 等），不暴露网站 Origin、配对 ID、设备 ID、密文、异常文本或 Cookie/Storage 内容。E2E 仅在断言失败后打印这些安全类别，结合已提交的源端 FAILED 状态定位具体原因；不能仅延长 timeout 或静默跳过失败。PR 保持 Draft，仍未合并 main。

> **2026-10-12 · HTTPS 反向代理 E2E 首轮失败，正在定位：** HEAD `f0f7cf0` 的 Chromium 单机直连、完整 Profile 重启、身份及 ACK 恢复均通过，但 [UI CI #38217082556](https://github.com/timwai/RelayProxy/actions/runs/38217082556) 的**代理转发**用例在重启接收端后再次发送会话，来源端等待 `APPLIED` 超时。当前无法判断是 B 端没有应用、Server 已收到终态但来源端没有收到推送、还是 Chrome Profile 竞态；不能用“已验证代理”替代真实结论。现在增强 E2E 的匿名状态追踪，仅显示 `APPLIED/CONFLICT/FAILED/RELAYED/UNKNOWN/ABSENT` 枚举和待处理标记，不记录 Cookie/Storage 内容或密文。根据后续 CI 输出继续修复；PR 保持 Draft、不合并 main。

> **2026-10-12 · P0-05/09 HTTPS 反向代理 WSS Upgrade 自动化补测（待本 SHA CI）：** 在 CI 专用 Go Fixture 中使用 Go 标准库 `httputil.NewSingleHostReverseProxy`，在第二个随机回环 HTTPS 端口构造**真实 HTTP/1.1 TLS 反向代理**，将 HTTP 注册、WSS Upgrade 和控制消息传给原有生产 Browser Sync Handler。上游 TLS 使用测试服务器自己信任的证书 Transport，**不绕过上游证书校验**，客户端 Chromium 仍仅对临时自签本机测试证书应用既有测试参数。保持浏览器看到的公网（本地模拟）`Host:port`，验证 WSS 签名必须绑定浏览器所配置的前端 Origin 而非后端监听端口。GitHub UI CI 对 **直连 TLS** 和 **HTTPS 反向代理 TLS** 分别运行同一套真实 Chromium A/B：pending 拒绝、管理员审批、WSS 签名、E2EE 配对/同步/ACK、断线恢复、完整 Profile 重启后重新授权订阅、撤销旧连接及重认证拒绝。**仅验证 Go httputil 回环代理，不代表用户的 Nginx/Caddy/公网证书/多层 CDN 环境已经验收，外部 WSS 403 仍需独立现场检查**。最新 SHA 的 Actions 未核验成功前不得标记完成；PR 继续 Draft，保留合并冲突，不合并 main。

> **2026-10-12 · P0-05/06 浏览器重启集成 CI 揭示限流误伤，已修复并待最新 CI：** 首次完整 Profile 重启测试的 UI CI [#38216401328](https://github.com/timwai/RelayProxy/actions/runs/38216401328) 在 Chrome B 重启并重新认证后、读取 `LIST_RULES` 时实际收到 `browser sync rate limit exceeded`，说明原 Handler 的全部控制消息共享 **30 次/10 秒**突发额度，合法的双端配对、会话发送、ACK 查询、Worker 恢复和测试读取可互相抢占预算。此次只对**只读控制消息**建立独立 **64 次/10 秒**短时窗口，并保留原控制写入/其他消息 **30 次/10 秒**、每设备总体 **240 次/分钟**、列表 **60 次/分钟**、状态查询 **120 次/分钟**及传输/邀请/网络写入的细分额度，不会取消任何准入校验。Go 单测覆盖只读/通用突发互不饥饿、读突发与分钟限额保留；真实 Chromium 完整关闭同 Profile 重启流程继续由 GitHub UI CI 回归。**原 CI 失败不能视为通过；最终必须再次确认 HEAD 的 Go/UI CI，遇到其他故障继续修复。** PR 仍 Draft，不合并 main。

> **2026-10-12 · P0-04/05 Chrome 完整 Profile 重启回归（待最新 SHA CI）：** 基于已通过的真实 RelayProxy TLS/WSS + Chromium A/B + IndexedDB v3 E2E，新增接收端 Chrome **完整浏览器进程关闭**并在原持久化 Profile 目录重新启动的阶段（区别于此前只关闭 WSS）。重启必须维持同一个 Extension ID、设备 ID 和不可提取 P-256 公钥，持久化双方已确认的配对指纹；无需手动 `CONNECT_BROWSER`，由真实 MV3 `onStartup` / Alarm 恢复签名 WSS；重建目标 HTTPS/HTTP 测试网站标签页后再次接受真实密文快照，Cookie / Local Storage / 新标签页 Session Storage / IndexedDB 数据均须实际写入且两端 APPLIED，原在线撤销及重连拒绝仍须通过。**仅为 CI/Linux headless Chromium 受控正常退出/重开**，不是实体 Windows/macOS 休眠、突然杀进程、浏览器签名升级、跨机器或 24h soak；直到最终 SHA 的 Actions 全绿不得宣布此项已验收。PR 保持 Draft，不合并 main。

> **2026-10-12 · P0-09 WSS 审批与在线撤销负向验收追加（待最新 SHA CI）：** 真实 Chromium/生产 Handler 集成测试在设备只完成 HTTP 登记并处于 `pending` 时，直接尝试签名 WSS `CONNECT_BROWSER`，必须拒绝；管理员撤销**已连线的 B 浏览器**后，先不主动执行 Chrome 断开，直接通过仍保存的 WSS 发 `LIST_RULES`，必须拒绝旧会话继续控制，再验证断开后的再次 AUTH 仍被拒绝。这是对“注册不等于授权”“撤销不能只约束下一次连接”的真实协议回归补充。与上一轮 WSS 重连/ACK 故障注入一起进入最新 Go/UI CI，**不能将前一个 SHA 的 CI 通过结果代替此 SHA**；正式外网、多机、MV3 完整进程杀死和独立安全审查仍待验收，Draft 不合并 main。

> **2026-10-12 · P0-09 WSS/MV3 故障恢复补测（待本 SHA CI 核验）：** 在测试专用、随机回环地址且临时 Bearer 保护的 Go WSS Fixture 中添加 `drop` 故障注入，仅调用生产 Handler 的 `DisconnectDevice` 主动断开当前认证 WSS，**不撤销设备审批，也不向正式 Server 暴露新接口**。真实 Chromium A/B + MV3 测试新增：记录来源端 Chrome storage 中的**不透明投递 messageId**（不读取明文 Cookie、Storage 或快照）；一次真实会话 ACK 完成后，用同一 ID 模拟 MV3 错过服务器已持久化的终态回执；强制断开来源端 WSS，通过真正 `chrome.alarms` 事件自动签名重连，并要求 `DELIVERY_STATUS` 对账将 UNKNOWN 恢复为 APPLIED、清理 pending；再断开接收端 WSS，由 Alarm 自动恢复订阅，并验证第二次真实密文快照投递和双端 APPLIED，最后维持原有撤销拒绝校验。**这是故障注入与测试覆盖，不等于公网丢包、Chrome 浏览器完全退出、24 小时 Soak 或实体双机已验收**；最终成功与否须核对这三个文件对应最新 SHA 的 GitHub Actions，未过则不应关闭 P0-09。PR #194 保持 Draft，不合并 main。

> **2026-10-12 · P0-09 真实 Relay WSS + Chromium A/B 自动化首次成功：** 在 `browser/chrome-extension/tests/wss-fixture/main.go` 新建**仅用于 CI 的临时本机进程**（独立于发行代码）：使用正式 `server/browser_sync.NewStore` 和 `NewHandler`、`httptest.NewTLSServer`，在随机回环端口提供真正的 HTTPS/WSS；管理员审批/撤销仅通过另一随机回环端口的**独立临时 Bearer 口令**执行，不向正式 Server/Web 注册任何调试 API。新增 `browser/chrome-extension/tests/chromium-wss-e2e.mjs`：启动真实的两个隔离 Chromium Profile 和 MV3 Service Worker、配置仅供 CI 的自签本地 TLS 与最小本机网站权限；双方通过正式 HTTP `/devices/register` 进入 pending，再由测试专用管理员控制端批准同一身份下的 A 发送/B 接收权限；两端经正式 WSS AUTH_HELLO → AUTH_CHALLENGE → ECDSA AUTH_PROOF → AUTH_OK，查询双方公钥、发起**加密 v3 配对**、分别核对校验码、B 接受和 A 确认；A 的**真实 Chrome Service Worker**采集 Cookie/LocalStorage/SessionStorage/IndexedDB 并加密签名，Server 实际检验和中继不透明密文，B 的实际 Service Worker 解密与恢复，经测试网站 `/whoami` 检验保持登录及 IndexedDB 记录，B 通过 WSS 返回 `APPLIED` ACK，A 收到终态确认。测试最后管理员**撤销 B 设备**、B 断开并尝试重新 AUTH，必须拒绝。**首轮 [UI CI #38209422984](https://github.com/timwai/RelayProxy/actions/runs/38209422984) 中 `browser-sync-chromium` Job 成功**；该轮 Go/UI 总体仍有新增 Go Fixture `gofmt` 失败，已于后续提交纠正，须以最终 SHA 复检。测试只为本机自签证书启用测试进程的 Chromium 忽略证书参数，绝不进入发行构建。

> **边界与发布门禁：** 这已覆盖真正 Relay Server HTTP 注册、Store 管理员审批行为、签名 WSS、配对和 E2EE 会话端到端。**尚未覆盖**真实 Server Web 管理员登录/人工审批 GUI、Windows/macOS 跨实体设备的浏览器 Profile、外网 TLS 证书/反向代理 WebSocket Upgrade、断线丢包/延迟/熄屏恢复、网站设备绑定、QUIC/DNS/WebRTC、防止 MAIN world 页面注入干预、Chrome 扩展打包签名与长期运行压力。因此不能将此 CI 视为正式 P0 关闭，也不合并 PR #194 到 main。

> **2026-10-12 · P0-09 首次真实 Chromium A/B 自动化通过：** 新增 `browser/chrome-extension/tests/chromium-e2e.mjs`，GitHub Actions `browser-sync-chromium` 在两个独立持久 Chromium Profile 中加载真正的 MV3 扩展，并访问只监听 `127.0.0.1` 的测试网站；用**测试环境临时 Manifest 副本**仅对该本机网站赋予 Host Permission，不修改发行 Manifest 的用户点击授权模型。真实网页的 HttpOnly Cookie、全 Local/Session Storage 和 IndexedDB v3（数据/索引/Blob/Date）在 A 采集、ECDSA/ECDH/HKDF/AES-GCM 签名加密、由测试程序转交密文、在 B 验签/解密/恢复后通过真实网站 `/whoami` 验证 B 登录保活；错误密文与 B 新的独立账号覆盖均被拒绝。**UI CI #38208112840 的独立 Chromium 任务 success**（对应代码 `d19fedcc7f4369104423eb543c60df7fcd8cc19c`）。新增另一个独立 WSS CI 已覆盖真正 Server TLS/WSS 注册、测试专用管理员 Store 审批/撤销、加密配对、ACK 和 A/B 登录态；Linux headless 仍不替代实体 Windows/macOS 和生产 Admin Web 审批。

> **实际 Chromium 揭露并修复的 P0-09 问题：** (1) v3 配对被 IndexedDB 入口错误按 v2 拒绝；(2) Chrome 的 `scripting.executeScript` 可在注入函数抛错时提供 `null` 结果，原代码未识别，造成数据库根本未写入却误报成功；现在内部抛错通过有界错误对象返回并在扩展中强制检查，写后再次全量验证数据库/记录；(3) 不同 Chrome JS 环境对合法对象的属性顺序可能不同，原 `JSON.stringify` 字符串比较会发生**假冲突**；现对快照字段作规范化排序后比较，并以同样格式计算拥有权 HMAC。Chrome IndexedDB 通过 **MAIN world** 访问网站自身数据库，Web Storage 仍使用 ISOLATED world；仅在明确获批的网站 Origin 执行，依然存在页面脚本影响数据读取/执行的风险，**需要独立安全审查，不能仅凭 CI 放行**。Node 回归覆盖 v2 拒绝、v3 同意、稳定字段排序、记录实变检测和注入错误显式上报。

> **2026-10-12 · P0-09 真实 Chromium 双 Profile 自动化首轮：** 在代码核对时发现此前 IndexedDB v3 存在真实入口版本误判：`session-sync.js` 只在 `schemaVersion===3` 采集/恢复 IndexedDB，`validatePolicy` 也只允许 v3 授权该数据面，但 `captureIndexedDB` 与 `prepareIndexedDBRestore` 却硬编码要求 `schemaVersion===2`，导致真实 v3 快照总被拒绝。现修正为两者只接受经过双方确认的 **v3**，增加 v2 拒绝 / v3 入口实际调用 Node 回归。新增隔离的 **Playwright Chromium MV3 双 Profile CI**：测试临时副本仅为 `127.0.0.1` 测试站点预授权 Host，原始发行 Manifest 的可选授权流程不变；用两个持久化 Chromium Profile 和真实扩展 APIs，测试本地自有网站 HttpOnly Cookie 登录、全部 Local/Session Storage Key、IndexedDB v3 数据库/索引/Blob/Date、两个 Profile WebCrypto ECDSA+ECDH/AES-GCM 端到端加密与篡改、目标登录保持、独立账号冲突拒绝。此测试模拟安全地传递 E2EE 快照，**未覆盖真实 Server WSS 注册/设备审批、网站外网或实体 Windows/macOS Chrome**（这些仍需联调，不能把 Chromium 自动化等同 P0 实机全部验收）。**本轮最新 SHA 的真实 Chromium CI 尚待完成并核验**；原有 Go WSS 测试仍负责 Server 密文转发与 ACK 路由。

> **2026-10-12 · P0 并发准入与 ACK 撤销竞态加固：** 检查发现 `ClaimDeliveryReceiptWithCode` 原先只在写入前校验浏览器授权；设备、身份、Origin 或配对规则可能在检查后发生撤销。现将 **RECEIVED 中间回执与 APPLIED/FAILED/CONFLICT 终态写入**统一到 SQLite 事务，并在事务内重新核对双方设备状态/权限、共同身份的活动状态、规则为 active/paused 且双方批准和密钥确认、关联 Chrome Origin 未撤销；拒绝的 ACK 不会改变待处理记录或生成审计终态。保留“已登记投递在管理员暂停后可完成 ACK”，撤销则拒绝。新增 `server/browser_sync/concurrent_quota_test.go` 的并发压力回归：24 请求竞争 5 个 Origin 注册名额、20 请求竞争 4 个全局库存名额、18 个并发审批竞争 3 个名额、20 条并发配对竞争 7 个身份规则名额、36 个并发投递竞争单规则 16 个名额，并行终态回执、重复 ACK 拒绝、回执对账、Origin 撤销双阶段拒绝。Go CI 额外运行 `go test -race ./server/browser_sync -run '^TestBrowserSyncConcurrent' -count=5 -timeout=4m`，可重复检测配额不穿透和竞态。**代码 SHA `24b1bf28dc9920f762055e7e9078c012270a6368`：** [Go CI #38203711736](https://github.com/timwai/RelayProxy/actions/runs/38203711736) 与 [UI CI #38203711746](https://github.com/timwai/RelayProxy/actions/runs/38203711746) **均 success**，包括 Browser Sync 全包 `-race`、独立 5 轮并发 `-race`、完整 Go 回归、React/Chrome/Windows/macOS。**范围说明：** 测试基于单个 SQLite writer 的可重复短时并发，未替代真实多连接/多 Server、24h 持续负载、资源曲线、网络故障注入和 Chrome A/B 实机验收；P0-06 状态仍为部分完成，PR #194 保持 Draft 且不合并 main。

> **2026-10-12 · P0-06 未审批注册库存限额：** Browser Sync 为待审批设备建立两级配额：**全 Server 512 条 pending**、**每个 Chrome Extension Origin 64 条 pending**。在同一 SQLite 事务中复核 Origin 未撤销、公钥不可变及已绑定 Origin 不可更换，统计配额并执行设备插入、Origin 绑定和首次请求身份意向登记；超额 HTTP **429**（无孤儿设备/授权/身份记录），已有同公钥且同 Origin 的设备在满额时仍可重新注册，拒绝不合法公钥或更换 Origin。管理员撤销 pending 记录即可释放库存槽位；历史数据保留，不会无通知自动删除。增加状态和 Origin 索引，可幂等迁移；SQLite 与 HTTP 端到端测试覆盖全局/单来源限额、重注册、公钥替换、跨 Origin 锁定、撤销释放、无残留及索引迁移。**本 SHA CI 尚待确认；单 Server 首发仍需要容量压测、安全审查、真实 Chrome 双端测试，跨实例配额暂不支持。**

> **2026-10-12 · P0-06 设备与规则的身份级预算（本轮）：** 管理员审批浏览器设备时，同一已认证身份最多允许 **32 台已批准设备**，限制在数据库事务内执行，覆盖直接 `Approve` 和 Chrome Origin 登记后的 `ApproveEnrollment` 两条路径；超限时 Admin API 返回 HTTP 409，申请保持 pending，撤销设备后自动释放一个审批名额。来源与目标分别复核为已批准、权限有效、属于同一身份后，创建新配对的事务再限制该身份最多 **128 条非撤销的配对规则**（包含 offered/active/paused），叠加原每台设备最多 32 条的限制，撤销规则后释放容量。身份级限制不采信申请者自行提交的 requestedIdentityId 作为批准配额；新增多设备/边界/撤销释放测试。此轮只约束**已批准设备与非撤销规则**，公开未审批申请的单独库存/速率治理、跨 Server 实例共享限额、压力测试仍待开发和验收。**以上是代码实现状态，当前 SHA 的 Go/UI CI 和真实 Chrome 双端验收需独立核对。**

> **2026-10-12 · P0-06 数据库恢复窗口配额（实施中）：** 原有每规则最多 16、每来源最多 128 条**待处理**投递限制无法约束已完成 ACK 在 15 分钟恢复窗口内的总量。现于 `RecordDelivery` 的 SQLite 事务中，先复核双方设备/身份/能力与有效配对，再合并统计**未过期待投递+终态记录**；设定每来源最多 **2048**、每身份最多 **4096**、全 Server 最多 **32768** 条占用。达到上限时拒绝新快照，**不提前清除其他设备未过期 ACK**，已登记的 ACK 仍能落库与查询，过期记录在下一次正常入库时清理。新增来源/到期索引可幂等迁移现有 DB；新增 SQLite 夹具针对来源、身份与全局上限、恢复与幂等索引回归。**当前提交需本 SHA 的 Go CI、Browser Sync -race、UI CI 验证；跨实例共享配额、设备/规则总量预算、突发/故障压力与真实 Chrome 仍待补齐，P0-06 不能标已验收。**

> **2026-10-12 · 真实 CI 验收与修复（本轮）：** 补上 Go/UI 工作流的 `feat/**` 推送触发与按分支取消过期运行。GitHub Actions 首次实际运行后，按 `gofmt` 原始差异修复 16 个 Go 文件；修复 `server/browser_sync/delivery.go` 中变量重复声明导致的 Go vet 错误；修复 Chrome IndexedDB 依赖的 `tabFor/execute` 未导出、过时的站点选择/双层审批测试；修复设备活动统计 `strftime(MAX(created_at))` 导致最后成功时间为空，改为从类型化审计时间字段折叠事件；按“Chrome Origin 自动登记、只审批设备”的最新设计调整已过时的扩展清理测试。**代码 SHA `5bb40b8215c1264308c434662f269b279714ae99` 的 Go CI 与 UI CI 均已 success：** Go 格式、模块、vet、完整 `go test ./...`、原有核心路径竞态、基准冒烟、Server/Wails React 测试与构建、Chrome 扩展 Node 测试、UI 全量 Go 回归以及 Windows/macOS 桌面 CI 全部通过。现又在 Go CI 增加 **`go test -race ./server/browser_sync ./server/api`** 专门覆盖浏览器同步与管理员 API，须按最终代码 SHA 确认结果。**PR #194 与 main 当前仍存在冲突（mergeable_state=dirty），且真实双端 Chrome Cookie/Storage/IndexedDB 登录保活、安全复核及 DNS/QUIC/WebRTC 尚未验收；继续 Draft，不合并 main。**

> **2026-10-12 · 一次性覆盖请求强绑定（P1-03 安全补强）：** Chrome B 在“允许本次覆盖并请求同步”时生成随机 UUID `transferRequestId`，与当前规则、站点、2 分钟过期时间共同存储一次性许可。B 发出的 `SYNC_REQUEST` 携带该 ID；Server 只向同一身份、有效双端批准的 A 转发，并校验 UUID 格式；A 生成密文快照时将 ID 放入 ECDSA 签名和 AES-GCM AAD 的末尾（**可选字段：普通无请求 ID 的 v1 快照签名头完全兼容旧版**），B 先验签/解密/序号检查，再仅当收到 **精确匹配 ID** 的合法快照时删除许可并允许覆盖。其他自动 Cookie 快照、旧快照、错误 ID、缺失 ID 不消费授权。来源 A 正在发送快照时收到 B 的用户请求，会暂存 token，在当前发送完成后优先发送该请求，不丢弃来源端的 Cookie 更新。扩展 Service Worker 限制含 ID 的请求必须来自 Options 原生设置页。新增 Go 线协议和 WSS 端到端、Chrome 加密篡改、一次性许可和 UI 契约回归；**18/18 静态集成检查通过，当前分支 Go/Node/UI CI 与真实双端 Chrome 仍须独立验收。** 兼容性边界：新 B 必须搭配支持 `transferRequestId` 转发的新版 Server 和 A；旧 Server 丢弃此字段会导致 B 无法获得覆盖许可，保持 fail-closed。单次覆盖不会扩大网站授权或站点权限。

> **2026-10-12 · Chrome 一次性账号覆盖与 MV3 恢复：** 已移除目标浏览器长期有效的“允许覆盖”勾选，改为在扩展 Options 页点击“允许本次覆盖并请求同步”，明确提示可能切换账号。服务端仍只传递 E2EE 快照；接收端仅在配对、来源密钥、站点与序号验证后使用当前规则+站点的 **2 分钟、单次消费**覆盖许可，且在 Cookie/Storage/IndexedDB 写入前删除许可；任何失败均不会自动续期。升级旧版本的持久布尔许可自动失效并在扩展启动/定时唤醒时清理；Options UI 发出的授权消息增加 sender URL 约束，失败的 SYNC_REQUEST 可离线取消许可。MV3 恢复拆分为每分钟 Alarm 的 **WSS 认证/在线监听恢复** 与每五分钟的周期快照请求：失败重连采用以 Server Origin 为作用域、持久化的 **1/2/4/8/15 分钟封顶退避**；原有待确认投递在重连成功后优先对账；WSS 握手增加 20 秒超时；手动成功连接、Server 变更清理退避；Popup/Options 显示自动重试状态。新增 Chrome storage mock、纯生命周期和 UI 契约回归测试。**这是代码交付而非完整验收：当前提交还需 npm test、Go/UI CI 和双端 Chrome 真实配对/冲突/重启/账号切换测试。** 注意单次许可适用于收到的下一份合法快照，现已通过接收端随机 transferRequestId 与来源端签名快照绑定，一般后台自动快照不能消费授权；仍待真实 Chrome 与恶意重放/并发验收；需要后续严格 request-bound 功能才可完全避免该竞态。

> **2026-10-12 · P1 管理增强第三轮（规则确认时间 / 审计导出 / 错误分类）：** 新规则单独持久化 `target_approved_at`（B 端批准）和 `activated_at`（来源端密钥指纹确认），来源批准时间使用规则创建时间；暂停/恢复/撤销不会覆盖这些时间。通过幂等 SQLite 列迁移兼容旧数据库，旧配对缺失的历史确认时间保持空值，不伪造。管理员只读审计新增带筛选的 CSV 导出（固定列、公式注入防护、`no-store`、最多 5000 条）和明确确认的清理；清理在事务内删除历史后插入 `audit_cleared` 操作者留痕。Chrome 失败回执现在只发送 `CONFLICT / PARTIAL_ROLLBACK / NETWORK_UNAVAILABLE / REPLAY_REJECTED / DECRYPT_FAILED / STORAGE_FAILURE / INDEXEDDB_FAILURE / COOKIE_FAILURE / UNSPECIFIED` 固定错误码；Server 拒绝任意异常文本并与投递结果原子审计，Web 展示友好诊断说明。新增 Go SQLite、管理 API、WSS 集成、Node/React 契约回归；**16 项静态集成检查通过，另有 SQLite SQL 冒烟验证，但当前 SHA 的完整 Go/Node/React CI、真实 Chrome A/B 仍未获验收，不得合并 main。** P1-01 / P1-06 基础功能更完整，P1-03 的一次性覆盖确认仍未实现。

> **2026-10-12 · P1 管理增强第二轮：** Server 管理 API 在保留旧版无参数规则列表数组响应的同时，增加 `limit/offset/identityId/status/q` 服务端分页及过滤，Server Web 规则页可按身份/状态/设备搜索并加载更多，不受旧列表一次 500 条限制。审计增加 `actor_id` 操作者标识（只从已认证管理员会话上下文写入，不接收浏览器或请求 JSON 自报），兼容旧 SQLite 表结构的幂等升级，并在审计页显示操作者。修复管理员暂停与投递竞态：新会话进入投递数据库时再检查配对处于 active；暂停前已登记的会话允许终态 ACK 与来源查询，不允许新会话；撤销后仍禁止回执查询。新增管理 API、SQLite 迁移、状态切换与分页回归测试，15 项静态集成契约核对通过。**最新 SHA 的 Go/Node/React CI 和真实 A/B Chrome 尚未通过可确认验收，PR 继续 Draft。**

> **2026-10-12 · Server Web 浏览器同步管理增强（当前 PR #194）：** 新增管理员独占规则查询和暂停/恢复/撤销接口（`/admin/rules`），规则恢复重新核验双端设备/身份/能力/来源禁用；Server Web 以“浏览器设备、配对规则、同步记录”三个视图显示在线 WSS 状态（15 秒轮询）、最近成功应用时间及终态计数。新增有界、脱敏持久审计：设备批准/撤销/删除、规则暂停/恢复/撤销、同步终态结果，同一数据库事务写入，最多保留 **30 天/5000 条**；支持类型/设备过滤及游标分页，绝不记录网站 Origin、Cookie、明文或密文快照。新增 `server/browser_sync/admin_test.go`、UI 静态契约测试及 WSS 历史来源撤销回归。**最新提交尚未获得 Go/Node/React 的本 SHA CI 通过记录，也未做真实 Chrome A/B 联调，P0 发布门禁不变。** P1-01/02/06 从原“部分完成”推进为“基础管理功能已编码，待 CI/真机验收，仍有细项”。

> **2026-10-12 · PR #194 设备单独审批：** Chrome 现在直接调用设备注册接口，提交身份 ID、设备名称与公钥；Extension ID 仅作为 Origin 绑定与历史禁用依据，不需要独立管理员审批。Server Web 仅保留浏览器设备待审批/已批准/已撤销流程和历史禁用来源的清理入口。WSS 需要已批准设备签名和与注册一致的扩展 Origin，且每条控制消息重新检查设备撤销与历史禁用。本轮已提交 Go/Node 契约测试和 WSS 不同来源签名冒用拒绝的集成测试；**当前容器无法访问 GitHub 克隆仓库运行全量测试，CI / 实际 Chrome A/B 验收仍待确认，PR 保持 Draft，不合并 main。** 历史阶段性进度条目里的“两层审批”均为旧设计，不代表当前分支的授权流程。

> **2026-10-11 IndexedDB v3 新增：** 以下历史进度条目中“不支持 IndexedDB”的说法属于当时版本的范围，不代表最新功能分支。v3 只通过**新建双端确认的配对**开启同源 IndexedDB，不自动扩大已有 v2 授权；暂未进行真实浏览器验收。

> **当前实现（2026-10-11）：** Browser Sync 在 `browser_sync.enabled: true` 且 Server TLS/Admin TLS 正常时自动复用既有 TLS/TCP 中继端口。**`browser_sync.network_proxy` 整块已废弃**，不再支持单独 `listen`、`advertise`、`allowed_exit_ids`、独立监听和 Server Web 出口配置表单。B Chrome 通过已配置的 Relay HTTPS Server Origin 访问网关；网络出口仍需同身份、`proxy.exit`、Agent 本地明确 opt-in 和 A/B 双端网络绑定同意。以下含旧配置的历史条目仅为进度留档，不再适用于现行版本。相关功能尚待 CI 和真实设备验收。

# RelayProxy Browser Session Sync — 开发进度与发布准入清单

> **持续维护的进度文档（Living Tracker）**  
> 首次核对：**2026-10-10** ｜ 跟踪分支：`feat/browser-session-sync` ｜ [Draft PR #194](https://github.com/timwai/RelayProxy/pull/194)  
> 首次代码核对基准：[`2050d08e`](https://github.com/timwai/RelayProxy/commit/2050d08e912a8defbbd4cd767dd101d8f4118b98)  
> 基准自动化结果：[Go CI](https://github.com/timwai/RelayProxy/actions/runs/38062048992)、[UI CI](https://github.com/timwai/RelayProxy/actions/runs/38062049049)、[Android Client](https://github.com/timwai/RelayProxy/actions/runs/38062049009) —— 三项均为 **success**。  
> **发布状态：开发预览 / 不可视为生产就绪；Browser Sync 服务端默认关闭；PR 尚未合并 main。**
>
> 2026-10-10 基准之后的新增提交，必须查看**相应 SHA** 的 CI 检查；不能沿用基准成功结论。

## 最新状态快照（2026-10-11 23:12 +08:00，按当前分支 HEAD 核对）

| 核对项 | 结果 |
|---|---|
| 开发分支 / 最新代码提交 | `feat/browser-session-sync` / [`e9aa691`](https://github.com/timwai/RelayProxy/commit/e9aa691963de926af3f7628f138fdbb26da600a5)（修复 IndexedDB 无效日期/共享对象引用的静默失真）；此前 `b504837` 的成功 CI 仅对当时 SHA 有效 |
| PR 状态 | [#194](https://github.com/timwai/RelayProxy/pull/194)：**Open / Draft / 未合并 main** |
| 当前新范围自动化 | **最新 `e9aa691` 无可确认的 Go/UI/Android CI 结果，不能声称通过。** 截至本次查询，GitHub Actions 最近可见运行创建于 **2026-10-11 07:20 UTC**（例如 [`d9b1698` Go/UI/Android](https://github.com/timwai/RelayProxy/actions?query=branch%3Afeat%2Fbrowser-session-sync) 均 success）；另 [`c4392cb` Go/UI/Network/Android](https://github.com/timwai/RelayProxy/commit/c4392cbb1f) 四项 success，但这些 SHA **均早于**当日 13:52–13:56 UTC 的 IndexedDB v3 新代码。需在最新 HEAD 上触发并核验 Go/UI/Android，且 UI CI 执行进度文档同步检查。 |
| Cookie 同步 P0 | P0-01–09：**5 项已实现·待验收（02/03/04/08/09）、2 项部分完成（05/06）、2 项未开始正式验收（01/07）**；P0-08 单独为已实现·待真机/安全验收。**0 项已验收，当前不放行** |
| Cookie 同步 P1 / P2 | P1：**4 项部分完成、1 项被新全量授权流程替代（P1-04）、1 项未开始（P1-05）**；P2：**2 项已实现·待真机/安全验收（P2-01/02）、2 项未开始（P2-03/04）**。IndexedDB v3 新增发布验收项 P0-09，不能以代码完成计为已验收 |
| A Agent → B Chrome 网络绑定 | NB-01–04 已实现·待真机/安全验收；NB-05 审计与正式准入未开始 |
| B 本机 Agent（当前已入分支） | NB-06/07/09/10：无鉴权、无 Agent 域名白名单的回环入口；新增普通 HTTP Forward、任意合法 TCP CONNECT 目标/端口及 IPv4/IPv6/内网/单标签域名；复用 Agent **DIRECT/PROXY/REJECT**，Chrome 侧增加与 Cookie 配对独立的全 Profile 代理开关；代码/Go/UI 自动化已通过，真实 Chrome/Agent 尚未验收 |
| 最新“完整 Chrome 本机 Agent 代理”需求 | **已集成·待真机/安全验收**：原草稿 [`b7546cf`](https://github.com/timwai/RelayProxy/commit/b7546cf4bb85de249763672d5d48a5a17cfaba57) 的业务改动已迁入功能分支 [`eaf13e8`](https://github.com/timwai/RelayProxy/commit/eaf13e83029eb0272eda81f99f727e1b07eeef02)，经格式与协议测试修复形成 [`b504837`](https://github.com/timwai/RelayProxy/commit/b504837a113a8e312c44335b9077fe3f2e80691e)；Go/UI/Network CI success，不等于全协议浏览器网络接管或 P0 验收 |
| 上线结论 | **不放行**。最新 IndexedDB v3 尚无对应 HEAD CI；P0-01/05/06/07/08/09、NB-05/08/11 仍需真实 Chrome/Agent、失败回滚、安全审计和跨平台发布验收。PR #194 保持 Draft，未合并 main |

### 核心能力边界（不可混淆）

**普通 Chrome 网络代理**的最新目标是无需 Cookie 配对即可使用 B 本机 Agent，代理目标不额外限定 HTTPS 443、公共 DNS 域名、IP 类型、端口或 Agent 的 DIRECT 动作；由 Agent 现有的分流/出口权限及用户配置决定。**Cookie 会话同步**是另一项敏感能力，仍需要设备授权、双端加密、规则同意和安全作用域校验。取消“浏览器网络代理的人为限制”**不代表自动取消 Cookie 读取权限或站点授权**。A 远端共享出口仍须沿用独立身份授权和临时认证，不能变成匿名开放代理。

此外，`chrome.proxy`/PAC **不是系统 VPN**；UDP/QUIC/WebRTC、系统 DNS、部分内建地址绕过和其他扩展的代理覆盖需要实机核对。**“支持任意有效 TCP CONNECT”≠“能够透明接管 Chrome 的所有协议”**。本机无鉴权回环监听可被同机其他进程使用，放宽到内网地址后风险更高，必须经过独立安全审查。

### 最近交付与下一步顺序

1. **先为当前 HEAD `e9aa691` 补齐 Go/UI/Android CI**（必要时单独运行 Network Acceptance）；验证新增 `indexeddb-sync.js`、v3 双端加密与回滚测试，核对 CI 的“进度文档同步”检查。之前 `b504837` 已集成普通 HTTP/CONNECT/Chrome 全局 Agent 代理，旧草稿 `b7546cf` 仅作历史参考；不得再把整合草稿列为未完成工作。
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
| P0-06 | **限流、资源配额、DoS 防护** | **部分完成** | [Server Handler](../server/browser_sync/handler.go)已有注册 IP 限频、64 连接槽及单帧限制；新增 [分层限流](../server/browser_sync/limits.go)（直接 IP 握手、设备重连、设备帧速率、单规则传输/突发频率、邀请及查询）、[规则数量上限](../server/browser_sync/rules.go)（每设备最多 32 条非撤销规则）、[待确认投递上限](../server/browser_sync/delivery.go)（每规则 16、每来源 128），有对应单测 | 已为未过期待投递+持久终态记录增加来源 2048/身份 4096/全局 32768 的事务配额，并预留 ACK 恢复容量；已新增已批准设备每身份 32、非撤销规则每身份 128 的事务配额；已增加未批准设备每 Server 512、每 Extension Origin 64 条库存配额及事务原子登记；已新增并发注册/审批/规则/投递/ACK 的 5 轮 -race 限额回归和 ACK 事务内撤销复核；仍需跨实例协调、实际多连接与 24h 压力验收、资源曲线、多设备真实断网重连与撤销验证；窗口限流仅在单 Server 进程内生效 |
| P0-07 | **独立安全审查 / 密码学复核** | **未开始（正式评审）** | [加密信封](../browser/chrome-extension/src/session-envelope.js)、[邀请加密](../browser/chrome-extension/src/envelope.js)和 Server 验签已有开发实现与单测 | 独立审查现有 P-256 ECDH+HKDF+AES-GCM 组合，优先评估标准 HPKE (RFC 9180)；核对签名/密钥绑定、来源 pin、重放、时序、恶意网站、扩展权限、日志、认证降级与密钥轮换；漏洞关闭后复测 |
| P0-08 | **Chrome 本机 Agent 完整网络代理能力与兼容性验收（新增）** | **已实现·待真机/安全验收** | [Agent 本机入口](../agent/app/browser_local_proxy.go)已支持 HTTP 正向代理、任意 TCP CONNECT Host/IP/端口；[RoutingDialer](../agent/routing/dialer.go)委托现有 DIRECT/PROXY/REJECT；[Chrome PAC](../browser/chrome-extension/src/network-binding.js)支持与 Cookie 配对无关的全 Profile 本机模式；[协议单测](../agent/app/browser_local_proxy_test.go)、[Chrome PAC 测试](../browser/chrome-extension/tests/network-binding.test.mjs)纳入 Go/UI CI | 仍需真实 Windows/macOS Chrome 的 HTTP/HTTPS/WebSocket、IPv4/IPv6、Agent 异常、回环绕过、DNS/WebRTC/QUIC 行为验收，以及无鉴权本机代理的安全/资源审计。Chrome proxy API 不等于透明 VPN，不得声称全协议覆盖 |

| P0-09 | **IndexedDB v3 新增敏感会话面验收** | **已实现·待 CI/真机/安全验收** | [`1334fd4`](https://github.com/timwai/RelayProxy/commit/1334fd4d8f) 新增 v3 双端同意、同 Origin IndexedDB 全量采集/恢复、Schema/记录校验、冲突保护与多库回滚；[`e9aa691`](https://github.com/timwai/RelayProxy/commit/e9aa691963de926af3f7628f138fdbb26da600a5) 对无效日期和别名对象图 fail-closed；[实现](../browser/chrome-extension/src/indexeddb-sync.js)、[模拟测试](../browser/chrome-extension/tests/indexeddb.test.mjs) | 已修复 v3 入口误判、Chrome 注入静默错误和跨上下文 JSON 顺序假冲突；Chromium 双 Profile 真实 Chrome API 自动化首次通过，最终 SHA 的全部 CI 待核验；仍需 Windows/macOS 实体 Chrome A/B 测试数据库版本/索引/Blob/非导出 CryptoKey/别名对象、打开中的 DB、断线、崩溃/部分回滚、旧 v1/v2 规则不自动升级、大小配额与双端确认。IndexedDB 不保证所有网站能迁移登录，未验收前不得发布 |

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
| IndexedDB v3：多库、版本冲突、Blob/非法日期/别名引用、部分回滚 | 待验证 | 待验证 | 旧配对不可自动扩权；`PARTIAL_ROLLBACK` 必须人工确认 |

## 二、正式版本应补齐（P1）

> 本节是正式版产品化需求，部分可以在 P0 验收期间并行推进，但不得以 UI 完成为由放行 P0。

| ID | 交付项 | 当前状态 | 现有能力 / 代码证据 | 下一步与验收条件 |
|---|---|---|---|---|
| P1-01 | **完整规则管理** | **部分完成（管理主流程已编码·待 CI / 真实体验验收）** | [管理员规则元数据及安全状态操作](../server/browser_sync/admin.go)、[Server Web 三视图](../server/web/frontend/src/BrowserSync.jsx)、[独占 Admin API](../server/api/browser_sync.go) 已支持配对列表、状态/设备搜索、暂停/恢复/撤销；恢复前检查双端已授权和身份有效，不解密网站策略。 | 已补齐新规则双端同意与生效时间戳及历史列迁移；下一步：实体 Chrome 与管理员操作联调、历史规则无法追溯的确认时间说明；不得让管理员直接修改加密站点范围或绕过双方审批。 |
| P1-02 | **设备在线/最后同步/失败统计** | **部分完成（基础状态和统计已编码·待 CI / 真机验收）** | [WSS 在线连接快照](../server/browser_sync/relay.go) + [终态元数据聚合](../server/browser_sync/admin.go)；Web 设备列表展示在线/离线、最近成功应用及成功/失败/冲突次数，每 15 秒轮询在线状态。 | 仍需持久最后上线/离线时间、端到端延迟/失败原因分析、在线规模与性能压测；不能把加密载荷“APPLIED”误报为网站登录成功。 |
| P1-03 | **冲突诊断与一次性确认** | **部分完成** | 已有 `CONFLICT`、持久覆盖开关、`PARTIAL` 手工恢复 | 固定枚举错误码与 Web 只读诊断已编码；仍需单次覆盖确认已绑定签名请求 UUID（限时、单次消费）；仍需真实 Chrome 竞态和兼容性验收、账号切换警示、权限变化时取消等待中的恢复 |
| P1-04 | **Cookie 名称选择器（需求被新流程替代）** | **需求调整·不再单独实施** | [v2/v3 全量站点 Cookie 采集](../browser/chrome-extension/src/cookies-all.js)、[站点授权选项页](../browser/chrome-extension/src/options.js)已取消新配对手填 Cookie 名称，改为双端明确同意当前站点的全量 Cookie/Storage；旧 v1 名称白名单仍可验证 | 不再按旧设计增加逐名称选择器；需真机验证全量授权提示、Cookie 范围、目标冲突保护、撤销后重配对及 v1→v3 不自动扩权。若重新提出逐名称选择需求，应另开任务评审 |
| P1-05 | **扩展安装、固定 ID、升级与发布** | **未开始（发布流程）** | [MV3 manifest](../browser/chrome-extension/manifest.json)和[开发者加载说明](../browser/chrome-extension/README.md)存在 | 正式扩展签名/固定 ID（Server Origin 白名单所依赖）、版本兼容策略、密钥和存储迁移、权限变更提示、打包/回滚与升级验证 |
| P1-06 | **专用审计、规则指标与排错页** | **部分完成（持久脱敏审计已编码·待 CI / 安全验收）** | [审计表及事务写入](../server/browser_sync/admin.go)只记录授权、规则操作及 APPLIED/FAILED/CONFLICT；`30 天/5000 条` 保留上限，Server Web 可按事件和设备筛选、游标分页。 | 已新增管理员操作者归属、固定错误码、带筛选 CSV 导出及事务化清理留痕；仍需跨事件诊断关联、分页压力与生产安全/故障验收；不记录 Cookie/Token/密文策略。 |

## 三、后续增强功能（P2）

> 不作为当前 P0 发布的强制依赖；每项都需单独威胁建模和显式授权。

| ID | 交付项 | 当前状态 | 下一步 / 范围限制 |
|---|---|---|---|
| P2-01 | 复杂 Cookie Scope（父域、不同 Path、CHIPS/分区） | **已实现·待真机/安全验收** | [v2/v3 全量 Cookie](../browser/chrome-extension/src/cookies-all.js)支持授权 HTTP(S) Origin 可达的父域 Cookie、非根路径、hostOnly 和受约束的第一方 CHIPS 分区；已有模拟测试，但必须真实 Chrome 119+ 验证多 Path、父域、分区键、冲突/删除归属和回滚，禁止跨站自动扩权。**历史“未开始”不再适用** |
| P2-02 | 全量 Local/Session Storage 同步（替代旧“指定 Key”需求） | **已实现·待真机/安全验收** | [Web Storage](../browser/chrome-extension/src/web-storage.js)已实现授权 Origin 全部 Local Storage 与**单标签页** Session Storage 的采集/恢复、HMAC 归属与部分失败保护；不需要手动 Key，模拟测试已添加。仍需真实网站登录/登出、标签页关闭/重启、覆盖冲突、4 MiB 限额与 Worker 中断测试；不宣称覆盖所有标签页 |
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

| 2026-10-11 | Chrome 原生路由：进程规则上下文补全 | 专用 `browser_sync.local_proxy` 的 `http.Server.ConnContext` 现在向 `RoutingDialer` 传递真实 Chrome→Agent 入站 TCP socket 的 `Source/Local` 元数据及 `browser_chrome` 入口标签，供 Agent 现有进程归属查找和命中进程级分流规则；覆盖 HTTP Forward 与 CONNECT，新增实 TCP 回归测试 `TestBrowserLocalProxyPreservesChromeSourceTuple`，不伪造进程名或突破原有 ACL。[Go CI #38109151334](https://github.com/timwai/RelayProxy/actions/runs/38109151334)、[UI CI #38109151283](https://github.com/timwai/RelayProxy/actions/runs/38109151283)、[Network Acceptance #38109151245](https://github.com/timwai/RelayProxy/actions/runs/38109151245)、[Android CI #38109151365](https://github.com/timwai/RelayProxy/actions/runs/38109151365) **四项 success**，包含 Windows/macOS 桌面检查。**仍缺实体 Chrome/Agent 的进程名规则与协议验收**。 | P0-08 / NB-10 继续已实现·待真实 Windows/macOS 进程名规则验收；NB-11 未开始，原 Cookie 同步门槛不变 | 新提交 CI 待核对 |

**本轮最终功能代码基准**：[e8307a5](https://github.com/timwai/RelayProxy/commit/e8307a58bef201d216cb48f1df147b0d872de0a3)：[Go CI](https://github.com/timwai/RelayProxy/actions/runs/38109151334)、[UI CI](https://github.com/timwai/RelayProxy/actions/runs/38109151283)、[Network Acceptance](https://github.com/timwai/RelayProxy/actions/runs/38109151245)、[Android CI](https://github.com/timwai/RelayProxy/actions/runs/38109151365) **全部 success**。P0-08/NB-06/07/09/10 保持「已实现·待真机/安全验收」，NB-11「未开始实体平台测试」，P0-01–07、P1/P2 其余状态不变。Chrome 扩展不能强制接管所有 UDP/QUIC/WebRTC/DNS；生产发布不放行，PR #194 不合并 main。

| 2026-10-11 | 完整 Chrome 代理兼容性：大 Cookie 头与长连接 | 本机入口将 HTTP 请求头上限恢复为 Go 默认（1 MiB），避免旧的 8 KiB 限制拒绝正常网站；移除 CONNECT/WebSocket 无条件 30 分钟绝对中断，连接由浏览器与 Agent 关闭管理，继续保留回环监听、8 秒 Header Timeout、最多 64 条并发及 Agent 路由 ACL。新增携带 16 KiB Cookie 的 HTTP Forward 回归。无鉴权、内网可达与空闲连接资源仍需 P0 真实压力/安全审查 | P0-08、NB-06/10 已实现·待真机验收；原 P0-01–07、P1/P2 状态不变 | 新功能代码需独立 CI |

**2026-10-11 Chrome 本机代理最终兼容性代码基准 [`5092c6f`](https://github.com/timwai/RelayProxy/commit/5092c6f8afdddd75900ecd6552dc6ef8435fac62)：**
[Go CI #38109445468](https://github.com/timwai/RelayProxy/actions/runs/38109445468) **success**（包含 16 KiB Cookie 头转发测试、竞态检查）；
[UI CI #38109445466](https://github.com/timwai/RelayProxy/actions/runs/38109445466) **success**（Chrome 扩展和 Windows/macOS 桌面构建）；
[Network Acceptance #38109445519](https://github.com/timwai/RelayProxy/actions/runs/38109445519) **success**；
[Android CI #38109445479](https://github.com/timwai/RelayProxy/actions/runs/38109445479) **success**。
当前 P0-08、NB-06/07/09/10 均是**已实现·待实体 Chrome/安全验收**；NB-08/11 的 GUI 入口、安全审计和真实浏览器协议兼容仍未完成。**绝不能把本机无鉴权回环代理的 CI 通过等同于 WebRTC/QUIC/DNS 无泄漏，或把本机 TCP 代理当成 OS VPN。** Cookie P0-01–07、P1、P2 其他状态不变；PR #194 保持 Draft，不合并 main。

| 2026-10-11 | Linux Server 管理页 Browser Sync 显示「服务端响应无效（HTTP 404）」 | 确认 `server/web/frontend/src/api.js` 在服务端 HTTP 404 为 HTML/纯文本时丢失 `error.status`，导致管理页 `BrowserSyncAdmin` 的「未启用」状态识别分支失效。修复为解析 JSON 失败也保留 HTTP status，并在 Browser Sync 管理页说明旧 Server、`browser_sync.enabled: false`、Admin HTTPS/TLS、扩展 ID 和反向代理路径等排查项；补 UI 404/200 HTML 响应测试与 Linux Server 示例配置注释 | Browser Sync P0/P1 功能状态不变；Linux 部署诊断体验修复仍须按新 SHA CI 复核、用户本机部署确认 | 本次仅功能分支，正式发布仍不放行 |

## 2026-10-11：用户实测结果与五项产品改进（本轮阶段性实施）

用户已验证 **10/12** 项：扩展安装、Agent 监听、HTTPS/HTTP 代理、PROXY/DIRECT/REJECT、进程匹配、全局开关、非 443、Agent 断线恢复、A/B Cookie 配对均通过；尚缺 **B 实际网站登录/冲突/登出恢复**、**DNS/QUIC/WebRTC 协议边界实测**。真实通过项来自用户现场反馈，不能替代后两项。

| 新需求 | 本轮分支状态 | 可复核实现与剩余工作 |
|---|---|---|
| 不手动填 Cookie 名称，同步所选网站全部 Cookie（含所有作用域） | **未交付／待专门兼容与安全评审** | 现有同步仍是经过授权的精确 Cookie 名称、Host-only、Secure、根路径范围。不得在未完成跨路径/父域/分区 Cookie 兼容、已存账号冲突、删除归属和回滚测试前宣称支持所有 Cookie |
| 不手动填 localStorage/sessionStorage Key，完整同步 | **未交付／待专门兼容与安全评审** | 必须加 Chrome scripting 与准确站点权限，选定来源/目标标签页；sessionStorage 是标签页级别。增加双端授权、加密载荷大小限额、覆盖冲突保护、崩溃/部分写入补偿后方可放行 |
| 允许的网站从界面选择而不是输入 URL | **已实现·待真实 Chrome 验收** | Chrome 弹窗从已打开的 HTTPS 标签页按 Origin 去重供选择，点击授权后请求该站点的 optional host permission；不自动读取其他站点 Cookie |
| Server 浏览器功能在 Web 全量配置 | **已实现·待 CI/真实 Linux 验收** | Server 配置 DTO + Web 设置组加入 browser_sync.enabled、兼容旧白名单 extensionIds、network_proxy.listen/advertise/allowed_exit_ids；通过原有 revision/CSRF/admin 权限保存 YAML，标注需要重启。注意启用 Browser Sync 要求 Server 原生 Admin TLS，不能仅通过代理终止 HTTPS |
| 扩展 ID 自动上报、管理员授权，不手工填写 | **已实现·待 CI/安全/真实 Chrome 验收** | 新的 chrome-extension Origin 登记通道只允许登记 pending ID；管理员在 Server Web 单独批准/撤销该 ID，**还需单独批准浏览器设备身份、send/receive 权限和双端加密配对**；新增速率限制及 500 pending/已审计记录资源上限；历史 extension_ids 白名单暂保留用于兼容迁移 |

本轮与全 Cookie/Web Storage 相关的实验性代码**未合入功能分支，不计已实现**。PR #194 保持 Draft，未合并 main；CI 完成后逐 SHA 更新状态。

## 十、全量站点状态与无手填流程（2026-10-11，待 CI / 真机验收）

本开发分支增加 v2 端到端加密策略：按已授权的 HTTPS Origin 捕获全部适用的
Cookie（包括不同 path/domain，符合浏览器权限的分区 Cookie）、Local Storage
全部 Key，以及**一个当前普通 Chrome 标签页**的 Session Storage 全部 Key；
不要求手填 Cookie 名称、Storage Key、网站域名或扩展 ID。网站从已打开
HTTPS 标签页下拉选取，可撤销站点授权；Chrome 扩展以 Origin 自动登记，
Server Web 批准扩展和设备，配置页编辑 browser_sync 与网络代理相关字段。
旧版手工 Cookie 白名单规则保留原有收窄语义；重新配对后才启用 v2，
防止未经双方同意扩大数据传输范围。

Session Storage 是标签页隔离的，不能承诺读取其他标签页或 Chrome 关闭
后丢失的 Key。IndexedDB、跨 Origin iframe 数据、浏览器密钥链和
HttpOnly 外的其他浏览器持久数据库不在同步范围。单次 Storage 180 KiB、
明文 224 KiB（服务端密文 256 KiB）上限，不自动截断；大小超限会报错。
Cookie、Local/Session Storage 应用采用冲突预检、仅删除当前规则持有且
未被本地站点修改的数据、元数据日志和异常时暂停；多 API 操作不是真正原子
事务，故浏览器在中途退出或网站并发写入时必须人工核验后恢复。

**验收阻塞维持不变**：真实 A/B Chrome 的登录保活、账号冲突、登出与撤销、
不同域/路径 Cookie、Session Storage 的 tab 语义，及 DNS/QUIC/WebRTC
边界仍没有实测证据。本次实现与单元测试不等于生产发布，PR #194 保持 Draft，
不得在通过 CI、独立安全审查和真实设备验收前合并 main。

### 2026-10-11 v2 配套回归审查（代码后置文档同步）

- 代码提交 `599f9b3`：Web 设置移除了旧版 Extension ID 的手填字段，配置字段统计测试从 46 调整到 45；原有扩展自动上报审批仍需分别批准扩展和设备。
- `932ac74`：修复历史扩展 Origin 审批测试的 Go 格式检查；格式问题不意味着自动授权验收通过。
- `640ae3a`：纠正活动 WSS 控制帧身份检查使用 HTTPS Server Origin 的错误；签名认证继续绑定 Server HTTPS Origin，扩展撤销复核使用真实 `chrome-extension://` Origin，不放松安全门禁。此前 Go CI 在 WSS 集成测试出现 `browser approval revoked`。
- `0c89b83`：Server 更新配置重启比较将 Browser Sync 的 nil/empty 列表规范化，避免意外显示 `browser_sync` 待重启；此前 Go CI 中 `TestServerSettingsPersistsWithoutReplacingRuntime` 暴露该回归。
- `2a90f68`：单个 Origin 存在多个标签页时，优先当前聚焦 Chrome 窗口的活动标签页读取 Session Storage，再后备其它普通标签页。只能承诺该标签页全部 Key，不能承诺跨标签页汇总。
- 历史 UI CI 部分步骤已通过 Server/Wails React、Chrome 扩展单元测试，但针对最终 SHA 的完整 Go/UI/Network/Android CI 需重新验证，不能跨提交继承通过状态。安全和功能验收：P0-01、P0-05–08、NB-05、NB-08、NB-11 仍待真实两端 Chrome 会话维持、冲突/登出、跨路径和分区 Cookie，以及 DNS/QUIC/WebRTC 证据；本开发 PR 继续 Draft，不合并 main。

### 2026-10-11 全量状态恢复补强：Session Storage 标签页语义与 CHIPS

- **RS-01 — Session Storage 回流同一标签页（已编码，待真实 Chrome 验收）**：v2 的“同步成功后打开网站”不再新建一个 Session Storage 为空的标签页；在接收端恢复并刷新成功后，严格校验目标 HTTPS Origin、排除隐私窗口，再激活承载该次恢复的同一标签页，必要时聚焦所在窗口。新增独立测试覆盖同 tab 激活、域名跳转拒绝和隐私窗口拒绝；不宣称已验证真实网站的 JS 登录态。
- **RS-02 — Partitioned Cookie / CHIPS（已编码，待 Chrome 多站点实测）**：Chrome Cookie API 默认仅返回未分区 Cookie；新版使用 `getAll({domain,partitionKey:{}})` 取得授权域下的分区和未分区记录，再根据网站的顶层站点及跨站祖先语义筛选。保留支持的分区属性，自动化模拟覆盖第一方 CHIPS 被采集/应用、无关第三方分区不被扩展授权传出。Chrome 需要 119+（Manifest 已声明）。
- **状态**：以上为新增单元/模拟回归，不能替代真实 A/B Chrome 登录、登出、冲突处理、站点 Cookie/Storage 与 DNS/QUIC/WebRTC 的网络边界记录。P0-01/05/06/07、NB-05/08/11 和整体 PR 发布禁令不变。最终 SHA 的 CI 必须单独核实。

### 2026-10-11 v2 端到端加密数据模拟与登出冲突回归

- **RS-03（新增自动化，待 CI）**：在独立模拟 Chrome Profile A、B 中生成各自的 P-256 身份密钥，A 组合所选 HTTPS Origin 的全路径 Cookie、全部 Local Storage / 单标签页 Session Storage Key，签名加密后 B 通过真实客户端解密、恢复和比对；检查密文不包含原文凭据、篡改被验签拒绝。
- **RS-04（新增自动化，待 CI）**：已安装的 Storage Key 后续被 B 网站/用户改写，再由 A 发送空值集合时必须拒绝远程删除；旧账号状态不能被静默清除。仅证明模拟存储层行为，不证明真实网站会话失效或保持登录。
- **门槛**：此测试补充 P0 会话恢复/冲突/登出的自动化证据，但 P0 真机验收、完整 Chrome 网络边界与安全审查仍未通过，PR 保持 Draft，不合并 main。

### 2026-10-11 Chrome 扩展交互分层：快捷弹窗 / 独立配置页

- **UI-01（编码已提交，CI 待跑）**：将原先单个 Popup 的全部 Server、站点、配对、网络绑定、冲突保护管理界面迁移至 Chrome `options_ui` 独立配置标签页（五个分组，可从弹窗入口打开）。保留原事件处理器及单次用户点击的 `chrome.permissions.request` 授权语义。
- **UI-02（编码已提交，CI 待跑）**：全新小尺寸 Popup：服务端连接状态、手动连接、Chrome 全局 Agent 代理快捷开关（沿用已配置端口）、当前网站优先的 ACTIVE+密钥合法规则、源端立即发送 / 接收端请求同步、接收端同步后打开；待审批、撤销、修改覆盖权限需跳转设置，避免误触。
- **UI-03（编码已提交，CI 待跑）**：增添独立的浅/深色 CSS、响应式侧栏、清楚的卡片/间距/可视焦点和空状态。新增静态 UI 契约测试确认配置控件不在 Popup、所有既有 options JS 操作目标 ID 仍在、快捷同步仅对有效活跃规则开放。
- **UI-04（待真实 Chrome）**：Windows/macOS 多 DPI、窄视口、扩展从关闭状态启动、Chrome `chrome.runtime.openOptionsPage` 入口、权限弹窗仍由用户手势触发的手工验收；UI CI 不替代这些验证。PR 仍为 Draft。

- **UI-05（保护补充）**：快捷面板对 `PARTIAL` 状态的 B 端禁止直接请求/打开，并提供完整设置入口；代理快捷切换报错后重新读取后台实际配置，避免 UI 显示与已写入的开关状态不符。

### 2026-10-11 首次浏览器接入“一次申请、一次管理员审批”

- **AUTH-01**：新版 Chrome 独立设置页仅保留一个“提交统一授权申请”入口。扩展通过 TLS 的 `POST /api/v1/browser-sync/extensions/register` 发送自身 `Origin: chrome-extension://<ID>` 以及设备 P-256 签名/加密公钥和设备名称，**不**提交可选身份、收发权限或其他敏感凭据。旧版仅上报扩展 ID 的空请求接口仍兼容。
- **AUTH-02**：Server 首次注册扩展/设备仅保存 pending，按设备 ID 锁定扩展来源；无权读取同步密文或建立已认证 WSS。更新管理 API 的设备记录，显示关联扩展 ID 与状态。已撤销扩展不得重新登记；已有设备公钥不可替换或换绑另一个扩展。
- **AUTH-03**：Server Web “浏览器同步”的待审批设备记录同时展示 Extension ID、设备 ID、公钥所在申请、身份和 send/receive 能力。管理员点一次“批准扩展与设备”，Go Store 在同一个 SQL 事务里校验激活 Identity、pending 设备和非撤销的扩展，统一提升两者状态。未来新 Chrome Profile / 新设备仍单独审批。仅扩展旧版申请保留独立批准按钮；扩展撤销继续支持。
- **AUTH-04**：新版 Chrome 配旧版 Server 时，客户端保持“扩展批准 → 注册设备”的兼容回退并明确提示升级 Server 才有一次审批。已建立的 A/B 双端独立会话配对校验仍需双端确认，站点 Host 授权不取消。
- **自动化边界**：扩展+设备一次提交、身份/能力提升拒绝、管理员单次原子审批、重放设备审批拒绝、扩展换绑/公钥替换拒绝、扩展撤销后拒绝新设备/待审批设备均增加 Go 回归。真实部署的服务端升级及 Server Web 审批操作尚需手工验证；此功能不解除 PR Draft 或真机安全验收门槛。

- **AUTH-05 安全补充**：旧版已审批的设备不允许仅凭公开 P-256 公钥被绑定到一个新出现、尚未批准的扩展 Origin；旧版无扩展关联的设备仍可沿用原审批流程。前后端增加一次申请页面契约测试。原审批接口/权限边界未取消。

- **AUTH-06 管理员撤销优先级**：已撤销的扩展即使曾被写入旧版静态 `browser_sync.extension_ids`，也必须拒绝它的 Browser Sync 设备注册/WSS；新增回归验证 DB revocation 覆盖静态 allowlist 的默认许可。老配置白名单仍可作为未入库时的兼容信任来源。

- **AUTH-07 现有 WebSocket 防旁路**：运行中已认证的浏览器连接在每次控制消息处理前重新检查扩展管理员撤销状态，不能因为它也位于旧版静态白名单而继续转发。

### 2026-10-11 HTTP 网站授权和跨端会话同步

- **SITE-01（实现并补测试）**：Manifest 增加可选 `http://*/*` Host 权限；站点授权从普通标签页枚举 HTTP/HTTPS 的准确 Origin，使用用户点击授予 Host 权限。HTTP 授权、创建邀请和接收确认有额外明文凭据风险提示。拒绝 FTP、file、Chrome 内部协议。
- **SITE-02（实现并补测试）**：v2 加密邀请支持 HTTP Origin；legacy v1 Cookie 名称白名单仍保留 HTTPS-only；HTTP Local Storage 和单标签页 Session Storage 所有 Key 均可采集恢复，仍受账户冲突、数据量、回滚及用户授权限制。HTTP/HTTPS 为不同的 Origin。
- **SITE-03（实现并补测试）**：HTTP Cookie 仅同步可供明文站点访问的非 `Secure`、非 CHIPS、非 `SameSite=None` 记录；阻断向 HTTP 接收端注入 HTTPS-only Cookie。避免 Cookie 域名枚举时意外扩权。
- **SITE-04（实现并补测试）**：HTTP 站点的逐站点 Agent 网络绑定按钮隐藏，保持该特性仅支持 HTTPS 443；全 Chrome Agent 代理开关仍独立。Server Admin HTTPS/WSS 必须继续使用 TLS，不能设置 HTTP。
- **真实验收未完成**：需要实际 A/B Chrome 验证 HTTP 页面 Cookie/Local/Session Storage、生效及登出、冲突覆盖、HTTPS 安全 Cookie 隔离、权限弹窗与跳转，以及 DNS/QUIC/WebRTC 网络边界。PR 仍 Draft、不合并 main。

### 2026-10-11 浏览器网络绑定错误可诊断性

- **NET-ERR-01**：Server 的 `NETWORK_EXITS` 与 `NETWORK_OFFER` 原先把网络代理未配置、来源浏览器无 send 权限、共享 Exit 不可用、网络规则未激活/配对不完整、已存在网络绑定等全部返回 `rule not authorized or invalid`。现通过已认证 WSS 使用固定错误码及不含 Cookie/Token/密钥的文字诊断返回；避免用户无方向重复审批。
- **NET-ERR-02**：扩展没有可选 Exit 时展示独立网络代理监听、同身份、`proxy.exit`、Agent `exit.browser_network_share`、Server `allowed_exit_ids` 的明确排查步骤。严格保持允许列表与双端审批检查，不为便于连接而允许自动授权。
- **NET-ERR-03**：新增错误响应合同测试；仍需真实 Server Web/Chrome 与 Agent 出口联调验证。旧版 Server 不认识新诊断码，必须更新 Server 才能收到更细错误。

### 2026-10-11 浏览器网络出口 Web 配置

- **WEB-NET-01**：在 Server Web“浏览器同步”内新增 Agent 网络出口配置面板。复用现有 Admin-only `/api/v1/server/config` 的 revision 校验、配置落盘和重启检测，不增加未认证写入接口。直接设置监听地址 / 对外域名 / 在线 Agent 勾选共享白名单，并保留离线但已选择的出口 ID。禁用时留空两项地址，仍不影响 Cookie/Storage E2EE。
- **WEB-NET-02**：Agent Web/GUI 的“本机出口共享”新增“允许浏览器网络共享”显式开关，保存 `exit.browser_network_share` 至 Agent 配置，默认关闭，纳入启动参数重启差异提示。Server 管理员无法代替 Agent 本地用户直接授予这个能力。
- **WEB-NET-03**：纯 JS Web 配置验证测试覆盖地址和端口、至少一个显式允许的 Agent ID、无关 Server 设置保留。Agent Go React 配置 roundtrip 测试增加浏览器共享开关及 Web JSON 响应契约。真实网页、代理监听和网络联调仍需部署后验收。

### 2026-10-11 完整 Storage 180 KiB 截止错误修复

- **SIZE-01**：问题来自三层独立大小上限：`web-storage.js` 180 KiB、`session-envelope.js` 224 KiB、Server `internal/browser_sync/protocol.go` 最大密文 256 KiB（外加 WSS 400 KiB 帧）。仅放大 Storage 大小不能成功中继。
- **SIZE-02**：完整 Local/Session Storage 上限改为 4 MiB，Cookie+Storage 明文 JSON 整包最大 5 MiB；超过 180 KiB 时优先 gzip，再在 P-256 ECDH / AES-GCM / ECDSA 内传输，采用原 `browser.sync.v1` Envelope 格式、同一防重放序号与整包终态 ACK。不把压缩前的秘密持久化在 Server 或扩展配置存储中，绝不跳过 Key。
- **SIZE-03**：单密文最大 1 MiB，Server WSS 输入 1.5 MiB。接收端只允许 5 MiB 内解压结果，流式计数限制 gzip bomb，损坏 wrapper 全包失败、不写入网站 Storage。无法压缩到单帧上限的超高熵数据返回明确错误；后续再设计经批准的分片 + 原子提交，不自动增加无限制容量。
- **SIZE-04**：新增 Node 小快照兼容、700 KiB 全量 Storage 的 A/B 加解密恢复、不可压缩数据、超限拒绝、gzip bomb 和格式篡改测试。Go 增加新版密文长度验证及跨原 WSS 400 KiB 档位的大快照中继测试。测试代码已提交，真实 Chrome A/B 与 CI 结果仍待验收。部署必须同时更新 Server 与 A/B 扩展；PR 保持 Draft。

### 2026-10-11 Browser Sync 复用中继 TLS/TCP 端口

- **PORT-01**：增加 `browser_sync.network_proxy.listen: relay` 显式共享配置，严格校验 `network_proxy.advertise` 和 `server.tls.listen` TCP 端口相同；留空继续关闭，旧式独立监听继续可用。Server Web 提供共享（推荐）/独立/停用选择。
- **PORT-02**：Gateway 保留 Agent TCP ALPN 和旧版无 ALPN 首字节兼容识别，将原一次性短 HTTP 处理改成 `net/http.Server` Accept-once TLS 连接处理，可持续 WebSocket/WSS/CONNECT 并支持 `http.Hijacker`。
- **PORT-03**：新 `api.PublicRelayHandler` 公网路由白名单严格限于 Browser Sync 注册/WSS、已授权 CONNECT 及历史公开消息推送；不挂载 Admin Router，Admin 登录、管理设置和 Browser Sync 管理 API 不可通过中继 TCP 访问。
- **PORT-04**：延后启动 Gateway 直至 Browser Sync 和网络出口权限初始化完成；原 `server.admin.listen` Admin HTTPS 服务保持独立。Chrome “RelayProxy HTTPS Server” 可使用 `https://relay.example.com:443`。
- **PORT-05**：增加 Go Gateway WSS Upgrade / HTTP CONNECT Hijack + 既有 Agent TCP 兼容测试、白名单不暴露 Admin 测试、配置冲突测试、Web JS 共享端口校验测试。真实部署中 TLS/长连接/登录态/内外网联调及 GitHub CI 尚未验收；当前保持 Draft PR。

### 2026-10-11 清理独立 Browser Sync 网络代理配置

- **CONFIG-REMOVE-01**：移除 Server 当前 `browser_sync.network_proxy` YAML 字段、配置校验、Server Web/Admin API 中的监听 / 广播地址 / Agent 白名单；`browser_sync.enabled` 启动时统一初始化同端口 CONNECT 代理，强制 TLS/TCP 中继启用。为已部署用户保留仅用于读取并抛弃旧 `network_proxy` 块的迁移 shim，后续保存不会写回。
- **CONFIG-REMOVE-02**：NetworkProxy 不再启动单独监听器，也不发送 host/port。Chrome 由已认证的 `settings.serverOrigin` 推导 HTTPS 代理主机和端口，Server Origin 改变时删除旧会话代理凭据并拒绝发往旧主机。只允许具备 `proxy.exit` 授权、同 Identity、Agent signed local browser sharing opt-in 的在线 Exit；需要 A/B 配对和网站网络绑定双方确认。
- **CONFIG-REMOVE-03**：保留普通 Cookie/Storage 同步不使用任何 Agent 的特性；如果需要来源 Agent 出口，Chrome 的 RelayProxy HTTPS Server 必须填写真正的公网 TCP 中继入口，不能再以仅管理用途的 Admin HTTPS 地址作为网络代理主机。
- **CONFIG-REMOVE-04**：移除旧 Web 编辑器、旧表单校验辅助与测试，补充 YAML 迁移、TLS 强制和令牌不含 host/port、Chrome Origin 地址绑定等回归用例。此记录替代更早的 NET/PORT 旧配置说明。

### 2026-10-11 浏览器旧设备和撤销扩展删除、重新申请

- **DELETE-01**：Server 管理入口新增 `DELETE /api/v1/browser-sync/admin/devices/{id}`。只能删除**已撤销**的浏览器设备，必须通过原有管理员会话与 CSRF 校验。单事务彻底清理该设备相关的加密配对元数据、规则邀请、网络绑定、序号、防重放投递收据及 Chrome Extension 关联，然后清理设备记录；不删除另一个浏览器设备。
- **DELETE-02**：新增 `DELETE /api/v1/browser-sync/admin/extensions/{id}`，仅用于彻底删除已撤销的扩展 ID；如仍存在关联设备，先逐个撤销并删除设备，返回 HTTP 409 而不是偷偷删除其他设备。删除后同扩展 ID 的下一次申请必须重新走 `pending` 审批流程，不会自动授予旧权限。
- **DELETE-03**：Server Web 在已撤销设备行加入“删除旧设备”、已撤销扩展行加入“删除旧扩展”，每次永久删除前明确确认，完成后刷新列表。新的待审批请求优先排序，避免旧记录占满最多 500 条查询结果。
- **DELETE-04**：已撤销设备在重新提交相同设备 ID / 签名密钥时会继续匹配旧记录，不会自动变成 `pending`；管理员完成删除后，Chrome 才能重新提交申请。扩展若也处于 revoked 状态，需要先删除旧设备，再删除旧扩展，最后在 Chrome 重新点击“提交统一授权申请”，然后在 Server Web 审批。此流程保持管理员显式审批，不自动恢复被撤销设备的信任。
- **DELETE-05**：测试新增删除仅限 revoked、完整数据库依赖清理、原浏览器和其他浏览器边界、同扩展 ID 再注册恢复待审批状态、Admin API 409 / 404 / 200 分支。最新提交仍需 GitHub CI 和实际 Server/Chrome 验证，不声明运行成功。

## 2026-10-11 新增 IndexedDB 会话同步（配对 v3）

新建的 HTTP/HTTPS 网站双端加密配对从 v2 升级为 **v3**：在已有全量 Cookie、Local Storage 和单标签页 Session Storage 的基础上，额外纳入**当前站点 Origin 下的 IndexedDB**，无需手动配置数据库名、Object Store 或记录 Key。已有 v1/v2 配对不自动扩权；请撤销旧配对并由 A 创建新的 v3 配对，在 B 重新接受和核对配对校验码。**A、B Chrome 扩展都必须升级**，无需额外 Agent，也无需变更 Server 数据库、端口或密文中继协议。

采集在获网站权限的**普通 Chrome 顶层标签页**中，采用 ISOLATED world 的 `chrome.scripting.executeScript` 访问同源 IndexedDB；不会执行网页提供的 JavaScript、不会抓取跨源 iframe 或浏览器私有数据库。使用 `indexedDB.databases()` 自动枚举所有可见同源库，并逐个保存版本、Object Store、主键路径、自增配置、索引（含 unique / multiEntry）和记录。结构化值支持常见的 string/number/boolean/undefined/null、日期、BigInt、二进制 ArrayBuffer/TypedArray/DataView、Blob/File、Map/Set、正则、普通对象和数组；不支持不可安全复制的 CryptoKey、循环引用及未知对象类型，遇到此类记录**整次采集失败，不会静默丢弃或输出部分数据**。

安全与一致性：

- IndexedDB 序列化快照上限 **4 MiB**、记录上限 **4096**、最多 **16** 库 / 每库 **64** Store（整个 Cookie + Storage + IndexedDB 明文组合仍受 **5 MiB** 上限；压缩后的密文单帧仍有 **1 MiB** 限制）。超限时明确失败，不采样、不丢 Key、不自行分批绕过安全上限。
- 来源端按当前 Origin 读取。接收端严格匹配 Origin、数据库版本、Object Store、索引定义。发现本地不同登录记录或冲突时默认拒绝覆盖；仅接收端显式启用覆盖，或者本规则之前写入的内容仍被 HMAC 标记拥有时，才允许覆盖。
- 每个 IndexedDB 数据库使用读写事务进行 clear/put，保存键和记录完整值；多数据库及 Cookie/Storage 不是单个浏览器事务，依赖仅保存时间戳的恢复 journal、按整库预期状态校验的回滚与 `PARTIAL_ROLLBACK` 暂停保护。新建 DB 时若同步失败会尝试清理，无法完成则提示人工处理，绝不声称原子成功。
- 来源缺失的数据库在接收端**不会直接删除**；避免把旧版缺字段或暂时消失的 Origin 数据误判为远程注销指令。跨 Origin iframe、Cache Storage、Service Worker 缓存、OPFS 与不在主站 Origin 的数据库不在此版本的覆盖范围。网站自身可能使用设备绑定或不可导出的密钥，恢复后仍可能需要登录。
- 所有 IndexedDB 值只在签名、ECDH/AES-GCM 端到端加密后的 `SESSION_SNAPSHOT` 中传输。Server 只转发密文；Chrome 本地配置持久化仅存按规则 HMAC 的所有权指纹和事务状态，不保存 IndexedDB 的 Token/记录明文。现有双端批准、序号防重放、冲突处理及终态 ACK 不变。

**实现状态：** 代码与模拟 IndexedDB 单元测试加入 PR #194 的功能分支。仍需 Chrome A/B 真实站点验收，包括浏览器重启、同时打开数据库、版本升级冲突、覆盖/回滚、网络断线、Blob 与非导出 CryptoKey；不能将模拟测试等同于真实会话恢复成功。

### 2026-10-11 23:12 +08:00 增量核对：IndexedDB v3、CI 覆盖缺口与发布风险

- **分支与 PR**：[Draft PR #194](https://github.com/timwai/RelayProxy/pull/194) 仍为 Open/Draft，目标 main，HEAD [`e9aa691`](https://github.com/timwai/RelayProxy/commit/e9aa691963de926af3f7628f138fdbb26da600a5)，未合并；本次核对仅修改进度文档，不触及业务代码。
- **实质新进展**：[`1334fd4`](https://github.com/timwai/RelayProxy/commit/1334fd4d8f) 为新建双端明确授权的 v3 配对增加同源 IndexedDB 全量快照、恢复、冲突/回滚与模拟测试；[`e9aa691`](https://github.com/timwai/RelayProxy/commit/e9aa691963de926af3f7628f138fdbb26da600a5) 修复无效 Date 和共享对象引用可能导致的静默数据失真，改为拒绝该次采集。此前 v2 全量 Cookie、Local Storage、单标签 Session Storage 和 CHIPS 的实现应从历史“未开始”修正为“已实现·待验收”。**新 v3 扩大了可同步的敏感数据范围**，因此新增 P0-09 发布门槛。
- **新增验收风险**：浏览器 IndexedDB 是有版本、索引、结构化克隆和打开中事务的复杂数据源；现有模拟测试不能证明真实 Chrome 中版本升级、数据库阻塞、跨库回滚、浏览器崩溃及网站登录态一致性。Server 不持有解密密钥，但扩展具有被授权站点的敏感状态访问能力，须复核权限与审计。
- **CI 证据缺口（未确认通过，不等同于已证实失败）**：查询时 GitHub Actions 可见最新运行创建时间为 **2026-10-11 07:20 UTC**；此后 [`1334fd4` 13:52 UTC](https://github.com/timwai/RelayProxy/commit/1334fd4d8f) 与 [`e9aa691` 13:56 UTC](https://github.com/timwai/RelayProxy/commit/e9aa691963de926af3f7628f138fdbb26da600a5) 的 Go/UI/Android 运行尚无可确认结果。先核对最新 SHA 的 Actions 是否触发、排队或被跳过；若仍无运行，应补跑对应检查并调查 PR 触发器。**不能引用旧 SHA 的 success 宣称 v3 已通过 CI**。
- **优先次序**：① HEAD CI 与安全回归；② 真机 Chrome A/B 的 v3/旧配对兼容、IndexedDB 事务与冲突；③ 原 P0-01/05/06/07/08 和 NB-11；④ P1 规则管理/发布流程。P2-03 离线邮箱和 P2-04 多实例仍未开始。

| 2026-10-11 | [PR #194 · `1334fd4` → `e9aa691`](https://github.com/timwai/RelayProxy/pull/194) | v3 同源 IndexedDB 全量会话快照/恢复、双端明确批准、4 MiB/4096 记录限额、冲突保护与回滚；后续修复无效日期和共享对象引用失真。最新代码尚无可确认 Go/UI/Android CI；真机验收未完成 | **新增 P0-09 已实现·待 CI/真机/安全验收**；P2-01/02 更新为已实现待验收；P1-04 标注已由新全量授权流程替代；P0-01–08 仍未完成，P2-03/04 未开始 | 维护进度表并记录 CI 覆盖缺口，PR 保持 Draft；不合并 main |
