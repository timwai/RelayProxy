# RelayProxy 下一代远程桌面设计

> 日期：2026-09-21  
> 状态：设计冻结，实施中 — RD0 已完成，RD1 进行中  
> 目标项目：RelayProxy  
> 前置基础：`docs/superpowers/specs/2026-09-16-rdp-integration-server-approval-design.md` 已完成 M1–M5  
> 设计原则：自动优先、低延迟优先、文本清晰优先、P2P 优先但不迷信 P2P、RDP 保留但不再等同于“远程桌面”

> **实施说明（2026-09-21）**：统一 Remote Desktop 模型、GUI、独立 Desktop 授权、RD/1 QUIC Datagram 与 Server Relay 已开始落地。为尽快验证 Windows Home 的完整媒体链路，RD1 当前功能分支先使用 **GDI + JPEG + Wails 预览** 打通 Host→Relay→Controller；这只是过渡验证路径，不修改本设计中 **DXGI/WGC + H.264 Hardware MFT + 原生 D3D11 Viewer** 的最终目标。实时实施状态以 `docs/superpowers/plans/2026-09-21-remote-desktop-development.md` 为准。
>
## 1. 背景与结论

RelayProxy 当前已经具备完整的设备身份、服务端审批、RDP Controller/Host 能力、设备间授权、QUIC/TLS 隧道、QUIC Datagram、P2P 打洞、Relay fallback、RDP 公网入口与 Windows `mstsc.exe` 自动启动能力。

现有 RDP 数据面最终连接目标 Agent 本机 `127.0.0.1:3389`。这意味着 Windows Home 等没有 RDP Host 的系统无法作为被控端，也意味着高帧率、镜像当前 console session、低延迟游戏、4:4:4、HDR、虚拟显示器等能力受 Windows RDP 本身约束。

下一代设计不删除现有 RDP，而是把它降级为统一 Remote Desktop 子系统中的一个 backend：

```text
Remote Desktop
├── Native RDP Backend
│   ├── Windows Pro / Enterprise / Server
│   └── mstsc + 现有 RDP P2P/Relay 数据面
└── Relay Desktop Backend
    ├── Windows Home / Pro / Enterprise
    ├── 原生桌面采集
    ├── 硬件编码
    ├── 独立输入/音频/剪贴板通道
    └── P2P / Relay 自适应传输
```

默认用户不选择协议，只点击“远程桌面”。Agent 根据目标能力、场景、网络与本地解码能力自动选择 backend 和传输路径。

## 2. 参考产品与取舍

本设计参考各产品公开表现和公开技术资料，但不复制其协议或代码。

| 参考 | 吸收的长处 | RelayProxy 的处理 |
|---|---|---|
| RustDesk | 自建、设备列表、一键连接、P2P→Relay、权限模型、ABR、质量预设 | 保留 RelayProxy 自有身份/授权体系，吸收产品体验和自动回退 |
| Sunshine | Windows GPU 捕获、硬件编码器探测、H.264/H.265/AV1、HDR、编码参数控制 | Relay Desktop Windows Host 的采集/编码方向 |
| Parsec | zero-copy、低延迟、动态码率、4:4:4、高刷、性能 overlay | 定义端到端延迟预算和内容感知画质策略 |
| 网易 UU 类低延迟产品 | 普通用户不理解 NAT/线路/码率，自动选择更稳线路 | 多路径探测、运行时路径切换、自动画质 |
| Windows RDP | 办公场景成熟、系统级登录/管理能力、文字与 Windows 集成 | 继续作为 Native RDP Backend，不强行替代 |

### 2.1 不做“品牌功能拼盘”

最终架构只保留少数稳定抽象：

1. Remote Desktop Session：用户看到的统一远程桌面会话。
2. Backend：Native RDP 或 Relay Desktop。
3. Media Pipeline：Capture → Convert → Encode → Transport → Decode → Render。
4. Control Pipeline：Input / Clipboard / Control / Audio。
5. Path Manager：LAN / P2P / Relay 的探测、评分和切换。
6. Capability Negotiation：双方能力协商，而不是按 Windows SKU 猜测。

