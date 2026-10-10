
# RelayProxy Browser Session Sync — 无 Agent 设计与开发方案

> **设计基线文档，不代表当前实施进度。** 现有实现、P0 上线门槛、P1 正式版功能、P2 后续增强及逐次 CI/验收记录，请以 [Browser Sync 持续更新进度清单](./browser-session-sync-progress.md) 为准。以下早期“待实现/拟新增”字段保留为历史设计提案，具体已实现接口以当前代码为准。


> 版本：v1.1（2026-10-10）  
> 仓库：timwai/RelayProxy；目标文档分支：main  
> 预计实现分支：feat/browser-session-sync  
> **默认架构：Chrome MV3 Extension ↔ RelayProxy Server ↔ Chrome MV3 Extension**

## 0. 总体决策

目标：用户本人控制或已授权的两台设备，在 A 的 Chrome 登录指定 HTTPS 网站后，通过 RelayProxy Server 让 B 在网站**允许会话迁移**的情况下尽量免去重复登录。

**本地仅需 Chrome 扩展**；不安装本机 RelayProxy Agent、Native Messaging Host、本地 Socket，不开启透明代理或 SOCKS5。RelayProxy Server 新增独立的浏览器设备身份、授权、HTTPS/WSS 消息转发能力；不复用无 Token 的公开消息推送 API。

| 维度 | 首期决定 |
|---|---|
| 设备支持 | Windows/macOS/Linux 桌面 Chrome |
| 同步范围 | 指定 HTTPS Origin、Cookie 名称白名单 |
| 同步方向 | A → B 单向、双方明确确认 |
| 网络层 | Server WSS Relay，MVP 不使用 P2P |
| 设备身份 | 浏览器 Profile 独立注册 browser device |
| 授权 | browser.sync.send 与 browser.sync.receive + 双端规则批准 |
| 机密性 | 浏览器端 E2EE 加密；Server 只转发密文 |
| 默认持久化 | Server 不保存 Cookie 或会话快照，密文邮箱后续可选 |
| 网站兼容 | 不绕过 DBSC、Passkey、MFA 或设备绑定 |

## 1. 现有 RelayProxy 与改造边界

基于 main 分支 README、P2P 设计、Go 依赖和 React/Wails GUI 核对：Server 已具备设备管理、TLS、身份与权限、SQLite 等能力；Agent 的网络代理/P2P/RDP 不适合直接用来承载无 Agent 的浏览器消息。

新的 Browser Sync 是**独立 Server 子模块**。浏览器 device_type=browser 不可伪装成 Agent 或默认继承 proxy.use、rdp.connect；同身份的浏览器设备仍需单独批准站点及接收目标。原有 Agent、Wails GUI、代理路径、RDP、公开 Push API 不做改动。v1.0 中关于 Native Host、Agent 本地 Socket、Agent Session Manager、Agent GUI 浏览器同步页面、Agent P2P 传输的设计全部废弃。

## 2. 用户体验与 MVP 范围

1. A 和 B 都安装扩展，分别输入支持 HTTPS 的 RelayProxy Server 地址。
2. 每个 Chrome Profile 产生独立密钥及 browser device ID，提交注册申请，管理员审批。
3. A 新建规则，指定 HTTPS Origin、Cookie 白名单、目标 B，发起邀请。
4. B 明确接受；双方在扩展内核对设备指纹或短校验码，绑定可信公钥。
5. A 正常登录网站。扩展识别白名单 Cookie 变化，防抖后取当前快照、本地加密、签名，走 HTTPS/WSS 传给 Server。
6. Server 审核双方设备/规则 ACL，仅转发密文。B 验签、解密、核对域与分区、检查冲突，再应用。
7. B 可通过“同步后打开”按钮等待 APPLIED 后访问网站。应用成功不等于网站已认证成功。

MVP 仅支持 Chrome 普通桌面 Profile、Cookie 白名单、单向自动/手动同步、离线重连后的在线来源补偿、暂停/撤销；不支持 Android Chrome、全浏览器 Profile、密码、全 IndexedDB、全 SessionStorage。LocalStorage 选择性 Key 同步进入下一阶段。

