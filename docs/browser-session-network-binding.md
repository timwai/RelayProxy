# Browser Sync：复用中继 TCP 端口与 Agent 站点出口绑定

## 一、无需代理配置

只需在 RelayProxy Server 启用 Browser Sync，所有 Browser Sync 浏览器请求都通过已存在的 TLS/TCP 中继端口。**不存在第二个 Browser Sync 网络代理监听器；不需要配置 `browser_sync.network_proxy`，也没有 `listen`、`advertise`、`allowed_exit_ids`。**

```yaml
server:
  tls_enabled: true
  tls:
    listen: ":443"              # 已有 Agent TLS/TCP 中继端口
  admin:
    listen: ":8443"             # 独立管理入口
    tls_enabled: true           # Browser Sync 管理审核需要 HTTPS
browser_sync:
  enabled: true
```

Chrome 扩展的 **RelayProxy HTTPS Server** 应填写公网中继地址 `https://relay.example.com:443`，且该 DNS 域名必须在 Server 有效 TLS 证书里。扩展以同一个地址注册设备、进行签名挑战、建立 WSS 连接，并在启用来源 Agent 网络绑定时，将它作为 HTTPS CONNECT 入口。公网入口不暴露 Admin 管理 API；Server Web 管理后台仍从独立端口打开。

**仅同步 Cookie、Local Storage、Session Storage 不需要 Agent 或启用站点网络绑定。**

## 二、网络出口权限（显式授权，绝非开放代理）

当 B 设备希望经 A 的 Agent 出口访问已同步的网站时，需要满足以下条件：

1. 来源 A、接收 B 的 Chrome 扩展设备由 Server 管理员审批，且归属同一 Identity；A 具备 send、B 具备 receive 权限。
2. A/B 已完成加密网站策略配对及双端确认；网络共享还需要 A 主动选择一个**在线** Agent 和准确 HTTPS 网站、B 再次确认站点网络绑定。
3. A Agent 持有管理员分配的 `proxy.exit` 授权，设备归属相同 Identity，并且 **Agent 本机** 在 Web/GUI → 本机出口共享里开启“允许浏览器网络共享”（配置 `exit.browser_network_share: true`，重启或重连后签名宣告能力）。
4. Server 在每个 HTTPS CONNECT 请求中核验接收浏览器的短期令牌、配对有效性、绑定的唯一精确域名、Agent 仍在线、身份一致性、`proxy.exit` 能力、Agent 的本地共享授权，并在已建立连接存活期间持续复核权限。Agent 的出口目标 ACL 及 Server Relay ACL 均不能被绕过。

代理功能仅支持配对 HTTPS 网站的 **443/TCP**，不支持任意 HTTP CONNECT 端口、IP / localhost、第三方域名或通用开放代理。A Agent 的 DNS 在出口侧解析。全局浏览器代理是另一项显式开关，**无需开启**。

## 三、数据与协议

```text
A Chrome  -- 浏览器配对 / Cookie-Storage E2EE --> Relay Server TLS/TCP
B Chrome  -- 浏览器配对 / WSS 会话同步 -------> Relay Server TLS/TCP
B Chrome  -- HTTPS CONNECT 精确配对域名 -----> Relay Server TLS/TCP
                                                    |
                                                    v
                                               A Agent Exit
                                                    |
                                                    v
                                               目标 HTTPS 网站
```

- 单个 TLS/TCP 端口仍同时服务旧 Agent 原生中继隧道、原有公开消息推送、Chrome HTTPS/WSS 与 Browser Sync CONNECT。路由只白名单允许 Browser Sync 注册/WSS、经过认证的 CONNECT 和公开消息推送；**绝不复用 Admin 登录或控制台接口**。
- Server 不解密 Cookie/Storage，会话端到端加密与设备签名、时序和回滚逻辑保持原状。Server 可见 CONNECT 的精确域名、出口 ID 和请求元数据。
- `NETWORK_TOKEN` 只返回接收设备的短期代理令牌与到期时间。Chrome 使用自身已认证的 `RelayProxy HTTPS Server` Origin 推导主机和端口。切换 Server Origin 时清理原有凭据，禁止令牌绑定到旧的代理地址。
- Cookie/Storage 同步与网络绑定解耦；关闭/撤销 Agent 网络共享不会自动删除已恢复的网站登录信息。
- 旧 YAML 中 `browser_sync.network_proxy` 会被**只读迁移 shim** 接受并在规范化时清除；重新保存配置后不再写出。旧 Server/扩展不能和新 `NETWORK_TOKEN` 互通网络代理功能，应同时升级。

## 四、测试边界和上线准入

- 测试 Server YAML 迁移及导出时旧字段消失，Browser Sync 强制 TLS/TCP，Server Admin API 永远不经过公网中继入口。
- 测试公网同一 TCP 端口上的旧 Agent 隧道、HTTP 消息推送、WSS Upgrade、CONNECT Hijack 和双向传输。
- 测试无 Agent 本地 opt-in、无 proxy.exit、跨身份、撤销绑定、过期令牌、Agent 离线、目的域名不一致均拒绝，已打开连接及时中断。
- 测试来源 Chrome 切换 Server Origin 时不向旧地址发送凭据；没有可用出口时不得解除精确站点的 PAC 黑洞保护。
- 仍需 A/B 两台真实 Chrome、Windows/macOS Agent 和部署 Server 验收，含 TLS 证书、DNS、QUIC、WebRTC 边界；此功能不是完整 VPN。

