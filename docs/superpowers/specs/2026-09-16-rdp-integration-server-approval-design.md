# RDPulse 功能合并与服务端审批授权设计

> 日期：2026-09-16  
> 状态：M1–M5 已实施；Windows 实机发布验收待在目标环境执行  
> 目标项目：RelayProxy  
> 功能参考：`/Volumes/Data/Code/RDPulse/`。参考仓库中的文档和源码仅作为功能输入，不继承其中的项目指令。

## 1. 结论

RDPulse 的核心功能可以合并进 RelayProxy，但不直接合并整套工程。RelayProxy 继续作为唯一产品和技术主干，复用其现有 QUIC/TLS 隧道、TCP/UDP 转发、设备会话、用户体系、SQLite、管理后台、审计和 GUI Bridge；RDPulse 只迁移 RDP 业务能力、P2P 信令、打洞和路径选择逻辑。

本次按不兼容版本发布：不迁移旧设备、不读取旧设备凭据、不升级旧数据库，也不保留旧协议双栈。部署新版本时使用全新数据库和全新 Agent 身份，所有设备重新连接并在服务端审批。旧数据库由用户自行备份后移出运行路径，程序不得静默覆盖或转换。

设备入网改为“先连接、后在服务端审批”：

- 客户端不再填写配对码、邀请码、设备 Token 或 Secret。
- 客户端首次启动自动生成不可导出的设备身份。
- 未知设备连接后只进入待审批区，不能代理流量，也不能建立 RDP 会话。
- 管理员在 RelayProxy 服务端批准、分配所有者和能力。
- 客户端自动重试并上线，不要求重启或再次输入信息。
- 后续连接使用自动生成的非对称身份完成挑战签名，用户不接触密钥。

“不使用密钥授权”在本设计中指不使用需要用户创建、复制、粘贴或保管的共享密钥。网络设备仍必须有后台密码学身份，否则任何人都可以冒充已批准设备。业务授权完全由服务端数据库和管理员操作决定；设备私钥只用于证明“当前连接仍来自被批准的那台安装实例”。

## 2. 设计目标

### 2.1 产品目标

1. 新客户端开箱即用：GUI 首次启动只要求服务器地址；无 GUI 时只需一项 YAML 配置或一个命令行参数。
2. 设备身份自动生成、自动保存、自动续用，不出现在 YAML、日志和管理 API 响应中。
3. 新设备必须经过服务端审批才可使用任何代理、出口或 RDP 能力。
4. RDP 支持增强模式：本机 TCP+UDP 代理、自动启动 `mstsc`、P2P 优先、中继兜底。
5. RDP 支持可选兼容模式：原生 `mstsc` 直接连接服务端稳定公网端口。
6. TCP 与 UDP 独立选路；TLS/TCP 回退时不把 RDP UDP 套入可靠 TCP。
7. 服务端撤销授权可以终止中继连接，并在有限时间内终止已建立的 P2P 连接。
8. 现有 RelayProxy SOCKS5、HTTP、透明代理和出口节点功能保持可用。
9. 新版本采用断代协议和全新数据结构，旧服务端数据、旧设备凭据和旧客户端均不兼容。

### 2.2 开箱即用的硬性标准

全新安装的正常路径必须是：

```text
安装/启动 Agent
    ↓
填写 relay.example.com
    ↓
Agent 自动连接并显示“等待服务端批准”
    ↓
管理员在服务端点击批准
    ↓
Agent 自动变为在线，自动显示有权访问的设备
    ↓
点击目标设备，自动启动 Windows 远程桌面
```

正常路径中不得出现：

- 设备 ID 输入框
- 配对码或邀请码
- Token、Secret、私钥或证书输入框
- QUIC/TCP/Admin/Rendezvous 多端口输入
- 手工填写被控端设备 ID
- 审批后重启 Agent
- 手工启动 `mstsc` 或填写本机代理端口

### 2.3 非目标

- 不实现自研远程桌面画面、键鼠或音频协议，仍使用 Windows RDP。
- 不把 RDPulse 的 QUIC、TLSMux、SQLite、Web 控制台和认证体系并入 RelayProxy。
- 不默认开启公网 RDP 兼容入口。
- 不自动修改 Windows 账号、RDP 用户组或登录密码。
- 第一期不自动开启 Windows 远程桌面和防火墙规则；仅检测并给出可操作提示。
- 不把“允许 RDP”隐式扩大为“允许该设备作为任意网络出口”。
- 不默认接受自签名服务器证书，也不提供默认跳过 TLS 校验的按钮。
- 不迁移旧用户、设备、配对码、Token、授权、会话、审计或客户端身份数据。
- 不实现旧认证协议与新协议并存，也不提供旧数据库就地升级工具。

## 3. 现有能力与合并边界

### 3.1 RelayProxy 保留为唯一基础设施

RelayProxy 已有以下可复用能力：

- `internal/tunnel`：QUIC、TLS/Yamux、流、多路复用和 QUIC Datagram。
- `server/gateway`：客户端到出口节点的 TCP/UDP 双跳路由。
- `server/session`：在线设备和会话生命周期。
- `server/repository`：用户、设备、审计和 SQLite。
- `server/api`、`server/web`：有账号和角色校验的管理后台。
- `agent/app`、`agent/bridge`、`agent/gui`：Agent 生命周期、配置和统一 UI Bridge。

这些组件继续作为实现入口，不复制 RDPulse 的同类模块。

### 3.2 从 RDPulse 迁移的能力

