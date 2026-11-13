# RelayProxy 通用端口转发与统一隧道设计

> 状态：P0 开发中；当前实现进度见 `docs/unified-port-forwarding-progress.md`  
> 设计日期：2026-11-13  
> 目标分支：`feat/unified-port-forwarding-20261113`  
> 基线分支：`main`（commit `5518c5a2c363d14678979ee27822f94fa75da076`）  
> 范围：Agent 到 Agent 的 TCP/UDP 端口转发、P2P/Relay 路径选择，以及 RDP 迁移  
> 核心原则：统一端口转发管理和路径选择，不重复实现现有的 QUIC、Datagram、P2P 穿透和安全认证基础设施。

---

## 0. 决策摘要

RelayProxy 应增加通用的 **Port Forwarding / TCP Tunnel / UDP Tunnel** 能力。用户在本地设备创建端口映射后，可以通过本机的 `127.0.0.1:<local-port>` 访问远程 Agent 设备上的服务；默认访问远程 Agent 本机的 loopback 服务，也可以在明确授权后访问远程 Agent 所在局域网的服务。

本设计确定以下决策：

1. **P0 同时实现 TCP 和原生 UDP Datagram 能力**，不是只做 TCP。
2. **P0 支持 P2P 直连**。P2P 优先尝试，失败或超时后由 Relay 兜底；采用有界竞速，避免 P2P 探测拖慢首包。
3. TCP 和 UDP 分别选择路径。RDP 的 TCP、UDP 可以走不同路径，不能假设二者始终共用一条链路。
4. **RDP 在 P0 纳入统一端口转发管理**，保留 TCP 3389 和 UDP 3389 的实际语义，不以 TCP 封装 UDP 来伪装 UDP 支持。
5. Relay UDP 在 P0 使用现有 QUIC 原生 Datagram 能力；当 Relay 连接只能使用 TLS/TCP 且 P2P 也不可用时，RDP 仍可退回 TCP，但 UDP 状态必须明确显示不可用。若要求这种极端网络下 UDP 也必须可用，需要 P1 的独立原生 UDP Relay 数据面。
6. 不把现有 `internal/tunnel.TunnelManager` 误当作 Agent 到 Agent 的通用端口转发管理器。它目前主要管理 Agent 到 Relay Server 的持久隧道连接。
7. 不立即推倒现有 RDP P2P 实现。先以统一接口接入并保持旧路径兼容，再逐步将 RDP 专用 P2P 迁移到通用的认证 QUIC Peer Session。

---

## 1. 目标与非目标

### 1.1 目标

- 访问远程 Agent 本机监听的 TCP/UDP 服务，例如 Web 管理端口、SSH、RDP、数据库、Docker API 和自定义服务。
- 支持一个远程设备配置多个映射。
- 支持多个本地 TCP 客户端并发连接。
- 支持 UDP 数据报、多个来源端口对应的独立 UDP Association、超时回收和分片。
- P2P 可用时优先直连；无法直连时自动使用 Relay。
- 支持远程 Agent 所在局域网的固定目标地址，但必须有显式授权和目标 ACL。
- 复用现有安全传输、P2P 候选发现、打洞、QUIC、流复用和 Datagram 能力。
- RDP 的 TCP/UDP 统一使用同一套端口映射生命周期、路径状态、审计和资源回收机制。

### 1.2 非目标（首期不做）

- 不替代现有系统代理、分流规则或默认出口功能。
- 不把端口转发变成不受限制的远程 SOCKS/HTTP 代理。
- 不允许用户通过端口转发扫描任意内网网段。
- 不在 P0 实现反向端口转发。
- 不在 P0 实现完整的 SOCKS5 动态代理。
- 不保证所有网络条件下 UDP 都能穿透；UDP 不可用时必须显式报告，不能静默降级为可靠 TCP 数据流。

---

## 2. 当前代码基线

以下是基于当前 `main` 分支代码的盘点。设计应复用这些能力，不重复造轮子。

### 2.1 已有通用隧道能力

