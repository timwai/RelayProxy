# RelayProxy Browser Sync — Chrome 扩展（P3 开发预览）

> **代码状态（2026-10-11）**：独立的 Chrome Profile 本机 Agent 代理、HTTP 正向代理、任意合法 TCP CONNECT 地址/端口及原生 DIRECT/PROXY/REJECT 已在 [`b504837`](https://github.com/timwai/RelayProxy/commit/b504837a113a8e312c44335b9077fe3f2e80691e) 集成，Go/UI/Network CI success，但真实 Chrome、WebRTC/UDP/QUIC/DNS/回环绕过及安全验收尚未完成。Cookie 同步仍无需本机 Agent，只有选择本机网络出口才需要安装 B Agent。发布状态仍为 Draft。详情见 [Living Tracker](../../docs/browser-session-sync-progress.md)。

**实时开发进度与发布准入**：[P0 上线前必须完成 / P1 正式版 / P2 后续增强及验证记录](../../docs/browser-session-sync-progress.md)。本 README 描述已经编写的开发功能，不能替代真实 Chrome 验收或安全审查。


Chrome 扩展直接连接 RelayProxy Server 的 **Admin HTTPS/WSS** 服务，不需要安装或运行本机 Agent，也不需要 Native Messaging、SOCKS5 或透明代理。

> **安全声明：** 同步网站会话 Cookie 相当于向另一台设备授予网站账号访问能力。仅限用户自己拥有或明确授权的账号与设备；生产部署前需完成真实 Chrome 双端验证、安全审计和跨浏览器版本回归。网站设备绑定、DBSC、Passkey、多因素验证等机制不能通过复制 Cookie 绕过。

## 功能进度

| 能力 | 状态 |
|---|---|
| 每个 Chrome Profile 独立设备签名/加密密钥 | 已实现 |
| Server 独立浏览器设备注册、管理员审批、撤销 | 已实现 |
| Chrome 通过签名挑战认证 Admin WSS | 已实现 |
| A/B 同一 Identity 双端配对及密钥指纹校验 | 已实现 |
| 指定站点/ Cookie 名称加密邀请 | 已实现 |
| A 登出后同步删除的 Cookie 安全恢复 | 已实现，B 只清理该规则之前写入且未被修改的 Cookie |
| 本地 Cookie 归属识别 | Profile 非导出 HMAC 密钥保护，避免保存普通哈希 |
| 浏览器端 WebCrypto P-256 ECDH/HKDF/AES-GCM 快照加密及签名 | 已实现 |
| Server 按 ACTIVE 规则签名验收、递增序号防重放、密文路由 | 已实现 |
| 精确 HTTPS Host、Host-only、Secure、根路径、非分区 Cookie 采集/恢复 | 已实现 |
| 目标已有不同登录 Cookie 的冲突保护、明确同意后覆盖 | 已实现 |
| 手动同步、Cookie 变化后自动尝试发送、启动和定时重连请求 | 已实现 |
| 同步成功后打开网站、RELAYED/APPLIED/CONFLICT/FAILED 状态 | 已实现 |
| Server Web 浏览器设备管理可视化页面 | 已接入管理员导航，可审批、关联身份和撤销浏览器设备 |
| 跨 Chrome 真机双端网站登录兼容性验收、完整安全审计 | **未完成** |
| LocalStorage、IndexedDB、分区 Cookie、跨子域、设备绑定会话 | 不在 P3 范围 |

Chrome 扩展保留最小 Host 权限；所有 Cookie 值只用于内存中的加密处理和指定站点的 Cookie API 写入，**不写入 Server 数据库、浏览器扩展本地存储或日志**。服务器无会话解密密钥，不保存离线快照。启动时如果 A 离线，B 不能恢复最新状态。

目前 `APPLIED` 仅代表 Chrome Cookie API 成功写入，**不代表目标网站服务器验证了登录**。请使用经授权的测试网站或测试账号验证。

## Server 配置

在 Server Admin HTTPS 监听器上启用独立 Browser Sync API：

```yaml
server:
  admin:
    listen: ":8443"
    tls_enabled: true

browser_sync:
  enabled: true
  extension_ids:
    - "abcdefghijklmnopabcdefghijklmnop" # 改成 chrome://extensions 里的真实扩展 ID
```

默认 **`browser_sync.enabled=false`**。使用浏览器信任的 TLS 证书。这里填的是 **Admin HTTPS Origin**，例如 `https://relay.example.com:8443`，不是 TCP/QUIC 隧道端口。无 Agent 模式暂不支持 P2P。

Browser Sync 设备审批仍由现有 RelayProxy 管理员登录/CSRF 检查保护：

```text
GET  /api/v1/browser-sync/admin/devices
POST /api/v1/browser-sync/admin/devices/{id}/approve
POST /api/v1/browser-sync/admin/devices/{id}/revoke
```

审批 JSON 示例：

```json
{"identityId":"已存在且 active 的身份 ID","send":true,"receive":false}
```

B 设备应由管理员赋予 `receive:true`；A 赋予 `send:true`。浏览器身份不能继承 Agent 的代理和 RDP 权限。

## 两台 Chrome 的测试流程

1. A/B 在 `chrome://extensions` 开启开发者模式，将本目录作为“已解压扩展”加载。确保两台扩展 ID 与 Server 白名单匹配。
2. A/B 在扩展弹窗配置并授权 Admin HTTPS 地址，分别点击 **注册浏览器设备**。
3. 在 Server Admin API 中审批 A（发送）、B（接收），并关联到同一 active Identity。
4. 两边在扩展弹窗点击 **连接 Server**。A 点击“请求站点权限”，添加准确的 HTTPS **Origin**，不是带路径/查询参数的 URL。
5. A 点击刷新设备，选 B、授权网站和 **明确指定的 Cookie 名称**，创建加密配对邀请。Cookie 名称应来自用户有权检查的网站测试环境，不要将 Cookie 值粘贴到 UI。
6. B 刷新邀请，阅读网站和 Cookie 白名单，并通过**独立可信渠道**与 A 比较校验码后接受。A 再最终核对并确认，规则状态为 `active`。
7. A 使用测试账号在 Chrome 正常登录网站；站点 Cookie 变化时扩展会自动尝试发送，也可按“立即发送加密登录状态”。
8. B 点击“从来源设备请求同步”或“同步成功后打开网站”。B 没有其他登录状态时，写入符合范围的 Cookie；存在不同账号时默认显示 `CONFLICT`，只有用户明确勾选覆盖后才会重试。
9. 观察最终状态 `APPLIED`。随后确认网站是否实际上无需再次登录；如需再次认证，属于网站兼容性问题，不宣称绕过。
10. 在 A 测试账号登出后，再观察 B 是否清除此前由该规则写入的 Cookie；B 自己创建或被网站修改的 Cookie 不会被清除。
11. 任一设备可撤销配对，Server 随即拒绝后续转发并清理本地同步元数据。撤销 RelayProxy 配对 **不会**撤销网站已经签发的会话令牌。

**Cookie 兼容限制：** P3 仅支持所选择网站准确主机上的 Secure、hostOnly、`path=/`、非分区 Cookie；不自动扩展到父域、子域或 CHIPS。对复杂网站，采集可能明确失败而不是扩大 Cookie 读取/写入权限。

## 测试

```bash
cd browser/chrome-extension
npm test
```

Go 侧使用 `go test ./internal/browser_sync ./server/browser_sync ./server/api`。GitHub PR 的 Go CI 和 UI CI 也包含扩展 WebCrypto、作用域和冲突测试，以及带 TLS 的真实 WebSocket 测试。

设计文档：[Browser Session Sync v1.1](../../docs/browser-session-sync-design-development.md)。本分支是 Draft PR，未合并 `main`。

## 安全状态与兼容性备注

- **设备批准与撤销**：Server Web 的“设备与身份 → 浏览器同步”现在提供浏览器专用管理页面；新接口仍默认关闭。
- **登出处理**：快照使用加密的 `removedNames` 字段指示来源 Cookie 不再存在。目标设备仅删除此规则上一次恢复且当前值未改变的 Cookie；目标自行登录的相同名称（即使值相同）也不自动取得规则所有权。
- **指纹数据**：本地只保存由不可导出 HMAC CryptoKey 计算的规则专属 Cookie 归属标签。老版本开发快照的普通 SHA-256 标签会在扩展初始化时清理，不会被用于判断是否可删除。
- **身份撤销**：停止 RelayProxy 后续同步不意味着目标网站账号退出；必要时使用目标网站的“全部设备退出”功能。
- **浏览器兼容性**：Chrome 与不同站点的真实登录保持情况仍需授权测试，`APPLIED` 只说明 Chrome Cookie API 处理成功。

## P3 稳定性与安全加固

- **ACK 防伪与一次性确认**：Server 只接受与已验证、已投递的会话密文消息对应的 `messageId`，同时核对来源设备、目标设备和有效规则。收到终态确认后清除投递记录；任意捏造、重复、过期、其他设备发送的 ACK 均拒绝。
- **旧结果不能覆盖新结果**：来源 Chrome 只接受当前最近一次快照对应的 ACK，按规则记录最新消息 ID 和时间，不保存 Cookie/Token 内容。
- **断线恢复限频**：浏览器初始化时尝试补齐在线会话；每分钟唤醒只触发检查，成功对账后的周期性请求最低间隔为五分钟。Chrome MV3 Worker 仍可能在断线时休眠，不保证秒级后台在线。
- **连接复用**：并发点击连接、定时唤醒和控制请求不会同时打开多个挑战认证 WSS 连接。
- **Cookie 变化合并**：前一次快照发送期间再次变化时，发送完成后会从浏览器重新读取当前状态并补发，而不是丢弃变化。
- **验证范围**：Go CI 含投递记录、ACK 伪造/重复/过期/撤销测试，Chrome 扩展 CI 含重连复用、限频和过期 ACK 测试。仍须使用经授权的两台实际 Chrome 进行登录/登出兼容性验收。

## P3.1 故障恢复和上线前验证（开发分支）

本阶段额外提供三项故障恢复保护：

1. **Cookie 多字段恢复的失败补偿**：接收端先完整检查 Cookie 作用域、账号冲突与删除权限，再开始写入。写入发生异常时，逆序尝试恢复操作前状态；若浏览器/网站并发修改导致无法安全回滚，标记为 `PARTIAL`，停止后续自动接收，只有用户检查实际登录状态后才能手动解除。
2. **MV3 Worker 中断保护**：写入前用 `chrome.storage.local` 记录不含 Cookie 值的恢复意图，完成后与管理标记一同清除。若浏览器在写入期间关闭，下次打开扩展会提示检查和恢复，不能默默继续覆盖旧登录。
3. **序号与同步结果对账**：经认证的来源设备可使用 `SEQUENCE_CURSOR` 读取自己当前 ACTIVE 规则的已接收序号；WSS 断线时服务端保留短期 `APPLIED/FAILED/CONFLICT` 元数据，A 可以通过 `DELIVERY_STATUS` 查询。查询结果 `UNKNOWN` 并不代表 B 一定没写入，不能自动认定为登录失败。

上述元数据不会包含 Cookie 内容。Server 的结果保留窗口是短期的，不是离线 Cookie 邮箱，A 离线时 B 仍可能缺少最新源会话。

新增回归测试包含：多枚 Cookie 第二次写入失败、写入期间网站更新、未清除的恢复意图、浏览器序号记录丢失、设备越权查询以及真实 TLS/WSS 断线后的 ACK 恢复。**这些测试仍不能替代两台真实 Chrome 对授权网站的兼容性测试。**

## B 端本机 Agent 可选出口（开发预览）

使用 B 本机 Agent 时，浏览器扩展可为已配对的 HTTPS 会话规则指定独立回环接入端口。Agent `browser_sync.local_proxy` 仅需 `enabled: true` 和 `listen: "127.0.0.1:21291"`，不需要 Token、用户名密码或 Agent 域名白名单，也不依赖通用 SOCKS5/HTTP/透明代理开关；实际出站遵循 Agent 的 Relay/P2P/QUIC/自定义出口路由，DIRECT/REJECT 不会静默直连。**此无鉴权回环代理也可以被同机其他进程使用**，须在可信设备上显式启用。来源 A 的远端网络代理仍保留独立认证。详见 [网络绑定设计](../../docs/browser-session-network-binding.md)。

## 独立全 Chrome 网络代理（已集成、待真机验收）

普通浏览器网络代理不依赖 Cookie 配对，不额外限定 HTTPS:443、公共 DNS 域名、IP/IPv6/内网地址或 Agent 的 DIRECT/PROXY/REJECT 规则；Chrome 全局代理开关与 HTTP 正向代理已进入分支并有自动化测试。要使用它，必须在 B Agent 上显式开启 `browser_sync.local_proxy`，并通过扩展勾选全 Chrome 代理。**真实 Windows/macOS Chrome、WebSocket、HTTP POST/大流量、Agent 异常及代理覆盖冲突尚未验收**。Cookie 会话的设备批准、双端加密和 Cookie 安全作用域规则继续独立存在。Chrome 代理 API 不是 VPN，UDP/QUIC/WebRTC/部分 DNS 路径需要实测。

## Linux Server Web 的浏览器同步 HTTP 404 排查

**错误「服务端响应无效（HTTP 404）」来自 Server Web 的 `GET /api/v1/browser-sync/admin/devices`**，不是 Chrome Cookie 解密或 WSS 传输失败。开发分支前端在服务端返回非 JSON 404 时会丢失状态，已补保留 HTTP status 的修复；但真正使端点可用仍需更新 Linux Server 二进制和配置。

1. 确认 Linux 当前运行的 `relay-server` 来自 `feat/browser-session-sync` 或包含该功能的正式发布版，不能只更新 Chrome 扩展或 Server React 静态资源。`BrowserSync` 的路由由 Go Server 注册。
2. 编辑 **Server 运行时真正加载的** YAML（可与 systemd `ExecStart` 中的 `-config` 路径核对），在 Server 现有的 `server.admin` 中设置 `tls_enabled: true`、准备受 Chrome 信任的服务端 TLS 证书和密钥，然后设置：
   ```yaml
   browser_sync:
     enabled: true
     extension_ids:
       - "abcdefghijklmnopabcdefghijklmnop" # 换成真实 Chrome 扩展 ID
   ```
3. 重启 Linux `relay-server` 进程，查找日志 `[BrowserSync] Browser session relay enabled; extension allowlist size=...` 以及 `[Admin] Web Console & API starting on https://...`。
4. 在 Linux Server 本机直测 Admin 端口，例如 `curl -k -i --noproxy '*' https://127.0.0.1:20001/api/v1/browser-sync/admin/devices`（按实际 Admin 端口替换）。**未登录时正常返回 401 JSON**；404 为路由尚未注册/版本错误；若直连 401、经公网域名 404，则检查 Nginx/Caddy 的 `/api/v1/browser-sync/` 反向代理路径及端口转发。浏览器同步必须走 **Admin HTTPS**，不能只由反代代理终止 TLS 后以 HTTP 回源，因为服务端扩展入口要求 `r.TLS != nil`。
5. 管理页面登录后在「设备与身份 → 浏览器同步」重新刷新。A/B 扩展设备注册仍要求配置真实 extension ID、Admin HTTPS origin、设备审批。

## Browser Sync v2: no manual key / extension-ID entry

Choose an open HTTPS tab's origin in the extension popup, grant Chrome host
permission, choose a receiving device and confirm the E2EE pairing code on
both devices. New rules capture **every Cookie reachable to that origin**,
with their paths/domains, plus all Local Storage keys and all Session Storage
keys in one ordinary, currently open tab. The extension ID is reported via
its `chrome-extension://` Origin and approved under Server Web → 浏览器同步.
Server Web → 服务配置 → 浏览器同步 controls service/network settings.
Legacy named-Cookie rules remain narrow and must be recreated as v2 to
include all keys.

Important constraints: Session Storage is per-tab, not per-browser or
across other tabs; a receiving tab may be created in the background to
restore it. No IndexedDB, Cache Storage, service-worker caches, browser
profiles, device-bound credentials, or cross-origin third-party sessions are
exported. Snapshots are AES-GCM encrypted and signed between approved
devices. The Server never decrypts stored website state. Data larger than
the per-message limit fails explicitly, never truncates keys. A website
may invalidate tokens or require device fingerprint, IP or re-login.

Initial target-state conflicts are blocked unless the user explicitly
allows overriding on B. Source logout deletes only state demonstrably
owned by that same pairing. Interrupted restores require inspection and
manual resumption. Chrome real-device end-to-end tests (login persistence,
logout, conflicting accounts, DNS/QUIC/WebRTC leakage boundaries) remain
mandatory before merging this draft into main.