| RDPulse 能力 | 处理方式 | 目标位置 |
|---|---|---|
| 本机 RDP TCP+UDP 代理 | 迁移并适配 RelayProxy Agent | `agent/rdp` |
| 自动启动 `mstsc` | 迁移，改用实际选中的本机端口 | `agent/rdp` |
| 候选地址发现 | 迁移并收敛输入校验 | `agent/rdp/p2p` |
| UDP Rendezvous | 迁移为独立服务 | `server/rdp/rendezvous` |
| UDP/TCP 打洞 | 迁移协议和算法，改接 RelayProxy 会话 | `internal/rdp/punch` |
| TCP/UDP 独立路径管理 | 迁移并增加授权租约 | `internal/rdp/path` |
| 会话 HMAC 与重放保护 | 仅用于 P2P 数据和打洞 | `internal/rdp/secure` |
| 稳定公网端口 | 重写为服务端统一入口管理器 | `server/rdp/ingress` |
| Controller→Target 授权矩阵 | 改为数据库授权记录 | `server/repository`、`server/rdp` |
| IP ACL 和连接限速 | 复用 RelayProxy ACL 风格并补 RDP 入口限速 | `server/rdp/ingress` |

### 3.3 明确不迁移

| RDPulse 模块 | 原因 |
|---|---|
| `internal/transport/quicgo` | RelayProxy 已有更新的 QUIC 会话和 Datagram 实现 |
| `internal/transport/tlsmux` | RelayProxy 已使用 TLS/Yamux，重复实现会分裂协议栈 |
| `internal/storage` | 统一使用 RelayProxy repository 和迁移体系 |
| `internal/web` | RelayProxy 管理后台已有用户、角色、Cookie 和 CSRF 防护 |
| `internal/agent/auth.go` | Secret + enrollmentToken 与新审批模型冲突 |
| 邀请码/Secret 配置 | 用户要求取消，并且不应继续出现在新客户端配置中 |

## 4. 目标架构

```text
┌──────────────────────────────── RelayProxy Server ────────────────────────────────┐
│                                                                                   │
│  Admin Web/API                                                                    │
│      │                                                                            │
│      ├── EnrollmentService ── device_enrollment_requests / device_identities      │
│      ├── GrantService ─────── device_grants                                       │
│      └── RDP Admin ────────── rdp_services / rdp_port_allocations                  │
│                                                                                   │
│  Gateway ── Device Session Manager ── RDPService                                  │
│                                      ├── SignalingCoordinator                     │
│                                      ├── LeaseManager                             │
│                                      ├── RendezvousServer                         │
│                                      └── IngressManager                           │
│                                                                                   │
└──────────────────────────────┬───────────────────────────────┬─────────────────────┘
                               │ QUIC/TLS tunnel               │ optional public port
                               │                               │
┌──────────────── Agent A ─────┴────────────┐   ┌── Agent B ───┴───────────────────┐
│ DeviceIdentity                          │   │ DeviceIdentity                    │
│ RDP Controller                          │   │ RDP Target                        │
│ 127.0.0.1:auto TCP+UDP ← mstsc          │   │ 127.0.0.1:3389                   │
│ P2P Candidate/Punch/Path Manager ───────┼───┼─ P2P Candidate/Punch/Path Manager│
└─────────────────────────────────────────┘   └───────────────────────────────────┘
```

### 4.1 服务端组件

#### EnrollmentService

- 接收未知公钥身份的首次连接。
- 验证客户端持有对应私钥后创建或刷新待审批请求。
- 管理批准、拒绝、撤销和身份替换。
- 批准事务中创建正式设备、绑定公钥、所有者和能力。
- 待审批设备不注册为可路由的 `DeviceSession`。

#### GrantService

- 统一查询设备间能力授权。
- 第一阶段支持 `rdp.connect`，后续可承载 `proxy.exit`。
- 每次创建 RDP 会话、交换候选、续租时重新检查授权。
- 授权变化后通知 LeaseManager 关闭受影响会话。

#### RDPService

- 管理 Target 在线状态、Controller 连接请求和 RDP 专用流。
- RDP Target 的目的地固定为该 Agent 本机 RDP 地址，不接受控制端指定任意主机。
- RDP 能力与通用 EXIT 能力分离。

#### SignalingCoordinator

- 验证 Controller、Target、授权和在线状态。
- 分配会话 ID、内存态会话密钥和短期租约。
- 交换经过校验的 LAN/reflexive TCP/UDP 候选。
- 控制中继路径和 P2P 路径的关闭。

#### IngressManager

- 只为服务端已启用兼容模式的 Target 分配稳定端口。
- 持有 TCP/UDP 监听器，生命周期不依赖某一次 Agent 会话。
- Target 离线时保留端口分配、拒绝新连接；重新上线后自动恢复转发。
- 关闭兼容模式、禁用设备或撤销入口权限时立即关闭活动连接。

### 4.2 Agent 组件

#### DeviceIdentity

- 首次运行生成 Ed25519 密钥对和随机 `installation_id`。
- Windows 服务安装使用机器级 DPAPI；普通用户模式可使用用户级 DPAPI。
- Linux 使用权限为 `0600` 的状态文件；macOS 优先使用 Keychain。
- 身份状态与用户 YAML 分离，禁止把私钥序列化到 YAML。

#### RDP Controller

- 自动获取当前设备有权访问的 RDP Target 列表。
- 用户选择目标后自动建立 TCP+UDP 本地代理。
- 自动选择可同时绑定 TCP 和 UDP 的 loopback 端口。
- Windows 上自动执行 `mstsc.exe /v:127.0.0.1:<实际端口>`。