Cookie 无法保证所有网站免登录：DBSC、Passkey、WebAuthn、硬件密钥、多因素验证、设备/IP 绑定及服务端风控可能要求 B 正常重新登录；不能绕过这些限制。

## 3. 架构图

~~~mermaid
flowchart LR
  subgraph A[设备 A · Chrome]
    EA[MV3 扩展 · 采集 / 加密]
  end
  subgraph S[RelayProxy Server]
    API[HTTPS + WebSocket API]
    AUTH[Browser Device 认证 / 双端授权]
    RELAY[密文中继 / ACK / 限流]
    DB[(身份/规则/审计元数据)]
    API --> AUTH --> RELAY
    AUTH --> DB
  end
  subgraph B[设备 B · Chrome]
    EB[MV3 扩展 · 验签 / 解密 / 写入]
  end
  EA <-->|TLS / E2EE| API
  API <-->|TLS / E2EE| EB
~~~

扩展是唯一接触明文会话的同步组件，Server 不接触或记录 Cookie/Token；Chrome 关闭后无法继续采集。Chrome MV3 Service Worker 可被暂停，因此必须在 onStartup、alarms、网络恢复及 UI 打开时重新连接、对账；WebSocket 心跳不能保证永久常驻。

## 4. Chrome 扩展

权限最小化：cookies、storage、alarms、按需 tabs，以及按用户添加站点时请求的 optional_host_permissions；**不申请 nativeMessaging** 或全域默认主机权限。

模块：manifest.json、src/service-worker.js、src/api.js（HTTPS/WSS）、src/device-identity.js（签名与密钥）、src/envelope.js（E2EE）、src/cookies.js（白名单与写入）、src/rules.js（站点规则）、src/popup.html、src/popup.js、测试目录。

Cookie 数据包括 name/value/domain/path/secure/httpOnly/sameSite/expirationDate/hostOnly/partitionKey（以各端 API 支持为准）。严格校验 Cookie Domain、Path、Secure、分区和指定 HTTPS Origin；hostOnly 写入时不指定 domain；不可直接复制源设备 storeId 到 B 的 Profile。Cookie overwrite 可能引发删除再添加，建议约 2 秒防抖后取最终集合。

B 已有非本规则管理的同名 Cookie 时默认拒绝静默覆盖；显示 CONFLICT，明确批准才能覆盖。SYNC_ACK 的 APPLIED 只表示 Chrome API 操作成功，LOGIN_VERIFIED 需独立的网站兼容性适配器。不得在扩展 UI、日志、chrome.storage.sync、URL、错误堆栈记录凭据明文。

## 5. Browser Device 身份认证与审批

Server 新增独立 browser 类型设备登记；每个浏览器 Profile 使用 WebCrypto 生成设备签名密钥及独立的加密密钥，登记公钥，初始 PENDING_APPROVAL。私钥应以不可导出 CryptoKey 管理，可使用 IndexedDB 持久保存，并明确安全限制（不可导出不等于可防操作系统恶意代码）。

认证流程：Server 生成一次性短期 challenge；浏览器签名绑定 serverOrigin、deviceId、challenge、protocolVersion 的认证数据。Server 检查签名、审批、独立浏览器设备能力、策略是否生效。

浏览器 WebSocket 不能随意设置 Authorization 请求头，所以 WSS 建立后必须先处理 AUTH_CHALLENGE/AUTH_PROOF；AUTH_OK 之前**不准处理业务帧**。检查 chrome-extension:// Origin，但不能把 Origin 当作身份依据。只接受 HTTPS/WSS，不把永久 Token 放进 URL。

设备和规则权限分离：browser.sync.send 允许来源设备发送；browser.sync.receive 允许目标接收；**双方分别确认具体规则与 Cookie 范围**。撤销设备或规则立即禁止后续数据转发。

## 6. 配对、加密与反重放

规则状态：DRAFT → OFFERED → ACCEPTED → KEY_CONFIRMED → ACTIVE → PAUSED/REVOKED。双方核对配对指纹并固定公钥，避免服务器替换。

推荐审计成熟的 HPKE (RFC 9180) 浏览器库做端到端加密，来源设备对规范化信封签名，接收端校验来源已配对公钥。AEAD AAD 至少包含协议版本、ruleId、来源/目标设备、sequence、expiresAt、type。不要自行发明密码学协议。