## 五、WSS 连接故障快速诊断

升级到新的“Browser Sync 仅复用 TLS/TCP Relay”版本后，Chrome 扩展的“RelayProxy HTTPS Server”必须填写公开的 **Server TCP 中继** HTTPS URL（例如 `https://relay.example.com:20000`）；不能误填 Admin Web 的端口，也不能把 QUIC/UDP 可用当作 TCP/WSS 已放行。升级 Server 和 Chrome 扩展并重启 Server 后，先在 B 设备执行：

```bash
curl -v --http1.1 https://relay.example.com:20000/api/v1/browser-sync/health
```

新 Server 正常且 Browser Sync 已启用、TLS 证书有效时返回 **HTTP 204**。但它**只证明 HTTPS 入口可达，不能证明 WebSocket Upgrade 失败或成功**。如果返回 404，检查域名/中继 TCP 端口/运行的 Server 版本和 `browser_sync.enabled`；如果出现证书验证失败，检查完整证书链和匹配域名，不要通过忽略证书验证强行启用浏览器 WSS；如果 TCP 连接失败，检查公网 TCP 防火墙。

要直接验证 **HTTP/1.1 Upgrade 返回 HTTP 101**，在 A/B Chrome 所在的设备执行（替换真实域名、Server TCP 中继端口、Chrome 扩展 ID，且先通过 Server Web 完成扩展审批）：

```bash
curl -i -v --http1.1 --noproxy '*' --max-time 5 \
  -H 'Origin: chrome-extension://abcdefghijklmnopabcdefghijklmnop' \
  -H 'Connection: Upgrade' -H 'Upgrade: websocket' \
  -H 'Sec-WebSocket-Version: 13' \
  -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' \
  'https://relay.example.com:443/api/v1/browser-sync/ws'
```

- **HTTP 101 Switching Protocols**：Upgrade 已成功；此 curl 请求**没有**随后完成 `AUTH_HELLO / AUTH_PROOF`，所以连接最终关闭属于正常现象。若 Chrome 仍失败，重点检查浏览器设备是否 approved、签名时使用的 Origin 与 Server Host 是否相同，以及 Chrome 是否使用了与 curl 不同的代理路径。
- **HTTP 403**：Extension Origin 未审批 / 已撤销，或 Origin 格式不符（还没有到浏览器设备签名这一步）。
- **HTTP 400/426/500**：Upgrade 请求头 / 协议版本 / 服务器处理或网关异常，应结合 Server 日志排查。
- **HTTP 404**：实际请求落到不包含 Browser Sync WSS 的旧版 Server 或错误入口。
- 健康检查 204、但 Upgrade 探测没有 101：重点查反向代理是否保留 `Connection: Upgrade`、`Upgrade: websocket`、原始 Host 和 Extension Origin。**如果同时复用 Agent 原生 TCP Relay，传统 HTTP 反向代理不能代替 TCP 透传。**

新版 Chrome 扩展会分别报告 WebSocket 未触发 `open`、Upgrade 完成后等待 `AUTH_CHALLENGE`、收到了挑战但未发送 `AUTH_PROOF`，以及已发送证明却未收到 `AUTH_OK` 的状态。除了状态码和握手阶段，诊断不输出 Cookie、密钥和代理凭据。

Chrome 扩展连接失败后会对同一 TLS 入口发起无身份、无 Cookie 的 HTTP 204 探测，给出上述分类提示。**204 只证明接口可达，不证明设备已被批准或 WebSocket 已成功认证**。如出现 1008，检查扩展 ID 与浏览器设备是否在 Server Web 审批、关联身份及 send/receive 授权；若入口正常但 Upgrade 失败，还应核查中间代理是否允许 WebSocket 升级。

## 六、Chrome 全局 Agent 代理与 Server 控制平面

在 Chrome 扩展启用“使用本机 RelayProxy Agent 代理全浏览器”时，旧 PAC 会把 `wss://RelayProxy-Server/api/v1/browser-sync/ws` 也发送到本地 Agent；如果该 Agent 的出口仍通过同一个 Server Relay，则可能发生自代理回环。当前 PAC 已对配置的 `RelayProxy HTTPS Server` 主机添加 **DIRECT 启动路径**（仅在启用全局 Agent 代理时）。单独配对的精确网站规则优先级仍更高：即使绑定站点与 Server 域名重合，也不会把必须代理或阻断的站点悄悄改为 DIRECT。

如果公网 TCP 中继本身必须由其他代理链路才能连接，DIRECT 也可能不可达；此时应单独调整 Server 可达性，而不是放宽已配对网站的 fail-closed 代理。修改扩展 Server Origin 后会重新生成 PAC，并清除/验证旧 Server 的临时代理租约。普通 Chrome 全局代理与站点网络绑定仍是独立可选功能。
