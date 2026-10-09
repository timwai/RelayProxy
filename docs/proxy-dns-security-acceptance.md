# RelayProxy 实际代理与 FakeIP DNS 防泄漏验收

> 状态：自动化测试已经覆盖独立的真实 SOCKS5/HTTP 代理进程，以及三种桌面操作系统的 DNS/FakeIP 策略和模拟数据包路径。**这不是跨平台零 DNS 泄漏认证**。macOS Network Extension、Windows WinDivert、Linux 代理进程故障/重启、Android VPN 的底层网络抓包必须在被测设备上补齐。

## 1. 可复现的自动化验收

入口：[Network Acceptance GitHub Actions](../.github/workflows/network-acceptance.yml)

| 环境 | 检查 | 可据此宣称 |
|---|---|---|
| Ubuntu CI + Dante 独立 SOCKS5 服务 | TCP CONNECT 回环 echo；RFC1928 UDP ASSOCIATE 真实 UDP echo | RelayProxy 与 Dante 的基本 TCP/UDP 互通 |
| Ubuntu CI + Tinyproxy 独立 HTTP 服务 | Basic 认证 TCP CONNECT 回环 echo；错误密码拒绝；HTTP UDP 禁止直连回落 | RelayProxy 与 Tinyproxy HTTP CONNECT 互通及身份验证行为 |
| Ubuntu / Windows / macOS Go test | FakeIP A/AAAA、TCP/UDP/53、DoH、DNS 出口作用域等策略/模拟数据包回归 | 代码逻辑及平台条件编译在对应操作系统通过 |
| Ubuntu CI 独立 Linux network namespace | 从仓库脚本提取原始 nftables OUTPUT 规则；veth 抓包验证无规则时可观测 DNS 端口数据、加规则后无数据 | **Linux nftables 规则**确实能阻止被测网络命名空间中的明文/专用加密 DNS 出站 |
| 真实 Windows/macOS/Android 机器 | 系统级驱动/VPN 接管、环回、故障重启、硬编码 DoH 443、OEM DNS 等 | **未由上述 CI 覆盖** |

在该 CI 中，代理和业务端点都限制在临时 runner 的 127.0.0.1，没有访问生产出口、真实用户 DNS、或使用生产密码。自动化测试不代表公网线路、远程 SOCKS5 认证方式、多用户隔离或受限网络环境全部通过。

### 手动执行真实代理集成测试

运行一个真实 SOCKS5（支持 UDP ASSOCIATE）的监听服务：

```bash
RELAYPROXY_LIVE_SOCKS5_ADDR="127.0.0.1:19081" \
  go test ./agent/exit -run '^TestLiveDanteTCPAndUDP$' -v -count=1
```

运行一个真实 HTTP CONNECT 服务（BasicAuth 用户名为 `relayproxy`、密码为测试专用固定值）：

```bash
RELAYPROXY_LIVE_HTTP_ADDR="127.0.0.1:19082" \
  go test ./agent/exit -run '^TestLiveTinyproxyHTTPConnect$' -v -count=1
```

`network-acceptance.yml` 自动安装、配置并启动这两个服务。集成测试使用固定的测试认证参数，**请不要直接指向带有真实凭据的生产代理**。

## 2. 跨平台系统级验收原则

1. 使用独立测试设备或网络命名空间，预先记录 Agent 版本、系统版本、测试时间、出站网卡以及规则修订号。
2. 启用 `routing.dns_mode: proxy`、`routing.fake_ip_enabled: true`；需要指定 DNS 出口时配置 `routing.dns_exit_id`。记录 Agent「诊断与日志」中的 DNS 防护状态。
3. 在**物理出站接口/路由器镜像口**上抓包；仅抓 `utun`、Wintun 或应用层代理接口，不足以证明物理网络未泄漏。仅用 `dig` 超时也不足以证明零泄漏。
4. 测试期间记录所有到非授权 DNS 服务的 TCP/UDP 53、853、784、8853，以及 TLS/QUIC 443 的外部连接；代理隧道中的加密数据本身不应误判为本机明文 DNS 泄漏。
5. 在完成 FakeIP 正常功能测试后，再单独启用独立 DNS Kill Switch，依次验证 Agent 停止、异常退出、代理失联、驱动/扩展停止及系统重启。确保始终保留本地控制台回滚入口。
6. 验收日志应包含原始 pcap、所选网卡、命令和失败原因。真实 DNS/pcap 可能暴露浏览历史及设备地址，**不要直接提交到公开 GitHub PR**。

