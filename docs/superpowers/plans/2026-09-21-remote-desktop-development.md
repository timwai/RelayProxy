# RelayProxy Remote Desktop 开发实施文档

> 日期：2026-09-21  
> 状态：实施中 — RD0 已完成，RD1 Windows 可交互 MVP 已合并 main，正在替换高性能媒体栈  
> 对应设计：`docs/superpowers/specs/2026-09-21-remote-desktop-design.md`  
> 基线：main 分支，现有 RDP M1–M5 已完成  
> 当前开发分支：`feature/relay-desktop-windows-mvp`

## 0. 当前进度

更新时间：**2026-09-21**

| 阶段 / 能力 | 状态 | 当前实现 |
| --- | --- | --- |
| RD0 Remote Desktop 统一模型 | ✅ 已完成 | `RemoteDesktopTarget`、`DesktopCapabilities`、统一 Connect/Disconnect/Status API 已落地 |
| RD0 GUI 统一入口 | ✅ 已完成 | Agent GUI 已提供“远程桌面”设备列表，不再要求用户手填 RDP Target ID |
| Native RDP 兼容 | ✅ 保持可用 | 原有 mstsc、RDP P2P / Relay、3389 目标链路继续保留 |
| Desktop 独立授权 | ✅ 已合并 main | `desktop.controller / desktop.host` 与 Native RDP 独立鉴权；Windows Home 不再要求存在 `rdp_services` / 3389 才能成为 Relay Desktop 目标 |
| Remote Desktop 目标发现 | ✅ 已合并 main | Welcome 同时支持兼容 `RDPTargets` 与新的 `RemoteDesktopTargets` |
| RD/1 媒体协议 | ✅ 已完成基础层 | 二进制媒体头、分片、重组、独立 Desktop Datagram association 已实现 |
| Server 双跳媒体 Relay | ✅ 已合并 main | Controller ↔ Relay ↔ Target 使用 QUIC Datagram 转发，媒体不进入 JSON |
| Windows Host 可视 MVP | ✅ 已合并 main | GDI 捕获虚拟桌面 + CPU JPEG；支持按会话选择画质、最高 4K/30 FPS 安全上限与 JPEG 软码率约束 |
| Controller Viewer MVP | ✅ 已合并 main | Controller 重组 JPEG 帧，Wails GUI 内置实时预览、全屏和键鼠控制 |
| Windows Home 完整“看到并操作”链路 | ✅ 已合并 main | PR #21 已合并，main merge commit `50610f16a9d1e8c8ac4d24a014e1b302775614d0`；Go/UI/Windows/macOS CI 通过 |
| 键盘 / 鼠标输入 | ✅ 已合并 main | Viewer 采集键盘、绝对鼠标、按键与滚轮；可靠控制流经 Relay 转发，Host 使用 `SendInput`，失焦/断线主动释放按键 |
| 分辨率 / FPS / 画质 / 码率控制 | ✅ JPEG MVP 已完成 | GUI 连接设置透传到 Host；preset + fixed/native resolution + FPS + JPEG 软码率预算，H.264 阶段替换为真正 rate control |
| 光标 | ⏳ 未开始 | 计划与视频分离传输并在 Viewer 本地绘制 |
| 剪贴板 | ⏳ 未开始 | RD1 先实现 Unicode 文本双向同步 |
| DXGI / WGC Capture | ✅ DXGI 已合并 main | 单显示器优先 DXGI Desktop Duplication，运行时不可用自动回退 GDI；多显示器仍暂用 GDI 直到显示器几何协议完成 |
| H.264 硬件编解码 | ✅ 端到端已合并 main | DXGI/GDI Capture → Media Foundation H.264 → RD/1 Datagram → Controller → WebCodecs Canvas 已贯通；硬件/软件 MFT、异步事件、ForceIDR、动态码率均已接入，并保留 JPEG fallback |
| H.264 Datagram 丢包恢复 | 🧪 分支验证中 | Controller 检测 FrameID 缺口后停止提交 delta frame，经可靠 session stream 请求 IDR；WebCodecs 解码错误/队列过载也触发同一恢复流程 |
| 原生 D3D11 Viewer | ⏳ 待实现 | 当前 Wails 图片预览仅用于功能闭环，不作为最终低延迟 Viewer |
| RD2 P2P / ABR / Stats | ⏳ 未开始 | 待 RD1 Relay-only 基础稳定后进入 |

