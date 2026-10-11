# RelayProxy Browser Sync：A Agent 网络出口绑定

> **实施状态（2026-10-11）**：开发分支 [`b504837`](https://github.com/timwai/RelayProxy/commit/b504837a113a8e312c44335b9077fe3f2e80691e) 已集成完整 Chrome 本机 Agent 入口：标准 HTTP Forward、任意合法 TCP CONNECT/IP/内网/端口、现有 Agent DIRECT/PROXY/REJECT 和无须 Cookie 配对的全 Profile 开关。Go/UI/Network CI success，但**尚无真实 Chrome/安全验收**。下文旧 B 本机 DNS:443/配对限定段落是历史行为说明，**已被文末完整代理实现取代**；A 远端共享网络仍独立认证/批准。最新状态见 [持续开发跟踪表](./browser-session-sync-progress.md)。

> 2026-10-11 第一阶段开发预览 · 分支 `feat/browser-session-sync` · 未通过双机真实浏览器验收，不得默认开放生产公网代理。

## 使用场景

A 安装 **RelayProxy Agent + Chrome MV3 扩展**；B 只安装 **Chrome MV3 扩展**。A/B 的 Cookie 已经过独立设备注册审批、双端规则确认与 E2EE。来源 A 选择已在线、属于同一身份且由 Server 管理员允许共享的 Agent Exit；B 独立核对加密 Cookie 策略的站点域名并点击同意。B 的站点 Chrome 请求走 HTTPS CONNECT → Relay Server → A Agent 的 OpenTCP → 目标网站；DNS 在 Agent Exit 解析。同步网站最终是否免登录仍由网站决定。

## 浏览器 Web 页面直接配置（新增）

不必手工编辑 Server / Agent YAML，管理员可在两个 Web 页面完成配置。Chrome 扩展自身只负责请求并审批配对、不能越权修改 Server 或 Agent 的共享设置。

1. **Server Web → 浏览器同步 → Agent 网络出口 · 浏览器配置**：设置代理 TCP 监听地址（如 `:21292`）与对外 DNS 地址（如 `relay.example.com:21292`），从在线 Agent 出口卡片中勾选要允许共享的设备 ID，然后点击“保存网络出口配置”。页面使用已存在的、仅管理员可用的 `GET /api/v1/server/config` 和 `PUT /api/v1/server/config` API；带 revision 保存，检测并拒绝并发配置覆盖。**保存需要重启 Server 才能让监听器和出口白名单生效**；浏览器页面不能远程开启防火墙或自动签发 TLS 证书。
2. **A Agent Web / GUI → 本机出口共享 → 允许浏览器网络共享**：显式开启该 Agent 的共享能力并保存。界面读写 `exit.browser_network_share`，原有 `proxy.exit` 能力、身份与 ACL 不会因此自动放宽。**需重启或重连 Agent** 才能重新发布带共享能力的握手。
3. **A Chrome 扩展 → 完整设置 → 会话与出口规则**：在已激活的 HTTPS Cookie 规则下选择在线 Agent，向 B 发送网络绑定邀请；B 再独立确认。HTTP、IP、非常规端口仍不在此站点级共享功能范围内。

现有 **Server Web → 服务配置 → 浏览器同步** 仍提供同一份配置的高级编辑入口。若 `allowed_exit_ids` 列表中已有离线设备，浏览器配置页保留并允许手动取消，不会因为暂时离线自动丢失授权记录。退出列表仅代表在线出口节点，最终是否可共享仍由 Agent 本地能力宣告和 Server 实时校验共同决定。设置页面不展示 Token、Cookie 或配对密钥。

## 必须显式启用

Server 配置示例（独立 TCP 监听端口不可与 admin、tunnel 冲突）：

```yaml
# A Agent（独立的 agent.yaml）：
# exit:
#   browser_network_share: true
#
# Server (server.yaml)：
browser_sync:
  enabled: true
  extension_ids:
    - abcdefghijklmnopabcdefghijklmnop # 实际发布扩展 ID
  network_proxy:
    listen: ":21090"
    advertise: "relay.example.com:21090" # 有效公开证书对应的 DNS 主机
    allowed_exit_ids:
      - dev_replace_with_approved_agent_device_id
```

要求 Admin HTTPS、TLS Server 证书校验正常；Server 的 21090/TCP 开放给 B Chrome。A Agent 必须正常连接 Server、已获 `proxy.exit` 能力并开启允许外网的 Exit，同时**在 A 端本地配置中明确设置 `exit.browser_network_share: true`，并重启/重连 Agent**。Server 明确列入 `allowed_exit_ids`。浏览器共享出口需要经过 Agent 签名握手的 `browser.network.exit.v1` 和本机独立 `FrameTypeBrowserNetworkTCP` 双重检查，只有普通代理 Exit 授权不足以开放此功能。保持 Browser Sync / network_proxy 默认关闭。此功能不会为 B 创建完整 Agent 设备，也不授予 B 任何 `proxy.client`、RDP 权限。


## 常见错误：rule not authorized or invalid

这是旧版 Server 将全部 Browser Sync 授权失败合并显示的一条通用错误，不能据此判断 Cookie 规则有问题。对于“为此 Cookie 规则绑定 Agent 网络”，应依次确认：

1. **Server 的独立网络代理监听已启用**：配置 `browser_sync.network_proxy.listen`、`advertise`、`allowed_exit_ids` 后重启 Server（见上方示例）。仅设置 `browser_sync.enabled: true` 只启用 Cookie/Storage 同步，不会创建 Agent 网络共享入口。
2. **来源 A 浏览器有 send 权限**：Server Web → 浏览器同步 → 浏览器设备；该配对必须是 `active`，A/B 双端校验码已确认，B 设备具有 `receive` 权限并与 A 使用相同 Identity。
3. **Agent 出口具备共享能力**：相同身份的 Agent 在线、具备 `proxy.exit`，在 Agent `exit.browser_network_share: true` 后重连；Server `allowed_exit_ids` 中列入准确的 Agent 设备 ID。普通可用的出口不代表它已授权分享浏览器网络。
4. **目标域名属于受支持的绑定范围**：仅准确 HTTPS 443 的普通 DNS 域名，不支持 HTTP、IP、域名自定义端口。已经撤销或存在先前网络绑定的 Cookie 规则，不可在同一记录上重发不变式冲突的邀请；需要重新配对后再绑定。

新版 Server 将为 `NETWORK_EXITS` 和 `NETWORK_OFFER` 返回专用错误代码（`NETWORK_DISABLED`、`SOURCE_SEND_REQUIRED`、`RULE_INACTIVE`、`BINDING_EXISTS`、`EXIT_NOT_ALLOWED`、`NETWORK_OFFER_DENIED`），扩展在错误消息中直接展示对应原因，不暴露 Cookie/Storage 密文或密钥。升级 Server 才能看到具体原因，单独更新扩展无法消除旧版 Server 的统一错误。

## 授权和工作流

1. A/B Chrome 完成原 Browser Sync 站点 Cookie 规则配对；A Agent 同身份在线。
2. A 的浏览器在已激活规则中点击“为此 Cookie 规则绑定 Agent 网络”，从 Server 提供的在线允许列表中选择 Agent，发出绑定邀请。服务端保存精确 hostname 与 Exit ID，不读取 Cookie。
3. B 的浏览器验证双端公钥、解密现有站点策略并确认 hostname 一致，明确同意网络绑定。
4. B **先安装禁止直连的 PAC**，Server 将网络绑定标记为 active，扩展请求短时接收设备代理令牌，经 `chrome.proxy` 为精确网站配置 `HTTPS relay.example.com:21090`，使用 `webRequestAuthProvider` 响应 Proxy 407。
5. 代理 Server 不接受直接的 HTTP 代发，仅支持 HTTPS CONNECT 目标 :443；校验令牌、目标域名、活跃配对规则、来源/接收设备同身份、Agent 在线/Exit 能力/Server 白名单。通过后建立 Agent OpenTCP 流。每次连接重新做授权，不转发匿名流量。
6. Agent 离线或规则撤销后，新 CONNECT 失败。B 的 PAC 对已绑定站点不得回退 `DIRECT`，故障下返回黑洞代理。主动撤销网络绑定可能恢复直连，UI 应清楚提示已同步 Cookie 不会自动清除。

## 安全边界与限制

- 服务端知道 HTTPS CONNECT 域名、出口设备 ID 和访问行为，但 **不读取 Cookie 明文**；A/B 浏览器端仍负责 Cookie 的 E2EE。
- Chrome 的代理设置属于整个浏览器 Profile；不能保证同 Profile 内与其他代理扩展并存。应优先在单独 Chrome Profile 进行验收。
- 代理只覆盖已批准 hostname；页面请求的**其他第三方域名**仍可走本地网络。WebRTC、扩展内网络请求、DNS、系统服务和浏览器特殊网络路径均需专门验证，因此这是 **单站点出口绑定**，不是全系统 IP 一致性或绝对防泄漏。
- Chrome 可能缓存同一 HTTPS 代理 realm 的 Basic 凭据：凭据只绑定 B 浏览器设备，Server 必须按每次 CONNECT 的 hostname 重新找唯一已授权规则，不能将 Basic 凭据当作来源站点的授权证明。
- 服务端内存代理令牌有效期 15 分钟，重启即失效；服务器不持久化令牌或隧道数据。浏览器 `chrome.storage.session` 仅保留临时认证信息。
- 必须实测 TLS 认证（不要在 B 忽略证书）、401/407 行为、Chrome PAC mandatory/代理失败处理、浏览器重启/MV3 休眠、Agent 崩溃、Cookie ACK 对账和撤销后连接断开边界。新增周期性的存量 CONNECT 授权复核（2 秒一次）：发现 Cookie 规则/网络绑定/设备权限/Agent 会话更替或服务端异常即关闭连接；Server Shutdown 还会主动关闭被 HTTP Hijack 的 CONNECT 连接；仍需真实跨端撤销测试、跨实例租约、第三方资源组策略和 UI 管理审计。
- 管理员不能仅凭浏览器用户自行提供的请求批准 Agent 出口；固定的 Server 出口白名单是第二道同意。若 A 设备应控制是否允许分享其网络，还需提供 Agent 端显式本地开关和管理员授权审计后再正式发布。

## 验收准入

- [ ] Windows A Agent + A Chrome → Windows B Chrome，测试站点 Cookie 同步与网站实际登录态。
- [ ] macOS A Agent + A Chrome → Windows/macOS B Chrome，验证目标公网 IP 与 A Agent 一致。
- [ ] 非批准 hostname / IP / 内网请求以及无代理凭据必须拒绝。
- [ ] A/B 单端撤销、Agent 离线、WSS 断开、Browser Worker 重启：绑定网站不能偷偷直连。
- [ ] PAC 与其他代理扩展/系统策略冲突、Chrome HTTPS 407 缓存、多 Cookie 失败均有真实 Chrome 测试记录。
- [ ] 压力、跨实例、日志脱敏、密钥/凭据生命周期、Agent 本地授权、现有 RDP/P2P/Proxy 全量回归均通过。

参见 [Browser Sync 发布进度](./browser-session-sync-progress.md)。
## B 端可选：使用本机 Agent 的原生网络出口（独立于 SOCKS5/HTTP/透明代理）

> 新增的本机模式是**可选**的，不影响原有「A Agent 出口」和「B 无 Agent Cookie-only」模式。功能开发预览，尚无双设备真实 Chrome 验收。

B 的 Chrome 扩展在一条**已激活的接收端 Cookie 规则**中可以按站点选：

- **使用 A 来源 Agent**：B Chrome → Server HTTPS CONNECT → A Agent，网站可见 A 的出口 IP；需要 A/B 双端网络授权和 Server 白名单。
- **使用 B 本机 Agent 原生出口**：B Chrome → 回环专用 CONNECT 适配入口 → B Agent `RoutingDialer` → 已配置的 Relay/P2P/QUIC/自定义出口。**不要求开启通用 SOCKS5、HTTP Proxy 或 WinDivert/Network Extension 透明代理**。Chrome 本身只能使用标准代理协议，此处 HTTP CONNECT 只是 Chrome→Agent 的适配层，**实际代理传输由 RelayProxy Agent 决定**。
- **Cookie-only**：不绑定代理出口，原有双端 Cookie 同步流程不受影响。

### B Agent 本地配置

在 **B 的** `agent.yaml` 中显式启用独立服务，示例：

```yaml
browser_sync:
  local_proxy:
    enabled: true
    listen: "127.0.0.1:21291"
```

本机 Agent 不使用用户名、密码、Token 或 Agent 端域名白名单，也不返回 HTTP 407；B 扩展点击「改用 B 本机 Agent 原生代理」即可选择入口端口。Chrome 扩展仍使用**已完成双端设备配对的 Cookie 规则**来生成精确主机名 PAC；浏览器启动和 Server/规则异常时先将绑定域名指向不可用的本机黑洞端口，经确认规则后恢复专用入口。**无鉴权仅适用于 B 本机入口**，A 的远端 Server HTTPS CONNECT 仍要求设备授权和代理租约。

**本机 Agent 服务端只进行结构与路由限制，不做域名白名单或代理身份验证**：TCP 监听限定为 127.0.0.1；只接受 HTTP CONNECT 到合规 DNS 名称的 443 端口；拒绝 IP 字面量、`.local`、`.internal`、`.localhost`、`.arpa` 后缀与其他非法主机名；最多 64 条并发连接；转发目标必须命中 Agent 的 **PROXY** 路由，`DIRECT`、`REJECT` 均阻断。Agent 使用原生规则判断目标出口，而非维持独立的授权站点名单。Chrome 自身仍仅为已绑定网站设置 PAC，其他网站保持原有网络。

### 重要限制

Chrome 的 PAC/HTTP CONNECT 不提供整个系统或浏览器的任意协议覆盖；WebRTC、DNS/第三方资源、Service Worker 内部请求仍需真实浏览器防泄漏验收。B 使用本机 Agent 的公网 IP 应当等于 **B Agent 当前选中或分流规则决定的出口 IP**，不必与 A 相同；网站仍可使用设备绑定、Passkey、DBSC 等机制拒绝会话迁移。

**安全取舍**：由于没有本机鉴权和 Agent 域名白名单，B 机器上的**其他本地程序也可以连接这一回环代理**，并向任何符合目标格式、路由规则允许的公开域名发起 CONNECT。Chrome PAC 的单站点约束只限制扩展管理的浏览器流量，**不是 Agent 监听器的访问控制**；它不能隔离同一台设备上的其他用户或进程。需确保本机系统可信、默认关闭此入口，并在正式发布前复核本地恶意进程、DNS 重绑定、目标内网解析和配额风险。若多个绑定网站使用同一 Chrome Profile，当前只允许一个本机端口；切换本机/来源 Agent 时重新验证浏览器代理控制权。真实 A/B 测试仍在 P0。

## 完整 Chrome 本机 Agent 代理：已集成代码、待真实浏览器验收

**已集成行为（未实机验收）**：本机 Agent 代理独立于 Cookie 同步，可在 Chrome Profile 中单独开关，不要求 Server 在线、设备注册、A/B 配对或站点 Cookie 权限。使用 Agent 原生 PROXY / DIRECT / REJECT 分流，不在本机 HTTP 入口额外限制目标域名/IP 类型/私网/环回/内部后缀或 TCP 端口；支持普通 HTTP Forward 与任意合法 TCP CONNECT（含 IPv4/IPv6/非 443 端口），明确已批准的来源 A 远端规则优先。**所有访问仍遵守 Agent 原有分流和 Exit ACL，并非绕过 Agent 安全规则。**

原草稿 [`b7546cf`](https://github.com/timwai/RelayProxy/commit/b7546cf4bb85de249763672d5d48a5a17cfaba57) 已整合到 [`eaf13e8`](https://github.com/timwai/RelayProxy/commit/eaf13e83029eb0272eda81f99f727e1b07eeef02)，随后在 [`b504837`](https://github.com/timwai/RelayProxy/commit/b504837a113a8e312c44335b9077fe3f2e80691e) 修复 Go 格式与 HTTP/CONNECT 测试。对应 [Go](https://github.com/timwai/RelayProxy/actions/runs/38108843827)、[UI](https://github.com/timwai/RelayProxy/actions/runs/38108843828)、[Network](https://github.com/timwai/RelayProxy/actions/runs/38108843833) CI 已 success；它们只能证明所测路径，并不代替真实 Chrome 端到端、UDP/QUIC/DNS 测试或安全审查。

**不能解除的技术事实**：Chrome Proxy API/PAC 不能代替 OS VPN；WebRTC、UDP/QUIC、系统 DNS、某些浏览器代理绕过规则可能走其他路径，须明确记录实际可控制的协议。Cookie 复制/恢复继续执行严格 E2EE、设备审批和显式授权；取消普通网络代理的限制并不等于取消 Cookie 权限校验。由于 B 本机 Agent 入口无鉴权且拟允许任何 TCP 地址（包括内网），同机程序滥用、内网连接与 DNS 重绑定风险必须单独审查，保持默认关闭、仅监听 127.0.0.1。

**验收清单**：HTTP GET/POST/Upgrade、HTTPS/WSS、非 443 CONNECT、IPv4/IPv6、单标签与 `.local` 域名、DIRECT/PROXY/REJECT、全 Profile 开关、Chrome 代理冲突、无 Server/无 Cookie 配对、Agent 退出/重启、真实网站出口 IP；另测 PAC loopback bypass、WebRTC/QUIC/DNS 非代理路径、内网 SSRF/同机滥用、资源并发与断连释放。详见 [P0-08、NB-09/10/11](./browser-session-sync-progress.md)。
