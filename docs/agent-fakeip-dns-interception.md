# FakeIP DNS 接管（实验性）

当前进度：Agent Windows（WinDivert）、Linux（NFQUEUE）、macOS（Network Extension）透明代理的 **UDP/53、TCP/53 A/AAAA** 查询接管已经接入。默认关闭，需要用户明确开启。Android VPN 是单独的实现，不在本次 Agent 变更范围内。

## 配置和热更新

```yaml
routing:
  dns_mode: proxy
  fake_ip_enabled: true
```

- 在 Agent React UI 的「分流规则 → 全局策略」选择「由代理解析」，再打开「FakeIP 接管 DNS（实验性）」；本机 DNS 模式与 FakeIP 不可同时开启。
- 捕获 UDP/53 请求会就地合成 A/AAAA 答案；TCP/53 遵循 RFC 7766 两字节长度帧，在本地 TCP 反射连接或 macOS IPC 内响应，**不转发原始 DNS 查询**。
- A 使用 198.18.0.0/15，AAAA 使用 2001:db8:198:18::/96。返回 TTL 60 秒，Agent 映射保留最多 15 分钟；每个实例最多保留 32768 条同时有效的映射。过期/未知的占位地址永不直连公网。
- 命中 FakeIP 的连接先映射回域名，按域名分流，IP/CIDR 规则不匹配占位地址。PROXY 流量发送域名给上游，避免本地查询。遇到 Relay 不可用或 FakeIP 对应 DIRECT 规则时，为避免占位地址被直连，当前采取拒绝而不是不安全回退。
- Relay 服务端的域名解析仅通过启动时已知的 relay IP 回应，缺少该信息时返回 SERVFAIL；不通过正常系统 DNS 查询来突破拦截。
- 未支持的 DNS RR 类型、畸形数据、已知专用加密 DNS 出口 TCP/UDP 853、784、8853 会拒绝或丢弃，不悄悄走本机 DNS。

## 尚不能宣称完整的零泄漏能力

1. **DoH/HTTPS/443**：应用自带 DNS-over-HTTPS 与普通 HTTPS 无法被当前流量层可靠区分，仍可能绕过 DNS/53 接管。ECH、DoH3/QUIC 也需要专门治理。仅阻断端口不足以解决问题。
2. **系统环回 DNS**：Windows WinDivert 当前过滤掉 loopback，Linux 应用到 127.0.0.1、::1 的请求，以及 macOS Network Extension 不交给透明代理的系统 DNS 流，可能绕过本次拦截。
3. **故障期间**：底层驱动关闭或 Packet Interceptor 发生不可恢复错误时，现有平台设计会释放拦截器（fail-open）。如果没有独立 OS 防火墙的始终在线 DNS 阻断规则，就不能提供严格的断线不泄漏保证。
4. **兼容性**：TXT/SRV/SVCB/HTTPS RR、DNSSEC、mDNS 与需要真实 IP 的 DIRECT / 局域网分流尚未形成完整语义，现阶段按安全拒绝处理，不保证相关应用正常。
5. **IPv6**：合成 IPv6 占位地址需要应用主机有适用的 IPv6 路由才有机会进入透明代理。不能依赖 FakeIP 自动补全 IPv6 网络能力。
6. **Android**：Android VPN 的 FakeIP/系统 DNS 接管需要在 mobile/androidcore 独立审计，不能从桌面 Agent 的测试推断 Android 已支持。

因此该功能的当前 UI 明确标记为 **实验性**。在实现平台 DNS 强制重定向、独立持久 OS 防火墙泄漏阻断、DoH 控制与全面应用兼容测试前，不能作为严格安全边界。

## 回归测试

- `go test ./agent/divert ./agent/routing ./internal/config`
- `go test -race ./agent/divert ./agent/routing`
- Windows/macOS GUI CI 及 Linux Go CI。
- 真实机器验收：分别测试系统 DNS、私有 DNS、TCP/53 回退、AAAA/IPv6、目标进程已有 DNS 缓存、DIRECT 规则、网络断线和重新连接。

参照 Proxifier 的 <https://www.proxifier.com/docs/win-v4/dns.html> 关于 FakeIP 占位/无法按真实 IP 过滤的说明，以及 <https://www.proxifier.com/docs/win-v4/proxy-advanced.html> 关于只有已知域名才能使用 hostname 的说明。