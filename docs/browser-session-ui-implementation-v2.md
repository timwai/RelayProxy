# RelayProxy 浏览器同步 UI V2 实施记录

> 分支：`feat/browser-session-sync`；关联 PR #194。设计原型：[`docs/prototypes/relayproxy-server-browser-extension-ui-v2.html`](prototypes/relayproxy-server-browser-extension-ui-v2.html)。

## 设计约束

- 复用 Server Web 原有的侧边栏、蓝色主色、React 卡片与表格组件，不调整全局主题。
- Chrome 选项页复用现有 `ui.css` / `options.css`、顶部品牌栏和侧边导航；弹窗继续保持 390px 操作面板。
- 页面只展示当前任务所需字段与状态，移除重复静态介绍。危险操作仍进行确认；HTTP 明文授权、配对指纹核对、跨设备覆盖保护和未完成恢复的暂停不能省略。
- 面向用户使用“当前设备 / 来源设备 / 接收设备 / 发送端 / 接收端”，不再以字母编号区分设备。

## 已落地

### Server Web

- `server/web/frontend/src/BrowserSync.jsx`：设备审批为唯一日常授权入口；三种设备状态统计可点击筛选；待审批优先，支持名称、设备 ID、扩展 ID 查询。
- 待审批设备可选择身份及发送/接收能力后批准。已批准设备仍能撤销，已撤销设备仍可删除。扩展来源不再单独审批；仅当历史禁用存在时显示清理入口，并要求先撤销/删除关联设备。
- `server/web/frontend/src/App.jsx` / `react.css`：服务配置去除 Browser Sync 配置组，其余分类/字段继续使用 revision 乐观并发保存、放弃修改、必需的安全提醒及 Server Exit 身份授权；减少重复标题和说明，紧凑布局适配手机。

### Chrome 扩展

- `options.html` / `options.js` / `options.css`：连接与设备、浏览器网络、站点授权、设备配对、会话与出口规则采用按导航选中的单页签布局，支持 URL hash 深链，保留所有表单 ID 与 Chrome runtime 消息调用。
- 简化长提示，仅保留表单必要说明；HTTP 网站权限确认、配对校验码、覆盖目标账号及网络出口调整的安全确认逻辑不变。
- `popup.js`：发送端 / 接收端角色名称，不再展示“来源 A / 接收 B”。原快捷发送、请求、同步后打开和本机 Agent 开关保留。

## 验证边界

- 增加 Server 静态布局/操作入口检查和 Chrome 选项页导航/控件引用检查。
- 仍须执行 Server React 构建、Chrome Node 测试、真实浏览器桌面/移动尺寸验收，以及管理员批准、撤销、删除和浏览器配对/IndexedDB 的交互回归。设计原型内容为示例，不包含可用于生产的设备密钥或 Cookie。
- 2026-10-12 更新：授权架构已收敛为设备单独审批。Server 在注册时记录扩展 Origin，WSS 验证设备签名与来源绑定；独立扩展审批移除。会话加密协议和站点授权/双端配对限制不变。