### 0.1 已合并主线的关键进度

- RD/1 原生 QUIC Datagram 媒体 association 已进入 `main`。
- Relay Desktop 媒体双跳 Relay 已进入 `main`。
- Relay Desktop 授权已从 Native RDP 中拆分，`desktop.controller / desktop.host` 已进入 `main`。
- Windows Home 类型目标可以仅凭 `desktop.host` 被发现和授权，不要求本机 RDP Host 或 `127.0.0.1:3389`。
- 上述授权模型合并后的主线提交为 `a044e70dc93ab10b94c6ebe289a880c45a2cf2b4`。

### 0.2 当前功能分支

当前 Windows 可交互 MVP 已通过 PR #21 合并到 `main`；后续高性能媒体栈在独立功能分支继续演进。

当前已完成的端到端路径：

```text
Windows Host
  ↓
GDI virtual desktop capture
  ↓
RGBA resize ≤ 1280×720
  ↓
JPEG encode ≈ 10 FPS
  ↓
RD/1 packetize
  ↓
QUIC Datagram
  ↓
Relay Server
  ↓
Controller reassembly
  ↓
JPEG latest-frame cache
  ↓
Wails GUI preview / fullscreen
  ↕ reliable session stream
keyboard / mouse / wheel
  ↓
Windows SendInput
```

该 JPEG 路径现在作为可运行的功能基线保留；后续 Capture / Codec / Viewer 可以独立替换，不需要重做授权、Relay Datagram 与输入控制链路。

### 0.3 当前实现与最终设计的差异

为了优先验证 Windows Home 的端到端链路，RD1 中间增加了一个功能验证阶段：

```text
当前验证：
DXGI Desktop Duplication（不可用时 GDI）→ RGBA/NV12 → Media Foundation H.264
→ RD/1 QUIC Datagram → WebCodecs Canvas
↘ H.264 不可用时自动回退 JPEG

最终目标：
DXGI / WGC → D3D11 texture → GPU convert → H.264 HW encoder
→ RD/1 → H.264 HW decoder → D3D11 native viewer
```

GDI + JPEG 不改变最终设计方向，只用于验证以下基础设施已经正确工作：

- Windows Home 不依赖 RDP Host 的授权和目标发现。
- Host Capture → Encoder → Packetizer 的生命周期。
- RD/1 QUIC Datagram 数据面。
- Server 双跳媒体 Relay。
- Controller 分片重组。
- GUI Session 状态与 Viewer 展示。
- Viewer → Relay → Host 的键盘/鼠标可靠控制链路与 Windows `SendInput` 注入。

在这条验证链路通过 CI 和 Windows 实机验证后，再替换 Capture / Codec / Viewer，而不重新改动授权和 Relay 协议层。

## 1. 开发策略

本项目采用“先抽象、再打通、后优化”的顺序，不允许一开始同时引入 H.265、AV1、HDR、虚拟显示器等高风险能力。

固定原则：

1. 每个阶段结束都必须保持现有 RDP 可用。
2. Relay Desktop 首先完成 Relay-only 可靠路径，再接 P2P。
3. 第一版只要求 H.264。
4. 视频不能依赖单一可靠 TCP 流。
5. Viewer 使用原生渲染窗口，不在主 WebView2 中承担最终媒体渲染。
6. 每个性能优化都必须有指标，禁止“感觉更快”。
7. 所有协议字段从第一版带版本和 capability，避免后续为 H.265/AV1/HDR 再断代。
8. 优先复用现有身份、设备审批、grant、session、tunnel、P2P 候选与审计能力。