每规则保持单调 sequence、唯一 messageId 与过期时间；拒绝重放、乱序或跨规则包。Server 只记路由所需元数据，不保存明文 Cookie/Token/密钥或完整密文帧日志。撤销浏览器同步权**不等于**使网站服务器已经发行的登录令牌失效，用户应通过网站自身的“退出所有设备”完成强制注销。

## 7. Server HTTPS / WSS 接口（待实现）

这些是**计划新增**端点，不能误认当前已存在；挂到现有 TLS HTTP Router，别新开未受保护管理端口：

| 方法 | 路径 | 作用 |
|---|---|---|
| POST | /api/v1/browser-sync/devices/register | 登记浏览器公钥并进入审批 |
| POST | /api/v1/browser-sync/auth/challenge | 一次性认证挑战 |
| GET | /api/v1/browser-sync/devices/me | 本设备状态与权限 |
| POST | /api/v1/browser-sync/rules | 创建规则邀请 |
| POST | /api/v1/browser-sync/rules/{id}/accept | B 批准 + 指纹确认 |
| POST | /api/v1/browser-sync/rules/{id}/revoke | 撤销规则 |
| GET Upgrade | /api/v1/browser-sync/ws | WSS 认证、加密消息传输 |

REST 也要做设备身份校验、签名、防重放与权限校验，不允许仅凭浏览器上传的 Device ID 完成操作。Server 必须使用已认证 WSS 会话身份校验 sourceBrowserDeviceId。

WSS 事件：AUTH_CHALLENGE、AUTH_PROOF、AUTH_OK、RULE_OFFER、RULE_ACCEPT、SYNC_REQUEST、SESSION_SNAPSHOT、SESSION_DELTA、SESSION_TOMBSTONE、SYNC_ACK、RULE_REVOKE、SYNC_ERROR、PING/PONG。最大载荷建议 256 KiB，设置消息与速率限制。

~~~json
{
  "protocol": "browser.sync.v1",
  "type": "SESSION_SNAPSHOT",
  "messageId": "75ee4145-0bbb-4e0e-909e-6070b39327d2",
  "ruleId": "rule_123",
  "sourceBrowserDeviceId": "browser_a",
  "targetBrowserDeviceId": "browser_b",
  "sequence": 12,
  "createdAt": "2026-10-10T10:00:00Z",
  "expiresAt": "2026-10-10T10:15:00Z",
  "encryption": {"suite": "HPKE-v1", "keyId": "key_b1", "enc": "<base64>"},
  "ciphertext": "<base64>",
  "signature": "<base64>"
}
~~~

Cookie 值、Storage 值与完整 Origin 必须位于端到端密文内；外层仅路由与重放检查字段。ACK 明确区分 RECEIVED、APPLIED、FAILED、LOGIN_VERIFIED。

## 8. Server 存储与配置

拟新增 browser_devices（独立 ID/公钥/审批/权限）、browser_sync_rules（双端设备/策略摘要/双方批准状态/版本/过期）、browser_sync_key_pins（指纹）、browser_sync_audit（仅不含 Cookie 的审计元数据）表；数据库迁移需幂等。

MVP 不缓存凭据（包括密文）：B 离线时无法立即收到更新，重连后在 A 在线条件下请求最新快照；后续可以由用户明确启用短期、有配额、读后清理的密文离线邮箱。

~~~yaml
# 新配置字段（目前尚未实现），默认关闭
browser_sync:
  enabled: false
  require_https: true
  max_frame_kib: 256
  max_rules_per_device: 20
  allow_offline_mailbox: false
~~~

## 9. 拟新增目录

~~~text
browser/chrome-extension/
  manifest.json
  src/service-worker.js
  src/api.js
  src/device-identity.js
  src/envelope.js
  src/cookies.js
  src/rules.js
  src/popup.html
  src/popup.js
  tests/
server/browser_sync/
  handler.go
  auth.go
  device.go
  rules.go
  relay.go
  store.go
internal/browser_sync/
  protocol.go
  validation.go
docs/browser-session-sync-design-development.md
~~~

