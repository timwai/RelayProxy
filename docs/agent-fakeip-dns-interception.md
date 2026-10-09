# FakeIP DNS 接管（实验性）

当前进度：Agent Windows（WinDivert）、Linux（NFQUEUE）、macOS（Network Extension）透明代理的 **UDP/53、TCP/53 A/AAAA** 查询接管已经接入。默认关闭，需要用户明确开启。Android VPN 是单独的实现，不在本次 Agent 变更范围内。

## 配置和热更新

```yaml
routing:
  dns_mode: proxy
  fake_ip_enabled: true
```

- 在 Agent React UI 的「分流规则 → 全局策略」选择「由代理解析」，再打开「FakeIP 接管 DNS（实验性）」；本机 DNS 模式与 FakeIP 不可同时开启。
- 捕获 UDP/53 请求会就地合成 A/AAAA 答案；TCP/53 遵循 RFC 7766 两字节长度帧，在本地 TCP 反射连接或 macOS IPC 内响应，**不转发原始 DNS 查询**。
- A 使用 198.18.0.0/15，AAAA 使用 2001:db8:198:18::/96。返回 TTL 60 秒，Agent 映射保留最多 15 分钟；每个实例最多保留 32768 条同时有效的映射。容量耗尽时先回收过期记录，但**同一次 Agent 运行过程中不把过期 FakeIP 分配给其他域名**，避免旧缓存错误分流。IPv4 在运行期间最多签发 131070 个不同地址，签发空间耗尽时返回 SERVFAIL，不会在空间里循环复用。
- FakeIP 模式关闭后仅防护本次运行中已签发的占位地址；没有签发过的 198.18.0.0/15 测试网络流量继续按普通规则处理。
- RFC 6761 的 `localhost` 和 `*.localhost` 直接返回 127.0.0.1/::1，`invalid` 和 `*.invalid` 返回 NXDOMAIN，均不分配 FakeIP 或请求上游 DNS。
- 命中 FakeIP 的连接先映射回域名，按域名分流，IP/CIDR 规则不匹配占位地址。PROXY 流量发送域名给上游，避免本地查询。FakeIP 命中 DIRECT 规则时先由选定出口访问经 TLS 证书校验的 DNS 解析器获取真实 IP，然后本机对该真实 IP 建立 TCP/UDP 直连（本机不查询明文 DNS）。如代理 DNS 不可达则连接失败，不会直连 FakeIP。
- Relay 服务端的域名解析仅通过启动时已知的 relay IP 回应，缺少该信息时返回 SERVFAIL；不通过正常系统 DNS 查询来突破拦截。
- HTTPS/SVCB（类型 65/64）返回 NOERROR/NODATA，阻止泄露实际 IP hints 并允许客户端回退到 A/AAAA。TXT/SRV 可启用 `routing.forward_other_dns`，通过所选出口访问 `9.9.9.9:853`、严格验证 `dns.quad9.net` TLS 证书；该模式**向 Quad9 披露域名**，默认关闭。认证/传输失败返回 SERVFAIL，不回退本机明文 DNS。其余未支持 RR、畸形数据、已知专用加密 DNS 出口 TCP/UDP 853、784、8853 拒绝或丢弃。
- `routing.block_doh_endpoints: true` 默认关闭，仅针对**已获得可靠域名**的常见 DoH 服务域名在 443 端口采取拒绝。无法通过此方式识别硬编码 IP、ECH 和自建 DoH，也不会为了识别 DoH 封锁所有 HTTPS。

## 已补充的平台防泄漏防线

- Windows：在 WinDivert 的 Relay-IP 过滤例外之外另有 DNS/53 捕获规则，避免发往 Relay 同一 IP 的 DNS 请求绕过 FakeIP 判定；现有普通 TCP 反射和 Relay 控制流量豁免保持不变。
- Linux：在 Relay-IP bypass 之前先通过 NFQUEUE 捕获 TCP/UDP 53；这些 DNS 队列规则**没有 `--queue-bypass`**，因而在 iptables 规则仍安装但 NFQUEUE 进程故障时 DNS/53 将被内核丢弃，而不是逃逸。普通非 DNS 规则仍按原来的可用性策略执行。
- 以上保护不能替代**独立、持久的系统防火墙规则**：Agent 正常退出会清理 Linux 的 NFQUEUE/iptables 规则，Windows/macOS 仍有环回和未接管路径。不能把它称为跨平台 Kill Switch。

## 独立 DNS Kill Switch（需要管理员明确操作）

以下命令会影响整个系统的 DNS 出站通信，请务必预先安排可用的本地控制台和回滚路径。**不会随 Agent/FakeIP 开关自动启用。**

Linux（nftables + systemd，阻止非 loopback TCP/UDP 53、853、784、8853）：

```sh
sudo bash scripts/dns-killswitch-linux.sh enable
sudo bash scripts/dns-killswitch-linux.sh status
sudo bash scripts/dns-killswitch-linux.sh disable
```