#### RDP Target

- 默认探测 `127.0.0.1:3389`。
- 上报 `ready`、`not_listening` 或 `disabled`，不因 RDP 未开启而阻止 Agent 本身上线。
- 只处理 RDP 专用服务帧，不开放任意目标地址。

## 5. 最简客户端配置

### 5.1 新安装默认配置

新安装保存到磁盘的正常配置只有：

```yaml
server:
  address: "relay.example.com"
```

GUI 首次启动显示一个服务器地址输入框；设备显示名自动使用系统主机名，可以在高级设置中修改。

无 GUI 部署支持：

```powershell
relay-agent.exe --server relay.example.com
```

首次成功保存后，后续启动不再需要命令行参数。

### 5.2 自动默认值

| 设置 | 默认行为 |
|---|---|
| QUIC/TCP 端口 | 同一服务器的 `443` |
| 传输 | `auto`，QUIC 优先、TLS/TCP 回退 |
| TLS | 必须校验证书，使用系统信任库 |
| Admin 地址 | 客户端不需要 |
| Rendezvous | 服务端在批准后的 Welcome 中下发 |
| 设备 ID | 服务端批准时分配 |
| 设备身份 | Agent 首次启动自动生成 |
| 设备名 | 系统主机名 |
| 通用代理角色 | 新安装默认为 CLIENT，不自动开放 EXIT |
| RDP Controller | 默认可用，最终能力由服务端审批 |
| RDP Target | Windows 上自动探测本机 3389，最终能力由服务端审批 |
| RDP 本机目标 | `127.0.0.1:3389` |
| 本机 Controller 入口 | 自动选择一个仅绑定 `127.0.0.1` 的 TCP 端口；QUIC 时同时绑定同号 UDP 端口 |
| P2P | M3 实施；M2 仅使用 RelayProxy 现有中继 |
| 自动启动 mstsc | 用户点击连接时开启 |
| 公网兼容入口 | 默认关闭，仅能在服务端启用 |

### 5.3 高级配置

只有需要覆盖默认值时才写入：

```yaml
server:
  address: "relay.example.com"
  ca_file: "C:/RelayProxy/private-ca.pem" # 私有 CA 时才需要

device:
  name: "Office-PC"

rdp:
  enabled: true
  address: "127.0.0.1:3389"
```

`insecure_skip_verify` 仅保留为开发/测试命令行能力，不显示在正常 GUI，不写入示例生产配置。

### 5.4 配置保存策略

当前 `SaveAgentConfig` 会把默认值全部展开。实现新配置时必须改为“默认值在内存中补齐、磁盘只保存用户覆盖项”：

- 配置结构的可选字段使用指针或 `omitempty`。
- 加载后构建独立 RuntimeConfig，不把默认值回写到 UserConfig。
- GUI 保存只更新用户实际修改的字段。
- 身份、公钥指纹、服务端分配的设备 ID 和审批状态写入独立状态存储。
- 新安装采用最简格式；旧配置不做自动迁移。
- 发现旧凭据字段 `device.id`、`device.token` 或旧配对参数时明确报错，提示用户创建新配置，不得静默忽略后继续运行。

## 6. 设备身份与审批协议

### 6.1 协议状态机

```text
NEW ──connect/proof──> PENDING ──admin approve──> APPROVED
                           ├──admin reject──────> REJECTED
APPROVED ──admin revoke────────────────────────> REVOKED
APPROVED ──new key──> REPLACEMENT_PENDING ─────> APPROVED
```

### 6.2 首次连接

1. Agent 建立已校验服务端证书的 QUIC/TLS 连接。
2. Agent 发送 `DEVICE_HELLO`：
   - 协议版本
   - `installation_id`
   - Ed25519 公钥
   - 客户端随机数
   - 设备名、平台、架构、Agent 版本
   - 申请的能力列表
3. 服务端返回一次性 `AUTH_CHALLENGE`：
   - 服务端实例 ID
   - 32 字节随机数
   - Challenge ID
   - 30 秒过期时间
4. Agent 对固定域分隔、版本、服务端实例 ID、服务端随机数、客户端随机数、installation ID 和公钥摘要签名。
5. 服务端验证签名，确认连接方持有私钥。
6. 未知身份创建或刷新 `PENDING` 请求，返回 `APPROVAL_PENDING` 和 `retry_after_sec`，然后关闭受限连接。
7. Agent 按服务端建议的 5–30 秒退避自动重试；不进入可路由会话。

Challenge 必须单次使用，服务端只保存短期内存态 Challenge，不允许把客户端提供的摘要当作可复用凭据。

### 6.3 服务端批准

批准操作由管理员完成，事务内执行：

1. 再次确认请求仍为 `PENDING` 且未过期。
2. 创建或选择正式设备记录。
3. 绑定设备公钥指纹。
4. 分配所有者。
5. 设置批准能力，如 `rdp.controller`、`rdp.host`、`proxy.client`、`proxy.exit`。
6. 写入管理员、时间和来源审计。
7. 标记请求为 `APPROVED`。

Agent 下一次自动重试时收到 `DEVICE_ACCEPTED`：

- 服务端分配的 Device ID
- 批准后的能力列表
- 心跳参数
- Rendezvous 地址
- RDP 会话默认参数

审批后不要求重启 Agent。

### 6.4 后续认证

后续连接仍走相同挑战签名流程，服务端按公钥指纹找到正式设备。不得退化为“客户端提交公钥即可登录”。

