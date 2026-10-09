# Agent DNS 解析位置与透明代理限制

参考文档：
- https://www.proxifier.com/docs/win-v4/dns.html
- https://www.proxifier.com/docs/win-v4/proxy-advanced.html

## 配置

在 Agent 的 `routing.dns_mode` 设置为 `proxy`（默认）或 `local`，也可在分流规则 UI 中热更新：

```yaml
routing:
  dns_mode: proxy
```

- `proxy`：代理连接中**已知的目标域名**直接传给选定 Relay 出口、SOCKS5/HTTP CONNECT 上游，由出口/上游解析；不在 Agent 本机解析该目标。
- `local`：代理连接中**已知的目标域名**先经 Agent 本地解析为 IP，再交给出口。查询失败则报错，不悄悄改用上游解析。
- DIRECT 流量始终使用系统 DNS，切换只影响新建的 PROXY 连接。
- 域名分流匹配只在路由决策前观察到域名时可用；IP/CIDR 规则仍需要可信的原始目标 IP。
- 实时监控独立展示命中规则、连接路径、原始目标 IP 与域名来源。

## 透明代理与 Proxifier 的差异

Windows/Linux 的透明包本身只有 IP；macOS Network Extension 有时能给出目标域名。Agent 可从匹配的 UDP/53 应答追踪 DNS 名称，但无法保证缓存、DoH/DoT、QUIC、ECH 等情况全部识别。

**重要：`dns_mode: proxy` 不代表完整接管系统 DNS 请求，也不保证没有 DNS 泄漏。** 它只是控制在 PROXY 连接已知目标域名时由 Agent 还是上游解析该目标。

Proxifier 的完整“通过代理解析 DNS”能力可使用 FakeIP 占位，并说明这会使部分基于真实目标 IP 的规则无法工作。RelayProxy 当前没有实现等价的全平台 FakeIP DNS 接管。

要达到同样能力，需要单独实现有界 FakeIP 池、客户端 DNS/UDP/TCP53 拦截、各平台请求关联、FakeIP->域名路由、DIRECT/局域网规则处理及泄漏防护，并针对 DNSSEC、IPv6、CNAME、DoH 等做兼容设计。当前配置项不能替代这些安全措施。