## 2. 开发里程碑

```text
RD0  Remote Desktop 抽象 + GUI                         ✅ 已完成
RD1  Windows Relay Desktop Relay-only MVP                🧪 进行中
RD2  P2P + ABR + 性能统计                                ⏳ 未开始
RD3  H.265 / 4:4:4 / 音频 / 多显示器                    ⏳ 未开始
RD4  AV1 / HDR / 虚拟显示器 / 高刷 / FEC                ⏳ 未开始
```

RD0、RD1、RD2 是首个可发布版本的范围。

## 3. RD0：Remote Desktop 抽象

### 3.1 新增协议模型

新增：

```text
internal/protocol/remote_desktop.go
internal/desktop/capability.go
internal/desktop/stats.go
```

核心结构：

```go
type DesktopBackend string

const (
    DesktopBackendAuto  DesktopBackend = "auto"
    DesktopBackendRDP   DesktopBackend = "rdp"
    DesktopBackendRelay DesktopBackend = "relay"
)

type RemoteDesktopTarget struct {
    DeviceID     string
    Name         string
    Online       bool
    Capabilities DesktopCapabilities
}

type ConnectOptions struct {
    Backend    DesktopBackend
    Scene      string
    Quality    string
    Resolution ResolutionOptions
    FPS        int
    MaxBitrate int
}
```

序列化字段采用现有项目的 JSON 命名风格；不要在多个 package 重复定义 Target。

### 3.2 Agent Manager

新增：

```text
agent/desktop/manager.go
agent/desktop/selector.go
agent/desktop/session.go
```

接口：

```go
type Backend interface {
    Name() string
    Available(target RemoteDesktopTarget) bool
    Connect(ctx context.Context, target RemoteDesktopTarget, options ConnectOptions) (Session, error)
}

type Session interface {
    ID() string
    Backend() string
    Stats() SessionStats
    Close() error
}
```

Manager 职责：

- 获取服务端下发目标。
- 合并 Native RDP 与 Relay Desktop capability。
- 选择 backend。
- 管理当前 active session。
- 对 GUI 暴露统一状态。
- 不实现具体视频或 RDP 数据面。

### 3.3 封装现有 RDP

不要立刻搬动现有 `agent/rdp`。

新增轻量适配：

```text
agent/desktop/backend/rdp/backend.go
```

内部调用现有：

```go
Agent.ConnectRDP(targetID, true)
Agent.DisconnectRDP()
Agent.RDPTargets()
```

RD0 完成后 GUI 不再直接绑定 `ConnectRDP`。

### 3.4 新 Bridge API

`agent/bridge/bridge.go` 新增：

```go
GetRemoteDesktopTargets()
ConnectRemoteDesktop(targetID string, options ...)
DisconnectRemoteDesktop()
GetRemoteDesktopStatus()
```

旧接口先保留：

```go
GetRDPTargets()
ConnectRDP()
DisconnectRDP()
```

标记为内部兼容路径，等新 GUI 和测试稳定后再决定是否删除。

### 3.5 GUI

修改：

```text
agent/gui/assets/index.html
agent/gui/... bindings
```

首页新增“远程设备”。

每行：

```text
名称
在线状态
路径/延迟（有数据时）
能力提示
[远程桌面]
```

点击默认直接连接，不先弹高级设置。

增加连接设置弹窗或侧栏，但只在用户主动打开“连接设置”时出现：

```text
场景
协议
画质
分辨率
FPS
```

### 3.6 RD0 验收

- Windows Agent GUI 能列出授权目标。
- 点击 Windows Pro 目标仍能启动现有 mstsc。
- GUI 不再出现“RDP 目标 ID”输入。
- backend auto selector 有单测。
- 现有 RDP P2P/Relay 测试全部不回归。

