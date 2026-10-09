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

Proxifier 的完整“通过代理解析 DNS”能力可使用 FakeIP 占位，并说明这会使部分基于真实目标 IP 的规则无法工作。RelayProxy 已新增**实验性的 FakeIP DNS 接管**：Windows WinDivert、Linux NFQUEUE、macOS Network Extension 对交付到拦截器的 UDP/TCP 53 A/AAAA 问题合成占位地址；Android VPN 使用独立的 DNS 机制。需设置 `routing.fake_ip_enabled: true`，且 `routing.dns_mode: proxy`。详细的接管范围、平台限制和验证要求见 [FakeIP DNS 接管（实验性）](agent-fakeip-dns-interception.md)。

- 本机生成的 FakeIP 对应的 PROXY 流量由上游解析真实域名；命中 DIRECT 规则时，经选定代理出口执行经 TLS 验证的 DNS 查询并在连接前复核真实 IP，失败则拒绝连接，而不将占位 IP 发送到网络。
- HTTPS/SVCB 返回 NODATA；TXT/SRV 可显式开启经代理 DoT 查询，默认关闭。DNSSEC、分流 DNS、多地址选择仍有限制。
- SOCKS5 上游返回域名形式的 TCP CONNECT 绑定地址时无需本机 DNS；SOCKS5 UDP ASSOCIATE 返回不能直接连接的域名绑定地址时将拒绝连接，不会为解析该地址而向本地 DNS 发送查询。
- FakeIP **不等于全平台零泄漏保证**。系统环回 DNS、应用自带 DoH/HTTPS 443、硬编码 DNS、底层驱动退出及 Windows/macOS/Linux/Android 平台差异仍需实机验证。独立 DNS Kill Switch 必须由管理员显式启用并验证，不会因打开 FakeIP 自动生效。