## 3. 产品目标

### 3.1 用户目标

正常路径必须是：

```text
Agent 首页
  ↓
远程设备
  ↓
目标设备 [远程桌面]
  ↓
自动连接
```

普通用户默认不需要理解：

- Windows Home / Pro 的 RDP 差异。
- P2P、NAT、QUIC、Relay。
- H.264 / H.265 / AV1。
- NVENC / QSV / AMF。
- QP、GOP、CBR、VBR。
- 码率和分辨率的联动。

### 3.2 技术目标

第一阶段目标：

- Windows x64 Host/Viewer。
- Relay Desktop 1080p60、1440p60、4K60。
- H.264 硬件编解码优先。
- P2P QUIC 优先、Relay QUIC fallback。
- 键盘、鼠标、剪贴板。
- 动态码率、动态 FPS、动态分辨率。
- 端到端性能统计。
- Native RDP 继续可用。

后续目标：

- H.265、AV1。
- 4:4:4。
- HDR / 10-bit。
- 120/144 Hz。
- 多显示器。
- 虚拟显示器。
- 音频、手柄、文件传输。
- macOS / Linux Relay Desktop Host。

## 4. 非目标

第一版明确不做：

- 不删除或重写当前 RDP M1–M5。
- 不把视频数据塞进可靠 TCP 流。
- 不要求第一版实现 AV1、HDR、虚拟显示器、手柄。
- 不把 WebView2 `<canvas>` / `<video>` 作为最终高性能 Viewer。
- 不暴露任意目标 host:port 给 Remote Desktop Controller。
- 不因为 P2P 建立成功就永久锁定 P2P。
- 不以 Windows Edition 名称作为唯一 RDP 可用性判断。
- 不依赖破解 Windows Home RDP Host 的第三方补丁。

## 5. 目标架构

```text
┌──────────────────────── RelayProxy Server ────────────────────────┐
│ Enrollment / Device Grants / Remote Desktop Grants / Audit        │
│                                                                    │
│ RemoteDesktop Coordinator                                         │
│ ├── Session Lease                                                 │
│ ├── Capability Exchange                                           │
│ ├── Candidate Exchange                                            │
│ └── Relay Routing                                                 │
└───────────────┬──────────────────────────────────┬─────────────────┘
                │                                  │
         QUIC/TLS Control                    QUIC Relay
                │                                  │
┌───────────────▼──────── Controller ──────────────▼─────────────────┐
│ Agent GUI → RemoteDesktop Manager                                  │
│            ├── NativeRDP Controller → mstsc.exe                    │
│            └── RelayDesktop Viewer                                 │
│                 ├── Decoder                                        │
│                 ├── D3D11 Renderer                                 │
│                 └── Input / Clipboard / Audio                     │
└──────────────────────────────┬─────────────────────────────────────┘
                               │
                     Direct / P2P QUIC
                               │
┌──────────────────────────────▼──────── Target ─────────────────────┐
│ RemoteDesktop Manager                                              │
│ ├── NativeRDP Host → 127.0.0.1:3389                               │
│ └── RelayDesktop Host                                              │
│      ├── DXGI / WGC Capture                                        │
│      ├── GPU Color Convert / Scale                                 │
│      ├── H.264/H.265/AV1 Encoder                                  │
│      ├── Input Injector                                             │
│      └── Clipboard / Audio                                         │
└────────────────────────────────────────────────────────────────────┘
```

## 6. 包与职责边界

目标代码结构：

```text
internal/
  protocol/
    remote_desktop.go
  desktop/
    capability.go
    media.go
    transport.go
    stats.go

agent/
  desktop/
    manager.go
    targets.go
    session.go
    selector.go
    path/
    backend/
      rdp/
      relay/
    capture/
      windows/
    codec/
      windows/
    input/
      windows/
    viewer/
      windows/

server/
  desktop/
    coordinator.go
    lease.go
    policy.go
    signaling.go
```

