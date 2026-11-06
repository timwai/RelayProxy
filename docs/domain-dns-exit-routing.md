# DNS 域名分流与指定出口解析（第一阶段）

此功能允许 Windows x64、Linux 的系统 DNS/53 捕获在查询**发生之前**按 QNAME 决定使用本地解析还是指定远端出口。通过 Agent 的「DNS → 按域名选择 DNS 解析出口」管理。

## 启用

```yaml
network:
  mode: divert
routing:
  dns_mode: proxy
  auto_detect_dns: false
  proxy_dns_enabled: true
  fake_ip_enabled: false
  dns_rules:
    - name: Google
      enabled: true
      domains: ["*.google.com", "*.googleapis.com"]
      action: REMOTE
      exit_id: "us-exit"
      resolver: exit_system # 可选：出口设备系统 DNS
    - name: Office
      enabled: true
      domains: ["*.corp.example"]
      action: REMOTE
      exit_id: "office-exit"
```

- 有 `dns_rules` 时按顺序匹配启用规则，首次命中生效；未命中使用**本机原定 DNS**。不存在 `dns_rules` 时保持旧的全部远程 DNS 解析行为。
- `*.example.com` 匹配域名本身及任意子域，`example.com` 仅匹配本身；按 DNS label 边界匹配，不将 `badexample.com` 误判为子域名。
- `action: LOCAL` 使用原始系统 DNS 目标，且不得填写 `exit_id`。 `REMOTE` 使用该规则指定出口的加密 DoH/DoT 查询；留空时跟随全局 `dns_exit_id` 或默认出口。
- `dns_direct_domains` 旧版白名单保持优先级；中继服务器主机名使用安全引导回答，不会发往本地或远端公共解析器。
- 若使用设备组，在 DNS 规则的 `exit_id` 中指定 `group:...`。出口只决定 DNS 查询路由，后续真实流量仍按原有 `routing.rules` 分流。

## 重要边界

**第二阶段已提供出口系统 DNS 的受限 A/AAAA 解析。** `resolver: exit_system` 会通过新类型的 RelayProxy DNS 请求帧发送 QNAME 到经过授权的出口 Agent，由出口设备的 `net.DefaultResolver`（操作系统解析器配置）执行查询，结果经 Relay 及出口双重 ACL 检查后返回。它不等于出口设备开放任意 UDP/53 转发；Relay、Client、Exit 都需升级，旧版本返回失败且不会降级到本地 DNS。`resolver: encrypted`（或不填写）完全保持原先的 DoH/DoT。对 TXT、SRV、HTTPS/SVCB 等非 A/AAAA 记录，当前仍使用指定出口的加密 DNS 路径；尚未实现任意记录类型的出口系统解析。出口 DNS 配置中 `dns_upstreams` 仍可用于企业 HTTPS DNS。

系统透明代理必须运行，DNS 被其他程序通过 DoH/DoT、环回或系统缓存解析时不会经过此功能。macOS Network Extension 的原始本地 DNS 转发能力仍有平台限制；DNS 规则的本地查询在不能安全回注的平台会返回 SERVFAIL，而不是错误地转发到远端。Windows ARM64 当前没有系统 DNS/53 捕获。

REMOTE 查询出口失败时返回 SERVFAIL，不回退本地明文查询；未匹配规则的 LOCAL 查询是用户明确配置的本地解析行为，不提供防泄漏承诺。DNS 解析出口与业务连接出口不强制一致；如需保持一致，请另行配置相同目标的代理分流规则。来自本地解析结果的 IP→域名关联具有共享 IP 歧义，需要观察实时连接明细。

## 验收点

1. 清理系统 DNS 缓存，查询 `api.google.com` 与 `www.baidu.com`，确认前者由指定 Exit 发起 DoH/DoT，后者仍向本地系统 DNS 发送。
2. 分别验证 UDP/53、DNS/TCP 及 A、AAAA、TXT、HTTPS/SVCB 记录；错误响应、截断/重试逻辑不改变。
3. 关闭指定出口或换成旧版出口，确认 REMOTE 的出口系统 DNS 返回 SERVFAIL，且入口本地 DNS 没有被请求；LOCAL 仍能解析。
4. 热更新域名规则、切换出口组、禁用规则，确认后续 DNS 查询立即使用新策略。
5. 检查 Windows x64 和 Linux 透明代理实机，排除环回 DNS、应用自带 DoH、捕获服务退出导致的漏流情况。

## 版本与权限边界

- 新协议 `FrameTypeExitSystemDNS=0x1B`，一条流仅处理一条完整 DNS A/AAAA 查询，不暴露系统 DNS 地址、DNS 配置或任意 UDP 数据包。
- Relay 校验 `proxy.client` 授权、设备间出口访问许可、目标出口 `CapabilityTargetACL` 以及中央域名与 IP ACL；出口 Agent 再校验本地出口 ACL 与由 Relay 绑定的 ACL。
- 返回记录上限 16，DNS 问题与答案长度上限各 4096 字节，出口解析上限 7 秒；请求者总超时 8 秒，外层 DNS 限流池仍然生效。
- 请求 ID、Question、RCode、A/AAAA 类型与 TTL 均验证；出口无响应、超时、验证失败、ACL 拒绝时返回 SERVFAIL，不访问入口系统 DNS。
- 出口 OS 解析器的 Go API 无法可靠区分 NXDOMAIN 和“该记录类型不存在”（NODATA），暂统一返回空的 NOERROR 应答，避免错误地缓存整域不存在的结论。
- 此阶段不支持将 `local:*` 自定义代理配置为出口系统 DNS。如果使用设备组，逐个试选符合规则的成员；任何 ACL 禁止错误不会切换到其他成员。