建议使用抓包过滤表达式（IPv4/IPv6 同时适用）：

```
(udp or tcp) and (port 53 or port 853 or port 784 or port 8853)
```

对于 DoH/443 必须再检查目标 IP、SNI/握手和应用是否主动使用内置解析器。由于 TLS、ECH 和共享 CDN，**不能只根据端口 443 证明或排除 DoH**。

## 3. Windows 验收（WinDivert）

测试环境：Windows 10 / Windows 11，Network Service 与 WinDivert 驱动按实际安装流程运行。

1. 在 Agent 中开启透明代理、代理解析 DNS、FakeIP。用 Wireshark/tshark 同时观察物理网卡和 IPv6 出站流量。
2. 在 PowerShell 执行：
   ```powershell
   Resolve-DnsName example.com -Server 1.1.1.1 -Type A -DnsOnly
   Resolve-DnsName example.com -Server 1.1.1.1 -Type AAAA -DnsOnly
   Resolve-DnsName example.com -Server 1.1.1.1 -Type A -DnsOnly -TcpOnly
   ```
   A 预期落入 `198.18.0.0/15`，AAAA 预期落入 `2001:db8:198:18::/96`；不能在物理出站接口看到原始查询。IPv6 前提是系统路由及接管能力已经生效。
3. 执行独立 DNS Kill Switch 时先在**测试机**由管理员运行 `scripts/dns-killswitch-windows.ps1 Enable`；检查 `Status` 和 Agent DNS 是否仍可用。Windows Firewall 与 WinDivert 的拦截优先级尚未经过普遍验证，**阻断规则可能同时阻断 FakeIP 的正常解析**。
4. 测试 DNS/53、DoT/853、UDP 784/8853、已知及未知 DoH/443；特别测 `127.0.0.1` 和 `::1` 本地 DNS Stub。停止 Agent 和网络服务后重复外部 DNS 测试，再重启系统验证规则是否仍然有效。
5. 最后在管理员 PowerShell 执行 `scripts/dns-killswitch-windows.ps1 Disable` 恢复测试系统。

结论门槛：FakeIP A/AAAA 实际可用、明文 DNS 无出站包、异常退出后独立拦截仍生效，三者缺一不可。Windows loopback、硬编码 DoH 443 的不受控路径若仍存在，必须单独列为限制而不是判定完全通过。

## 4. Linux 验收（NFQUEUE + nftables）

需要具备 CAP_NET_ADMIN、CAP_NET_RAW，并已安装 iptables/ip6tables/nftables。

```bash
# FakeIP 正常运行时分别测试 UDP 和 TCP/53：
dig @1.1.1.1 example.com A +time=2 +tries=1
dig @1.1.1.1 example.com AAAA +time=2 +tries=1
dig +tcp @1.1.1.1 example.com A +time=2 +tries=1

# 在物理接口抓包（替换 eth0）：
sudo tcpdump -ni eth0 '(udp or tcp) and (port 53 or port 853 or port 784 or port 8853)'

# 在独立测试环境中启用持久出站阻断（需 root）：
sudo bash scripts/dns-killswitch-linux.sh enable
sudo bash scripts/dns-killswitch-linux.sh status

# 验证 Agent 退出、NFQUEUE 不存在、重启系统后的结果；完成后：
sudo bash scripts/dns-killswitch-linux.sh disable
```

