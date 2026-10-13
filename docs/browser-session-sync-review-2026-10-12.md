# PR #194 自审记录：设备单审批与 WSS 权限闭环

> 分支：`feat/browser-session-sync`。审查范围：2026-10-12 对浏览器设备单审批改动进行重点自审，同时复核相关连接中继、历史来源禁用、配对撤销、Chrome 注册和 Server Web。此记录不是完整生产验收报告。

## 设计核对

- **真正授权对象是浏览器设备**：Chrome 发送设备 ID、设备名、申请身份 ID、公钥，Server 设备记录初始为 pending；仅管理员可以绑定真实 Identity 和 send / receive。
- **Origin 是来源约束，不是审批主体**：`chrome-extension://<extension-id>` 格式校验后自动登记。设备身份在 WSS 中必须同时通过签名验证、已批准状态、注册 Origin 匹配和历史来源禁用检查。
- **不能在浏览器申请时越权**：注册请求禁止注入身份授权字段、发送/接收权限和批准状态；申请的身份 ID 仅是管理员候选值。
- **撤销优先**：管理员撤销设备时同时撤销配对；WSS 在发送 AUTH_OK 前以及每条控制消息处理前都重新检查设备与 Origin 状态。已撤销设备不得凭重复请求直接复活。
- **不弱化网站会话安全**：站点 Host 授权、双端配对确认、E2EE、目的端冲突和账号覆盖保护仍单独执行，不因省略扩展审批而自动放行。

## Review 发现 / 修复证据

| ID | 严重度 | 问题 | 修复点 | 回归覆盖 |
|---|---|---|---|---|
| BS-R01 | P1 | 原本来源记录满 500 后，已存在的合法来源再次注册仍先执行容量统计，造成无意义阻塞及失败面 | `server/browser_sync/extensions.go`：先识别已有记录，只有新来源才执行上限检查 | `TestOriginInventoryExistingRegistrationSurvivesQuota` |
| BS-R02 | P0 | 历史来源禁用状态的数据库查询失败会被视为“未禁用” | `ExtensionOriginAllowed` 明确返回数据库错误；HTTP 阻断、WSS 每条消息处理均 fail-closed | `TestOriginAuthorizationFailsClosedWhenDatabaseUnavailable` |
| BS-R03 | P0 | WSS 签名验证和发送 AUTH_OK 之间可能发生设备撤销 / 来源禁用 | `handler.go` 在握手完成后重新读取有效设备及来源状态，并继续在每条控制消息复核 | 新增握手前置复核、Origin 错绑拒绝和已认证会话撤销断开集成测试 |
| BS-R04 | P1 | 删除旧设备没有清理新加入的申请身份记录，可能阻止 FK 删除或在重复设备 ID 注册时留下旧请求 | `store.go` 删除设备的同一事务里额外删除 `browser_sync_identity_requests` | `TestDeleteRevokedBrowserRemovesDependentRecordsAndAllowsFreshPending` |
| BS-R05 | P1 | 设备撤销与配对规则撤销拆成两次数据库写入，数据库故障会留下不一致状态 | `Store.Revoke` 同一事务内更新设备和配对规则；失败整体回滚，API 报错而不误报成功 | `TestRevocationAndPairingAreAtomic`（模拟数据库规则更新失败） |
| BS-R06 | P1 | 新扩展申请仅持公开密钥可能更改已有 pending 设备的申请身份，或先改请求再发现来源冲突 | 已记录的身份候选值不可被同公钥重试更改；先检查设备绑定 Origin 再变更注册信息 | `TestEnrollmentCannotRewritePendingIdentityChoice`、`TestForeignOriginCannotChangePendingDeviceRequest` |
| BS-R07 | P2 | 已废弃的申请身份 ID 不在可用 Identity 列表时，管理员 UI 仍开放批准按钮 | UI 只能预选已存在且 active 的 Identity，无合法选择则不能批准 | `server/web/frontend/src/browser-sync-ui.test.js` 静态契约，需要浏览器交互验收 |

## 新增与既有回归路径

- Go：`go test ./server/browser_sync ./server/api`，重点为 `approval_review_test.go`、`device_delete_test.go`、`unified_enrollment_test.go`、`wss_integration_test.go`、`extensions_test.go`、`handler_test.go`。
- Chrome：`cd browser/chrome-extension && npm test`，包含 `enrollment-request.test.mjs` 和认证阶段提示检查。
- Server Web：`cd server/web/frontend && npm test && npm run build`，覆盖 `browser-sync-ui.test.js` 与配置字段完整性。
- 端到端：两台真实 Chrome，分别申报不同 Profile 的身份 ID/名称，Server 一次批准各设备；验证首次 WSS、配对和 Session Storage/IndexedDB、撤销即时断连、新扩展 Origin 错绑拒绝、旧记录删除重申请。
- 安全：不批准设备时 WSS 不应返回 AUTH_OK；Chrome 模拟外部 Origin 即使持有真实设备公钥，也不能以该设备身份连接；已禁用历史 Origin 不允许登记和 Upgrade。

## 检查状态 / 发布门禁

1. **代码修复与回归测试：已提交到 PR 分支。** 审查了 10 份关键文件中的 11 条静态安全/一致性契约，均满足预期；该核对不代替 Go/Node 实际运行。
2. **自动化测试执行：尚未验收。** 当前环境无法通过 Git DNS 克隆仓库运行 Go / Node 命令；截至审查时 GitHub 最新 SHA 未显示完整的 Actions 成功运行。测试源码提交不等于通过结果。
3. **真实双机登录维持、冲突、登出、HTTPS/WSS 反向代理、DNS/QUIC/WebRTC 协议边界：尚未验收。**
4. **发布决定：保持 Draft，不合并 main；待 CI、构建、真实浏览器 / 安全验收有证据后再关闭发布门禁。**

## 剩余风险与注意事项

- 来源清单上限 500 是防资源耗尽的硬限制。高并发的新来源登记仍需通过压力测试验证容量竞态与拒绝行为；无真实压力数据时不得宣称容量并发验收完成。
- 已批准设备使用旧版无来源绑定模式时只允许显式配置的 Legacy Extension ID；建议未来完成迁移后收紧这一兼容路径。
- 数据库不可写或撤销事务失败时，管理员应收到错误并重试，而不能向用户展示“撤销成功”。异常场景需要服务端日志、连接断开及数据一致性实测。