| 现有代码 | 当前职责 | 本设计中的复用方式 |
|---|---|---|
| `internal/tunnel.TunnelManager` | Agent 与 Relay Server 之间的持久连接、QUIC/TLS 选择和重连 | 继续作为 Agent 到 Relay 的底层连接管理器 |
| `internal/tunnel.TunnelSession` | 多路复用会话接口 | 作为 Relay TCP Stream 的基础 |
| `internal/tunnel.TunnelStream` | 双向可靠流、半关闭和 Deadline | 映射一个 TCP 本地连接 |
| `internal/tunnel.DatagramChannel` | QUIC 原生 Datagram Association | 承载 UDP 数据报 |
| `internal/tunnel.UDPDatagramConn` | 原生 Datagram 到 `net.PacketConn` 的适配、分片和重组 | 复用 UDP 本地转发 |
| `internal/tunnel.UDPStreamConn` | 通过可靠 Stream 封装 UDP 数据报 | 仅用于明确允许 HOL 的业务，不作为 RDP UDP 传输 |
| `internal/tunnel.DirectQUIC` | 直接 QUIC 监听/拨号配置，启用 QUIC Datagram | 通用 P2P Peer Session 的底层传输候选 |

### 2.2 已有 P2P 能力

当前存在两套职责不同的 P2P 实现：

- `agent/p2p`：代理 Client/Exit 之间的 P2P QUIC。通过 Server 控制面交换候选、打洞、指纹验证和租约管理；Endpoint 保持同一个 UDP Socket 用于候选发现、打洞和 QUIC，避免 NAT 映射变化。QUIC 配置已启用 Datagram。
- `agent/rdp/p2p`：RDP 专用直连。通过 RDP 控制面分配 Lease，支持 TCP 打洞和 UDP 打洞；UDP 数据使用独立会话认证和分片逻辑。其设计与通用代理 P2P 不同，不能简单认为它已经是通用 TunnelSession。

`agent/p2p.Manager` 与 `agent/rdp/p2p.Manager` 都有价值，但前者包含代理 Exit/ACL 等业务语义，后者包含 RDP 专用会话语义。新设计应抽取可复用的 Peer Transport / Peer Session 能力，不应直接把代理业务 Manager 改名后用于所有协议。

### 2.3 当前 RDP 路径

当前 RDP 已有 TCP + UDP 本地监听，并且已有 P2P：

- `agent/rdp/controller.go` 在本机 loopback 上创建 TCP Listener；当 UDP 能力可用时，在同一端口创建 UDP Socket。端口按设备稳定分配。
- Relay TCP 通过 `agent/client/rdp.go` 的 `DialRDPTCP` 打开逻辑 Stream，Server 通过目标设备的 Tunnel 再打开一条 Stream。
- Relay UDP 通过 `DialRDPUDP` 打开 Datagram Association；Server 的 `handleOpenRDPUDP` 要求 Client-Agent/Server 和 Server/Target-Agent 两段隧道都支持并协商了原生 QUIC Datagram。
- RDP 专用 P2P 已支持 TCP 和 UDP。当前 `agent/app.ConnectRDP` 分别对 TCP、UDP 进行直连与 Relay 竞速；RDP 性能设计记录了直连先启动、约 300 ms 后启动 Relay 的策略。
- 当前 Relay UDP 不会把 UDP 静默转换为可靠 TCP Stream。若当前 Relay 会话是 TLS/TCP 或不具备 Datagram 能力，RDP UDP 会被禁用，但 TCP 仍可工作。

因此，**“RDP P0 支持 UDP”和“所有网络环境都保证 UDP 可用”是两件事**。P0 应覆盖 P2P 原生 UDP 和 Relay QUIC 原生 Datagram；Relay 只能走 TLS/TCP 且 P2P 失败时，UDP 仍有明确的能力边界。若产品要求这一条件下也保证 UDP，需要增加独立 UDP Relay（P1）。

### 2.4 现有安全边界

- RDP 的控制端只传目标设备 ID，目标服务地址由目标 Agent 配置，Server 根据设备授权和 RDP Grant 校验。
- 通用代理 P2P 使用 Server 控制面协调，并校验会话、设备身份和 Peer 指纹。
- 当前安全传输设计文档明确：现有 E2EE 覆盖范围尚未包含 RDP。不能因为两段连接都使用 TLS，就声称 Relay 无法读取转发内容。

新端口转发必须延续“Server 授权、目标 Agent 最终执行 ACL”的边界，并为 Relay 路径明确端到端加密要求。

---

## 3. 用户模型与配置模型

### 3.1 使用方式

示例：远程 Agent 所在 Windows 设备运行一个只监听本机的 Web 管理服务：

- 远程目标：`127.0.0.1:8080`
- 本地监听：`127.0.0.1:18080`
- 用户访问：`http://127.0.0.1:18080`

本地应用只访问本机端口，不需要知道远程设备的公网地址，也不需要在远程路由器配置端口映射。

其他示例：

