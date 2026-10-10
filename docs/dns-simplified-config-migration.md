# DNS 简化配置（第一阶段）

## 用户模式

桌面 Agent DNS 页仅直接展示三个模式，旧版 routing 配置字段暂不删除，以便旧版本 Agent、Android 和配置文件继续读取。

| 新模式 | 现有 routing 字段 | 行为与限制 |
|---|---|---|
| 自动（推荐） | `dns_mode: proxy`, `auto_detect_dns: false`, `proxy_dns_enabled: true`, `fake_ip_enabled: false`, `dns_association_enabled: true` | 对可捕获的系统 DNS/53 经出口执行真实 IP 查询。不是自动检测网络条件，也不保证捕获私有 DoH、环回 DNS。 |
| 真实 IP | `dns_mode: proxy`, `proxy_dns_enabled: false`, `fake_ip_enabled: false`, `dns_association_enabled: true` | 不接管系统 DNS，使用旁路 DNS 关联；真实 DNS 可以从本机发出。 |
| FakeIP | `dns_mode: proxy`, `proxy_dns_enabled: false`, `fake_ip_enabled: true`, `dns_association_enabled: true` | 虚拟地址映射，仍需要底层透明捕获正常运行。 |

其他 routing 字段（出口、规则、DoH 列表、已有高级参数）不随模式投影清空，除了模式互斥或依赖条件要求关闭的字段。

## 向后兼容

- 读取旧配置不会修改持久化文件；只在用户切换模式并保存时写回对应字段。
- 旧配置只用两种证据推导界面模式：FakeIP 开启 -> FakeIP；否则 proxy_dns_enabled -> 自动；其余 -> 真实 IP。
- 原先的 `auto_detect_dns: true`（本地解析失败才使用代理）在新界面中归入「真实 IP」展示；不会在打开页面时自动覆盖该值。**只有用户主动切换 DNS 模式才会应用新投影。**
- 高级设置中仍能编辑原先开关，便于排障。此举不代表它们已被完全替代。

## 后续必须单独实现与验证的工作

1. 统一 Windows、macOS、Android 原生界面和 Web 的 DNS 模式字段，不直接删除旧 YAML 字段。
2. Windows WinDivert DNS/53 规则与默认阻断的优先级检查；不能直接豁免所有 53 流量。
3. 明确 DNS 解析结果交给连接匹配器的时序，并记录域名关联来源、DNS 缓存未命中和决策路径。
4. 用多域名共享 IP 测试规则误命中；缓存不能靠一个 IP 任意猜单一域名。
5. 验证代理出口 DNS、FakeIP、DoH/DoT、IPv6、环回 DNS、Agent 退出、以及配置热更新。
6. 真实 IP DNS 通过所选代理出口请求外部 DoT 时的可用性和访问控制；不能把防泄漏的 UI 选项当成已验证保护。

这次是**配置/UI 第一阶段**，不应标记为完整 DNS 安全修复。

## 2026-10-10 Windows 默认阻断与 DNS/53 修复

- 在真实 IP 观察模式下，当且仅当路由结果是默认 `REJECT`，放行普通 UDP/53 与 TCP/53；显式 DNS REJECT 继续生效。
- FakeIP 与代理侧真实 IP DNS 接管模式不启用这项旁路，避免与严格接管策略冲突。
- 第一阶段仅恢复 TCP/53 系统解析器的可达性，后续版本已增加 DNS/TCP 旁路监听及受限重组；是否解决首次连接仍需在 Windows 浏览器实测。
- 如果使用真实 IP 观察模式，DNS 允许本地发出，不能视为严格防泄漏配置。严格防泄漏需选择代理 DNS 接管并验证 Windows 防火墙保护。
- 需在 Windows 实机验证 DNS UDP 截断后 TCP 重试，以及默认 REJECT 下 Chrome/YouTube 首次访问、代理 DNS 与 FakeIP 的回归。

## Windows 代理接管的 TCP/53 域名关联

- 通过 Agent 代理 DNS 接管处理的 TCP/53 查询，只有经过 `interceptedDNSReply` 验证并获得响应后才写入现有 `dnsAssociations`，写入发生在响应交给客户端之前。
- 域名关联继续使用严格的问答匹配、A/AAAA 和共享 IP 歧义处理，不从任意 TCP 负载猜测域名。
- DNS 关联关闭时跳过写入。后续独立加入普通 DIRECT TCP/53 旁路监听；Chrome 私有 DoH 仍不可见。
- 单元测试已添加，但仍需通过 CI 和 Windows 端到端测试验证。

## 2026-10-10 被动 DNS/TCP 关联（Windows）

