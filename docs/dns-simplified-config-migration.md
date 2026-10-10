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