| 服务 | 本地监听 | 远程目标 |
|---|---:|---:|
| Web | 18080 | 127.0.0.1:8080 |
| SSH | 12222 | 127.0.0.1:22 |
| PostgreSQL | 15432 | 127.0.0.1:5432 |
| RDP | 13389 或设备固定映射端口 | 127.0.0.1:3389 |
| 自定义 TCP | 用户指定 | 用户指定 |
| 自定义 UDP | 用户指定 | 用户指定 |

### 3.2 映射配置字段

建议一个 Mapping 表示一个目标服务，协议可选 TCP、UDP 或 TCP+UDP。

| 字段 | 说明 | 约束 |
|---|---|---|
| `mappingId` | 映射唯一 ID | Server/Agent 生成，不由目标地址推导 |
| `name` | 映射名称 | 用户可编辑 |
| `remoteDeviceId` | 远程 Agent 设备 ID | 必须是当前已授权设备 |
| `protocol` | tcp / udp / both | 不允许隐式转换 |
| `listenAddress` | 本地监听地址 | 默认 127.0.0.1 |
| `listenPort` | 本地监听端口 | 支持固定端口；冲突时明确报错 |
| `targetAddress` | 远程目标地址 | 默认 127.0.0.1 |
| `targetPort` | 远程目标端口 | 1–65535 |
| `enabled` | 是否启用 | 停用后关闭 Listener 和活动 Association |
| `targetScope` | loopback / allowlisted-network | 默认 loopback |
| `createdAt / updatedAt` | 配置时间 | 用于审计和同步 |

目标地址由远程 Agent 解析并连接，避免本地客户端先解析目标域名导致 DNS 泄漏或地址语义错误。若目标为域名，目标 Agent 必须在解析后对所有 A/AAAA 结果执行 ACL，并在拨号时绑定通过校验的地址，防止 DNS Rebinding。

### 3.3 配置归属

- Mapping 由发起访问的本地 Agent 管理，便于在设备详情页创建、启停和删除。
- Server 是设备授权、目标服务权限、租约和审计的权威来源。
- 目标 Agent 保存并执行本机允许的目标地址/端口策略，不能信任发起端自行声明的权限。
- 若后续需要多台本地设备共享同一套 Mapping，可将配置同步到 Server，但不能因此放宽目标 Agent ACL。

---

## 4. 总体架构

### 4.1 数据路径

~~~text
本地应用
   |
   | TCP connect / UDP datagram
   v
本地 Agent PortForward Manager
   |
   +-- TCP Listener / UDP Listener
   |
   v
Transport Selector（TCP、UDP 独立选择）
   |
   +---------------------------+
   |                           |
   v                           v
P2P Peer Session           Relay Session
优先尝试                   兜底/竞速
   |                           |
   | direct QUIC               | Agent -> Server -> Agent
   | TCP Stream + Datagram     | Stream + QUIC Datagram
   |                           |
   +-------------+-------------+
                 |
                 v
          远程 Agent PortForward Handler
                 |
                 v
          Target ACL / Resolver
                 |
                 v
          远程目标 IP:Port
~~~ 

### 4.2 控制面与数据面

**Server 控制面负责：**

- 验证发起设备和目标设备在线状态、所有者关系、审批状态及功能 Grant。
- 验证发起设备是否有权访问目标设备。
- 创建短期 Session/Lease，交换候选地址和能力信息。
- 下发 Server 侧目标策略版本或授权引用。
- 处理会话续租、撤销、设备离线和权限变化。
- 记录映射创建/删除、访问设备、协议、路径、流量和结果。

**数据面负责：**

- P2P 成功时，TCP/UDP 数据不经过 Server。
- P2P 不可用时，TCP 通过 Relay Stream 转发；UDP 通过 Relay 原生 Datagram 转发。
- 目标 Agent 最终执行 DNS 解析、目标地址校验和 Socket Dial。
- Relay 不能因为客户端提交了一个地址就允许目标 Agent 连接它。

### 4.3 不要混淆两种 Manager

建议明确区分：

- `TunnelManager`：Agent 到 Relay Server 的持久底层连接，负责连接、重连和 QUIC/TLS。
- `PortForwardManager`：用户可见的端口映射生命周期，负责 Listener、Association、目标服务、路径选择和审计。

新代码建议放在独立的 `agent/portforward` 包；协议和通用传输接口放在 `internal/protocol`、`internal/tunnel` 或新的小型共享包中。不要把用户映射配置直接塞进现有 TunnelManager。

---

## 5. 传输设计

### 5.1 TCP

每个本地 TCP 接入对应一条远程逻辑 Stream：