- Windows WinDivert 入站筛选器增加 `tcp.SrcPort == 53`，使用与原有 UDP DNS 相同的旁路重注入路径，不修改入站数据。
- 本机出站的 `TCP/53` 在发送前尝试读取完整 RFC 7766 DNS 报文；入站 TCP/53 响应在交给 Windows 网络栈之前提取完整报文并匹配问题。
- DNS 查询关联缓存现在按 TCP 与 UDP 传输分别匹配，避免相同端点和事务 ID 跨协议误关联。
- 为控制开销，现在支持**序号连续的有限 TCP 分段重组**：最多 512 个方向流、单条 DNS/TCP 报文不超过 8192 字节，闲置/异常状态 10 秒过期。序号跳跃、部分重叠、非法长度时丢弃该方向的观察状态，不重写 TCP 流量。乱序重排、任意丢包恢复和加密 DNS 仍不支持。
- 新增保守解析、错误响应与过滤器覆盖测试。此实现只是域名识别的补充，不提供对 DoH、DoT 或环回 DNS 的全覆盖承诺。

## Windows DNS 缓存启动同步与 Android UX

- Windows 仅在 **DNS 真实 IP 被动关联模式** 下，在 WinDivert 捕获启动后尝试调用 `dnsapi.dll!DnsFlushResolverCache`。这使 Windows DNS Client 重新发出的明文 DNS 查询能被新启动的 DNS snooping 捕获，减少因缓存先于 Agent 启动而导致的首次域名规则未命中。
- 缓存刷新为尽力而为：不支持该 API 或失败时只记录日志、不阻断代理启动；不会清理 Chrome、Firefox 私有 DNS 缓存，也无法保证 DoH/DoT 被观察到。
- Android 当前使用 VPN 内自动 `Mapped DNS`，并没有与桌面 DNS 三模式一一对应的 FakeIP/真实 IP 开关。因此 Android 设置展示“DNS 自动处理”及能力边界，不添加不能兑现的模式选择。
- Windows 默认 REJECT 下 DNS 放行、TCP DNS 分段关联、Windows DNS Client 缓存刷新、Android Mapped DNS 文案均仍需要各自的端到端验证。**这些变更不是完整的系统防泄漏保证。**

## 2026-10-10 自动 DNS 无法解析的实机反馈与修复

用户实测：桌面 Agent 的 **自动** DNS 模式无法访问代理目标，但「真实 IP」和「FakeIP」模式正常。问题排查发现自动模式以前只允许走所选出口连接 `9.9.9.9:853` 的 Quad9 DNS-over-TLS。某些代理出口无法建立 853 连接，系统 DNS/53 又已被 RelayProxy 拦截，因此返回 SERVFAIL，造成整站无法解析。

修复策略：

- 自动模式的 **代理接管真实 IP DNS** 优先使用 Quad9 `https://dns.quad9.net/dns-query`（RFC 8484 `application/dns-message` POST），TCP 443 由配置的 Relay/自定义代理出口拨号到固定地址 `9.9.9.9`。TLS 使用 `dns.quad9.net` 严格验证证书；不得依赖本机域名解析、环境 HTTP_PROXY 或跳转其他目标。
- HTTPS/443 故障时再尝试原有 DoT/TLS 853；两个路径失败才返回 SERVFAIL，**禁止**自动改走本机明文 DNS/53。
- 将 16 路并发槽位的「队列满立即失败」替换为有总超时时间的等待，不再因为浏览器批量 DNS 查询直接产生突发 SERVFAIL。
- 记录每 30 秒最多一条自动 DNS 上游故障摘要：`[divert] proxy DNS resolution failed ...`，注明 DNS 出口及 DoH/443、DoT/853 的失败原因，便于实机故障定位。
- 不改变 FakeIP 解析行为、不增加用户侧 DNS 开关。新增回归测试验证出口/端口顺序、DoH 报文验证及限时排队。

说明：此补丁解决 853 单点依赖及 DNS 高并发立即失败的可疑根因，但需更新 Windows Agent 并通过同一网络环境重复测试，才能确认用户报告的症状完全消失。

## 2026-10-10 Quad9 地域可用性与 HTTP/2 兼容修复