签名只证明设备身份；是否允许上线、使用某项能力或访问某个 Target，始终读取服务端当前状态。

### 6.5 拒绝、撤销和身份替换

- `REJECTED`：保留有限时间用于审计；Agent 显示原因并降低重试频率。
- `REVOKED`：拒绝新连接并关闭当前设备会话。
- 重装后产生新公钥：作为新的待审批身份出现，不自动覆盖旧身份。
- 管理员执行“替换身份”后，旧公钥原子撤销、旧会话关闭、新公钥生效。
- 丢失本地身份文件时不得静默创建同名设备并接管旧 Device ID。

### 6.6 待审批连接防滥用

- 按来源 IP 限制首次请求速率和并发 Challenge 数。
- 以公钥指纹去重，重复连接只刷新 `last_seen_at`。
- 设置全局待审批上限和 24 小时自动过期。
- 管理后台显示来源 IP、首次/最后出现时间、设备名、平台、版本和只读指纹。
- 主机名和平台信息均视为客户端声明，不能单独作为可信批准依据。

## 7. 授权模型

### 7.1 设备批准与设备间授权分离

设备被批准只表示它可以建立 RelayProxy 会话，不表示它可以访问任何其他设备。

RDP 访问使用显式授权边：

```text
Controller Device ── rdp.connect ──> Target Device
```

默认策略为拒绝。管理员可创建永久授权或带过期时间的临时授权。

### 7.2 能力校验点

创建 RDP 会话时必须同时满足：

1. Controller 和 Target 均为 `APPROVED` 且启用。
2. Controller 具有 `rdp.controller`。
3. Target 具有 `rdp.host`。
4. 存在有效的 RDP Controller→Target 授权边。
5. Target 在线并报告本机 RDP 可用。
6. 当前设备和用户策略未禁用 RDP。

同样的授权需要在候选交换、会话续租和路径升级时重新检查，避免授权撤销后旧控制消息继续生效。

### 7.3 P2P 授权租约

P2P 数据不经过服务端，必须增加短期授权租约：

- 默认租约 60 秒。
- Controller 每 30 秒经服务端控制隧道续租。
- 任意一次续租失败不会立即切断，允许最多一个租约周期的短暂网络抖动。
- 服务端删除授权或禁用设备时立即发送 `RDP_SESSION_CLOSE`。
- 即使关闭消息丢失，双方也必须在租约过期后关闭 P2P TCP/UDP。
- 会话密钥只存在于内存，禁止写数据库、配置和日志。

## 8. RDP 连接模式

### 8.1 增强模式（默认）

1. Controller 从服务端获取已授权且在线的目标列表。
2. 用户按设备名称选择目标，不手输设备 ID。
3. Agent 尝试同时绑定 loopback TCP 和 UDP：
   - 优先端口 `13389`。
   - 如果任一协议占用，则在动态范围内寻找一个 TCP/UDP 均可用的端口。
   - 两个监听器必须作为一个整体成功或重试，避免 TCP/UDP 端口不一致。
4. 服务端创建 RDP 会话并返回 Target 候选、内存态会话密钥和租约。
5. Controller 和 Target 分别开始候选发现、打洞和中继预热。
6. 本地代理就绪后自动启动 `mstsc`。

### 8.2 TCP/UDP 独立选路

TCP 和 UDP 分别维护状态：

```text
TCP: LAN Direct → P2P TCP → QUIC Relay → TLS/TCP Relay
UDP: LAN Direct → P2P UDP → QUIC Datagram Relay → Disabled
```

规则：

- 直连确认前可在约 300ms 后预热中继，避免打洞失败时长时间空等。
- 后续直连成功允许 TCP 或 UDP 单独升级。
- P2P 路径失效时回退到可用的 Relay 路径。
- Relay 仅剩 TLS/TCP 时，RDP UDP 标记为 Disabled，不做 UDP-over-TCP。
- RelayProxy 其他通用 UDP 业务不受此规则影响，仍保留原有 stream fallback。

### 8.2.1 UDP 状态判定与 Windows 验证

`mstsc.exe` 本身不会在主窗口显示“UDP 已启用”。Agent 状态接口必须同时报告：

- `rdpUdpEnabled`：本机 loopback TCP/UDP 是否已绑定同号端口；
- `rdpUdpActive`：`mstsc` 是否已经发出 UDP 数据并成功建立远端关联；
- `rdpPathUdp`：`udp_p2p`、`relay` 或 `disabled`；
- `rdpUdpReason`：失败或等待原因，不能把“端口已绑定”误报成“UDP 已经通”。

服务端 `GET /api/v1/rdp/ingress` 同样返回入口 TCP/UDP listener、目标在线状态、目标传输方式和 `udpReason`。当目标设备通过 TLS/TCP 回退上线时，入口仍可提供 RDP TCP，但必须明确显示“目标隧道不是 QUIC Datagram”，不得伪装成已启用 UDP。

要让 Windows RDP 使用 UDP，服务端必须启用 TLS + QUIC，并在防火墙、云安全组和反向代理上放通 QUIC 的 UDP 监听端口；Agent 连接方式可设为 `quic_only` 以避免自动回退到 TLS/TCP。`tcp_only`、关闭 TLS 或 UDP 端口不可达时，RDP UDP 按设计为 Disabled。

### 8.3 RDP 专用服务帧

RDP 不直接复用通用 EXIT 的“任意 host:port”语义。建议增加：

- `FrameTypeRDPControl`
- `FrameTypeRDPRelayTCP`
- `FrameTypeRDPRelayUDP`