现有 `agent/rdp` 在过渡期保持可用。完成 Remote Desktop 抽象后，RDP Controller/Target 逻辑逐步作为 `backend/rdp` 使用；不要求一次性移动文件，避免大重构和功能开发同时发生。

边界要求：

- `server/desktop` 不处理视频内容，只做授权、协调、租约、路径元数据和 Relay。
- Relay Desktop Host 不接受 Controller 指定本地地址。
- Capture/Codec 不知道 Server、设备授权或数据库。
- Viewer 不知道审批数据库，只接收已授权 Session。
- Backend 选择器不依赖 GUI。
- GUI 不直接调用 `ConnectRDP`，统一调用 `ConnectRemoteDesktop`。

## 7. 能力模型

目标 Agent 上报真实能力，而不是上报“Windows Home/Pro”后让 Controller 猜。

示例：

```go
type DesktopCapabilities struct {
    NativeRDP     bool
    RelayDesktop  bool

    Capture       []string // dxgi, wgc
    Codecs        []CodecCapability
    Displays      []DisplayCapability

    Audio         bool
    Clipboard     bool
    MultiMonitor  bool
    HDR           bool
    VirtualDisplay bool

    MaxWidth      int
    MaxHeight     int
    MaxFPS        int
}
```

CodecCapability 至少包含：

```go
type CodecCapability struct {
    Codec         string // h264, h265, av1
    Hardware      bool
    Encoder       string // mf, nvenc, qsv, amf, software
    Decode        bool
    Encode        bool
    Chroma420     bool
    Chroma444     bool
    BitDepth8     bool
    BitDepth10    bool
    MaxWidth      int
    MaxHeight     int
    MaxFPS        int
}
```

### 7.1 Native RDP 探测

不能只判断 SKU。目标 Agent 启动和状态变化时探测：

1. 平台是否 Windows。
2. TermService 是否存在且服务状态允许。
3. `127.0.0.1:3389` 是否监听。
4. 短超时 TCP connect 是否成功。
5. 服务状态变化后立即刷新能力。

Native RDP 能力只是候选 backend，不代表一定优先使用。

## 8. Backend 自动选择

连接 API：

```go
ConnectRemoteDesktop(targetID string, options ConnectOptions)
```

模式：

```text
protocol: auto | relay | rdp
scene:    auto | office | performance | gaming | quality
quality:  auto | smooth | balanced | high | extreme | custom
```

### 8.1 默认策略

默认 `protocol=auto`。

推荐策略：

| 场景 | Backend 优先级 |
|---|---|
| auto | Relay Desktop 硬编可用时优先 Relay Desktop；否则 Native RDP；最后 Relay Desktop 软件路径 |
| office | Native RDP 可用时优先 RDP；用户要求镜像 console 时改用 Relay Desktop |
| performance | Relay Desktop |
| gaming | Relay Desktop |
| quality | Relay Desktop |

这比“有 RDP 就一定 RDP”更合理，因为 Relay Desktop 的目标是成为 Windows Home 与高性能场景的统一方案。

### 8.2 运行时回退

连接前依赖能力协商避免无意义超时；运行时仍允许回退：

```text
Native RDP 启动失败
    ↓
若 Relay Desktop 可用
    ↓
切换 Relay Desktop

Relay Desktop 初始化硬编失败
    ↓
尝试备用硬编 / 软件编码
    ↓
仍失败且 Native RDP 可用
    ↓
切换 Native RDP
```

回退原因必须进入状态和日志，不静默改变用户显式选择的 `protocol=rdp` 或 `protocol=relay`。

## 9. Relay Desktop 视频流水线

Windows Host 首选：

```text
DXGI Desktop Duplication / WGC
          ↓
      D3D11 Texture
          ↓
 GPU Scale / Color Convert
          ↓
  NV12 / P010 / YUV444
          ↓
 Hardware Encoder
          ↓
   Encoded Frame
          ↓
 Packetizer / QUIC Datagram
```