规则在 OUTPUT priority 0 执行，低于 RelayProxy iptables mangle/NFQUEUE 处理优先级。Linux 脚本写入独立 nftables table 和 systemd unit；Agent 停止也不会主动撤销该独立规则。启动期间是否存在极短空窗仍需要实机验证。

Windows（需要管理员 PowerShell）：

```powershell
.\\scripts\\dns-killswitch-windows.ps1 Enable
.\\scripts\\dns-killswitch-windows.ps1 Status
.\\scripts\\dns-killswitch-windows.ps1 Disable
```

此命令添加独立的持久 Windows Firewall 出站封禁规则。**不同 Windows 版本的 WFP/WinDivert 规则优先级尚未实机验证**，可能阻止 FakeIP 查询，首次启用必须在测试机上验证；不保证环回流量可被此规则阻止。

macOS：当 FakeIP 启用时，Agent 在 App Group 内持久化 `dns-guard.enabled` 状态。Network Extension 在 Go IPC 失联但仍能看到系统流量时，会拒绝捕获到的 TCP/UDP 流，不再将这些流静默放行。它**不是持久 PF/网络过滤器**，无法覆盖 Network Extension 未交付的系统 DNS/loopback 流，也不能阻止用户卸载/停用扩展。

Android：VPN Service 已声明支持系统 **Always-on VPN**，系统重启服务时可重新建立隧道，VPN 状态附带 Always-on/Lockdown 信息，主界面可显示锁定状态。要确保 VPN 断线时阻止其他 App 走底层网络，用户还必须在 Android「设置 → VPN → RelayProxy」手工打开「始终开启 VPN」和「无 VPN 时阻止连接」。应用自身不能替用户直接开启系统 Lockdown。选择性应用 VPN 会受系统 Lockdown 限制，需实机验证。

## 后续安全加固（本轮）

- **FakeIP DIRECT 二次校验**：经所选出口的 TLS DNS 解析得到真实 IP 后，拒绝环回、RFC1918、链路本地、CGNAT、文档/保留地址、FakeIP 段等非公网目标；使用原始进程、域名、端口和新 IP 再匹配全量分流规则，真实 IP 命中 PROXY/REJECT 不允许绕过。如果目标和 Relay/本地监听器回环保护冲突也拒绝。DNS 解析或复核失败时不走系统明文 DNS，也不直接发送占位 IP。
- **DNS 专用出口**：新增 `routing.dns_exit_id`。经代理的加密 DNS 查询使用优先级：原连接明确指定的出口 → DNS 专用出口 → 当前默认出口。TXT/SRV 查询发生在应用连接建立前，通常没有可靠进程身份；只能使用显式 DNS 出口或默认出口，不能宣称跨进程 DNS 身份完全隔离。指定不存在/停用的本机自定义 DNS 出口时拒绝加载或热更新配置。
- **Linux 按模式同步切换 DNS 队列**：FakeIP 关闭时 DNS/53 NFQUEUE 使用 `--queue-bypass`；启用 FakeIP 前，在共享策略锁内先安装无旁路 DNS 规则，再发布 FakeIP 策略。关闭时先发布普通策略、后放宽内核规则。逐项记录 IPv4/IPv6 × TCP/UDP 四条规则的更新结果，部分失败不会宣称完全成功，后续仅重试失败项。防火墙关闭时禁止后台线程重新写入。**iptables 更新并非系统级原子事务**；若内核规则被第三方删除，或 Agent/驱动清理拦截器，没有独立 Kill Switch 仍可能泄漏。
- 这些改动并不解决应用通过硬编码 IP 的 DoH、操作系统未交付的环回 DNS、或平台级防火墙优先级差异；严格模式必须使用独立 Kill Switch 并配合真实系统验收。

## DNS 防护诊断证据

Agent「诊断与日志」新增 DNS 安全状态：FakeIP 策略开关、内核 DNS 队列模式、局部更新失败，以及独立 Kill Switch 查询结果。Linux 通过限时缓存的 `nft list table inet relayproxy_dns_guard` 检查 DNS 出站规则，并检查独立 `relayproxy-dns-killswitch.service` 是否启用且运行；服务缺失/不可用则标为持久性未验证。Windows 通过 PowerShell 检查 ActiveStore/PersistentStore 内的 TCP/UDP 阻断规则及端口、启用状态；macOS 标为未验证。所有结果都不等同于系统级零泄漏认证。

诊断中的 `rules-present` 仅代表观察到独立阻断规则：无法证明其下次重启前生效、iptables/nftables 优先级配置正确、其他网络命名空间也受保护，或 DoH/443 无泄漏。要达到强保障，需按平台测试 DNS/53、853、DoH/443、环回解析、Agent 异常退出、IPv6、切换出口和重启后的网络路径。

## 本轮：DoH 指定目标 IP、FakeIP DNS 出口隔离与防火墙状态