- 自动真实 IP DNS 不能把 Quad9 当成**唯一**必须可达的公共解析器，即使通过 Relay 出口访问，仍可能受出口位置/网络对 `9.9.9.9` 或 853 端口的限制。
- Quad9 官方自 2025-12-15 起禁用 DoH HTTP/1.1；此前 DoH 客户端显式禁用 `ForceAttemptHTTP2`，这是独立的协议兼容风险。现在 DoH Transport 启用 HTTP/2，并为每个上游使用匹配的 TLS ServerName 验证证书。
- 自动模式通过**所选出口**依次尝试 Cloudflare (`1.1.1.1`, `cloudflare-dns.com`) 、Google (`8.8.8.8`, `dns.google`) 和 Quad9 (`9.9.9.9`, `dns.quad9.net`) 的 DNS-over-HTTPS/443。只有这些上游均无法成功应答时再尝试原有 Quad9 DoT/853；失败后返回 SERVFAIL，不会自动泄漏至本机 DNS/53。
- 每个 DoH 上游尝试限制为 2 秒，总体 DNS 查询仍受 10 秒超时约束。可信的 NXDOMAIN 保持原样返回，上游 SERVFAIL 可尝试下一个上游。切换公共 DNS 服务商可能造成过滤策略、CDN 地址及隐私实践的差异。
- **不能保证任何公共 DNS 在中国境内的出口网络都可达**。如果选定出口位于受限制网络，应使用能访问加密 DNS 上游的境外出口；长期方案是支持受控、自定义、证书验证的加密 DNS 上游或出口本地 resolver，而非默认回退本机 DNS。
- 这是代码级问题修补。Windows 实测仍要确认浏览器初次访问恢复，现有 CI 通过不等于当地网络可用。

## 自定义加密 DNS 上游（出口节点可提供独立 DoH）

自动模式不再必须依赖预设公共 DNS。可在桌面 Agent「DNS 设置 → 高级设置 → 自定义 DoH 上游」按行填写 `https://hostname/path | 固定连接 IP`，或者直接修改 Agent 路由配置：

```yaml
routing:
  dns_mode: proxy
  proxy_dns_enabled: true
  dns_exit_id: ""  # 可填经当前身份授权的出口 ID
  dns_upstreams:
    - url: "https://dns.exit.example/dns-query"
      bootstrap_ip: "10.0.0.53"
    - url: "https://dns.backup.example/dns-query"
      bootstrap_ip: "192.0.2.53"
```

- `url` 必须为 HTTPS 且使用有效域名及证书；`bootstrap_ip` 是经**选定代理出口**连接的固定 IPv4/IPv6 IP，Agent 不会解析该 URL 的域名（防止 DNS 自举循环）。
- 最多 8 条，按照配置顺序故障切换。若列表为空，使用内置的 Cloudflare / Google / Quad9；若非空，**只使用列出的上游**，全部失败则 SERVFAIL，不能暗中再访问公共 DNS 或本机 DNS。
- 配置私网 `bootstrap_ip` 时，需要出口节点确实能够访问该内网 HTTPS DNS，同时出口 ACL 明确允许相关私网地址和端口。此功能不会自动部署 DoH 服务器。
- TLS 证书必须与 URL 的域名匹配且由系统受信任的 CA 签发；不会忽略 TLS 验证。URL 不得包含用户凭据、任意端口、查询参数或片段。
- 当前定制化只覆盖**自动模式的真实 IP 加密 DNS**。FakeIP 的特殊 TXT/SRV 透传仍沿用现有 DoT 独立实现，后续需要单独统一。
- 增加路由校验、配置复制、GUI 保存和自定义上游独占性回归测试；Windows 实机连通性仍需用具体出口与私有 DoH 服务确认。

## 2026-10-10 实机复测：DNS 专用出口影响自动模式

- 用户反馈：原 DNS 查询出口下，桌面 Agent「自动」模式无法正常访问国外网站，而「真实 IP」和「FakeIP」模式可用。
- **复测确认**：仅将「加密 DNS 查询出口」（`routing.dns_exit_id`）切换为国外节点后，**自动模式恢复可用**。
- 该结果有力支持「原 DNS 出口到加密解析器的可达性不佳」的推断，但**不能单凭本次操作判断究竟是 Quad9/Cloudflare/Google 哪一个被阻断、是否存在 DoH/DoT 协议协商失败或超时**；需要代理 DNS 上游故障日志及出口侧网络测试才能确认具体原因。
- DNS 专用出口只负责加密解析，正常代理连接仍由分流规则选择出口。因此可以长期采用「DNS 用可靠的国外出口、应用流量继续按分流规则」的配置。
- 本次验证说明调整出口能恢复自动模式，**尚未证明 Chrome/YouTube 无缓存首次连接、独立 Kill Switch、私有 DoH/环回 DNS 和断线防泄漏均通过**。
- 建议后续在 DNS 设置中提供上游可达性诊断（按选中 DNS 出口分别测试 DoH 443、DoT 853 并显示错误原因），避免用户只能靠手工切换节点排查。
