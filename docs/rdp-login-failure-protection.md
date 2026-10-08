# Windows RDP 登录失败审计与安全封禁

本功能在 `feature/rdp-security-audit-ban` 开发，作用范围仅限 **Server 公网 RDP TCP 入口**，不使用 NAT 公网 IP 封禁正常 Agent、Relay 或 P2P 传输。

## 数据链路

1. Server 为公网 RDP TCP 连接创建独立审计 ID，记录公网来源 IP、入口 ID 和目标 Host ID。
2. Windows Host Agent 使用自己的固定 RDP 服务地址（一般 `127.0.0.1:3389`）建立本地 TCP 连接，将该连接**本地源端口**通过已有加密 RDP 转发应答交给 Server。
3. Server 将「Host ID + 本地源端口 + 有效连接时间窗」关联到自己观察到的公网 IP。
4. Windows Host Agent 每 5 秒从 **Windows Security 日志**读取最近 30 秒的 **4625 登录失败事件**，以结构化 XML 获取事件记录 ID、时间、登录类型、用户名、失败状态、来源地址、来源端口。采集只接受本机回环来源及登录类型 3/10。
5. Agent 经已认证的 Relay RDP 控制流上报。Server 再次验证 `rdp.host` 和 `rdp.public` 授权，依据先前登记的本地源端口查找公网连接。
6. 只有**恰好一个**仍处于有效时间窗的公网会话匹配时，Server 才赋予事件公网源 IP，执行自动封禁规则；否则记录为未关联事件，**禁止猜测并封禁**。

源端口为 0、缺失、候选不唯一、非回环来源、事件时间异常的登录失败，都不会自动封禁公网来源。Windows 4625 可能记录不到源端口，因此此功能并不承诺每次 Windows 登录失败都能关联公网 IP。

## 配置与管理

在 Server Web → **RDP 公网入口**：

- **Windows 登录失败审计**：时间、目标 Windows Host、用户名、NTSTATUS、关联公网 IP 或未关联状态。
- **自动封禁规则** →「Windows 登录失败防护」：默认启用，**5 分钟内同一个入口的同一来源 IP 匹配 5 次**，自动封禁 **30 分钟**，可修改或关闭。
- **IP 黑名单 / 白名单**：按现有管理页面封禁、解封和设置自动封禁豁免。白名单不绕过手动黑名单或已有来源 CIDR 策略。

事件写入 SQLite `rdp_auth_failures`，由目标 Host ID + Windows 事件记录 ID 去重。审计记录按现有 RDP 安全日志保留策略清理，默认 30 天。

## Windows 系统前提

- Windows Host Agent 必须有 `rdp.host` 和 `rdp.public` 授权，并升级到包含此功能的版本；仅升级 Server 不会产生 Windows 登录事件。
- Windows 需要开启 **高级审核策略 → 登录/注销 → 审核登录 → 失败**。组策略可覆盖本地设置。
- Host Agent 运行账户需要读取 **Windows 安全日志**的权限；某些部署需要以管理员或具备相应权限的服务账户运行。
- 监控失败只写日志，不影响 RDP 转发。建议使用测试账户验证 4625 的 `IpAddress` 和 `IpPort` 确实映射到本地代理 TCP 连接。

## 安全边界

- 服务端**不**解析 NLA/CredSSP、不会看到 RDP 密码，网络断线不等于登录失败。
- Server 不采信 Agent 上报的公网 IP，只采信自己监听公网入口时看到的 TCP 对端 IP。
- P2P 直连没有经过 Server 公网入口，不能凭这个 IP 映射自动封禁。
- 同源 NAT 下不同终端仍共享公网 IP；建议为办公固定出口设置自动封禁白名单。
- Windows 4625 仅能证明系统记录了一次失败登录；当无法匹配公网入口时，不能把失败归因给 RelayProxy 公网客户端。