1. 本地 Agent 接受 TCP 连接。
2. 根据 Mapping ID 和远程设备 ID 创建 Open 请求。
3. Transport Selector 选择 P2P 或 Relay。
4. 目标 Agent 校验 Lease、Mapping 权限和目标 ACL。
5. 目标 Agent Dial 目标 IP:Port。
6. 双向复制字节，保留 Deadline、取消、半关闭和完整关闭语义。
7. 记录实际成功建立的数据路径和双向字节数。

要求：

- 同一 Mapping 支持多个并发连接。
- 单个连接失败不能影响其他连接。
- 限制每个设备、每个 Mapping 的并发连接数和打开速率。
- TCP 半关闭必须保留；不能因单方向 EOF 无条件立即丢弃另一方向剩余数据。
- 活动连接的生命周期由本地连接、目标连接和授权 Lease 共同控制。

### 5.2 UDP

UDP 必须以 Datagram 为单位传输，不能把一次 UDP 连接简单等同于 TCP Stream。

每个 Mapping 的本地 UDP Listener 根据来源地址（源 IP + 源端口）建立独立 Association：

1. 本地 Agent 收到一个 UDP 数据报。
2. 根据来源地址查找或创建 Association。
3. 通过控制 Stream 建立 Association，并分配不可猜测的 Association ID。
4. 通过 Datagram Channel 发送原始 UDP payload。
5. 目标 Agent 按 Mapping 固定目标地址发送 UDP 数据报。
6. 目标返回的数据报经同一 Association 返回本地来源地址。
7. 来源空闲、会话撤销或资源达到上限时回收 Association。

要求：

- 保留 Datagram 边界，不合并多个包。
- 允许丢包、乱序；不得依赖可靠字节流语义。
- 支持最大 UDP payload 限制、分片/重组和重组内存预算。
- 每个来源 Association 有数量、速率、空闲超时和总内存上限。
- 一个 UDP 来源的失败不影响同 Mapping 的其他来源。
- 关联必须绑定发起设备、目标设备、Mapping ID、Lease 和协议，不能跨会话复用。
- RDP UDP 使用原生 Datagram；不允许退化为可靠 TCP Stream，以免产生 Head-of-Line Blocking。

### 5.3 P2P 直连

P2P 是 P0 能力，不是后续可选增强。

优先复用现有通用 P2P 的基础设施：

- 候选发现和候选优先级。
- 同一 UDP Socket 上的候选发现、打洞和 QUIC，避免 NAT 映射被更换。
- UDP Hole Punching。
- TLS 1.3 和 Peer 证书/指纹验证。
- QUIC Stream 复用和原生 Datagram。
- Lease、Keepalive、网络变化检测和失败冷却。

需要注意：`agent/p2p.Manager` 当前包含代理 Client/Exit、代理 Grant 和 ACL 语义，不能不经拆分就直接作为所有端口转发协议的 Manager。应抽取底层 Peer Session 生命周期和传输接口，由 Proxy、RDP、PortForward 分别实现自己的业务授权和 Handler。

目标方向：

~~~text
Peer Session Manager
   |
   +-- Candidate / Rendezvous / Punch
   +-- Authenticated Direct QUIC
   +-- Multiplexed Stream
   +-- Native Datagram
   +-- Lease / Keepalive / Reconnect
           |
           +-- Proxy Handler
           +-- RDP Handler
           +-- PortForward Handler
~~~

### 5.4 Relay 兜底

TCP：

- 复用 Client Agent 与 Server、Server 与 Target Agent 的现有 TunnelSession。
- Server 校验两端会话与权限后建立对应 Stream。
- Server 负责路由和审计，不允许 Client 指定未经授权的目标设备。

UDP：

- P0 复用现有 QUIC Datagram Channel。
- Server 必须同时验证两段隧道的 Datagram 协商状态和对端能力。
- 两段隧道中任一段不支持 Datagram，则该 Relay UDP Association 不可建立。
- 不把 RDP UDP 退化为 `UDPStreamConn`。
- P1 可增加独立原生 UDP Relay 数据面，使 Relay 控制连接退化到 TLS/TCP 时仍有 UDP 数据通道；该能力需要单独的认证、加密、抗重放、流量限制和 DDoS 防护设计。

### 5.5 路径选择和竞速

TCP、UDP 分别选择路径，不共享“已连接”结论。

建议策略：

