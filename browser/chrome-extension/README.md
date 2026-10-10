# RelayProxy Browser Sync — 无 Agent Chrome 扩展（开发预览）

本项目使用 Chrome MV3 扩展直接连接 RelayProxy Server 的 **Admin HTTPS** 服务，无需本机 RelayProxy Agent 或 Native Messaging Host。

## 当前实现

- 各 Chrome Profile 独立生成设备 ID 和非导出 WebCrypto P-256 签名、加密密钥，存储在 IndexedDB。
- Chrome 扩展在用户点击后为 Server 和目标网站请求 HTTPS Host 权限。
- `POST /api/v1/browser-sync/devices/register` 注册 pending 浏览器设备。
- Server Admin API 审批、撤销独立浏览器设备。
- Chrome 使用一次性随机挑战和 ECDSA 签名完成 WSS AUTH_OK 验证。
- **WSS 仅允许认证和 PING/PONG**；所有 Cookie/Session 数据帧均被拒绝。

**当前不能同步登录会话。** 尚未实现站点规则双端配对、公钥指纹确认、端到端加密、Cookie 收发和 Server Web 浏览器管理 UI。

## Server 设置

`browser_sync` 默认关闭。只有管理监听端口启用 TLS，且指定允许的 Chrome Extension ID，才能开启：

```yaml
server:
  admin:
    listen: ":8443"
    tls_enabled: true

browser_sync:
  enabled: true
  extension_ids:
    - "abcdefghijklmnopabcdefghijklmnop" # 改为 chrome://extensions 中实际 ID
```

扩展应填写 `https://relay.example.com:8443` 一类的 **Admin HTTPS Origin**，不是 QUIC/代理隧道端口。证书必须由浏览器信任。

### Admin API

以下端点由现有 RelayProxy 管理员登录态和服务端 Origin/CSRF 检查保护：

```text
GET  /api/v1/browser-sync/admin/devices
POST /api/v1/browser-sync/admin/devices/{id}/approve
POST /api/v1/browser-sync/admin/devices/{id}/revoke
```

审批 JSON 样例：

```json
{
  "identityId": "已有且状态为 active 的 Identity UUID",
  "send": true,
  "receive": false
}
```

不要将 Admin Cookie 提供给扩展，审批只赋予浏览器专用权限，不授予 proxy.use 或 RDP 权限。

## 开发验证

1. Chrome 打开 `chrome://extensions`，启用开发者模式。
2. 选择“加载已解压的扩展程序”，目录为 `browser/chrome-extension`。
3. 查看扩展弹窗中显示的实际扩展 ID，加入 Server `extension_ids`，重启 Server。
4. 输入并保存 Admin HTTPS 地址；点击“注册浏览器设备”。
5. 管理员审批设备后，点击“连接 Server”。
6. 验证 Server AUTH_OK 返回 `sessionTransferEnabled:false`，不会传输 Cookie 数据。

完整设计参见 [Browser Session Sync v1.1](../../docs/browser-session-sync-design-development.md)。