目标是尽量避免：

```text
GPU → CPU BGRA → CPU YUV → GPU Encoder
```

最终应尽量做到：

```text
GPU Texture → GPU Convert → Hardware Encoder
```

### 9.1 Capture 选择

- DXGI Desktop Duplication：MVP 默认，适合桌面镜像、dirty/move rect。
- WGC：作为备用和后续增强，处理部分 DXGI 不理想的应用/窗口场景。
- 自动模式先试 DXGI，初始化或目标场景不满足时切 WGC。
- Cursor 与视频分离，避免鼠标移动导致整帧编码。

### 9.2 Encoder 选择

MVP 首选 Windows Media Foundation H.264 Hardware MFT，原因：

- 不强绑定单一 GPU 厂商 SDK。
- 可以先完成 NVIDIA / Intel / AMD 的通用硬件路径。
- 后续再增加 NVENC/QSV/AMF direct backend 获取更细的低延迟参数控制。

未来优先级可基于启动 benchmark，而不是固定厂商顺序。

## 10. Codec 协商

MVP：

```text
H.264 8-bit 4:2:0
```

后续：

```text
AV1 / H.265 / H.264
```

但不能简单认为 AV1 永远最好。选择器需要同时考虑：

- 双方是否支持。
- 编码时间。
- 解码时间。
- 分辨率/FPS。
- 网络带宽。
- 场景。
- 4:4:4 / 10-bit 需求。

Host 启动后可缓存轻量 benchmark：

```text
H264 encode 1.4 ms
H265 encode 1.8 ms
AV1  encode 2.5 ms
```

高性能/游戏模式优先满足延迟预算，画质模式优先压缩效率。

## 11. 分辨率策略

区分：

- Source Resolution：物理或虚拟显示器分辨率。
- Capture Resolution：采集区域。
- Encode Resolution：编码分辨率。
- Viewer Viewport：观看窗口实际像素。

支持模式：

```text
native
follow_viewport
fixed
virtual_display  # 后续
```

默认 `follow_viewport`。

Viewer 报告 viewport：

```text
width
height
device_scale
fullscreen
```

Host 根据最大画质档和网络决定 Encode Resolution。例如物理 4K 屏、Viewer 1280×720 时，不应该持续发送 4K。

调整分辨率时：

1. 通知 Viewer 即将 reconfigure。
2. Flush encoder。
3. 创建新 encoder config。
4. 强制 IDR。
5. Viewer 丢弃旧 generation 的 frame。

## 12. 帧率策略

帧率不是固定常量，而是上限：

```text
max_fps = 60
```

内容感知策略：

```text
静止桌面      1–5 FPS
文字输入      15–30 FPS
滚动/拖窗口   30–60 FPS
视频/游戏     60 FPS
高刷模式      120/144 FPS（后续）
```

鼠标位置通过独立 cursor/input 通道更新，不因为鼠标移动强制编码完整视频帧。

## 13. 码率与画质

用户层只暴露预设：

| 预设 | 目标 |
|---|---|
| smooth | 优先延迟和弱网稳定 |
| balanced | 默认 |
| high | 文字和桌面清晰度 |
| extreme | 高带宽、高画质 |
| custom | 高级用户 |

初始参考范围不是硬编码保证值：

| 场景 | 1080p60 | 1440p60 | 4K60 |
|---|---:|---:|---:|
| smooth | 2–4 Mbps | 4–7 Mbps | 8–14 Mbps |
| balanced | 4–8 Mbps | 7–12 Mbps | 14–24 Mbps |
| high | 8–14 Mbps | 12–20 Mbps | 24–40 Mbps |
| extreme | 15–25 Mbps | 20–35 Mbps | 35–60+ Mbps |

码率表示目标上限/控制区间，不意味着静态桌面持续占满。

