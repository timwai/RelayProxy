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
- TCP/53 目前只恢复系统解析器的可达性，**没有**实现 TCP DNS 应答解析或域名关联；不能宣称 TCP DNS 首次域名规则已修复。
- 如果使用真实 IP 观察模式，DNS 允许本地发出，不能视为严格防泄漏配置。严格防泄漏需选择代理 DNS 接管并验证 Windows 防火墙保护。
- 需在 Windows 实机验证 DNS UDP 截断后 TCP 重试，以及默认 REJECT 下 Chrome/YouTube 首次访问、代理 DNS 与 FakeIP 的回归。

## Windows 代理接管的 TCP/53 域名关联

- 通过 Agent 代理 DNS 接管处理的 TCP/53 查询，只有经过 `interceptedDNSReply` 验证并获得响应后才写入现有 `dnsAssociations`，写入发生在响应交给客户端之前。
- 域名关联继续使用严格的问答匹配、A/AAAA 和共享 IP 歧义处理，不从任意 TCP 负载猜测域名。
- DNS 关联关闭时跳过写入。此修改不监听普通 DIRECT TCP/53 的 DNS 消息，也不保证 Chrome 私有 DoH 可见。
- 单元测试已添加，但仍需通过 CI 和 Windows 端到端测试验证。