## 4. Capability 上报与服务端

### 4.1 Capability 消息

在 Agent Welcome/状态更新机制中扩展 Remote Desktop capabilities。

建议区分：

```text
静态能力：
codec/capture/decoder/max resolution

动态状态：
RDP listening
display list
encoder availability
```

静态能力只在连接/变化时发送，动态状态可 debounce 后更新。

### 4.2 Server 目标模型

Server 不需要理解每个 Encoder 细节，只负责透传允许公开给 Controller 的 capability 摘要。

新增/扩展目标响应：

```text
deviceId
name
online
desktopControllerAllowed
desktopHostAllowed
nativeRdpAvailable
relayDesktopAvailable
relayDesktopSummary
```

详细 codec negotiation 在 Session 建立后由双方完成，避免 Welcome 过度膨胀。

### 4.3 授权迁移

过渡期策略：

```text
已有 rdp.connect grant
    ↓
允许 Native RDP

新增 desktop.connect grant
    ↓
允许统一 Remote Desktop
```

首个版本可采用服务端兼容映射：

- desktop.connect 存在：允许所有批准 backend。
- 只有旧 rdp.connect：只允许 Native RDP。
- 不自动把旧 RDP grant 扩大为 Relay Desktop 控制权限。

这一点避免升级后权限被静默放大。

## 5. RD1：Relay Desktop Session 骨架

新增：

```text
agent/desktop/backend/relay/
  backend.go
  host.go
  controller.go
  session.go

server/desktop/
  coordinator.go
  lease.go
  signaling.go

internal/protocol/remote_desktop.go
```

Session 建立流程：

```text
Controller
  ↓ DESKTOP_CONNECT_REQUEST
Server authorization
  ↓
Target notify
  ↓ capability exchange
Server returns session lease/token
  ↓
Transport establish
  ↓
Media/control channels ready
  ↓
Viewer opens
```

MVP 先只使用 Relay QUIC 路径，P2P 在 RD2 接入。

## 6. Wire Protocol

协议消息建议：

```text
DESKTOP_CAPS
DESKTOP_CONNECT_REQUEST
DESKTOP_CONNECT_NOTIFY
DESKTOP_CONNECT_RESPONSE
DESKTOP_CONFIG
DESKTOP_CONFIG_ACK
DESKTOP_STATS
DESKTOP_IDR_REQUEST
DESKTOP_PATH_CHANGE
DESKTOP_LEASE_RENEW
DESKTOP_SESSION_CLOSE
```

媒体不是 JSON。

### 6.1 Media Packet Header

二进制头最少包含：

```text
version
packet_type
session_id
stream_id
generation
sequence
frame_id
fragment_index
fragment_count
timestamp
flags
payload_length
```

flags：

```text
KEYFRAME
CONFIG
END_OF_FRAME
CURSOR
AUDIO
```

安全层可复用现有 P2P session token 派生机制，但必须使用新的 domain separation，不能直接复用 RDP payload MAC 字符串。

### 6.2 Generation

以下变化必须递增 generation：

- codec 变化。
- 分辨率变化。
- bit depth/chroma 变化。
- encoder 重建。

Viewer 收到新 generation 的 CONFIG 后：

1. flush decoder；
2. 应用新参数；
3. 等待 IDR；
4. 丢弃旧 generation 数据。

## 7. Windows Capture

### 7.1 MVP 路径

目录：

```text
agent/desktop/capture/windows/
  capture.go
  dxgi.go
  cursor.go
```

接口：

```go
type Capturer interface {
    Start(ctx context.Context, displayID string) error
    NextFrame(ctx context.Context) (*Frame, error)
    Close() error
}
```

Frame 不应默认持有 CPU BGRA slice。定义抽象 surface：

```go
type Frame struct {
    Width      int
    Height     int
    Timestamp  int64
    DirtyRects []Rect
    Moves      []MoveRect
    Surface    Surface
}
```

