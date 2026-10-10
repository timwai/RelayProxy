# RelayProxy Browser Sync — Chrome 扩展（P3 开发预览）

Chrome 扩展直接连接 RelayProxy Server 的 **Admin HTTPS/WSS** 服务，不需要安装或运行本机 Agent，也不需要 Native Messaging、SOCKS5 或透明代理。

> **安全声明：** 同步网站会话 Cookie 相当于向另一台设备授予网站账号访问能力。仅限用户自己拥有或明确授权的账号与设备；生产部署前需完成真实 Chrome 双端验证、安全审计和跨浏览器版本回归。网站设备绑定、DBSC、Passkey、多因素验证等机制不能通过复制 Cookie 绕过。

## 功能进度

| 能力 | 状态 |
|---|---|
| 每个 Chrome Profile 独立设备签名/加密密钥 | 已实现 |
| Server 独立浏览器设备注册、管理员审批、撤销 | 已实现 |
| Chrome 通过签名挑战认证 Admin WSS | 已实现 |
| A/B 同一 Identity 双端配对及密钥指纹校验 | 已实现 |
| 指定站点/ Cookie 名称加密邀请 | 已实现 |
| A 登出后同步删除的 Cookie 安全恢复 | 已实现，B 只清理该规则之前写入且未被修改的 Cookie |
| 本地 Cookie 归属识别 | Profile 非导出 HMAC 密钥保护，避免保存普通哈希 |
| 浏览器端 WebCrypto P-256 ECDH/HKDF/AES-GCM 快照加密及签名 | 已实现 |
| Server 按 ACTIVE 规则签名验收、递增序号防重放、密文路由 | 已实现 |
| 精确 HTTPS Host、Host-only、Secure、根路径、非分区 Cookie 采集/恢复 | 已实现 |
| 目标已有不同登录 Cookie 的冲突保护、明确同意后覆盖 | 已实现 |
| 手动同步、Cookie 变化后自动尝试发送、启动和定时重连请求 | 已实现 |
| 同步成功后打开网站、RELAYED/APPLIED/CONFLICT/FAILED 状态 | 已实现 |
| Server Web 浏览器设备管理可视化页面 | 已接入管理员导航，可审批、关联身份和撤销浏览器设备 |
| 跨 Chrome 真机双端网站登录兼容性验收、完整安全审计 | **未完成** |
| LocalStorage、IndexedDB、分区 Cookie、跨子域、设备绑定会话 | 不在 P3 范围 |

Chrome 扩展保留最小 Host 权限；所有 Cookie 值只用于内存中的加密处理和指定站点的 Cookie API 写入，**不写入 Server 数据库、浏览器扩展本地存储或日志**。服务器无会话解密密钥，不保存离线快照。启动时如果 A 离线，B 不能恢复最新状态。

目前 `APPLIED` 仅代表 Chrome Cookie API 成功写入，**不代表目标网站服务器验证了登录**。请使用经授权的测试网站或测试账号验证。

## Server 配置

在 Server Admin HTTPS 监听器上启用独立 Browser Sync API：

```yaml
server:
  admin:
    listen: ":8443"
    tls_enabled: true

browser_sync:
  enabled: true
  extension_ids:
    - "abcdefghijklmnopabcdefghijklmnop" # 改成 chrome://extensions 里的真实扩展 ID
```

默认 **`browser_sync.enabled=false`**。使用浏览器信任的 TLS 证书。这里填的是 **Admin HTTPS Origin**，例如 `https://relay.example.com:8443`，不是 TCP/QUIC 隧道端口。无 Agent 模式暂不支持 P2P。

Browser Sync 设备审批仍由现有 RelayProxy 管理员登录/CSRF 检查保护：

```text
GET  /api/v1/browser-sync/admin/devices
POST /api/v1/browser-sync/admin/devices/{id}/approve
POST /api/v1/browser-sync/admin/devices/{id}/revoke
```

审批 JSON 示例：