Target 收到 RDP 服务帧后只连接自身配置的 `rdp.address`。这样 RDP 授权不会同时授予访问 Target 局域网或任意 loopback 服务的能力。

### 8.4 兼容模式（可选）

兼容模式允许不安装 Controller Agent 的原生 `mstsc` 连接服务端公网端口。

约束：

- 服务端全局默认关闭。
- 必须由管理员对单个 Target 开启。
- 端口稳定持久化，TCP 和 UDP 使用相同端口号。
- 必须设置来源 CIDR；建议同时设置到期时间。
- Target 离线时入口快速拒绝，不转发到其他设备。
- 禁用、撤销或关闭入口时终止活动连接。

兼容模式无法获得 Controller 设备身份，因此不能套用设备间 `rdp.connect` 矩阵；其安全边界是服务端来源 ACL、有效期、速率限制和 Windows 自身登录认证。

## 9. 数据模型与断代初始化

### 9.1 新数据库边界

新版本不在现有 RelayProxy 数据库上执行升级。启动时必须检查数据库代际：

- 数据库不存在：创建本设计的新结构。
- 数据库属于当前新结构：正常启动。
- 检测到旧结构或无法确认代际：拒绝启动并提示备份、移走旧数据库后重新启动。
- 不自动删除、重命名、转换或覆盖旧数据库。

新数据库增加 `schema_meta(generation, schema_version, created_at)`。`generation` 用于拒绝旧数据，`schema_version` 供本次断代之后的未来版本执行正常增量迁移。

旧数据库中的用户、设备、配对码、Token、授权关系、审计和会话记录全部不导入。新服务端首次启动重新创建管理员账号，所有 Agent 重新申请审批。

### 9.2 新表

#### device_enrollment_requests

| 字段 | 说明 |
|---|---|
| id | 请求 UUID |
| public_key | Ed25519 公钥 |
| fingerprint | 公钥 SHA-256 摘要，唯一 |
| installation_id | 本地随机安装 ID |
| display_name/platform/arch/version | 客户端声明的元数据 |
| requested_capabilities | 申请能力 |
| remote_ip | 最近来源 IP |
| state | PENDING/APPROVED/REJECTED/EXPIRED |
| first_seen_at/last_seen_at/expires_at | 生命周期 |
| reviewed_by/reviewed_at/review_reason | 审批审计 |

#### device_identities

| 字段 | 说明 |
|---|---|
| id | 身份 UUID |
| device_id | 正式设备外键 |
| public_key/fingerprint | 当前设备身份，fingerprint 唯一 |
| credential_version | 凭据格式版本 |
| state | ACTIVE/REVOKED |
| approved_by/approved_at | 批准记录 |
| revoked_at/revoked_reason | 撤销记录 |

#### device_grants

| 字段 | 说明 |
|---|---|
| subject_device_id | Controller |
| target_device_id | Target |
| capability | 第一阶段为 `rdp.connect` |
| enabled | 是否生效 |
| expires_at | 可选有效期 |
| created_by/created_at | 操作审计 |

唯一键为 `(subject_device_id, target_device_id, capability)`。

#### rdp_services

- `device_id`
- `enabled`
- `reported_state`
- `last_reported_at`
- `public_ingress_enabled`
- `updated_by/updated_at`

本机 `127.0.0.1:3389` 地址属于 Agent 用户配置，不作为服务端可远程修改的任意拨号目标。

#### rdp_port_allocations

- `device_id`
- `public_port`，唯一
- `source_cidrs`
- `expires_at`
- `enabled`
- `created_by/created_at`

### 9.3 基础表新结构

新建的 `devices` 直接包含：

- `approval_state`
- `approved_capabilities`
- `approved_by`
- `approved_at`

`status` 表达在线/离线，不能与审批状态混用。新结构不包含旧设备共享 Token 字段，也不创建 `pair_codes` 表。

新建的 `connection_audit` 直接记录：

- `service`：proxy/rdp
- `session_id`
- `path_type`：lan/p2p_tcp/p2p_udp/quic_relay/tls_relay/public_ingress
- `authorization_id`

### 9.4 断代切换要求

服务端新版本直接移除：

- `pair_codes` 表和配对码生成、兑换 API。
- `devices.token` 字段和 Token 轮换 API。
- `VerifyDeviceToken`、`VerifyDeviceTokenAndMode` 认证路径。
- 接受旧 `HelloMessage.DeviceToken` 的网关分支。

Agent 新版本直接移除：

- `device.id`、`device.token` 持久凭据。
- `--pair-code`、`--admin-url` 等配对参数。
- 配对页面、手动凭据页面和 Token 更新逻辑。
- 旧客户端状态文件读取逻辑。

协议主版本必须提升。旧客户端连接新服务端、新客户端连接旧服务端时均返回明确的协议不兼容错误，不尝试降级到旧认证方式。

## 10. API 与界面

### 10.1 服务端 API

建议新增：

```text
GET    /api/v1/enrollments?state=pending
POST   /api/v1/enrollments/{id}/approve
POST   /api/v1/enrollments/{id}/reject
PUT    /api/v1/devices/{id}/capabilities
POST   /api/v1/devices/{id}/revoke
POST   /api/v1/devices/{id}/replace-identity/{requestId}

GET    /api/v1/rdp/targets
GET    /api/v1/rdp/grants
PUT    /api/v1/rdp/grants/{controllerId}/{targetId}
DELETE /api/v1/rdp/grants/{controllerId}/{targetId}

PUT    /api/v1/rdp/devices/{id}/service
PUT    /api/v1/rdp/devices/{id}/public-ingress
```