### 13.1 ABR 输入

每 100–250ms 更新：

```text
RTT
RTT variation / jitter
packet loss
delivery rate
send queue bytes
send queue delay
encoder queue depth
encode time
decode time
render delay
dropped frames
```

### 13.2 ABR 原则

- 降码率快，升码率慢。
- 输入延迟优先于视频完整性。
- 先限制帧率，再降低分辨率，最后明显牺牲文字清晰度。
- 高丢包时优先避免 queue buildup。
- 恢复期间采用滞回，避免 5↔10 Mbps 震荡。

示例：

```text
1440p60 12 Mbps
   ↓ 网络变差
1440p30 8 Mbps
   ↓
1080p30 5 Mbps
   ↓
720p30 2 Mbps
```

## 14. 内容感知画质

远程桌面与在线视频不同。至少区分：

```text
DESKTOP
MOTION
```

判断输入：

- dirty rect 占比。
- frame difference。
- move rect。
- 最近输入事件。
- 持续运动时间。

DESKTOP：

- 优先保留分辨率。
- 优先文本边缘。
- 可降低 FPS。
- 后续支持 4:4:4。

MOTION：

- 优先 FPS 和低延迟。
- 默认 4:2:0。
- 动态提高码率或适当降分辨率。

## 15. 4:4:4、HDR 和高刷

这些不是 MVP 阻塞项，但协议第一天保留能力字段。

4:4:4 主要面向：

- IDE 彩色文字。
- UI/设计。
- 精细图形。

HDR/10-bit 需要：

- 采集端 HDR 元数据。
- P010 或等价 10-bit pipeline。
- Codec Main10 能力。
- Viewer HDR swapchain 与显示器能力。

高刷协议不得写死 60 FPS，FPS 字段至少支持 16-bit 范围。

## 16. 传输通道设计

禁止把所有数据放进单一可靠流。

逻辑通道：

```text
Control     reliable, ordered
Keyboard    reliable, ordered, high priority
MouseBtn    reliable, ordered, high priority
MouseMove   datagram, latest-state wins
Clipboard   reliable
AudioCtl    reliable
Audio       datagram
Video       datagram / partial reliability
File        reliable, low priority
Stats       low priority
```

### 16.1 视频 Datagram

视频帧分片：

```text
Frame
  ├── frame_id
  ├── generation
  ├── pts
  ├── flags(IDR...)
  └── fragments[]
```

普通 P-frame 数据不因为一个丢失 fragment 阻塞后续帧。严重丢包时 Viewer 请求 IDR。

关键帧可以增加：

- 更高重发/保护优先级。
- 可选轻量 FEC。
- 分片冗余。

MVP 可先不实现 FEC，但 wire format 预留 capability。

## 17. 路径选择

现有 RelayProxy 已经具备 RDP P2P + Relay fallback。下一代抽象为通用 Desktop Path Manager。

候选路径：

```text
LAN Direct
P2P IPv4
P2P IPv6
QUIC Relay
TLS Relay（Control fallback；Relay Desktop 视频不优先）
```

路径选择不是“P2P 成功即锁死”。

评分输入：

```text
RTT
loss
jitter
delivery rate
stability
relay cost penalty
```

例如 P2P 70ms/5% loss，而 Relay 38ms/0.1% loss 时，应允许 Relay 获胜。

### 17.1 运行时切换

Session ID、输入序号、video generation 不随路径改变。

```text
P2P QUIC
  ↓ 质量恶化
Relay QUIC
  ↓ P2P 恢复并持续稳定
P2P QUIC
```

切换必须具备滞回和最短驻留时间，防止来回抖动。

## 18. 输入与光标

控制消息与视频解耦。

Windows MVP：

- 键盘：SendInput。
- 鼠标按钮/滚轮：SendInput。
- 鼠标移动：绝对坐标映射 + latest-state。
- 光标图像/热点：Host 上报 cursor metadata，Viewer 本地绘制。