```json
{"identityId":"已存在且 active 的身份 ID","send":true,"receive":false}
```

B 设备应由管理员赋予 `receive:true`；A 赋予 `send:true`。浏览器身份不能继承 Agent 的代理和 RDP 权限。

## 两台 Chrome 的测试流程

1. A/B 在 `chrome://extensions` 开启开发者模式，将本目录作为“已解压扩展”加载。确保两台扩展 ID 与 Server 白名单匹配。
2. A/B 在扩展弹窗配置并授权 Admin HTTPS 地址，分别点击 **注册浏览器设备**。
3. 在 Server Admin API 中审批 A（发送）、B（接收），并关联到同一 active Identity。
4. 两边在扩展弹窗点击 **连接 Server**。A 点击“请求站点权限”，添加准确的 HTTPS **Origin**，不是带路径/查询参数的 URL。
5. A 点击刷新设备，选 B、授权网站和 **明确指定的 Cookie 名称**，创建加密配对邀请。Cookie 名称应来自用户有权检查的网站测试环境，不要将 Cookie 值粘贴到 UI。
6. B 刷新邀请，阅读网站和 Cookie 白名单，并通过**独立可信渠道**与 A 比较校验码后接受。A 再最终核对并确认，规则状态为 `active`。
7. A 使用测试账号在 Chrome 正常登录网站；站点 Cookie 变化时扩展会自动尝试发送，也可按“立即发送加密登录状态”。
8. B 点击“从来源设备请求同步”或“同步成功后打开网站”。B 没有其他登录状态时，写入符合范围的 Cookie；存在不同账号时默认显示 `CONFLICT`，只有用户明确勾选覆盖后才会重试。
9. 观察最终状态 `APPLIED`。随后确认网站是否实际上无需再次登录；如需再次认证，属于网站兼容性问题，不宣称绕过。
10. 在 A 测试账号登出后，再观察 B 是否清除此前由该规则写入的 Cookie；B 自己创建或被网站修改的 Cookie 不会被清除。
11. 任一设备可撤销配对，Server 随即拒绝后续转发并清理本地同步元数据。撤销 RelayProxy 配对 **不会**撤销网站已经签发的会话令牌。

**Cookie 兼容限制：** P3 仅支持所选择网站准确主机上的 Secure、hostOnly、`path=/`、非分区 Cookie；不自动扩展到父域、子域或 CHIPS。对复杂网站，采集可能明确失败而不是扩大 Cookie 读取/写入权限。

## 测试

```bash
cd browser/chrome-extension
npm test
```

Go 侧使用 `go test ./internal/browser_sync ./server/browser_sync ./server/api`。GitHub PR 的 Go CI 和 UI CI 也包含扩展 WebCrypto、作用域和冲突测试，以及带 TLS 的真实 WebSocket 测试。

设计文档：[Browser Session Sync v1.1](../../docs/browser-session-sync-design-development.md)。本分支是 Draft PR，未合并 `main`。

## 安全状态与兼容性备注

- **设备批准与撤销**：Server Web 的“设备与身份 → 浏览器同步”现在提供浏览器专用管理页面；新接口仍默认关闭。
- **登出处理**：快照使用加密的 `removedNames` 字段指示来源 Cookie 不再存在。目标设备仅删除此规则上一次恢复且当前值未改变的 Cookie；目标自行登录的相同名称（即使值相同）也不自动取得规则所有权。
- **指纹数据**：本地只保存由不可导出 HMAC CryptoKey 计算的规则专属 Cookie 归属标签。老版本开发快照的普通 SHA-256 标签会在扩展初始化时清理，不会被用于判断是否可删除。
- **身份撤销**：停止 RelayProxy 后续同步不意味着目标网站账号退出；必要时使用目标网站的“全部设备退出”功能。
- **浏览器兼容性**：Chrome 与不同站点的真实登录保持情况仍需授权测试，`APPLIED` 只说明 Chrome Cookie API 处理成功。
