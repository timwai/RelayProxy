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
