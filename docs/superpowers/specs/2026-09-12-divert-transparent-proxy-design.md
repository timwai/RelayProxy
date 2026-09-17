# Divert 系统透明代理设计（替代 TUN）

> 日期：2026-09-12  
> 状态（2026-09-15）：共享规则/隧道和三端适配器已实现。Windows x64 使用 WinDivert；Linux 使用 NFQUEUE/iptables；macOS 使用 `NETransparentProxyProvider` 与认证 Unix IPC。自动化测试和交叉编译已完成，仍需分别在管理员 Windows、root Linux 与已签名批准扩展的真实 Mac 上完成系统级验收。详见各平台使用文档。  
> 参照：[ProxyBridge](https://github.com/InterceptSuite/ProxyBridge) 的系统级拦截思路；出口为 Relay 隧道，非第三方 SOCKS/HTTP。

## 1. 背景与目标

### 现状问题

当前 `agent/tun` 基于 Wintun/utun + gVisor + fake-IP DNS：

- 仅路由 `198.18.0.0/15`，非全局透明
- DoH/DoT、硬编码 IP、自带解析应用会静默绕过
- 基本只支持 TCP + DNS；非 DNS UDP 被丢弃
- Windows 依赖 Wintun

### 目标

按 ProxyBridge 模式重做透明代理：

| 决策 | 选择 |
|------|------|
| 平台 | Windows + macOS + Linux 同一产品版本一起交付 |
| 规则 | **按进程为主**，可叠加 IP / 端口 / 域名过滤器 |
| 旧 TUN | **整段删除**，不做并存、不做静默兼容 |
| 出口 | 拦截层 **直接** 调隧道 Dialer（不经本机 SOCKS5/HTTP） |
| UDP | **与 TCP 同一期** 必须进隧道（扩展协议 + 出口转发） |
| 架构 | **共享 Go 策略/隧道核心 + 各平台薄拦截层**（方案 2） |

本地 SOCKS5/HTTP **保留**，供手动配置代理的应用使用。

## 2. 架构总览

```text
Application traffic
        │
        ▼
┌───────────────────────────────┐
│ Platform intercept              │
│  Win: WinDivert                 │
│  Lin: NFQUEUE + iptables/nft    │
│  Mac: Network Extension (Swift) │
└───────────────┬─────────────────┘
                │ process + 5-tuple + proto
                ▼
┌───────────────────────────────┐
│ Go agent (shared)               │
│  ProcessEngine.Match            │
│  loop guards                    │
│  DivertDialer                   │
│    PROXY  → TunnelDialer        │
│              DialTCP / DialUDP  │
│    DIRECT → release / dial out  │
│    REJECT → drop / RST          │
└───────────────────────────────┘
```

- Go：规则引擎、隧道、配置、GUI bridge、环路排除
- 平台层：只负责抓流、识别进程、把连接交给 Go（macOS 经 XPC/本地 IPC）

## 3. 配置模型

### 3.1 替换关系

| 现状 | 新模型 |
|------|--------|
| `network.mode: tun` + `network.tun.*` | 删除 |
| 顶层 `routing`（域名/CIDR 全局规则） | 语义迁入 `network.rules` 的可选过滤器；SOCKS/HTTP 路径可暂留 `RoutingDialer`，最终以进程规则为准 |
| SOCKS5/HTTP | 保留 |

旧配置出现 `network.mode: tun` 时：**拒绝启动或明确报错提示迁移**，不做静默映射到 divert。

### 3.2 YAML 形状

```yaml
network:
  mode: divert          # "" 关闭 | divert 系统级透明代理
  default_action: PROXY # 无规则命中：PROXY | DIRECT | REJECT
  exclude_processes:    # 防环路；实现侧始终包含本进程
    - RelayProxy.exe
    - relayproxy
  rules:                # 自上而下，first-match-wins
    - name: 浏览器走隧道
      enabled: true
      process: "chrome.exe"   # 通配：*.exe / /usr/bin/*
      hosts: []               # 可选；与 cidrs/ports 为 AND
      cidrs: []
      ports: []               # 如 ["443", "80-90"]
      protocols: [tcp, udp]   # 默认两者
      action: PROXY           # PROXY | DIRECT | REJECT
      exit_id: ""             # 可选
    - name: 其余直连
      enabled: true
      process: "*"
      action: DIRECT
```

### 3.3 GUI

- 「本地代理服务」中 TUN 块 → **「系统透明代理」** 开关（`mode: divert`）
- 「路由分流」改造为 **进程规则表**（进程、协议、动作、启用、可选过滤器）
- Windows：可选「从运行中进程选择」；macOS/Linux：第一期允许手填路径/名

### 3.4 热加载

- `rules` / `default_action` / `exclude_processes`：热加载
- `mode` 开/关 divert：需提升权限并重新挂载拦截（GUI 提示重启或重新授权）

## 4. 连接数据面

### 4.1 匹配与动作

1. 平台层提供：进程路径/名、协议、源/目的地址与端口
2. `ProcessEngine` first-match；未命中用 `default_action`
3. 硬环路排除（先于用户规则或作为强制 DIRECT）：
   - 本进程（RelayProxy）
   - 目标为中继服务器 address:quic_port / tcp_port
   - 目标为本机 SOCKS5/HTTP 监听地址:端口

### 4.2 TCP

拦截后 `PROXY` → `TunnelDialer.DialTCP` → 双向拷贝；`DIRECT` 放行；`REJECT` RST/丢弃。

### 4.3 UDP（同一期必做）

当前隧道仅有 `DialTCP`。同一交付必须包含：

1. 协议扩展：UDP 关联/数据帧（具体帧格式在实现计划中定稿，需兼容现有 stream 控制面）
2. `TunnelDialer.DialUDP(ctx, exitID, host, port) (net.PacketConn 或等价, error)`
3. 出口节点：按关联转发 UDP，处理超时与清理
4. divert 层：`PROXY`+udp 走 `DialUDP`；单测覆盖客户端→中继→出口→外网 UDP

IPv6：规则与拦截支持 IPv6 五元组；PROXY 失败时显式日志，不静默黑洞。

### 4.4 与 SOCKS/HTTP 共存

- 手动代理应用继续用本地 SOCKS5/HTTP → 现有隧道路径
- divert 开启时，对本机代理端口的连接必须 DIRECT，避免二次劫持

## 5. 三端拦截与安装

### 5.1 Windows

- WinDivert 抓/注入 TCP+UDP；PID → 进程路径
- 管理员权限；**不需要 Wintun**
- 注意与其它 WinDivert 工具冲突

### 5.2 Linux

- iptables/nft → NFQUEUE；用户态匹配后 PROXY/DIRECT/REJECT
- root / `CAP_NET_ADMIN`；内核需 NFQUEUE
- 进程退出必须清理规则
- 容器/特殊 netns：标为不支持或降级

### 5.3 macOS

- Network Extension（System Extension）拦截 TCP+UDP
- 用户批准扩展；正式分发需 Developer ID + notarization
- Swift 扩展经 XPC/本地 socket 交给 Go agent
- divert 模式建议强制 GUI/助手流程（纯 CLI 体验差）

### 5.4 失败行为

无权限/驱动/扩展失败 → **明确错误**；不回退旧 TUN（已删除）。用户仍可仅用 SOCKS5/HTTP。

## 6. 迁移与删除清单

| 区域 | 动作 |
|------|------|
| `agent/tun/**` | 删除 |
| `wireguard/tun`、`gvisor`、`wintun` 依赖 | 移除 |
| `Network.Tun` / `mode: tun` | 改为 divert 配置 |
| `agent.go` `tunSrv` | 换 divert 生命周期 |
| GUI TUN UI / home-tun | 换 divert + 进程规则 |
| `bridge` fingerprint Tun* | 换 divert 启动项 |
| 文档与 `configs/relay-agent.yaml` | 更新为 divert 说明 |
| ProxyBridge JSON 导入 | **不做**（本设计边界） |

## 7. 里程碑（同一产品版本，可分 PR）

| 里程碑 | 内容 | 验收 |
|--------|------|------|
| M1 | UDP 隧道端到端（可先不接 divert） | DialUDP 经出口出网；单测 |
| M2 | ProcessEngine + 配置/GUI 骨架 | 匹配单测；YAML/ bridge 读写 |
| M3 | Windows WinDivert + TCP/UDP PROXY | 指定 exe 流量进隧道 |
| M4 | Linux NFQUEUE + TCP/UDP | 同上 |
| M5 | macOS Network Extension + TCP/UDP | 同上；安装/签名文档 |
| M6 | 删除全部 TUN + 依赖清理 + 回归 | `go test`；无 gvisor/wintun |

顺序：**M1 → M2 → M3/M4/M5（可并行）→ M6**。

## 8. 非目标（本设计明确不做）

- 保留或兼容 fake-IP TUN / Wintun
- 将出口改为任意第三方 SOCKS/HTTP（始终 Relay 隧道）
- 第一期 ProxyBridge 规则 JSON 导入
- 容器网络命名空间内 divert

## 9. 规格自检

- [x] 无 TBD/占位未决项（UDP 帧字节布局留给实现计划定稿，范围已锁定）
- [x] 与已确认决策一致：三端、进程规则、删 TUN、直连 Dialer、同期 UDP
- [x] 与现有 SOCKS/HTTP、环路排除无矛盾
- [x] 范围边界（非目标）已写明
- [x] 未把实现细节（具体 WinDivert filter 字符串、NE entitlements 全文）塞进规格，避免绑死实现

---

**下一步**：用户审阅本文件；确认后编写 `docs/superpowers/plans/` 实现计划，再按 M1→M6 开工。