1. 首先启动 P2P。
2. P2P 明确失败时立即启动 Relay。
3. P2P 在可配置的短暂优先窗口内仍未成功时，启动 Relay 竞速，默认可参考当前 RDP 约 300 ms 的策略。
4. 第一个完成认证和业务 Open 握手的路径获胜。
5. 关闭失败路径和竞速输家，不能留下后台 Punch、Socket 或 QUIC Session。
6. 将获胜路径、候选类型、RTT、Fallback 原因和重试次数写入状态。
7. 网络变化、Lease 过期或活动路径断开时，新的连接/Association 重新选择路径。

P0 不要求对已建立的 TCP 连接做无损跨路径迁移。已有连接断开时按应用协议重新连接；P1 再评估活动会话迁移与 UDP Association 恢复。

路径状态示例：

| 协议 | 实际路径 | 状态 |
|---|---|---|
| TCP | P2P QUIC Stream | 已连接 |
| UDP | Relay QUIC Datagram | 已连接 |
| TCP | Relay TLS Stream | 已连接 |
| UDP | 无原生 Datagram 路径 | 不可用/降级 |

---

## 6. RDP 迁移设计

### 6.1 迁移目标

RDP 不应长期维护一套独立的端口监听、路径选择、P2P/Relay 竞速、审计和重连逻辑。

P0 将 RDP 视为一个特殊的固定 Mapping：

~~~text
Mapping:
  remoteDeviceId = 已授权 RDP 目标设备
  targetAddress  = 127.0.0.1
  targetPort     = 3389
  protocol       = TCP + UDP
  localAddress   = 127.0.0.1
  localPort      = 按设备稳定分配
~~~

### 6.2 保留现有用户体验

- 继续使用设备名称和现有 hosts alias 行为。
- 同一目标设备的本地监听端口保持稳定。
- 继续支持已连接设备再次唤起本地 RDP 客户端。
- RDP 连接仍然按目标设备独立管理，不允许连接另一设备时误关掉前一个设备。
- 保留当前连接审计、路径显示和 UDP 状态。
- RDP 的目标端口由目标 Agent 固定为其配置的本地 RDP 服务地址，不接受客户端任意指定。

### 6.3 P0 路径要求

| 场景 | TCP | UDP |
|---|---|---|
| P2P QUIC 可用 | P2P Stream | P2P Datagram |
| P2P 不可用，Relay QUIC Datagram 可用 | Relay Stream | Relay Datagram |
| P2P 不可用，Relay 仅 TLS/TCP | Relay Stream | 明确显示不可用，RDP 继续使用 TCP |
| 网络切换或 P2P Lease 失效 | 新连接重新选择路径 | 重建 Datagram Association；失败时使用可用 Relay Datagram 或报告不可用 |
| 旧 Agent 不支持新协议 | 兼容旧 RDP 路径 | 兼容旧路径已有能力，不允许静默改变 UDP 语义 |

P0 的“支持 RDP UDP”是指新架构提供真实 UDP Datagram 通路，并在 P2P 或 Relay QUIC Datagram 可用时启用。它不代表在所有 UDP 被封锁、只能使用 TCP Relay 的网络中都能强行保证 UDP。若要求最后一种场景也支持 UDP，应将 P1 独立 UDP Relay 提前为发布阻断项。

### 6.4 迁移策略

不要一次性删除现有 `agent/rdp/p2p.Manager`。

1. 定义统一 `PortForwardTransport` 接口，支持 TCP Stream、UDP Association、路径信息和 Close。
2. 先把现有 RDP Relay TCP/UDP Dialer 包装成适配器。
3. 把现有 RDP 专用 P2P TCP/UDP Dialer 包装成适配器。
4. RDP Controller 改为调用统一 PortForwardManager，保持本地端口、hosts、UI 和审计兼容。
5. 通用 PortForward 使用认证 QUIC Peer Session。
6. 对比现有 RDP 专用 P2P 与通用 QUIC Peer Session 的真实网络表现和能力后，再迁移 RDP P2P，最后删除重复实现。

这样可以先统一管理和路径选择，不在第一步就同时重写穿透协议。

---

## 7. 协议与兼容性

### 7.1 新增协议能力

建议新增独立能力标识，例如：

- `port_forward_v1`
- `port_forward_tcp_v1`
- `port_forward_udp_datagram_v1`

具体名称在实现前以现有 Capability 命名规范为准。

建议新增的逻辑消息类型：

- OpenPortForwardTCP
- OpenPortForwardUDP
- PortForwardOpenResponse
- PortForwardAssociationClose
- PortForwardSessionClose

消息必须绑定：

- Protocol Version
- Mapping ID
- Client Device ID
- Target Device ID
- Lease/Session ID
- Association ID（UDP）
- 目标策略版本/授权引用
- Request ID 和超时