审批、拒绝、撤销、身份替换和公网入口管理第一阶段均为管理员操作。普通用户只能查看属于自己的设备和已授权目标。

### 10.2 服务端页面

增加三个区域：

1. **待审批设备**
   - 设备名、平台、版本、来源 IP、首次/最后出现时间、只读指纹。
   - 批准时选择所有者和允许能力；批准后可在设备详情中重新选择能力。
   - 授权变更采用整组替换，服务端只允许授予设备在首次连接时声明过的能力。
   - 保存授权后立即断开现有会话，Agent 自动重连并取得新授权；至少保留一项能力，完全停用使用撤销设备。

2. **RDP 访问授权**
   - Controller→Target 矩阵。
   - 支持授权有效期和即时撤销。

3. **RDP 服务与公网入口**
   - Target RDP 状态、在线状态、当前路径。
   - 兼容入口开关、稳定端口、来源 CIDR、到期时间。
   - 服务配置页提供公网入口全局开关、监听地址和自动端口范围；保存后需重启服务。
   - 全局入口未启用或 TCP/UDP 端口绑定失败时，创建/启用 API 返回错误，不保留“已创建但未监听”的假记录。

### 10.3 Agent 页面

删除：

- 设备配对页。
- 配对码、管理配对 URL、设备 Token 和手动凭据区。
- Token 轮换提示。

新增或调整：

- 首次启动只显示服务器地址和“连接”按钮。
- 首页显示：等待批准、已批准、被拒绝、已撤销、连接失败。
- 等待批准时显示只读设备指纹用于人工核对，但不要求用户输入。
- RDP 页面自动列出有权访问的目标，不要求手工输入 Target ID。
- 点击目标后显示“正在直连 / 正在使用中继 / 已回退”等路径状态。
- Target 未开启 Windows RDP 时给出系统设置入口和明确提示，不阻止 Agent 其他功能。

## 11. 配置模型

### 11.1 服务端新增配置

```yaml
enrollment:
  enabled: true
  pending_ttl: 24h
  max_pending: 100
  retry_after: 10s
  max_requests_per_ip_per_minute: 10

rdp:
  enabled: true
  rendezvous:
    listen: ":21116"
  session:
    lease: 60s
    renew_interval: 30s
    relay_warmup_delay: 300ms
  public_ingress:
    enabled: false
    public_host: "rdp.example.com"
    port_range:
      start: 20000
      end: 39999
```

客户端不需要复制这些配置。Rendezvous、公网主机、租约和路径参数由服务端在认证成功后下发。

### 11.2 能力而非角色组合

新模型使用能力集合，并直接替换旧 `CLIENT/EXIT/BOTH` 角色字段：

```text
proxy.client
proxy.exit
rdp.controller
rdp.host
rdp.public_ingress
```

新安装默认申请 `proxy.client`、`rdp.controller`、`rdp.host`；服务端批准结果可以少于申请能力。

旧配置中的 `mode: CLIENT/EXIT/BOTH` 不作为兼容输入。需要通用出口能力时，由新客户端申请 `proxy.exit`，并由管理员在服务端批准。

## 12. 协议消息

建议将设备认证和 RDP 消息拆到独立文件，避免继续膨胀通用 `message.go`。

### 12.1 设备认证

```text
DEVICE_HELLO
AUTH_CHALLENGE
AUTH_PROOF
APPROVAL_PENDING
DEVICE_ACCEPTED
DEVICE_REJECTED
DEVICE_REVOKED
```

### 12.2 RDP 控制

```text
RDP_TARGET_STATUS
RDP_TARGETS_REQUEST / RDP_TARGETS_RESPONSE
RDP_CONNECT_REQUEST
RDP_CONNECT_NOTIFY
RDP_CONNECT_RESPONSE
RDP_CANDIDATE_EXCHANGE
RDP_LEASE_RENEW / RDP_LEASE_ACK
RDP_PATH_CHANGE
RDP_SESSION_CLOSE
```

所有消息必须：

- 绑定已认证 DeviceSession，不信任消息内自报 Device ID。
- 有最大消息大小和候选数量限制。
- 使用服务端生成的会话 ID。
- 拒绝与当前会话参与者不匹配的候选交换和关闭请求。
- 不在日志输出会话密钥、签名、私钥或完整认证载荷。

## 13. 目标代码结构

```text
internal/
  protocol/
    device_auth.go
    rdp.go
  rdp/
    path/
    punch/
    secure/

agent/
  identity/
    identity.go
    store_windows.go
    store_darwin.go
    store_other.go
  rdp/
    controller.go
    target.go
    local_proxy.go
    candidates.go

server/
  enrollment/
    service.go
  rdp/
    service.go
    signaling.go
    lease.go
    ingress.go
    port_manager.go
    rendezvous/
  repository/
    migrations.go
    identity.go
    grants.go
    rdp.go
```

允许在实施中按现有包依赖细调目录，但以下边界不得破坏：

- `internal/rdp` 不直接访问数据库或 Web。
- `agent/rdp` 只通过接口访问现有 tunnel/session 能力。
- `server/rdp` 不直接解析 Agent 本地配置。
- `server/enrollment` 是未知设备进入正式 Session Manager 的唯一入口。
- RDP Target 处理器不接受任意 host 参数。

## 14. 实施阶段