输入权限必须是会话授权的一部分，可支持未来的 view-only。

安全要求：

- Viewer 输入只作用于当前授权 Session。
- Session Close 后立即拒绝输入。
- 不允许通过通用 Agent API 注入系统输入。
- Ctrl+Alt+Del 等安全序列后续使用单独的受控实现，不伪装成普通按键。

## 19. Viewer

高性能 Relay Desktop Viewer 使用独立原生窗口，不以 Agent 主 WebView2 页面承担视频渲染。

推荐：

```text
GUI
  ↓ ConnectRemoteDesktop
Desktop Session
  ↓
Native Viewer Window
  ├── Media Foundation Decoder
  ├── D3D11 Renderer
  ├── Cursor Overlay
  └── Overlay Toolbar
```

GUI 负责设备选择和设置；Viewer 负责低延迟渲染。

Viewer 顶部隐藏 overlay：

```text
HOME-PC | 60 FPS | 8.4 Mbps | 22 ms | H.264 HW | P2P QUIC
[显示] [画质] [声音] [输入] [统计] [断开]
```

## 20. Agent GUI

当前 GUI 没有远程连接入口。新增“远程设备”区域，但不新增“RDP”概念作为一级产品菜单。

示意：

```text
远程设备

Office-PC                         在线
Windows
18 ms · P2P
                          [远程桌面]

Home-PC                           在线
Windows
Relay Desktop 可用
                          [远程桌面]
```

点击直接自动连接。

高级连接设置：

```text
场景：自动 / 办公 / 高性能 / 游戏 / 高画质
协议：自动 / Relay Desktop / Windows RDP
画质：自动 / 流畅 / 均衡 / 高清 / 极致
分辨率：跟随窗口 / 原始 / 固定
FPS：自动 / 30 / 60 / ...
```

默认不弹配置窗口。

## 21. Server 与授权

原有 `rdp.controller` / `rdp.host` 不直接扩展为“可以任意远控”。

引入通用能力：

```text
desktop.controller
desktop.host
```

Backend 细能力由 capability negotiation 决定：

```text
native_rdp
relay_desktop
```

过渡期：

- 现有 RDP grant 继续工作。
- 新 Remote Desktop grant 可以映射/兼容旧 RDP 授权。
- 不在一个版本中强制删除旧 capability。
- 审计统一 service=`desktop`，backend=`rdp|relay`。

## 22. 安全

1. 延续设备挑战签名认证和服务端审批。
2. Controller→Target 必须显式授权。
3. 每个 Desktop Session 使用服务端签发的短期租约。
4. P2P 仍受租约约束。
5. 媒体数据端到端绑定 Session，不接受裸 UDP。
6. 所有媒体包至少绑定 session_id、generation 和防重放序号。
7. 输入通道比视频通道安全等级更高，必须可靠认证。
8. Remote Desktop Host 不接收任意 host:port。
9. 默认不暴露公网端口。
10. 日志不得输出 Session 密钥、设备私钥或完整认证材料。

## 23. 可观测性

每个 Session 维护：

```text
backend
path
codec
encoder / decoder
capture_resolution
encode_resolution
fps_capture
fps_encode
fps_decode
fps_render
bitrate_target
bitrate_actual
rtt
jitter
loss
delivery_rate
encode_ms
decode_ms
render_ms
queue_ms
frames_dropped
idr_requests
path_switches
```

这些统计：

- Viewer overlay 实时显示。
- Agent 日志按聚合周期记录，不逐帧刷日志。
- Server 只记录必要会话审计，不收集完整逐帧指标。

## 24. 性能预算

MVP 1080p60 硬件路径目标：

| 阶段 | 目标 |
|---|---:|
| Capture | < 3 ms |
| Convert/Scale | < 2 ms |
| Encode | < 5 ms |
| Host queue | < 5 ms |
| Network | 由路径决定 |
| Decode | < 5 ms |
| Render | < 5 ms |