- 在 `routing.block_doh_endpoints: true` 下可额外配置 `routing.doh_blocked_ips`，接受用户手工指定的单个公网 IPv4/IPv6 或较窄 CIDR（IPv4 /24 及以上精度、IPv6 /48 及以上精度，最多 256 项），仅阻断这些目标的 TCP/UDP 443。**会阻断这些 IP 上的其他 HTTPS/QUIC 服务**，尤其共享 CDN 的 IP，必须由用户确认；不是依据 HTTPS 内容可靠识别 DoH，也不是自动识别所有硬编码 IP 的 DoH。关闭 DoH 阻断开关后 IP 列表不生效。
- FakeIP A/AAAA 映射按选定的加密 DNS 出口划分缓存作用域。同一域名经 DNS 出口 A、B 分配不同的合成地址；出口切换后，新流量访问旧作用域 FakeIP 会安全拒绝并显示 `fakeip-dns-exit-changed`，应用需重新发起 DNS 查询。仍然不保证某个系统 DNS 进程代表哪个应用，也不宣称应用进程/用户间 DNS 隔离。客户端设备身份由 Agent 实例管理，切换身份需要重新启动实例。
- Windows 独立 Kill Switch 诊断通过 `Get-NetFirewallRule` 查询 ActiveStore/PersistentStore，并通过 `Get-NetFirewallPortFilter` 核对 TCP/UDP、远端 53/853/784/8853、出站 Block、启用状态和防火墙 Profile 是否开启；结果仍只表示**发现配置规则**，不能证明 WFP/WinDivert 拦截优先级或全部绕过路径。<https://learn.microsoft.com/powershell/module/netsecurity/get-netfirewallportfilter>
- macOS Network Extension 对系统环回 DNS 的不可见范围、Android OEM 厂商 VPN/Private DNS 的差异仍需要真机断线、重启及网络抓包验收；不具备可被单元测试替代的零泄漏保证。

## 尚不能宣称完整的零泄漏能力

1. **DoH/HTTPS/443**：已知 DoH 主机名可选阻断，但应用自带私有解析器、直连固定 IP、ECH 或其他 HTTPS 请求无法被当前层完全区分。DoH3/QUIC 也需要专门治理。
2. **系统环回 DNS**：Windows WinDivert 当前过滤掉 loopback，Linux 应用到 127.0.0.1、::1 的请求，以及 macOS Network Extension 不交给透明代理的系统 DNS 流，可能绕过本次拦截。
3. **故障期间**：底层驱动关闭或 Packet Interceptor 发生不可恢复错误时，现有平台设计会释放拦截器（fail-open）。如果没有独立 OS 防火墙的始终在线 DNS 阻断规则，就不能提供严格的断线不泄漏保证。
4. **兼容性**：HTTPS/SVCB 采用 NODATA 回退，TXT/SRV 可选经代理 DoT，FakeIP DIRECT 可先经代理 DNS 恢复真实 IP。但 DNSSEC、mDNS、split-horizon DNS、本地网络 DIRECT 与 IPv6/多地址选择尚不等同原系统解析语义。
5. **IPv6**：合成 IPv6 占位地址需要应用主机有适用的 IPv6 路由才有机会进入透明代理。不能依赖 FakeIP 自动补全 IPv6 网络能力。
6. **Android**：Android 使用 hev-socks5-tunnel Mapped DNS 和 Go 层 FakeIP 防护；已支持系统 Always-on/Lockdown 模式的集成状态，但无 Android 流量抓包实测，不能宣称任何 ROM/第三方 DNS App 均无泄漏。

因此该功能的当前 UI 明确标记为 **实验性**。在实现平台 DNS 强制重定向、独立持久 OS 防火墙泄漏阻断、DoH 控制与全面应用兼容测试前，不能作为严格安全边界。

## 独立实际代理及系统 DNS 防泄漏验收

[实际代理与 FakeIP DNS 防泄漏验收手册](proxy-dns-security-acceptance.md) 列出了可复现的 Dante / Tinyproxy 实际服务测试、Linux nftables 隔离网络内核抓包，以及 Windows、macOS、Linux、Android 必需的物理出口/驱动/VPN 验收条件。**协议和规则的 CI 成功不代表已完成跨平台零泄漏认证**。

## 回归测试

- `go test ./agent/divert ./agent/routing ./internal/config`
- `go test -race ./agent/divert ./agent/routing`
- Windows/macOS GUI CI 及 Linux Go CI。
- 真实机器验收：分别测试系统 DNS、私有 DNS、TCP/53 回退、AAAA/IPv6、目标进程已有 DNS 缓存、DIRECT 规则、网络断线和重新连接。

参照 Proxifier 的 <https://www.proxifier.com/docs/win-v4/dns.html> 关于 FakeIP 占位/无法按真实 IP 过滤的说明，以及 <https://www.proxifier.com/docs/win-v4/proxy-advanced.html> 关于只有已知域名才能使用 hostname 的说明。