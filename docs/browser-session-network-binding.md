# RelayProxy Browser Sync：A Agent 网络出口绑定

> 2026-10-11 第一阶段开发预览 · 分支 `feat/browser-session-sync` · 未通过双机真实浏览器验收，不得默认开放生产公网代理。

## 使用场景

A 安装 **RelayProxy Agent + Chrome MV3 扩展**；B 只安装 **Chrome MV3 扩展**。A/B 的 Cookie 已经过独立设备注册审批、双端规则确认与 E2EE。来源 A 选择已在线、属于同一身份且由 Server 管理员允许共享的 Agent Exit；B 独立核对加密 Cookie 策略的站点域名并点击同意。B 的站点 Chrome 请求走 HTTPS CONNECT → Relay Server → A Agent 的 OpenTCP → 目标网站；DNS 在 Agent Exit 解析。同步网站最终是否免登录仍由网站决定。

## 必须显式启用

Server 配置示例（独立 TCP 监听端口不可与 admin、tunnel 冲突）：

```yaml
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

要求 Admin HTTPS、TLS Server 证书校验正常；Server 的 21090/TCP 开放给 B Chrome。A Agent 必须正常连接 Server、已获 `proxy.exit` 能力并开启允许外网的 Exit；Server 明确列入 `allowed_exit_ids`。保持 Browser Sync / network_proxy 默认关闭。此功能不会为 B 创建完整 Agent 设备，也不授予 B 任何 `proxy.client`、RDP 权限。

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
- 必须实测 TLS 认证（不要在 B 忽略证书）、401/407 行为、Chrome PAC mandatory/代理失败处理、浏览器重启/MV3 休眠、Agent 崩溃、Cookie ACK 对账和撤销后连接断开边界。新增周期性的存量 CONNECT 授权复核（2 秒一次）：发现 Cookie 规则/网络绑定/设备权限/Agent 会话更替或服务端异常即关闭连接；仍需真实跨端撤销测试、跨实例租约、第三方资源组策略和 UI 管理审计。
- 管理员不能仅凭浏览器用户自行提供的请求批准 Agent 出口；固定的 Server 出口白名单是第二道同意。若 A 设备应控制是否允许分享其网络，还需提供 Agent 端显式本地开关和管理员授权审计后再正式发布。

## 验收准入

- [ ] Windows A Agent + A Chrome → Windows B Chrome，测试站点 Cookie 同步与网站实际登录态。
- [ ] macOS A Agent + A Chrome → Windows/macOS B Chrome，验证目标公网 IP 与 A Agent 一致。
- [ ] 非批准 hostname / IP / 内网请求以及无代理凭据必须拒绝。
- [ ] A/B 单端撤销、Agent 离线、WSS 断开、Browser Worker 重启：绑定网站不能偷偷直连。
- [ ] PAC 与其他代理扩展/系统策略冲突、Chrome HTTPS 407 缓存、多 Cookie 失败均有真实 Chrome 测试记录。
- [ ] 压力、跨实例、日志脱敏、密钥/凭据生命周期、Agent 本地授权、现有 RDP/P2P/Proxy 全量回归均通过。

参见 [Browser Sync 发布进度](./browser-session-sync-progress.md)。