**不新增** browser/native-host、agent/browser_sync、Native Messaging 安装脚本、Agent GUI 浏览器同步入口；这些属于已弃用的有 Agent 方案。

## 10. 开发任务与优先级

| 阶段 | 任务 | 预估 |
|---|---|---|
| P0 | 检查 Server TLS/HTTP Router/身份/数据库真实集成点；授权网站迁移实验和威胁模型 | 1–2 人日 |
| P1 | MV3 扩展骨架、设备密钥与签名认证、Options、WSS/重连和在线状态 | 3–5 人日 |
| P2 | Server 独立 Browser Device 审批、双端规则授权、WSS 密文 Relay、撤销与数据库迁移 | 4–6 人日 |
| P3 | E2EE、密钥钉扎、Cookie 白名单与冲突检测、ACK、同步后打开 | 4–6 人日 |
| P4 | 安装打包、Server Web 管理、跨系统测试、安全测试、代理/RDP 回归和发布 | 3–5 人日 |

粗略估计 15–24 人日；这是规划不是已交付功能。建议 P0 完成后再投入完整同步链路。

## 11. 主要验收测试

| 测试 | 期望 |
|---|---|
| A/B 未安装 Agent | 浏览器注册、配对、WSS 通信正常 |
| 未审批浏览器 | 不允许业务数据 |
| 未经双端确认的规则 | 无法发送会话 |
| A Cookie 发生覆盖/删除 | 防抖后正确同步最终状态 |
| B 端存在独立账号 | 拒绝静默覆盖 |
| MV3 Worker 休眠/Chrome 重启 | 自动恢复并按序号对账 |
| 来源离线 | 显示等待，不谎报登录成功 |
| 篡改设备 ID/重放消息 | 拒绝并只记录安全元数据 |
| Cookie 分区或域名不匹配 | 拒绝越界应用 |
| 服务端数据库及日志 | 不含明文凭据 |
| 撤销浏览器设备或规则 | 停止后续消息 |
| DBSC/Passkey 网站 | 不绕过；提示需要 B 重新登录 |
| 新模块被关闭 | 原代理/RDP/消息推送行为完全不变 |

建议目标：两端 Chrome 在线、会话可迁移时，P95 轻量更新从 A 采集到 B 应用低于 5 秒（待实测）。

## 12. 安全与风险

恶意扩展/页面、误授权、跨 Profile 和 Cookie 分区写入、被替换公钥、重放、Server 泄露、刷新令牌竞争是核心威胁。统一使用最小站点权限、双端明确批准、指纹核对、设备独立公钥、E2EE、序号校验、冲突暂停、敏感日志脱敏以及即时撤销。只适用于用户自己管理或获得明确授权的账号。网站不允许迁移的登录方式必须尊重其验证流程。

## 13. 兼容和发布

文档直接更新 main。正式实现代码建议在 feat/browser-session-sync 上开发，默认关闭新服务端接口，旧版 Agent 完全无需更新。Chrome 扩展与 Server 均带 browser.sync.v1 协议版本；旧 Server 返回不支持，而不是降级为未授权连接。先灰度自建或已获授权测试站点，再进入正式发布。

## 14. 官方参考

- RelayProxy：<https://github.com/timwai/RelayProxy>
- 原代理 P2P 设计：<https://github.com/timwai/RelayProxy/blob/main/docs/proxy-p2p-direct-path-design.md>
- Chrome Cookies：<https://developer.chrome.com/docs/extensions/reference/api/cookies>
- Chrome 扩展权限：<https://developer.chrome.com/docs/extensions/develop/concepts/declare-permissions>
- Chrome MV3 WebSocket：<https://developer.chrome.com/docs/extensions/how-to/web-platform/websockets>
- Chrome Storage：<https://developer.chrome.com/docs/extensions/reference/api/storage>
- Chrome DBSC：<https://developer.chrome.com/docs/web-platform/device-bound-session-credentials>
- HPKE RFC 9180：<https://www.rfc-editor.org/rfc/rfc9180.html>

---

**v1.1 结论：** 无本机 Agent 与 Native Host。Chrome 扩展直连 RelayProxy Server，使用独立浏览器设备认证、双端授权、浏览器端加密与 WSS Relay；未获授权或不可迁移的会话不得同步。