Windows 实现的 Surface 指向 D3D11 texture/handle。

### 7.2 DXGI

MVP 使用 Desktop Duplication API：

- 枚举 Adapter/Output。
- DuplicateOutput。
- AcquireNextFrame。
- 获取 dirty rect / move rect。
- 获取 PointerPosition / PointerShape。
- ReleaseFrame。

必须处理：

- DXGI_ERROR_ACCESS_LOST。
- 分辨率变化。
- 锁屏/切用户。
- 显示器拔插。
- RDP session 导致的 output 状态变化。

Access lost 不能导致 Agent 崩溃，应重建 capturer。

### 7.3 WGC

RD1 可只定义接口，RD2/RD3 再完成 WGC fallback。

## 8. Windows Encoder

### 8.1 MVP

目录：

```text
agent/desktop/codec/windows/
  encoder.go
  mf_h264.go
  device.go
```

接口：

```go
type Encoder interface {
    Configure(VideoConfig) error
    Encode(ctx context.Context, frame *Frame) ([]EncodedPacket, error)
    ForceIDR() error
    Reconfigure(VideoConfig) error
    Stats() EncoderStats
    Close() error
}
```

MVP 使用 Media Foundation H.264 Encoder，优先选择 Hardware MFT。

启动时探测：

```text
hardware H.264 encoder
hardware H.264 decoder
max resolution
supported profile
low latency flags
```

### 8.2 实现边界

首选通过 Go + Windows API 封装完成。

如果 D3D11 / Media Foundation 的 COM interop 在 Go 中导致大量不安全代码或稳定性风险，允许引入一个很薄的 Windows native shim，但必须满足：

- shim 只负责 capture/codec/render API。
- Session、网络、授权、ABR 保持 Go。
- API 使用显式 handle/lifetime。
- 不在 shim 中再造网络线程。
- 构建脚本可重复构建。
- 依赖许可证清晰。

不要直接把 Sunshine 源码嵌入项目。

### 8.3 Software Fallback

MVP 可允许 software fallback 用于功能验证，但发布默认必须优先硬件。

software 路径如果无法达到 1080p60，不应阻止产品可用，应自动降低 FPS/分辨率并在状态中显示。

## 9. Packetizer 与发送

目录：

```text
agent/desktop/transport/
  packetizer.go
  sender.go
  receiver.go
  channel.go
```

MVP 视频：

```text
H.264 access unit
  ↓
按 MTU 分片
  ↓
QUIC Datagram
```

目标 payload size 根据 tunnel 当前安全 MTU 计算，不写死以太网 1500。

发送策略：

- 不在队列中堆积过时 P-frame。
- 新 frame 到来时，如果旧非关键 frame 仍大面积积压，允许丢弃旧 frame。
- Keyframe/CONFIG 有更高优先级。
- 输入 channel 永远不等待视频队列清空。

## 10. Viewer

目录：

```text
agent/desktop/viewer/windows/
  window.go
  decoder.go
  renderer.go
  input.go
  overlay.go
```

### 10.1 Viewer Window

使用独立 Win32/D3D11 window。

功能：

- 无边框/普通窗口。
- 全屏。
- DPI aware。
- 正确处理 aspect ratio。
- 鼠标捕获/释放。
- overlay 自动隐藏。

### 10.2 Decoder

MVP Media Foundation H.264 hardware decoder。

Decoder 输入：

```text
CONFIG
IDR/P frame
```

输出尽量保留 GPU surface，避免下载 CPU 后再上传。

### 10.3 Renderer

D3D11 swap chain：

```text
decoder surface
  ↓
scale/convert if needed
  ↓
swap chain
```

需要记录：

```text
decode_ms
render_ms
present_ms
```

## 11. Input

目录：

```text
agent/desktop/input/windows/
  injector.go
  keyboard.go
  mouse.go
```

Wire 消息：