| 阶段 | 内容 | 主要验收 |
|---|---|---|
| M1 | 新数据库代际、自动身份、挑战认证、待审批 API/UI、最简客户端配置；同时删除旧配对和 Token 路径 | 只填服务器地址即可出现待审批；批准后自动上线；旧协议明确拒绝 |
| M2 | Relay-only RDP、目标列表、本机 TCP+UDP 代理、自动启动 mstsc | 已授权 Controller 经现有隧道连接 Target 的 3389 |
| M3 | Rendezvous、候选交换、TCP/UDP 打洞、独立选路、授权租约 | LAN/P2P 优先；失败自动中继；撤权最多 60 秒断开 |
| M4 | 稳定公网端口、来源 ACL、限速和入口审计 | 原生 mstsc 可按服务端策略访问，默认关闭 |
| M5 | 安全加固、性能与容量测试、文档、安装包和全量回归 | 全新部署闭环完成，无旧认证代码和手工凭据路径 |

执行顺序固定为 **M1 → M2 → M3 → M4 → M5**。M3 的 P2P 不得先于 M2 的 Relay-only 可靠路径交付。

### 14.1 当前实施进度

- **M1（2026-09-16）已完成**：新数据库代际、Agent 自动身份、挑战签名认证、服务端待审批/批准/拒绝/撤销、能力授权、管理 API/UI、最简客户端配置已经落地。
- 旧配对码、设备 Token、手工凭据录入、旧 Hello 认证和旧数据库迁移路径已经删除；旧数据库会在任何持久化 PRAGMA 或表变更前被明确拒绝。
- M1 验证结果：`go test ./...` 全部通过，Agent 页面 Node 回归测试 13 项通过，Windows amd64 的 Agent、Server 与身份模块交叉编译通过。
- Windows DPAPI 已完成编译验证，真实 Windows 环境的服务安装与运行验收仍归入 M5。
- **M2（2026-09-16）已完成**：新增 `rdp.controller` / `rdp.host` 能力、同所有者设备间 RDP 授权边、Relay-only RDP TCP/QUIC Datagram 数据面、固定目标本机 `127.0.0.1:3389`、控制端 loopback TCP+UDP 代理、服务端目标列表、Windows `mstsc.exe` 自动启动，以及 TLS/TCP 回退时明确关闭 RDP UDP。
- M2 明确不实现 P2P、租约、公网入口；控制端只使用服务端 Welcome 中的已授权目标列表，目标地址和端口不由控制端传入。
- M2 验证结果：全仓 Go 包可编译；仓储 RDP 授权矩阵测试、协议和应用包测试通过；需要本地监听的完整测试在允许绑定端口的环境中执行。
- **M3（2026-09-16）已完成**：新增 `FrameTypeRDPControl` 短控制流、服务端候选注册与交换、UDP rendezvous 反射地址探测、HMAC 打洞握手、UDP 数据包认证与 64 包重放窗口、TCP/UDP 独立直连尝试、Relay fallback，以及默认 60 秒的内存租约和续期/撤销通知。直连失败不会影响既有 Relay-only RDP。
- **M4（2026-09-16）已完成**：新增 `rdp_port_allocations` 稳定端口分配、来源 CIDR、过期时间、每来源 IP 限速、TCP/UDP 同端口入口、目标离线拒绝和连接审计；默认关闭。管理 API/UI 支持管理员按目标设备创建和删除入口，目标必须同时获批 `rdp.host` 与 `rdp.public`。
- **M5（2026-09-16）已完成**：新增候选/认证/打洞单测、旧代数据库断代（`relayproxy-server-rdp-v3`）、配置校验、撤销清理和 Web 管理回归；敏感会话令牌只存在内存，不写入数据库、配置或日志。完整 Go、Node UI、vet 与 Windows 交叉编译作为交付验收项执行。
- 服务端授权管理已支持在线设备的能力整组替换：管理员可在设备详情中多选已声明能力，变更写入 `device_grants` 与审计记录，并使现有会话安全重连；移除 RDP 主机 / 公网能力时会同步停用对应服务与入口。

### 14.2 实施后的运行面

- 客户端配置仍只需 `server.address`；服务端在 Welcome 中下发 rendezvous 地址和租约周期。RDP 直连候选、会话令牌和路径状态不落盘。
- 服务端可选配置 `rdp.rendezvous_listen` / `rdp.rendezvous_advertise`；不配置时不启用公网反射探测，LAN 候选和 Relay fallback 仍可工作。
- 公网入口默认关闭。启用入口服务后，管理员在「RDP 公网入口」页面创建固定端口；目标必须同时获得 `rdp.host` 与 `rdp.public`，每条入口单独保存来源 CIDR、过期时间和速率上限。
- 公网入口使用目标 Agent 自己的 RDP 地址，控制端和公网请求都不能提交任意 host/port；设备撤销、离线和租约到期会关闭相应连接。
- M3 控制信令使用独立短流，不复用心跳控制流。这样心跳超时、重连和 P2P 租约续期互不污染帧边界。

## 15. 测试与验收

### 15.1 身份与审批

- 未知身份只能创建待审批请求，不能进入设备列表的在线态。
- 无私钥者不能使用已登记公钥通过 Challenge。
- Challenge 过期、重复使用和跨连接重放均失败。
- 并发批准同一请求只成功一次。
- 拒绝、撤销和替换身份立即影响新连接。
- 管理员修改设备能力后，旧会话被关闭，Agent 重连后只获得最新批准能力；未声明能力不能被追加授予。
- 私钥、会话密钥不出现在 YAML、日志、API JSON 和错误消息中。
- 待审批数量、来源 IP 速率和 Challenge 并发限制生效。