不得将任意目标地址直接当作 Server 已授权的事实。Server 的授权结果和目标 Agent 的 ACL 检查必须同时成立。

### 7.2 版本兼容

- 通过 Capability 协商新功能。
- 新旧 Agent 混合部署时，未声明新能力的目标设备不得接收新协议请求。
- RDP 保留现有 FrameTypeOpenRDP / FrameTypeOpenRDPUDP 兼容期。
- 新协议失败时，只能回退到已授权的旧协议实现；不得把安全认证失败当作“协议不兼容”并降低认证级别。
- 新增的 UDP Datagram 必须确认两端和底层传输都支持，不只检查一端的配置位。

---

## 8. 安全设计

端口转发本质上允许一台设备代表另一台设备访问网络，因此必须防止它演变为开放代理或 SSRF 跳板。

### 8.1 设备与映射授权

- 只有已审批设备可以创建或接受端口转发。
- Server 必须验证 Client 对 Target Device 的访问权限。
- Target Agent 必须再次验证 Lease、Mapping ID、设备身份、协议和目标策略。
- Lease 使用高熵随机 Token，短期有效，定期续租，可随设备撤销、离线或授权变化立即失效。
- Token、证书私钥和认证材料不得写入普通日志。
- 映射并发数、连接创建速率、UDP Association 数和总流量均需有上限。

### 8.2 目标地址 ACL

默认只允许远程 Agent 本机的 loopback 服务。

若用户启用“访问远程局域网”：

- 必须配置允许的 CIDR 和端口范围。
- 默认拒绝未授权的公网目标、任意内网扫描、组播、广播和未指定地址。
- 对 IPv4/IPv6 分别进行地址分类和策略校验。
- 默认拒绝链路本地地址、云元数据地址和其他敏感保留地址；如有合法运维需求，必须显式配置例外。
- 域名由目标 Agent 解析，所有解析结果逐个执行 ACL；拨号使用通过校验的地址，防止 DNS Rebinding。
- 每个连接记录实际目标 IP、端口、ACL 结果和拨号结果。

### 8.3 本地监听安全

- 默认绑定 `127.0.0.1` 或 `::1`。
- 绑定 `0.0.0.0` 或 `::` 必须显式开启、显示风险提示并要求更高权限。
- 不允许无提示地把 SSH、数据库或管理面板暴露到本地局域网。
- 本地端口冲突时返回明确错误，不自动换成用户不可预期的端口；如允许自动分配，必须把实际端口显示给用户。

### 8.4 加密与 Relay 可见性

- P2P 使用已认证的 TLS 1.3 Peer Session 和指纹校验。
- Relay 路径的两段 TLS 只保证 Client-Agent 到 Server、Server 到 Target-Agent 的链路加密；它本身不等于端到端加密。
- 现有 E2EE 设计尚未覆盖 RDP。通用端口转发可能承载 SSH、数据库密码和管理 API，生产发布前应将 TCP Stream E2EE 和 UDP Datagram AEAD 纳入安全验收。
- E2EE 应绑定 Client/Target 身份、Mapping、协议、Lease 和 Association，具备认证、抗重放、密钥轮换和降级保护。
- 若首期未完成 Relay 路径 E2EE，必须明确标注 Server 可接触 Relay 转发的应用层 payload，不得宣称 Relay 对数据内容不可见。

---

## 9. UI 与状态设计

建议在设备详情页增加“端口转发”，不要把配置散落到系统代理或 DNS 设置中。

### 9.1 映射列表

列表至少展示：

- 名称
- 本地监听地址/端口
- 远程目标设备
- 目标地址/端口
- TCP/UDP
- 启用状态
- TCP 当前路径
- UDP 当前路径
- 活动连接数、UDP Association 数
- 上下行流量
- 最近错误

TCP 和 UDP 路径必须分别显示，避免 TCP 已连接就误报 UDP 也已可用。

### 9.2 新建/编辑映射

配置步骤：

1. 选择已授权且在线的远程 Agent。
2. 填写名称。
3. 选择 TCP、UDP 或 TCP+UDP。
4. 填写本地监听地址和端口（默认 loopback）。
5. 填写远程目标地址和端口（默认 127.0.0.1）。
6. 如目标不属于远程 Agent loopback，要求选择/配置允许的网络范围。
7. 保存后显示映射启动结果和实际路径。

### 9.3 运行状态

建议状态枚举：