```text
KEY_DOWN
KEY_UP
MOUSE_MOVE
MOUSE_BUTTON
MOUSE_WHEEL
```

规则：

- 键盘/Button 走可靠高优先级 stream。
- Mouse move 可使用 Datagram；带 sequence，Host 只处理最新坐标。
- 坐标使用规范化绝对坐标或明确 display coordinate，避免分辨率变化后漂移。
- Viewer 失焦时释放所有本地记录的 pressed keys，防止粘键。
- Host Session Close 时释放可能残留的 modifier。

## 12. Cursor

Host 独立上报：

```text
position
visible
shape_id
hotspot
shape bitmap（shape 变化时）
```

Viewer 本地绘制。

不要把 cursor 永久烘焙进视频。

调试模式可以支持“host rendered cursor”用于问题排查，但不是默认路径。

## 13. Clipboard

RD1：

- UTF-8 / Unicode text。
- 双向。
- 会话级 enable/disable。
- 防止循环同步：clipboard content 带 origin/change id。

RD3 后续：

- 图片。
- 文件列表。
- 大内容分块。

## 14. RD2：P2P

不要再复制一份 RDP P2P。

重构方向：

```text
现有 agent/rdp/p2p
         ↓
抽象公共 candidate / punch / secure / path
         ↓
RDP backend 和 Relay Desktop 共用
```

允许分两步：

1. Relay Desktop 暂时调用现有候选/协调代码的适配层。
2. 测试稳定后再移动到通用 `internal/path` / `agent/path`。

严禁在重构和功能首通阶段同时改变打洞算法。

## 15. Path Manager

接口：

```go
type Path interface {
    Name() string
    SendDatagram([]byte) error
    OpenReliable(ctx context.Context, channel string) (io.ReadWriteCloser, error)
    Metrics() PathMetrics
    Close() error
}
```

候选：

```text
lan
p2p-v4
p2p-v6
relay-quic
relay-tcp-control-only
```

评分示意：

```text
score =
  rtt_ms * W1 +
  loss_pct * W2 +
  jitter_ms * W3 +
  queue_ms * W4 +
  relay_penalty
```

不要把常数散落在代码里，集中到策略结构并可测试。

切换：

- 新路径连续优于当前路径一段时间才升级。
- 当前路径严重恶化时快速降级。
- 路径切换不关闭 Desktop Session。
- 切换后强制统计 event；必要时请求 IDR。

## 16. RD2：ABR

目录：

```text
agent/desktop/adapt/
  estimator.go
  controller.go
  content.go
  presets.go
```

状态：

```go
type NetworkEstimate struct {
    DeliveryRate int64
    RTT          time.Duration
    Jitter       time.Duration
    Loss         float64
    QueueDelay   time.Duration
}

type MediaDecision struct {
    Width        int
    Height       int
    FPS          int
    TargetBitrate int
    ForceIDR     bool
}
```

### 16.1 控制周期

建议：

- transport metrics：100–250ms。
- media decision：250–500ms。
- resolution 改变设置最小间隔，例如 3–5s。
- bitrate 可更快变化。
- FPS 变化介于两者之间。

### 16.2 降级顺序

默认 desktop：

```text
降低 motion FPS
→ 降低 target bitrate
→ 降 encode resolution
→ 增大 QP/降低 chroma（未来）
```

gaming：

```text
保持 FPS
→ 降 bitrate
→ 降 resolution
```

因此 ABR 必须接收 scene。

## 17. Quality Presets

实现为参数边界而非固定值：

```go
type QualityPreset struct {
    MinBitrate int
    MaxBitrate int
    MaxFPS     int
    MaxWidth   int
    MaxHeight  int
    Prefer444  bool
    PreferHDR  bool
}
```

GUI 预设：

```text
自动
流畅
均衡
高清
极致
```

“自动”可以根据 Viewer 分辨率、Host GPU、路径和场景选择 preset baseline。

## 18. Stats 与 Overlay

