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

**第一阶段没有新增出口操作系统 DNS 查询协议。** REMOTE 查询仍通过选定出口访问 RelayProxy 已实现的可信 DoH/DoT 上游；企业内部 DNS 可使用既有 `dns_upstreams` 配置指向可从出口访问的 HTTPS DNS 服务，但不是直接使用出口网卡的 DNS 配置。第二阶段再支持 `exit_system` 和自定义域名专用解析器。

系统透明代理必须运行，DNS 被其他程序通过 DoH/DoT、环回或系统缓存解析时不会经过此功能。macOS Network Extension 的原始本地 DNS 转发能力仍有平台限制；DNS 规则的本地查询在不能安全回注的平台会返回 SERVFAIL，而不是错误地转发到远端。Windows ARM64 当前没有系统 DNS/53 捕获。

REMOTE 查询出口失败时返回 SERVFAIL，不回退本地明文查询；未匹配规则的 LOCAL 查询是用户明确配置的本地解析行为，不提供防泄漏承诺。DNS 解析出口与业务连接出口不强制一致；如需保持一致，请另行配置相同目标的代理分流规则。来自本地解析结果的 IP→域名关联具有共享 IP 歧义，需要观察实时连接明细。

## 验收点

1. 清理系统 DNS 缓存，查询 `api.google.com` 与 `www.baidu.com`，确认前者由指定 Exit 发起 DoH/DoT，后者仍向本地系统 DNS 发送。
2. 分别验证 UDP/53、DNS/TCP 及 A、AAAA、TXT、HTTPS/SVCB 记录；错误响应、截断/重试逻辑不改变。
3. 关闭指定出口，确认 REMOTE 返回 SERVFAIL，且本地 DNS 没有被请求；LOCAL 仍能解析。
4. 热更新域名规则、切换出口组、禁用规则，确认后续 DNS 查询立即使用新策略。
5. 检查 Windows x64 和 Linux 透明代理实机，排除环回 DNS、应用自带 DoH、捕获服务退出导致的漏流情况。