- STOPPED
- STARTING
- LISTENING
- CONNECTING_P2P
- CONNECTING_RELAY
- ACTIVE_P2P
- ACTIVE_RELAY
- DEGRADED
- ERROR

UDP 另显示 Datagram 能力、Association 数、最近一次重建时间和错误原因。

---

## 10. 开发阶段

### P0：通用 TCP/UDP + P2P + RDP 接入

**P0 是可用的第一版，不是只实现 TCP 的骨架。**

- [ ] 新建 `agent/portforward`，定义 Mapping、Manager、Session 和生命周期。
- [ ] 定义 TCP Stream、UDP Datagram、Transport Path 的统一接口。
- [ ] 复用现有 Relay TunnelStream 和 QUIC Datagram，不重复造传输层。
- [ ] 抽取/复用认证 P2P Peer Session，支持 QUIC Stream + Datagram。
- [ ] P2P 优先、Relay 有界竞速、输家取消和资源回收。
- [ ] TCP 固定端口映射、多并发、半关闭、Deadline 和取消。
- [ ] UDP 固定端口映射、来源隔离、Association、分片/重组、超时和容量限制。
- [ ] Relay UDP 使用现有 QUIC 原生 Datagram；不使用可靠 TCP 作为 RDP UDP 的隐式后备。
- [ ] Server 控制面授权、Lease、候选交换、撤销和审计。
- [ ] Target Agent 执行目标 ACL、DNS 解析和实际 Dial。
- [ ] 本地 Agent UI 支持创建、编辑、启停和删除映射。
- [ ] RDP TCP/UDP 接入统一 Manager，保留旧协议适配器和旧 Agent 兼容。
- [ ] RDP 路径、UDP 状态、错误原因和流量统计继续可见。
- [ ] Relay 路径 E2EE/UDP AEAD 完成安全评审；未完成时不得宣称 Relay 不可见 payload。

### P1：增强 Relay UDP 与局域网访问

- [ ] 独立原生 UDP Relay 数据面，覆盖 Relay 仅能建立 TLS/TCP 的环境。
- [ ] 更完善的局域网网段、域名、IPv6 和 DNS Rebinding 策略。
- [ ] P2P 网络变化快速恢复、失败冷却和路径质量自适应。
- [ ] 将 RDP 专用 P2P 底层迁移到通用认证 Peer Session，删除重复打洞/UDP 数据实现。
- [ ] 对活动 UDP Association 做更完善的恢复；评估 TCP 活动连接迁移边界。
- [ ] Server Web 统一管理授权策略和审计。

### P2：扩展访问能力

- [ ] SOCKS5 动态代理。
- [ ] 反向端口转发。
- [ ] 映射模板和批量配置。
- [ ] 更细的流量配额、QoS 和访问时间策略。

---

## 11. 测试与验收标准

### 11.1 TCP

- 本机 HTTP 服务映射到远程 Agent 的 loopback HTTP 服务，浏览器可访问。
- SSH、数据库客户端和自定义 TCP 服务可连接。
- 同一映射多个并发连接互不影响。
- 大数据、长连接、半关闭、Deadline、远端进程退出和 Agent 重启行为正确。
- 本地端口冲突、目标端口关闭、授权撤销均返回明确错误。

### 11.2 UDP

- UDP Echo 双向数据报正确，数据报边界保持不变。
- 64 B、1200 B、MTU 附近及需要分片的较大数据报测试。
- 丢包、乱序、重复包、超时、NAT 重绑定和 Association 重建测试。
- 多个本地来源端口同时访问同一个远程 UDP 服务，返回数据不能串流。
- 达到 Association/内存上限时按策略拒绝并回收资源。
- RDP UDP 不允许落到可靠 TCP Stream。

### 11.3 P2P 与 Relay 路径矩阵

| 场景 | 预期 |
|---|---|
| 同一局域网 | 优先 P2P，记录 LAN 候选和实际路径 |
| 普通异网 NAT | 尝试 P2P 打洞，成功后数据不经过 Server |
| 对称 NAT / 打洞失败 | 自动回退 Relay |
| UDP 被阻断 | TCP 可用；UDP 报告不可用，不假报成功 |
| P2P 建立慢 | 有界启动 Relay 竞速，不能无限等待 |
| P2P 建立后断线 | 后续新连接重新选择 Relay/P2P |
| Relay QUIC Datagram 可用 | TCP/UDP 都可经 Relay，UDP 使用原生 Datagram |
| Relay 只有 TLS/TCP | TCP 可用；P0 UDP 不可用，P1 独立 UDP Relay 完成后才可覆盖 |
| 一端旧版本 | 新能力关闭，RDP 旧路径保持兼容 |