统一：

```text
internal/desktop/stats.go
```

Host、Viewer 分别维护本地 stats；Controller 聚合展示。

至少：

```text
FPS capture/encode/decode/render
target/actual bitrate
RTT/jitter/loss
delivery rate
capture/encode/decode/render ms
send queue delay
dropped frames
IDR count
path
backend
codec
resolution
```

不要每帧通过 Server 上报。

Viewer overlay 本地 1s 聚合刷新。

## 19. Native RDP Backend 改造

RD0 后继续使用现有：

```text
127.0.0.1:auto → RDP P2P/Relay → target 127.0.0.1:3389
```

增强项：

1. Target capability 实时包含 nativeRdpAvailable。
2. `ConnectRemoteDesktop` 根据策略选择它。
3. 未来生成临时 `.rdp` 文件控制：
   - width/height。
   - fullscreen。
   - multimon。
   - smart sizing。
4. Native RDP 的画质/码率不复用 Relay Desktop encoder 参数。

## 20. GUI 状态模型

Remote Desktop 状态：

```text
idle
preparing
authorizing
negotiating
connecting
connected
reconnecting
closing
failed
```

附加：

```text
backend
path
target
error_code
error_message
stats
```

前端不得解析日志文本判断状态。

## 21. 错误码

新增稳定错误码：

```text
DESKTOP_NOT_AUTHORIZED
DESKTOP_TARGET_OFFLINE
DESKTOP_BACKEND_UNAVAILABLE
DESKTOP_NATIVE_RDP_UNAVAILABLE
DESKTOP_CAPTURE_FAILED
DESKTOP_ENCODER_UNAVAILABLE
DESKTOP_DECODER_UNAVAILABLE
DESKTOP_TRANSPORT_FAILED
DESKTOP_SESSION_EXPIRED
DESKTOP_PROTOCOL_MISMATCH
DESKTOP_RECONFIGURE_FAILED
```

错误消息用于用户展示，业务逻辑只匹配 code。

## 22. 配置

Agent 用户配置第一版：

```yaml
remote_desktop:
  enabled: true
  protocol: auto
  scene: auto
  quality: auto

  relay:
    codec: auto
    resolution:
      mode: follow_viewport
      max_width: 3840
      max_height: 2160
    fps:
      max: 60
    bitrate:
      mode: adaptive
      min: 1000000
      max: 30000000
```

默认值留在 RuntimeConfig，不强制全部写回 YAML。

高级内部参数（ABR gain、path score 权重等）不要第一版暴露到 GUI/YAML。

## 23. 测试计划

### 23.1 单元测试

必须覆盖：

```text
backend selector
capability intersection
quality preset
ABR hysteresis
path scoring
path switch hysteresis
packet fragmentation/reassembly
generation rollover
sequence/replay rejection
input coordinate mapping
clipboard loop prevention
```

### 23.2 模拟网络测试

增加可测试的 NetEm/transport shim，模拟：

```text
RTT 20 / 50 / 100 / 200 ms
loss 0 / 0.5 / 2 / 5 / 10 %
jitter
bandwidth 2 / 5 / 10 / 20 / 50 Mbps
突发丢包
带宽突然下降/恢复
```

验收 ABR：

- 不持续积压。
- 降码率快速。
- 恢复缓慢稳定。
- 不频繁改变分辨率。
- 输入 channel 延迟不跟视频 queue 一起飙升。

### 23.3 Windows 实机矩阵

至少：

```text
Windows 11 Home
Windows 11 Pro
Intel iGPU
NVIDIA GPU
AMD GPU
单显示器
双显示器
1080p
1440p
4K
```

网络：

```text
同 LAN
IPv4 NAT
IPv6
只可 Relay
UDP 丢包
Wi-Fi 抖动
```

### 23.4 回归

每阶段：

```bash
go test ./... -count=1
go vet ./...
```

Windows 构建：

```powershell