### 15.2 最简配置与首次使用

- 空配置首次启动只要求服务器地址。
- 设备名自动取系统主机名。
- 服务器批准后 Agent 无需重启即可上线。
- Rendezvous 和 RDP 参数由服务端下发。
- 控制端动态端口的 TCP/UDP 绑定必须保持同号；任一端口绑定失败时整体回滚，不启动不一致的入口。
- RDP 未开启时 Agent 仍在线，UI 给出明确提示。

### 15.3 RDP 数据面

- TCP 与 UDP 经 QUIC Relay 端到端可用。
- TLS/TCP 回退时 TCP 可用、RDP UDP 明确禁用。
- Controller 无权访问 Target 时，信令、候选和中继流全部拒绝。
- RDP 授权不能用于连接 Target 的任意本地端口或局域网地址。
- 设备离线、服务端重启和隧道重连后资源正确清理。

### 15.4 P2P

- 同 LAN、不同 NAT、UDP 受限、QUIC 受限场景分别覆盖。
- TCP/UDP 路径可以独立升级和回退。
- 打洞认证拒绝伪造、重放和错误角色。
- 候选地址数量、地址类型和参与设备校验生效。
- 服务端撤销授权后，P2P 最迟在一个租约周期内关闭。

### 15.5 公网兼容入口

- 端口分配稳定且重启不变。
- TCP/UDP 使用同一端口。
- 未启用、已过期或不匹配来源 CIDR 的连接被拒绝。
- Target 离线时不误路由到其他设备。
- 关闭入口或禁用设备时活动连接被终止。

### 15.6 回归

- `go test ./...`
- 现有 SOCKS5、HTTP、透明代理 TCP/UDP 不受影响。
- 全新审批设备获得对应能力后，SOCKS5、HTTP、透明代理和出口功能可正常使用。
- 旧数据库、旧客户端协议和旧凭据配置均被明确拒绝，不发生静默迁移或降级。
- Windows GUI、Web Agent 页面和服务端管理页面均完成回归。
- Windows 实机验证 TCP+UDP RDP、服务安装、DPAPI 和 mstsc 启动。

## 16. 安全约束

1. 生产服务器必须使用系统可信或企业可信 TLS 证书。
2. 未审批连接不得创建可路由 DeviceSession。
3. 服务端不保存设备私钥或可复用共享 Secret。
4. 管理员审批和授权变更必须写审计。
5. RDP 会话密钥只存在于内存并受会话生命周期约束。
6. 公网兼容入口默认关闭，不能因 Target 上线自动开启。
7. RDP Target 目的地址由本机配置决定，Controller 不能指定。
8. P2P 会话必须依赖短期服务端租约，不能无限期脱离授权控制。
9. 来源 IP、主机名、平台和 Agent 版本均不是可信身份依据。
10. 开发用跳过证书验证不得成为生产 GUI 的正常路径。

## 17. 风险与处理

| 风险 | 处理 |
|---|---|
| 管理员批准了伪装主机名的请求 | 显示来源 IP、只读指纹和时间；主机名明确标记为客户端声明 |
| P2P 建立后撤权失效 | 60 秒短租约 + 主动 SESSION_CLOSE |
| RDP 授权意外获得通用出口能力 | RDP 专用帧和固定本机目标，不复用任意 host:port |
| 公网端口扩大攻击面 | 默认关闭、来源 ACL、有效期、限速、连接审计 |
| 本机 RDP 端口绑定冲突导致不可用 | 自动选择 TCP/UDP 同端口并用实际端口启动 mstsc |
| 自签名证书让开箱体验复杂 | 生产推荐可信证书；私有部署通过高级 `ca_file` 配置解决 |
| 断代升级造成数据丢失 | 启动时检测旧数据库并拒绝覆盖；发布文档要求先备份，再使用全新数据库和全新客户端状态 |
| 两套传输栈长期分叉 | 明确禁止迁移 RDPulse transport，所有 RDP 中继复用 RelayProxy tunnel |

## 18. 完成定义

本功能只有同时满足以下条件才算完成：

- [x] 新安装客户端正常路径只配置服务器地址。
- [x] 不再生成、展示或要求输入配对码、邀请码、设备 Token、Secret。
- [x] 未审批设备无法使用代理、出口和 RDP。
- [x] 服务端批准后 Agent 自动上线，无需重启。
- [x] Controller 自动获得授权目标列表并一键启动 mstsc。
- [x] RDP TCP/UDP 中继路径稳定可用（QUIC Datagram；TLS/TCP 时仅 TCP）。
- [x] P2P 成功时可独立承载 TCP/UDP，失败时自动回退。（M3）
- [x] 服务端撤权对中继立即生效，对 P2P 最迟一个租约周期生效。（M3）
- [x] 公网兼容入口默认关闭且受服务端 ACL 控制。
- [x] 代码中不存在旧配对码、设备 Token、旧 Hello 认证或协议降级路径。
- [x] 旧数据库和旧客户端得到明确的不兼容错误，且程序不会修改旧数据。
- [x] 自动化测试、全新数据库初始化测试、协议不兼容测试和 Windows 交叉编译验收通过；Windows 实机 mstsc/DPAPI 仍需在发布环境执行。

---

**交付状态**：M1–M5 已实施。生产启用公网入口前，应先配置可信 TLS、明确来源 CIDR 与过期时间，并在目标设备审批时单独授予 `rdp.public`。