### 11.4 RDP 真实验收

- Windows mstsc 经本地稳定端口连接远程 RDP。
- 分别记录 RDP TCP 与 UDP 的路径，确认二者可以独立选择 P2P/Relay。
- P2P TCP + P2P UDP、P2P TCP + Relay UDP、Relay TCP + Relay UDP 均验证。
- UDP 不可用时 TCP RDP 仍能正常使用，UI 显示原因。
- 连续输入、拖动窗口、视频播放和长时间空闲后恢复。
- 网络切换、Agent 重启、Lease 过期和权限撤销时无 Socket/Goroutine 泄漏。
- 与当前 RDP 实现进行同设备、同网络条件下的延迟、吞吐、丢包和 CPU 对比。

### 11.5 安全验收

- 未授权设备、过期 Lease、错误 Token、错误 Mapping ID 和伪造 Association 均被拒绝。
- 未允许的内网 IP、端口、组播、广播和元数据地址不能访问。
- DNS Rebinding 和多 A/AAAA 解析结果逐一校验。
- 本地默认仅监听 loopback；未显式授权时不能监听公网/局域网接口。
- 日志不包含 Token、密钥和应用层敏感 payload。
- E2EE 开启后篡改、重放、乱序和降级攻击测试失败关闭。
- P2P/Relay 竞速输家、取消路径和设备撤销后所有资源被释放。

### 11.6 建议自动化测试命令

实现后按实际包名补齐：

~~~sh
go test -race ./internal/tunnel ./internal/p2p ./agent/portforward ./agent/rdp ./agent/rdp/p2p ./server/gateway ./server/rdp
go test ./agent/app -run 'RDP|PortForward' -count=1
go vet ./...
~~~

除单元测试外，必须有 Client Agent + Server + Target Agent 的真实三端集成测试；不能只用两个本地 Mock 证明 P2P/Relay 可用。

---

## 12. 风险与实现注意事项

1. **不要把 UDP over TCP 当成 UDP 支持。** RDP 对时延和 Head-of-Line Blocking 敏感。
2. **不要把 P2P 与 Routing DIRECT 混淆。** P2P 是到指定远程 Agent 的传输路径，不能改变目标设备或出口选择。
3. **不要让 Server 信任 Client 提供的任意目标地址。** 最终 Dial 必须由目标 Agent 执行并校验。
4. **不要并发读取同一个 UDP Socket。** Rendezvous、打洞、QUIC 和数据报分发必须有明确的 Socket 所有权和单一读取分发机制。
5. **不要把 RDP 的 P2P TCP 与 UDP 状态合并。** 二者可能走不同路径，生命周期也不同。
6. **不要在 P0 一次性重写所有 P2P。** 先统一接口和管理，再逐步收敛底层实现。
7. **不要在 Relay 仅 TLS/TCP 时伪报 UDP Active。** 明确显示 UDP 能力缺失和原因。
8. **不要忽略旧版本兼容。** 新能力需要 Capability 协商、协议版本和明确的回退边界。
9. **不要把 per-hop TLS 说成 E2EE。** 端口转发可能承载凭据，Relay 路径必须明确数据可见性。
10. **不要让 Mapping 变成开放代理。** 必须有目标范围、端口范围、连接配额、审计和撤销能力。

---

## 13. 实施顺序建议

推荐按以下顺序推进：

1. 先完成当前代码路径和接口审计，确认已有 Datagram、P2P QUIC、RDP 专用 Punch 的复用边界。
2. 定义统一协议和 `PortForwardTransport` 接口，不先改 UI。
3. 实现通用 TCP + UDP 的本地 Listener 和目标 Agent Handler。
4. 接入 Relay Stream + Relay QUIC Datagram，完成三端集成测试。
5. 接入认证 P2P Peer Session，验证同 LAN、异网 NAT 和 Relay 兜底。
6. 将 RDP 作为固定 TCP+UDP Mapping 接入统一 Manager，旧实现先保留适配器。
7. 增加 UI、路径状态、审计、权限配置和安全验收。
8. 通过 Windows RDP 真实验收后，再删除重复的 RDP 专用路径代码。
9. P1 再补独立 UDP Relay，覆盖 Relay 只能使用 TLS/TCP 的网络。

**最终目标：RelayProxy 只有一套统一的端口转发管理和路径选择体系；TCP 使用可靠 Stream，UDP 使用原生 Datagram；P2P 直连优先，Relay 自动兜底；RDP、SSH、Web、数据库和后续服务都复用这套能力。**
