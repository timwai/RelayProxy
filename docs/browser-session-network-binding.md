# RelayProxy Browser Sync：A Agent 网络出口绑定

> **实施状态（2026-10-11）**：下方“A 远端出口”和“B 本机 Agent”章节描述**当前 `feat/browser-session-sync` HEAD `031baf9` 的旧范围**：远端 A 需认证与双端批准；B 本机 opt-in、无鉴权/域名白名单，但 CONNECT 仍仅 DNS:443，使用已配对 Cookie 规则生成 PAC，且强制拒绝 DIRECT。下一阶段拟移除本机浏览器代理的人为目标限制，详见文末“完整 Chrome 代理目标”；尚未进入分支且不可视为已实现。完整进度以 [持续开发跟踪表](./browser-session-sync-progress.md) 为准。

> 2026-10-11 第一阶段开发预览 · 分支 `feat/browser-session-sync` · 未通过双机真实浏览器验收，不得默认开放生产公网代理。

## 使用场景

A 安装 **RelayProxy Agent + Chrome MV3 扩展**；B 只安装 **Chrome MV3 扩展**。A/B 的 Cookie 已经过独立设备注册审批、双端规则确认与 E2EE。来源 A 选择已在线、属于同一身份且由 Server 管理员允许共享的 Agent Exit；B 独立核对加密 Cookie 策略的站点域名并点击同意。B 的站点 Chrome 请求走 HTTPS CONNECT → Relay Server → A Agent 的 OpenTCP → 目标网站；DNS 在 Agent Exit 解析。同步网站最终是否免登录仍由网站决定。

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

## 完整 Chrome 本机 Agent 代理：最新需求与未实现工作

**目标（暂未入功能分支）**：本机 Agent 代理是独立于 Cookie 同步的普通浏览器网络能力，可在 Chrome Profile 中单独开关，无须 Server 在线、设备注册、A/B 配对或站点 Cookie 权限。使用 Agent 原生 PROXY / DIRECT / REJECT 分流，不在扩展或本机 HTTP 入口另行限定目标域名、IP 类型、私网/环回/内部后缀或 TCP 端口；支持普通 HTTP 正向代理及任意合法 TCP CONNECT（含 IPv4/IPv6、非 443 端口），明确已批准的来源 A 远端网络规则可以覆盖对应站点。**所有访问仍须遵守 Agent 自己的访问策略/出口 ACL，不能将“无扩展人工限制”理解为绕过 Agent 安全规则。**

现有分支 `031baf9` **尚未达到该目标**。草稿 [`b7546cf`](https://github.com/timwai/RelayProxy/commit/b7546cf4bb85de249763672d5d48a5a17cfaba57) 包含新增 HTTP Forward/通用 CONNECT、原生路由委托、Chrome 全局代理 UI/PAC 与部分测试，但**该提交尚未位于 `feat/browser-session-sync`，没有任何 CI runs，也无真实 Chrome 验收**。应先进入功能分支、修复格式/构建/单元测试与完整集成行为，再按全新 SHA 重新验收。

**不能解除的技术事实**：Chrome Proxy API/PAC 不能代替 OS VPN；WebRTC、UDP/QUIC、系统 DNS、某些浏览器代理绕过规则可能走其他路径，须明确记录实际可控制的协议。Cookie 复制/恢复继续执行严格 E2EE、设备审批和显式授权；取消普通网络代理的限制并不等于取消 Cookie 权限校验。由于 B 本机 Agent 入口无鉴权且拟允许任何 TCP 地址（包括内网），同机程序滥用、内网连接与 DNS 重绑定风险必须单独审查，保持默认关闭、仅监听 127.0.0.1。

**验收清单**：HTTP GET/POST/Upgrade、HTTPS/WSS、非 443 CONNECT、IPv4/IPv6、单标签与 `.local` 域名、DIRECT/PROXY/REJECT、全 Profile 开关、Chrome 代理冲突、无 Server/无 Cookie 配对、Agent 退出/重启、真实网站出口 IP；另测 PAC loopback bypass、WebRTC/QUIC/DNS 非代理路径、内网 SSRF/同机滥用、资源并发与断连释放。详见 [P0-08、NB-09/10/11](./browser-session-sync-progress.md)。