LAN/P2P 环境期望“输入到画面反馈”的本地处理部分显著低于一个 60Hz frame interval；网络 RTT 另计。

CPU/GPU 指标必须纳入验收，不允许通过大量 CPU copy 达成表面 FPS。

## 25. 兼容与迁移

现有用户升级后：

- SOCKS5/HTTP/透明代理不受影响。
- 现有 RDP 连接继续工作。
- GUI 新入口优先走统一 Remote Desktop API。
- 旧 `ConnectRDP` 在内部保留，供 Native RDP Backend 调用。
- 新协议使用新的 frame/control type，不复用旧 RDP 帧表达视频。
- Server 可以同时服务旧 RDP 和新 Relay Desktop Agent，直到明确发布断代版本。

旧设计文档中的“不实现自研远程桌面”属于 2026-09-16 RDP 集成阶段的边界；本设计作为后续产品阶段明确扩大范围，不回写历史结论。

## 26. 交付阶段

### RD0 — 抽象层与 GUI 入口

- RemoteDesktopTarget / Capabilities。
- RemoteDesktop Manager。
- Native RDP Backend 封装现有逻辑。
- GUI “远程设备 / 远程桌面”。
- 自动协议选择框架。

### RD1 — Relay Desktop MVP

- Windows DXGI Capture。
- H.264 Hardware Encode。
- H.264 Hardware Decode。
- D3D11 Viewer。
- QUIC Datagram Video。
- 键鼠、光标、剪贴板。
- Relay 路径先稳定。

### RD2 — P2P + ABR

- 通用 Desktop Path Manager。
- P2P QUIC。
- P2P/Relay 质量评分与切换。
- 动态码率/FPS/分辨率。
- 性能 overlay。

### RD3 — 画质增强

- H.265。
- 4:4:4。
- 多显示器。
- 音频。
- 120/144 FPS。

### RD4 — 高端能力

- AV1。
- HDR10 / 10-bit。
- 虚拟显示器。
- 手柄。
- FEC / 更精细的媒体拥塞控制。

## 27. MVP 完成定义

RD1–RD2 完成后至少满足：

- [ ] Windows Home 可以作为远程桌面 Host。
- [ ] Windows Pro 仍可使用 Native RDP。
- [ ] GUI 有统一“远程桌面”入口。
- [ ] 默认无需选择 backend。
- [ ] Relay Desktop 1080p60 稳定。
- [ ] 1440p60 可用。
- [ ] 4K60 在硬件和网络满足时可用。
- [ ] P2P 失败自动 Relay。
- [ ] P2P 质量显著差于 Relay 时允许切 Relay。
- [ ] 键鼠输入不被大视频帧阻塞。
- [ ] 网络恶化时画质可自动降级而不是长时间卡死。
- [ ] 网络恢复后画质平滑恢复。
- [ ] Viewer 显示 FPS、码率、RTT、丢包、编解码耗时和路径。
- [ ] Session 关闭或撤权后输入和媒体立即停止。
- [ ] 现有 RDP、代理、透明代理回归通过。

## 28. 参考资料

实现阶段优先阅读官方/上游资料，而不是依赖二手文章：

- RustDesk documentation: https://rustdesk.com/docs/
- RustDesk source: https://github.com/rustdesk/rustdesk
- Sunshine source/configuration: https://github.com/LizardByte/Sunshine
- Parsec: https://parsec.app/
- Microsoft Desktop Duplication API: https://learn.microsoft.com/windows/win32/direct3ddxgi/desktop-dup-api
- Microsoft Media Foundation H.264 Encoder: https://learn.microsoft.com/windows/win32/medfound/h-264-video-encoder

---

本设计的核心不是“再做一个 VNC”，而是让 RelayProxy 的现有设备身份、授权、QUIC、P2P 和 Relay 能力成为统一远程桌面基础设施；RDP 负责它擅长的 Windows 管理场景，Relay Desktop 负责 Windows Home、console 镜像、高帧率和可控媒体质量场景。