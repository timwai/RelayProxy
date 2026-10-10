# RelayProxy DNS 独立能力及配置

## 三种功能的分工

| 字段 | 默认 | 行为 |
| --- | --- | --- |
| `routing.dns_association_enabled` | **true**（旧配置未设置时） | 观察传统 UDP/53 的请求与响应，保守记录域名→真实 IP 的关联；共享地址存在多个有效域名时不猜测。关闭后新透明连接不再使用此关联。 |
| `routing.proxy_dns_enabled` | **false** | 系统 DNS/53 查询由已运行的透明代理接管，通过当前授权的 DNS 出口（`dns_exit_id`，否则默认出口）访问 Quad9 DoT/TLS 并返回**真实** DNS 记录，不分配 FakeIP。支持 A/AAAA、TXT/SRV、HTTPS/SVCB 等可转发的 IN 记录。 |
| `routing.fake_ip_enabled` | **false**（维持旧配置） | 截获可捕获的 DNS/53 A/AAAA 查询并返回保留地址，透明连接命中映射后以原始域名连接代理；其他保护能力保持原先的配置及约束。 |

DNS 关联可以独立启用、关闭；代理真实 IP 解析与 FakeIP 同时接管同一 DNS/53 端口会发生冲突，因此两者互斥。新功能均**不会默认接管原先未接管的系统 DNS**。

## 与「通过代理解析主机名」的区别

`dns_mode=proxy` 只影响上游代理拨号时**已经知道域名**的连接；浏览器已把域名解析为 IP 的透明连接，无法单凭这一配置恢复域名。`proxy_dns_enabled=true` 则主动接管传统系统 DNS/53 并向代理出口的 DoT 解析器查询，从而规避部分本地解析错误。为避免二次本机解析，该模式要求手动 `dns_mode=proxy` 且 `auto_detect_dns=false`。

启用或切换真实 IP DNS、FakeIP 后可热更新；在 Linux 使用专用非旁路 NFQUEUE 保护队列，Windows 依赖现有 WinDivert 捕获，macOS 依赖 Network Extension。只有相应透明代理正在运行时接管才生效。

## 安全及兼容性边界

- 查询经远端 Quad9 解析会暴露查询域名给该 DNS 服务（在选定代理出口之外不可见）。TLS 使用证书验证；无法连接代理、超时、证书失败或结果校验失败时返回 SERVFAIL，不自动降级为明文 DNS。
- DNS 接管不是零泄漏保证：Chrome 内置 DoH/HTTPS 443、环回 DNS、操作系统缓存以及关闭底层捕获服务后的防护强度仍需独立测试。Linux 和 macOS 的持久防护状态需检查本机设备实际运行情况。
- 真实 IP DNS 允许 DIRECT 规则访问原目标 IP；**仅靠观察的 DNS 关联不是应用归属的确证**，同 IP 多域名时可能不可用于域名规则。若必须确保每个连接都保留域名，可以明确使用 FakeIP。
- Relay 启动依赖的已知服务器主机名会从已验证的 Relay IP 列表回答，避免循环依赖还未建立的代理通道。其它查询不会因为代理不可用而本地回退。
- 当单个 UDP 响应过大时返回 DNS TC 标志，要求客户端用被接管的 DNS/TCP 重试；DNS/TCP 返回完整 DoT 回答。

## Windows Chrome 验证清单

1. 确认 Windows GUI 的网络模式为透明代理，已选择有效出口。
2. 关闭 FakeIP，开启 `通过代理解析系统 DNS（返回真实 IP）` 与 `DNS 关联`，保存；确认 `通过代理解析主机名` 已开启，自动 DNS 已关闭。
3. 关闭并重新打开 Chrome，访问 `www.google.com`，同时在实时监控中比较域名/IP、规则、出口与错误情况。
4. 如 Chrome 仍失败，分别验证 Chrome 安全 DNS（DoH）和 HTTP/3 QUIC 路径；不能把缓存、DoH 失败简单归因于 DNS/53 捕获。检验 DNS 回复的源 IP 与原始系统 DNS 目标一致。
5. 进行出口断线重连和模式切换（真实 IP↔FakeIP↔无接管），确认没有假地址漏出、明文回退或配置开关重置。