在 CI 中，`scripts/ci-dns-guard-netns.sh` 无需启用系统服务，只在临时网络命名空间测试实际 nftables + veth 出站抓包。它**不能证明真实机器 systemd 服务在重启前的短暂窗口中也完全无泄漏**。真实系统必须补测 DNS Stub 和 loopback、NFQUEUE 清理后的状态、不同 iptables/nftables 后端以及真实 IPv6 出口。

## 5. macOS 验收（Network Extension）

需要**已签名、具备授权 entitlement 且被系统批准的 Network Extension**，未签名 CI 构建不具备系统级拦截能力。

```bash
dig @1.1.1.1 example.com A +time=2 +tries=1
dig @1.1.1.1 example.com AAAA +time=2 +tries=1
dig +tcp @1.1.1.1 example.com A +time=2 +tries=1

# 选择实际物理出口 en0 / en1 等，不只抓 utun
sudo tcpdump -ni en0 '(udp or tcp) and (port 53 or port 853 or port 784 or port 8853)'
```

验证 Network Extension 启用后 DNS/53 在网络外口不可见，Agent IPC 断开时是否由扩展拒绝流量，以及扩展停用/卸载后的系统 DNS 路径。专门验证 `127.0.0.1`、`::1` 和系统 DNS 代理路径。macOS 现有持久标记不是独立 PF 防火墙，**不能据此宣称重启/扩展退出后零泄漏**。

## 6. Android 验收（VpnService）

Android VPN 的 Mapped DNS 与桌面 Agent FakeIP 并非完全相同的拦截实现；必须分别验收。

1. 用 ARM32/ARM64 的真实测试设备安装客户端，记录 Android 版本、OEM、网络类型（Wi-Fi/移动数据）、VPN 模式与纳管应用列表。
2. 在「设置 → VPN → RelayProxy」分别测试 Always-on 和「无 VPN 时阻止连接」（Lockdown）。后者必须由用户在系统设置里开启。
3. 用 `adb shell dumpsys connectivity` 和 `adb shell settings get global private_dns_mode` 记录 VPN、Private DNS 状态。逐一测试 Private DNS 关闭、自动和指定主机名。
4. 通过网关镜像/受控路由器抓包（或其他可信的底层抓包方式）检查 DNS/53、DoT/853、DoH/443。Android 应用沙箱/VPN 内部抓包**不能独立证明物理出口无泄漏**。
5. 分别运行浏览器、Google Play、YouTube、内置 DoH App、系统服务；验证纳管应用之外的流量路径。模拟 VPN 进程杀死、切换 Wi-Fi/蜂窝、断开服务器、重启手机。
6. 若启用 Lockdown，期望 VPN 不可用时非豁免 App 无直接公网出口；如仍有 DNS 出站必须记录 ROM/OEM、进程和链路。不能单凭 manifest 声明判断通过。

## 7. 判定与剩余限制

| 验收项 | 当前判定 |
|---|---|
| 独立 Dante SOCKS5 TCP/UDP | 自动化覆盖；以对应 workflow 绿色结果为准 |
| 独立 Tinyproxy HTTP CONNECT + 错误密码 | 自动化覆盖；以对应 workflow 绿色结果为准 |
| Windows/macOS/Linux FakeIP 策略及包模拟 | 自动化覆盖；不等于物理驱动验收 |
| Linux nftables 真实出站阻断 | 隔离 veth CI 覆盖；需要观察 workflow 结果 |
| Windows 真正 WinDivert + WFP 物理出口抓包 | 待设备实测 |
| macOS 真正 Network Extension + 物理出口抓包 | 待签名设备实测 |
| Linux 系统重启、systemd 恢复、NFQUEUE 异常关闭 | 待真实系统实测 |
| Android OEM ROM、VPN Lockdown、Private DNS、DoH App | 待真机实测 |
| 未知 DoH/HTTPS 443、硬编码 IP 和环回 DNS | 当前实现仍无法完整阻止 |

**禁止以“CI 通过”表述跨平台 DNS 完全零泄漏。** 最终验收结果要以每一平台的真实物理接口证据以及每项失败/绕过路径的清单为准。
