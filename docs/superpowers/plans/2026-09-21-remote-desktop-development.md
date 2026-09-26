# RelayProxy Remote Desktop 开发实施文档

> 日期：2026-09-21  
> 状态：实施中 — RD0 / RD1 已完成；RD2 P2P / ABR / 弱网 / 诊断 / WGC / D3D11 zero-copy 主链已完成；RD3 HEVC 4:2:0 与 Intel oneVPL HEVC 4:4:4 已形成完整代码链，4:4:4 编解码两端均已接入 D3D11 AYUV GPU surface，runtime GPU capability 已改为真实 D3D11 / codec 运行时探测并精确上报；当前进入 Intel 双机/驱动矩阵实测，NVIDIA / AMD 4:4:4 vendor-native 路径仍待实现。  
> 对应设计：`docs/superpowers/specs/2026-09-21-remote-desktop-design.md`  
> 基线：main 分支，现有 RDP M1–M5 已完成  
> 当前开发基线：`main`（PR #123 已合并，merge `8d46877291b9ecfc33be0e697c63cc361bc03b4e`；gofmt 修复 `83616916782571fe252e66b2014641d8d2ba7720` / `bd41520e3749a9f2eea5826ff056005f8af9d1a8` / `b491ef8de8d405eedcd9f2e682e3dcd15fdfc7f8`）

## 0. 当前进度

更新时间：**2026-09-25**

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
| 光标 | ✅ 已合并 main | Windows Host 以 60 Hz 独立采集位置/可见性，形状仅在 HCURSOR 变化时生成 PNG；可靠 session stream 传输，Controller 缓存形状，Wails Viewer 在视频表面本地叠加；PR #31 merge commit `f7101025c98fe1c09547a3a1203d20a6f3b6b888` |
| 剪贴板 | ✅ 已合并 main | Relay Desktop 可靠 session stream 双向同步 Unicode 文本；连接时仅建立基线不互相覆盖，后续变化按序号传播并做回环去重；GUI 可关闭同步，文件/图片暂不传输；PR #32 merge commit `010b8f5abc1408e3c824ebdaf13e943cf003dcc1` |
| DXGI / WGC Capture | ✅ 指定显示器链路已合并 main | PR #53 已打通 capability 驱动的 per-target 选屏、session-local `DisplayID`、单屏 DXGI/GDI Auto 捕获、光标局部坐标与 Windows `SendInput` 虚拟桌面坐标映射；未指定显示器时继续保留原虚拟桌面行为 |
| H.264 硬件编解码 | ✅ 端到端已合并 main | DXGI/GDI Capture → Media Foundation H.264 → RD/1 Datagram → Controller → WebCodecs Canvas 已贯通；硬件/软件 MFT、异步事件、ForceIDR、动态码率均已接入，并保留 JPEG fallback |
| H.264 Datagram 丢包恢复 | ✅ 已合并 main | Controller 检测 FrameID 缺口后停止提交 delta frame，经可靠 session stream 请求 IDR；WebCodecs 解码错误/队列过载也触发同一恢复流程；PR #30 merge commit `b9a074cc338dbfeb92acd570313bc243398ac888` |
| 原生 D3D11 Viewer | ✅ RD1 高性能链路已完成 | PR #33 原生 Viewer、PR #34 DXVA、PR #35 零拷贝视频、PR #36 GPU 光标均已合并；能力不足时保留 CPU/WebCodecs/JPEG 回退 |
| RD2 P2P / ABR / Stats | 🧪 核心能力已合并，进入验证/硬化 | Stats、码率 + scene-aware FPS ABR、Relay Desktop P2P、stale-frame/drop 与组合弱网验证已进入 main。PR #42–#47 完成 P2P 自动恢复、路径评分/滞回、direct RTT/Jitter、确定性 NetEm 与 send-queue ABR；PR #48 增加过期采样丢弃；PR #49 固化组合弱网下 ABR + path switch 联动；PR #50 补齐 Viewer 拥塞指标；PR #51 在持续严重压力下为 Office/Auto/Quality 动态降低采集 FPS，Gaming/Performance 保持 negotiated FPS，并在链路恢复后先恢复 bitrate、再慢恢复 FPS。PR #52 已补在线 Host capability snapshot / 显示器枚举，PR #53 已完成指定显示器捕获与输入/光标坐标映射，PR #54 已把 scene-aware ABR 场景选择开放到 GUI，PR #55 已补齐 negotiated media 与 Capture / Encoder / Decoder 实际 backend 诊断，PR #56 已完成 generation-aware Viewer rebuild，PR #57 已完成 Host Encoder generation rebuild 与运行期分辨率热切换，PR #58 已把 100% / 75% / 50% resolution tiers 接入 scene-aware ABR，PR #59 已补齐可导出的实机会话诊断时间序列，PR #60 已加入 schema v2 聚合 Summary、percentile 与路径/Generation/ABR/backend 分布统计；RD2 当前进入实机矩阵验证与参数标定阶段 |
| RD3 HEVC / 4:4:4 GPU | 🧪 代码链与 runtime capability 已完成，进入实机验证 | H.265 generation-aware Host/Viewer、Intel oneVPL HEVC RExt 8-bit 4:4:4、D3D11 AYUV GPU encode/decode zero-copy 已进入 main；4:4:4 CPU I444 路径继续作为 fallback。GPU capability 现按真实 D3D11 capture + encode/decode/display 端到端探测精确上报；下一步完成 Intel 双机/驱动矩阵，NVIDIA/AMD 4:4:4 仍需 vendor-native backend |

### 0.1 已合并主线的关键进度

- RD2 send-queue ABR 拥塞闭环已通过 PR #47 合并到 `main`（merge `35a69ac6b1900ad2a53a6c6718c5d8cc4e902354`）：H.264 与 JPEG Host 媒体发送路径现在测量每次 `Send()` 阻塞时间并以 EWMA 上报 `SendQueueDelayMs`；Controller ABR 新增 mild/normal/severe queue congestion 分级，在带宽下降导致发送队列堆积但尚未形成明显丢包时即可快速降码率，稳定恢复仍需要连续健康窗口。transport shim 新增运行中 `SetProfile`，可在同一 UDP socket 上切换限速/延迟阶段；同时增加带宽骤降/恢复、queue-delay 阻止过早恢复、真实媒体 Send 阻塞测量，以及 clean/lossy/queued/near-equal 默认路径评分标定测试。Go CI、UI 全量回归、Windows/macOS Desktop package 均通过。
- RD2 确定性弱网 transport shim 已通过 PR #46 合并到 `main`（merge `c8ba2163a708c51d4232a3da0c40f660c069e91e`）：新增 `internal/testnetem` 的 `net.PacketConn` 包装层，可配置固定 delay、jitter、随机丢包、周期性 burst loss 与写侧带宽限速，并通过固定 seed 重现相同网络序列；新增稳定 P2P 晋升、瞬时劣化恢复、持续劣化回退、路径不可用立即 failover 等路径切换场景；`desktop_media` 的认证 `PunchKeep/PunchAck` RTT 测试也实际经过 25 ms impairment shim，验证 direct-path probe 能观测到弱网层引入的延迟。Go CI、UI 全量回归、Windows/macOS Desktop package 均通过。
- RD2 direct-path RTT/Jitter 探针已通过 PR #44 合并到 `main`（merge `bd241b1daf7a60396559002b6a87d303938e6b81`）：不新增 wire message，而是在现有 `desktop_media` 安全域内复用认证 `PunchKeep/PunchAck`；Controller 的 P2P `PacketConn` 以 1 秒 cadence 发 probe，ACK 在媒体解码前被消费并计算真实直连 RTT，Session 以 EWMA 维护 RTT/Jitter。Relay 基线仍使用 Relay session probe，P2P 只使用直连 socket 指标，两者不再混用。Native RDP 保留原 10 秒 keepalive 行为。Go CI、UI 全量回归、Windows/macOS Desktop package 均通过。
- RD2 路径评分与切换滞回已通过 PR #43 合并到 `main`（merge `275f78d717befb5aafa241db915fc1dfc2c8d915`）：新增集中式 `PathScorePolicy` 与 `PathSwitchGate`，把 RTT/Jitter/Loss/QueueDelay/Relay penalty 权重和升级阈值统一收口到可测试策略结构；当前运行时只使用可明确归因到媒体路径的丢包与 Host send-queue delay，避免把可靠 Relay 控制流上的 RTT/Jitter 错当作 P2P 指标。Controller 在切到 `udp_p2p` 前保存 Relay 媒体质量基线，P2P 连续劣化超过滞回窗口会主动降级回 Relay；路径变化会请求 H.264 IDR，且主动降级继续复用原有 P2P 指数退避恢复。Go CI、UI 全量回归、Windows/macOS Desktop package 均通过。
- RD2 P2P 恢复硬化已通过 PR #42 合并到 `main`（merge `7bde35595f2cc3103b56d40dc0eab8f6347f926b`）：Controller 不再只尝试一次直连；首次 punch 失败或 `udp_p2p` 运行中丢失后继续使用 QUIC Datagram Relay，并按 2/4/8/16/30 秒上限退避自动重试，不重建 Desktop Session。短时抖动保留退避历史，直连连续稳定 20 秒后再清零；Controller Session 关闭会立即结束重试 worker。Go CI 与 UI CI（Windows/macOS）全部通过。
- RD2 Relay Desktop P2P 已通过 PR #39 合并到 `main`（merge `00c547e6e643664d34ec479c9ce4b9be4559baa2`），Go CI / UI CI 全部通过：新增独立 `desktop_media` P2P purpose，复用既有 rendezvous、候选发现、UDP punch、HMAC 与 replay protection；Controller 先建立 Relay Desktop 基线会话，再后台打洞并把视频 Datagram 热切到 `udp_p2p`；可靠 session stream 继续走 Relay，直连失败或关闭后自动回退 QUIC Datagram Relay。Target 侧按服务端写入的 `ClientDeviceID` 精确绑定媒体会话，并限制 Desktop P2P lease 不能打开 Native RDP TCP/3389，避免跨 purpose 权限复用。
- RD2 bitrate-only ABR 已通过 PR #38 合并到 `main`（merge `0cfb166c6a8df29579db07a0f7b377f17bec437d`）：500 ms 网络窗口基于丢包/Jitter/异常 RTT/新增 dropped frame 快速降码率，稳定窗口后缓慢恢复；用户设置码率保持为上限，Media Foundation H.264 通过 `ICodecAPI MeanBitRate` 热更新，无需重建 Encoder。
- RD2 Stats 基础已通过 PR #37 合并到 `main`（merge `f29cca17aeef2e104f240b397c3e00db92282f0d`）：Controller 聚合 RTT/Jitter/丢包/接收码率与帧率，Host 上报 Capture/Encode 指标，Native Viewer 上报 Decode/Render FPS 与耗时。
- GPU 光标合成已通过 PR #36 合并到 `main`：amd64 优先使用第二个 BGRA VideoProcessor stream 在 GPU 叠加远端光标；能力不足和 ARM64 自动回退 CPU 光标合成。
- 零拷贝视频呈现已通过 PR #35 合并到 `main`（merge `afcb5a8b71d800a6812301ed17ec1a299dfdbd04`）：Viewer 与 MF Decoder 共用 D3D11 device，DXGI NV12 surface 由 VideoProcessor 直接转换并呈现到 swap chain，保留 staging/CPU 回退。
- D3D11-aware / DXVA 解码已通过 PR #34 合并到 `main`（merge `9abb7fdd9b5c966fb0da3b988f9efc65db832a70`）：支持异步 Decoder MFT、`IMFDXGIDeviceManager` 和 DXGI NV12 surface，并在协商失败时安全回退系统内存硬解/软解。
- 原生 D3D11 Viewer 第一版已通过 PR #33 合并到 `main`（merge `9dcce5255ca7c49bb6e81bba6a0147b7d04980d4`）：Controller H.264 帧不再必须经过 JS/Base64/WebCodecs，可直接由 Go 侧 Media Foundation 解码并交给独立 Win32/D3D11 窗口显示；原生键鼠和独立远端光标也已接通。
- Unicode 文本剪贴板已通过 PR #32 合并到 `main`：可靠 session stream 双向同步 CF_UNICODETEXT，连接时建立基线，后续按序号传播并避免回环。
- 独立光标通道已通过 PR #31 合并到 `main`：Windows 光标位置/可见性与形状脱离视频帧传输，Viewer 本地叠加并按 hotspot 缩放定位。
- H.264 Datagram 丢包恢复已通过 PR #30 合并到 `main`：FrameID 缺口或 WebCodecs 解码失败会停止消费依赖帧并经可靠控制流请求 IDR，恢复到新 keyframe 后继续播放。
- RD/1 原生 QUIC Datagram 媒体 association 已进入 `main`。
- Relay Desktop 媒体双跳 Relay 已进入 `main`。
- Relay Desktop 授权已从 Native RDP 中拆分，`desktop.controller / desktop.host` 已进入 `main`。
- Windows Home 类型目标可以仅凭 `desktop.host` 被发现和授权，不要求本机 RDP Host 或 `127.0.0.1:3389`。
- 上述授权模型合并后的主线提交为 `a044e70dc93ab10b94c6ebe289a880c45a2cf2b4`。

### 0.2 当前开发状态

当前 Windows 可交互 MVP 与 RD1 高性能媒体链路均已进入 `main`；RD2 的 Stats、bitrate-only ABR、P2P 媒体直连、运行期自动恢复以及路径评分/切换滞回也已合并。当前不再以旧的 `feature/relay-desktop-windows-mvp` 为开发基线，后续工作从最新 `main` 拉分支继续。

当前已完成的端到端路径：

```text
Windows Host
  ↓
DXGI Desktop Duplication（不可用时 GDI）
  ↓
Media Foundation H.264（不可用时 JPEG）
  ↓
RD/1 Datagram
  ├─ 优先：authenticated UDP P2P（desktop_media / udp_p2p）
  └─ 回退：QUIC Datagram → Relay Server → QUIC Datagram
  ↓
Controller reassembly / H.264 loss recovery
  ↓
Native Win32 Viewer
  ↓
Media Foundation H.264 decode → DXGI NV12 surface → D3D11 VideoProcessor → swap chain
  ↘ 独立光标：amd64 优先第二 BGRA VideoProcessor stream；不支持时 CPU 合成
  ↘ 不支持共享 surface 时回退 NV12/BGRA CPU 路径
  ↕ reliable session stream 始终经 Relay
keyboard / mouse / cursor / clipboard / ping-pong / ABR control / stats
  ↓
Windows SendInput / CF_UNICODETEXT
```

该 JPEG 路径现在作为可运行的功能基线保留；后续 Capture / Codec / Viewer 可以独立替换，不需要重做授权、Relay Datagram 与输入控制链路。

### 0.2.1 指定显示器链路（已合并 PR #53）

- GUI 在每个支持 Relay Desktop 且上报多显示器的目标卡片上提供显示器选择器；默认“全部显示器”保持原虚拟桌面行为，不改变既有用户路径。
- `RemoteDesktopConnectOptions.DisplayID` 现在进入 session-local `HostConfig`，不会修改 Host 全局默认设置，也不会跨会话残留。
- Windows Host 每次会话重新枚举显示器并按 session-scoped HMONITOR ID 校验目标；显式选屏失败时直接报错，不会静默回退到其他显示器或整个虚拟桌面。
- 指定显示器通过 `screencapture.CaptureDisplay + BackendAuto` 捕获：DXGI Desktop Duplication 可用时优先使用，不可用时由 capture 层回退单显示器 GDI。
- Cursor channel 使用所选显示器 `Bounds` 生成局部坐标；光标位于其他屏幕时标记为不可见，Viewer 不会把其他屏幕的指针叠到当前画面。
- Windows `SendInput` 将 Viewer 的单屏归一化坐标重新映射到整个 virtual desktop 的绝对坐标，覆盖左侧负 X、副屏右侧及主屏上方负 Y 等布局。
- `DesktopVideoConfig.DisplayID` 与 `RemoteDesktopStatus.DisplayID/DisplayName` 回显当前会话选择，GUI session banner 可直接确认实机正在控制哪块屏幕。
- Windows 单测覆盖 session-scoped DisplayID、默认多屏虚拟桌面、左右双屏与负 Y 坐标映射；GUI 回归覆盖 capability 驱动的 per-target selector 与状态展示。

### 0.2.2 Scene 策略 GUI（已合并 PR #54）

- `DesktopScene` 与 scene-aware FPS ABR 已在 PR #51 落地，但此前 GUI 始终固定发送 `scene: auto`，用户无法选择 Gaming / Performance 的保帧率策略。
- PR #54 在 Remote Desktop 连接设置中新增“场景”：自动、办公、性能、游戏、画质，并直接透传现有 `RemoteDesktopConnectOptions.Scene`，不新增协议字段。
- Gaming / Performance 继续使用 negotiated FPS 作为 adaptive minimum，只通过 bitrate 应对拥塞；Office / Auto / Quality 在持续 severe pressure 下可降低采集 FPS。
- 连接摘要显示所选场景，设置帮助文字明确说明场景只影响自适应取舍，避免与“画质”预设混淆。

### 0.2.3 实机媒体链路诊断（已合并 PR #55）

- `RemoteDesktopStatus` 回显当前 negotiated `generation / codec / width / height / fps`，GUI 会话横幅可直接确认当前实际媒体配置。
- Host `DesktopSessionStats` 新增 `CaptureBackend / EncoderBackend / EncoderHardware`：Windows 单屏链路可区分 DXGI / GDI，H.264 可区分 Media Foundation 硬件或软件 MFT，JPEG fallback 明确标记 `jpeg-go`。
- 原生 D3D11 Viewer 通过现有 viewer stats 回报 `DecoderBackend / DecoderHardware`；Controller 聚合时保留 Host 诊断并叠加 Viewer 诊断，不互相覆盖。
- GUI 网络统计条现在同时显示 Path、Capture、Encoder(HW/SW)、Decoder(HW/SW)、RTT/Jitter/Loss、Queue、Dropped、吞吐和各阶段 FPS/耗时；WebCodecs fallback 在本地 decoder 活跃时标记为 `webcodecs`。
- 该诊断闭环用于后续 LAN / IPv4 NAT / IPv6 / Relay-only / Wi-Fi 抖动，以及 Intel / NVIDIA / AMD 实机矩阵，避免只根据 FPS 或日志猜测实际媒体路径。
- PR #55 不启用动态分辨率 ABR：Media Foundation 编码器对尺寸变化返回 `ErrEncoderRebuildRequired`；本轮 generation 分支先解决 Controller / WebCodecs / Native Viewer 的安全 generation 边界和 decoder rebuild，再进入 Host encoder rebuild 与尺寸 ABR。

### 0.2.4 Generation-aware 媒体切换基础（已合并 PR #56）

- 本地 `RemoteDesktopFrame / FrameSnapshot` 现在携带 RD/1 `Generation`，Viewer 不再只用可重复的 `FrameID` 识别帧。
- Controller 只允许媒体帧使用相同 generation 的 `DesktopVideoConfig`：旧 generation 的迟到 Datagram、以及新 generation 配置到达前抢跑的 Datagram 都会被丢弃，避免用错误尺寸/Codec 配置解释帧。
- 新 generation 配置生效时清空上一 generation 的 latest frame，并重置 H.264 recovery；一旦会话已进入非零 generation，迟到的 generation=0 / 更旧配置不会覆盖当前配置。
- WebCodecs Viewer 以 `(codec, generation)` 作为 decoder 生命周期边界；generation 改变即关闭旧 decoder，新 decoder 在收到 keyframe 前不接受 delta frame，并通过可靠控制流请求 IDR。
- 原生 Win32/D3D11 Viewer 在同尺寸 generation 变化时重建 Media Foundation Decoder；尺寸变化时先创建新的 D3D11 Viewer + 对应 Decoder，全部成功后再原子切换并关闭旧 pipeline，避免先拆现有画面再尝试恢复。
- Native Viewer 的 frame 去重改为 `(generation, sequence)`；因此新 generation 从 FrameID=1 重新编号也不会被上一 generation 的序号误判为重复。
- Windows 单测覆盖 generation/尺寸变化的 native rebuild 判定；Controller 单测覆盖 stale/future generation 拒绝、latest frame 清理和配置单调性；GUI 回归覆盖 WebCodecs generation reset / keyframe gate。
- PR #56 不主动发送尺寸 ABR 控制，只建立安全切换语义；当前分支继续完成 Host H.264 Encoder rebuild、可靠 VideoConfig 发布和新 generation keyframe。

### 0.2.5 H.264 运行期分辨率 Generation（已合并 PR #57）

- `DesktopVideoControl` 新增可选 `TargetWidth / TargetHeight`；仅 H.264 Relay Desktop 会话允许运行期切换，JPEG 和 Native RDP 不进入该路径。
- `DesktopVideoConfig / RemoteDesktopStatus` 新增 `MaxWidth / MaxHeight`，区分“当前实际编码尺寸”和“本次会话允许恢复到的分辨率上限”；Controller 会在发送控制消息前按 negotiated ceiling 拒绝越界请求。
- Host 收到尺寸控制后先重新 Capture，并使用目标宽高作为最大边界按源屏幕比例计算真实偶数尺寸，因此 16:10、超宽屏不会被强制拉伸成 16:9。
- 新尺寸先创建新的 Media Foundation H.264 Encoder 并请求 IDR；只有新 Encoder 创建成功、可靠 `VideoConfig(generation+1)` 发送成功后，才替换旧 Encoder。
- 新 generation 的 `FrameID` 从 1 重新开始，但 Generation 单调递增；Host 在首个 keyframe 产生前不发送任何 delta frame，Viewer 可从该 generation 独立恢复。
- 分辨率 generation 切换保留现有 ABR Controller 状态：只要 Codec、协商 FPS 和 bitrate ceiling 未变，就不会因尺寸变化清空 bitrate/FPS pressure/recovery 历史。
- H.264 运行期失败后的 JPEG fallback 同样保持 Generation 单调递增，例如 G2 H.264 失败后使用 G3 JPEG，避免被 Controller 的 stale-config 防护正确拒绝后造成黑屏。
- Wails Viewer 顶部新增“运行中分辨率”选择器；只在 Relay Desktop + H.264 时显示，并根据 `MaxWidth / MaxHeight` 禁用超出本次会话上限的档位。会话横幅同步显示实际尺寸与 `G<n>`，便于实机验证 Encoder / Decoder rebuild。
- PR #57 先提供用户手动运行期切换用于验证；当前分支继续把分辨率档位接入 scene-aware ABR，并为降档/升档增加更长的 pressure/recovery hysteresis。

### 0.2.6 Scene-aware Adaptive Resolution（已合并 PR #58）

- ABR 新增独立 resolution state：默认从 100% 开始，使用 100% → 75% → 50% 档位；Quality 场景最低保持 75%，Office / Auto / Gaming / Performance 最低可到 50%。
- 对已经很小的会话，`AdaptiveMinResolutionScale` 会根据 H.264 320×180 最小尺寸自动抬高分辨率下限，避免请求不可编码尺寸。
- 降档顺序保持保守：码率仍按 500 ms 网络窗口立即响应；只有 severe queue / severe loss / severe jitter / 持续 dropped frame 且码率已经降到约 ceiling 的 45% 以下后，resolution pressure 才开始累计。
- 默认 resolution downshift hold 为 8 个 500 ms 窗口；达到 hold 后每次只降一档，避免一次弱网事件直接从 100% 跳到 50%。Gaming / Performance 仍保持 negotiated FPS，但在持续严重压力下允许用分辨率换实时性。
- 恢复顺序严格串行：先按现有 StableWindows 慢恢复 bitrate → 再按 FPSRecoveryWindows 恢复 FPS → 两者均回到上限后，resolution 还需额外 16 个健康窗口才允许 50% → 75% → 100% 逐档恢复。
- `MediaDecision` 只有在 resolution tier 真正变化时设置 `ResolutionChanged`；Controller 仅在该标志为真时发送 `TargetWidth / TargetHeight`，普通 bitrate/FPS ABR 不会触发 Media Foundation Encoder rebuild。
- 新 `VideoConfig` 到达后会根据 `Width/Height` 相对 `MaxWidth/MaxHeight` 反向同步 ABR resolution scale，因此用户手动切到 720p 后，自动策略不会误以为仍处于 100% 而在拥塞时反向升档。
- H.264 `MaxWidth / MaxHeight` 现在使用本次会话实际最高编码尺寸，而不是用户输入的矩形上限；例如 16:10 源在 1920×1080 bound 下实际 ceiling 为 1728×1080，从而 75% / 50% 档位能按真实纵横比稳定缩放。
- Viewer 的运行中分辨率选择器新增动态“最高”项，可精确恢复到 1728×1080 等非标准 negotiated ceiling。
- 新测试覆盖：短时 severe pressure 不降分辨率、完整 hold 后 100→75、第二个 hold 后 75→50、Quality 75% 下限、Gaming 保 FPS/降尺寸、恢复顺序、手动 generation scale 同步，以及 ABR control 只在 tier change 时携带尺寸字段。

### 0.2.7 可导出实机会话诊断（已合并 PR #59）

- Controller 新增 session-local diagnostics recorder，默认每 500 ms 记录一条样本，固定最多 1200 条，即保留最近约 10 分钟；使用有界环形缓冲，长时间会话不会无限增长内存。
- 诊断样本使用独立的短窗口统计，而不是 GUI `Snapshot()` 的全会话累计平均：记录窗口 RX bitrate / RX FPS / packet loss，并叠加当前 RTT、Jitter、Host Send Queue Delay、Dropped Frames、Capture/Encode/Decode/Render 指标。
- 每条样本同时保存当时的 `DesktopVideoConfig`（Generation / Codec / 当前分辨率 / ceiling / FPS / bitrate）和本轮 ABR decision（reason、目标 bitrate/FPS、resolution scale、是否触发 generation rebuild），可直接回看网络变化与自适应动作的因果时间线。
- Path 切换会重置 diagnostics 短窗口基线，避免第一条 `udp_p2p` 样本混入上一条 Relay 路径的 bytes/loss，反向切换同理。
- 报告额外保留 scene、连接 options、当前 config/stats；不包含 session token、剪贴板正文、键盘输入或视频帧内容。
- Agent 在 Relay Desktop 断开时保存最后一份 diagnostics report，因此测试结束后再点击导出仍可取到本次会话数据。
- Wails 新增 `GetRemoteDesktopDiagnostics`，Remote Desktop 页面增加“导出诊断”按钮，直接保存带 schemaVersion 的 JSON；文件名包含目标 ID 与生成时间，便于多机矩阵归档。
- 新测试覆盖 500 ms window bitrate/FPS/loss、diagnostics 与 ABR loss window 相互独立、1200 样本有界保留、报告副本隔离，以及 GUI/Wails 导出绑定。

### 0.2.8 诊断聚合 Summary（已合并 PR #60）

- Diagnostics schema 升级到 v2，在原始 500 ms samples 之外新增 `summary`，用于不同网络、设备和 GPU 样本的直接比较。
- Summary 为 RTT、Jitter、Loss、Send Queue Delay、Actual Bitrate、Receive/Decode/Render FPS、Capture/Encode/Decode/Render latency 计算 `min / avg / p50 / p95 / max`；不可用的 RTT/阶段耗时不会以 0 污染 percentile。
- 汇总 `PathSwitches / GenerationChanges / ABRChanges / ResolutionChanges / DroppedFrames`，可以直接量化一次会话中路径振荡、Encoder/Decoder rebuild 与画质降档频率。
- 同时统计 `Paths / Codecs / Resolutions / CaptureBackends / EncoderBackends / DecoderBackends / ABRReasons` 的样本分布，以及硬件编码/解码样本数，方便确认 Intel / NVIDIA / AMD 或 WebCodecs/JPEG fallback 的真实执行路径。
- Summary 保留 `SessionDurationMs` 与实际 `SampleSpanMs`，避免把短测试与长测试直接按样本数误比较。
- Percentile 使用确定性的 nearest-rank 计算；原始 samples 继续保留，因此需要更复杂统计时仍可离线重算。
- 新测试固定 p50/p95、路径/Generation/ABR/Resolution 事件计数、backend/codec/resolution 分布和不可用值过滤行为。

### 0.2.9 Host BGRA Fast Path（已合并 PR #62）

- Windows `go-mswin/screencapture` 的 DXGI Desktop Duplication / GDI stream 原生输出均为 top-down BGRA，并保留真实 stride；此前 Relay Desktop 会先逐像素复制/交换为 RGBA，再由 Media Foundation H.264 路径逐像素转 NV12。
- Encoder `RawFrame` 新增 `PixelFormatBGRA`，`BGRAtoNV12` 可直接消费带 padding RowPitch 的 BGRA，BT.709 limited-range 转换结果与现有 RGBA 路径保持一致。
- `windowsCapture` 新增可选 borrowed raw capture 接口：像素只借用到下一次 `CaptureRaw / Capture / Close`；Host 同步完成 NV12 转换后才请求下一帧，不跨帧持有底层 DXGI/GDI buffer。
- H.264 在捕获原生尺寸不需要缩放时直接走 `BGRA → NV12 → Media Foundation Encoder`，省掉一遍全帧 BGRA→RGBA staging；当用户设置较低最大分辨率或 ABR 降分辨率时，仍自动回退现有 RGBA scale 路径。
- 分辨率恢复到本次会话最高尺寸后会再次尝试 BGRA direct fast path；因此 resolution ABR 不会永久关闭该优化。
- 原 RGBA/JPEG 路径保留 DXGI idle-frame cache：静止桌面不会因为本轮重构反复复制同一 staging texture。
- `DesktopSessionStats.CaptureFormat` / diagnostics `CaptureFormats` 新增 `bgra-direct` 与 `rgba` 可观察值；Viewer 实时统计显示 `Capture dxgi/bgra-direct` 等实际链路。
- Capture backend 改为每次 stats 上报时读取当前 backend，因此 DXGI 运行期失败转 GDI 后，GUI/diagnostics 不再错误保留 `dxgi` 标签。
- 本轮仍然是 CPU BGRA→NV12；最终目标依旧是 `D3D11 texture → GPU scale/color convert → NV12 surface → hardware encoder`。当前 capture dependency 只提供 DXGI Desktop Duplication / GDI，WGC 需要后续单独实现 WinRT capture backend 或替换/扩展 capture 层。

### 0.2.10 Capture Backend Policy（已合并 PR #63）

- `RemoteDesktopConnectOptions` 新增 `CaptureBackend`：`auto / dxgi / gdi / wgc`；`wgc` 先作为 wire/API 预留值，当前 Windows Host 明确返回“未实现”，不会静默当成 Auto。
- `HostConfig` 把 capture preference 保持为 session-local，不修改 Host 全局默认；Diagnostics 导出的连接 options 会自然记录请求值，便于同一机器做 DXGI/GDI A/B。
- `auto` 保持现有行为：单屏/指定屏幕优先 `screencapture.BackendAuto`，必要时回退虚拟桌面 GDI。
- 显式 `dxgi` 映射到 Desktop Duplication 并采用 strict policy：初始化失败、显示器枚举失败都直接结束会话，不允许偷偷切 GDI。
- 多显示器“全部显示器”当前依赖 virtual desktop GDI；因此显式 DXGI 必须选择具体显示器。该约束在 Host 侧强制，不依赖 GUI 正确性。
- 显式 `gdi` 固定 GDI；选择具体显示器时使用 per-display GDI stream，未选具体显示器时允许 virtual desktop GDI。
- GUI 连接设置新增“采集：自动 / DXGI / GDI”，连接摘要显示显式 Capture backend；帮助文字明确该选项主要用于实机矩阵和问题定位。
- 当前 capability snapshot 继续只公布真实可用的 GDI/DXGI；在 WinRT WGC backend 真正实现之前 GUI 不提供 WGC 选项。
- 测试覆盖协议→HostConfig 透传、空值归一到 Auto、DXGI/GDI/WGC 映射、strict backend 识别和 DXGI concrete-display 约束。

### 0.2.11 Windows Capture Stream Abstraction（已合并 PR #65）

- 新增 backend-neutral `windowsCaptureFrame`：只暴露 BGRA `Pix / Width / Height / Stride / Sequence / At`，保留真实 padded RowPitch，不再让 Host capture 主循环依赖 `screencapture.Frame`。
- 新增 `windowsFrameStream`：统一 `Frame / WaitFrame / Backend / Close` 生命周期；`windowsCapture` 只依赖该接口，borrowed BGRA fast path、RGBA fallback 与 idle-frame cache 均保持原语义。
- 新增 `windowsFrameStreamFactory`：负责按 `auto / dxgi / gdi / wgc` preference 创建具体 stream。当前 `screencaptureFrameStreamFactory` 适配现有 DXGI Desktop Duplication / GDI。
- 第三方 `*screencapture.Stream` 与 `screencapture.Frame` 被限制在 adapter 文件内；后续 WinRT WGC backend 只需新增另一套 factory/stream 实现，不需要改 H.264 Host 主循环、Stats 或 ABR。
- PR #65 合并时 `WGC` 仍显式返回 unavailable；该限制已被隔离在 adapter/factory 边界，Host capture 主逻辑无需感知 WGC。
- DXGI/WGC 仍要求 concrete display，Auto/GDI 保留 virtual desktop 行为；错误提示改为 backend-neutral，避免未来 WGC 复用时错误显示 DXGI。
- Windows-only 单测改为验证通用 frame 的 padded stride / row 边界、capture preference→当前 adapter 映射、WGC unavailable sentinel 与 concrete-display 约束。

### 0.2.12 Windows Graphics Capture（已合并 PR #66）

- Windows amd64 新增真实 `wgcFrameStream`：通过 `IGraphicsCaptureItemInterop::CreateForMonitor` 创建指定显示器的 `GraphicsCaptureItem`。
- 使用 `Direct3D11CaptureFramePool.CreateFreeThreaded`，Agent 无需依赖 UI `DispatcherQueue`；WGC 可在现有后台 Host capture 链路中工作。
- 新建带 `D3D11_CREATE_DEVICE_BGRA_SUPPORT` 的 D3D11 device，并通过 `CreateDirect3D11DeviceFromDXGIDevice` 包装为 WinRT `IDirect3DDevice`。
- 每次取帧会 drain FramePool，只保留最新 frame，避免网络/编码阻塞恢复后追赶历史帧；这与现有 stale-frame/drop 实时性策略保持一致。
- WinRT `IDirect3DSurface` 通过 `IDirect3DDxgiInterfaceAccess` 解包为 `ID3D11Texture2D`，复制到可复用 staging texture 后 `Map` 为 padded BGRA RowPitch，直接适配 `windowsCaptureFrame`。
- WGC 自带 cursor composition 被关闭，继续使用 Relay Desktop 已有的独立 cursor channel，避免视频帧与本地 cursor 重复叠加。
- monitor content size 改变时自动 `Recreate` FramePool；staging texture 按 texture desc 复用/重建。
- `captureBackend=wgc` 已路由到真实 WGC stream；非 amd64 Windows 保留明确 unavailable fallback，不影响 Auto / DXGI / GDI。
- 依赖固定为正式兼容组合 `go-bindings-winrt v0.6.0 + go-bindings-win32 v0.2.1`；不依赖 unreleased WinRT bindings。
- 当前阶段仍是 D3D11 → staging CPU BGRA readback，再进入现有 NV12/H.264；WGC capture 已是真实 GPU surface 来源，但 capture→encoder 零拷贝仍属于后续优化。
- `GraphicsCaptureSession.IsSupported()` 已接入 capability snapshot：仅 Windows amd64 且运行时确认支持 WGC 时才上报 `wgc`；Go/UI/Windows/macOS CI 均已通过，PR #66 merge `9d91c0d951ce5acb6cc47f36e04506982d6ac776`。

### 0.2.13 Capability-aware Capture Selector（已合并 PR #67）

- GUI 不再静态写死 DXGI/GDI；采集下拉框根据当前 Relay Desktop 目标 `capabilities.captures` 动态生成 `WGC / DXGI / GDI`。
- WGC 只有目标明确上报时才出现；旧节点没有 capture capability snapshot 时仅保留历史 DXGI/GDI 兼容选项，不推断 WGC。
- 当前选择的后端在设备刷新后如果不再存在，会自动回到 `auto`，避免显示器/驱动/系统能力变化后保留失效配置。
- 明确 Relay Desktop，或 Auto 但目标没有 Native RDP、因此必然使用 Relay Desktop 时，连接前会校验显式采集后端是否由该目标上报；不支持时在本地直接提示，不发起注定失败的会话。
- Auto 协议仍由现有 `SelectBackend` 决定 RDP/Relay，不因为选择采集后端而改变协议选择语义。
- 帮助文案明确 WGC/DXGI/GDI 都可用于实机 A/B；WGC 不会在未确认支持的目标上出现；多屏“全部显示器”仍需要 GDI。
- GUI/Windows/macOS CI 已通过，PR #67 merge `e1841d2da67d578c10532d84fe16b251b2ec462a`。

### 0.2.14 Automatic Capture Fallback（已合并 PR #68）

- 单屏或已选择具体显示器时，`captureBackend=auto` 不再直接委托给第三方 BackendAuto，而是由 Relay Desktop 显式编排候选后端。
- 保留性能优先顺序：可 Desktop Duplication 的显示器先尝试 `DXGI`；失败后若 Windows runtime 支持 WGC，则尝试 `WGC`；最后回退 `GDI`。
- 不可 Duplication 的显示器直接从 `WGC` 开始；不支持 WGC 时直接使用 `GDI`。
- 显式 `wgc / dxgi / gdi` 仍保持 strict semantics：初始化失败直接返回错误，不静默切换到其它后端，保证 A/B 诊断结果可信。
- Auto 只在候选初始化失败时向下回退；成功后通过现有 Session Stats / Diagnostics 上报实际 Capture backend。
- 新增纯编排回归测试，固定 `DXGI → WGC → GDI` 顺序、WGC 不可用时的 `DXGI → GDI`、不可 Duplication 时的 `WGC → GDI`，并验证成功后立即停止继续尝试。
- GUI 帮助文案同步说明新的 Auto 回退顺序。

### 0.2.15 WGC Frame-rate Cap（已合并 PR #69）

- WGC 初始化不再忽略 HostConfig.MaxFPS；Windows amd64 在创建 GraphicsCaptureSession 后尝试 QueryInterface 到 IGraphicsCaptureSession5。
- 支持 Session5 的系统会通过 MinUpdateInterval 把 negotiated MaxFPS 转成 Windows.Foundation.TimeSpan（100 ns tick），降低 WGC 在 Host 只消费 10/15/24/30 FPS 时仍按高刷新率生成 GPU frame 的无效开销。
- Session5 是可选能力：旧 Windows 不支持该接口、或 SetMinUpdateInterval 被 runtime 拒绝时，不中断 WGC，会继续依赖 Relay Desktop Host ticker / latest-frame drain 保证输出帧率与实时性。
- 显式 WGC、Auto 选到 WGC 两条路径都会复用同一限制，不改变 DXGI/GDI 行为。
- Windows amd64 单测固定 1 / 30 / 60 FPS 与极高 FPS 的 TimeSpan 换算，并覆盖 0/负数代表“不设置 runtime 限制”。
- PR #69 已合并到 `main`，merge `baf5c35e129c607cbdba437d9496e70391baf9ed`。

### 0.2.16 Runtime Adaptive Capture FPS（已合并 PR #70）

- Host 新增可选 `CaptureFPSController`；JPEG 与 H.264 两条运行期 FPS 更新路径在重置发送 ticker 后，同时把新的 `TargetFPS` 下推给 capture backend。
- Windows `windowsCapture` 把运行期 FPS 控制转发给当前 frame stream；不支持动态 producer rate 的 DXGI/GDI 保持现有行为，不影响会话。
- WGC 实现 `SetFrameRateLimit`：ABR 从 30 FPS 降到 15/10 FPS 时会同步更新 `IGraphicsCaptureSession5.MinUpdateInterval`，减少后台 GPU frame 生成和随后丢弃的无效工作。
- Session5 仍保持 best-effort 兼容语义：旧 Windows 没有该接口、或 runtime 拒绝设置时不终止远程桌面，会继续由 Host ticker 保证最终发送帧率。
- 新增 Host optional controller 与 Windows stream delegation 单测，固定运行期 FPS 控制链路。
- PR #70 已合并到 `main`，merge `60d2fd2d4fe350e3196fe2cf361382b9d31a5643`。

### 0.2.17 WGC FrameArrived Event Wakeup（已合并 PR #71）

- `CreateFreeThreaded` WGC frame pool 注册 `FrameArrived` typed handler，替换 `WaitFrame` 原先每 4 ms 主动轮询 `TryGetNextFrame` 的等待方式。
- WinRT 回调只向容量 1 的 channel 做非阻塞 signal，重复通知自动合并；D3D11 texture 获取、latest-frame drain、staging copy 与 CPU readback 仍全部在正常 capture consumer 路径执行。
- `WaitFrame` 等待新帧时不再长期锁住 OS thread；收到事件后才重新进入 WinRT/D3D11 consumer 路径。
- Close 生命周期先停止 capture session，再注销 `FrameArrived` token、关闭 frame pool，最后释放 Go typed handler，避免 native callback 持有悬挂 delegate。
- 新增 signal coalescing 单测；Go CI、UI full regression、Windows/macOS desktop package CI 全部通过。
- PR #71 已合并到 `main`，merge `6df077d1a237b4ebd08a377fa19cae21de943588`。

### 0.2.18 Media Foundation D3D11 Input Surface（已合并 PR #72）

- Codec 新增 `D3D11EncodeFrame` / `D3D11Encoder` 可选接口，描述外部 `ID3D11Texture2D` resource、subresource、尺寸与时间戳，不改变现有 CPU `RawFrame` encoder contract。
- Windows H.264 encoder 新增 `OpenMFH264EncoderWithD3D11`；使用已有 DXGI device manager 基础设施把外部 D3D11 device 通过 `MFT_MESSAGE_SET_D3D_MANAGER` 绑定到 D3D11-aware hardware encoder MFT。
- D3D11 模式只在 MFT 明确上报 `MF_SA_D3D11_AWARE` 时成立；不支持的 encoder 不伪装成零拷贝路径。
- 新增 `MFCreateDXGISurfaceBuffer` input sample：NV12 `ID3D11Texture2D` 可以直接封装为 `IMFMediaBuffer/IMFSample` 并送入同步或异步 `ProcessInput`，不经过 CPU `frameToNV12` / memory buffer copy。
- 原有 `OpenMFH264Encoder`、CPU NV12/BGRA/RGBA 输入与非 Windows stub 保持兼容；只有显式选择 D3D11 encoder API 才要求外部 device。
- 当前尚未把 WGC BGRA capture texture 接到该入口；下一步是在同一 capture device 上用 D3D11 VideoProcessor 做 BGRA→NV12（同时承担 resolution scale），再把生成的 NV12 texture 交给本轮新增的 `EncodeD3D11`。
- PR #72 已合并到 `main`，merge `39d436279cf684d71b6c83ff105f242f576c2d61`；Go CI（含 race/benchmark）、UI full regression、Windows/macOS desktop package 均通过。首次 Go 全量测试仅遇到既有 `internal/tunnel/TestTLSTunnelMultiplexing` QUIC flaky，原 head 重跑后通过，未修改无关 tunnel 代码。

### 0.2.19 WGC GPU Surface + BGRA→NV12 VideoProcessor（已合并 PR #73）

- Host 新增可选 `D3D11CaptureSource`：捕获端可以借出 D3D11 device + texture resource；texture 持有独立 COM 引用并通过 `Close()` 精确释放，device 仅在 capture session 生命周期内借用。
- WGC 新增 `WaitD3D11Frame`，直接从 WinRT `IDirect3D11Surface` 获取 `ID3D11Texture2D`，不创建 staging texture、不执行 `CopyResource → Map → CPU BGRA`；现有 CPU `Frame/WaitFrame/CaptureRaw` 路径保持不变。
- WGC D3D11 device 创建优先启用 `D3D11_CREATE_DEVICE_VIDEO_SUPPORT`，为 VideoProcessor 提供完整视频能力；若驱动拒绝该 flag，会回退原有 BGRA-only device，保证 WGC 本身仍可工作。
- Codec 新增 `D3D11NV12Converter`：在外部 D3D11 device 上创建 VideoProcessor，验证 BGRA input / NV12 output format support，并维护可复用 NV12 output texture。
- Converter 使用 `VideoProcessorBlt` 同时完成 BGRA→NV12 色彩转换和输入尺寸→编码尺寸缩放，因此后续 resolution ABR 不需要 CPU resize；BGRA 输入允许奇数尺寸，NV12 输出仍强制偶数尺寸。
- `Convert` 返回借用的 NV12 `D3D11EncodeFrame`；调用方必须在下一次 Convert/Close 前同步送入 PR #72 新增的 `EncodeD3D11`，避免额外 texture allocation/copy。
- 新增 converter 配置、D3D11 capture frame COM lifetime 与 Windows surface delegation 单测；当前尚未切换 Host H.264 主循环，下一轮会用 capability/fallback 方式把 WGC GPU surface → converter → Media Foundation D3D11 encoder 串成真实运行路径。
- PR #73 已合并到 `main`，merge `1a0840673a1a17cd862753e38c330f8b57d997d1`；Go CI（重跑既有 tunnel flaky 后含 race/benchmark）、UI full regression、Windows/macOS desktop package 全部通过。

### 0.2.20 Host H.264 WGC Zero-copy Pipeline（已合并 PR #74）

- Host H.264 会话继续用现有首帧逻辑确定 negotiated encode size，随后探测可选 `D3D11CaptureSource`；只有 WGC surface、VideoProcessor converter 和 D3D11-aware Media Foundation encoder 三者全部成功时才启用 GPU path。
- GPU 初始化失败不会中断 H.264：直接保留现有 `BGRA/RGBA → CPU NV12 → Media Foundation` 路径；因此不支持 VideoProcessor、D3D11-aware encoder 或 WGC surface 的机器行为不变。
- GPU 稳态路径为 `WGC ID3D11Texture2D(BGRA) → VideoProcessor(scale + NV12) → MFCreateDXGISurfaceBuffer → EncodeD3D11`，不执行 staging `CopyResource/Map`、CPU BGRA copy、CPU resize 或 `frameToNV12`。
- Stats 的 `CaptureFormat` 在 GPU path 下上报 `d3d11-nv12`，encoder backend 继续由 PR #72 上报 `media-foundation-d3d11`，可通过导出 diagnostics 确认真实零拷贝路径。
- adaptive FPS 继续只控制 capture producer/ticker；D3D11 converter/encoder 保持 negotiated FPS 配置，不因临时降帧重建。
- adaptive resolution 在 GPU path 下直接抓取 D3D11 source，按纯尺寸计算生成目标偶数分辨率，成对重建 converter + D3D11 encoder generation，再发送新的 `VideoConfig/G<n>`；不再为了分辨率切换读取 CPU RGBA。
- 如果显示器运行期改变源分辨率但编码输出分辨率不变，仅重建 VideoProcessor input geometry，不重建 encoder generation。
- WGC 300 ms 内无新 D3D11 frame 视为 dropped frame 并继续 GPU path，不因静态画面回退到 CPU staging。
- 资源析构顺序固定为 encoder → converter/output texture，generation 切换和 session close 保持一致，避免异步 MFT flush 仍引用最后一个 NV12 surface。
- PR #74 已合并到 `main`，merge `6cb0d3291d483d02149fab6eeee6c93208be1e69`；Go CI、race、benchmark、UI full regression、Windows/macOS desktop package 全部通过。

### 0.2.21 GPU Runtime → CPU H.264 Generation Migration（已合并 PR #75）

- WGC zero-copy 会话启动成功后，运行期出现非 timeout 的 D3D11 capture 错误、VideoProcessor 几何重建失败或 `EncodeD3D11`/转换失败时，不再立即退出 H.264 并触发 JPEG fallback。
- Host 新增 CPU H.264 generation 迁移事务：先按当前分辨率/bitrate/FPS 打开新的 CPU-input Media Foundation H.264 encoder，再发送新的 `VideoConfig(G+1)` 与独立 SPS/PPS，随后要求新 generation 首帧 IDR。
- Viewer 因 generation 变化会走现有 generation-aware decoder rebuild，不会把 GPU encoder 的 codec state 继续用于 CPU-input encoder。
- 迁移成功后当前 session 固定留在 CPU H.264，不自动重新探测 GPU，避免故障设备上 GPU ↔ CPU 来回振荡；后续 bitrate/FPS/resolution ABR 继续沿用现有 CPU H.264 路径。
- 资源析构顺序仍保持 encoder → converter：新 generation 已成功广告后才关闭旧 D3D11 encoder，然后释放 converter/output texture，避免异步 MFT 仍引用最后一个 NV12 surface。
- 如果 CPU H.264 generation 无法创建或新的 `VideoConfig` 无法发送，才让现有 `h264RuntimeError` 边界继续升级到 JPEG generation fallback。
- 新增 CPU fallback generation 单测，覆盖 generation 递增、独立 sequence header、ForceIDR 与 generation overflow 不应调用 encoder opener。

- PR #75 已合并到 `main`，merge `0833f53b3c7da42903ea666ac39eba623e91f858`；Go CI、race、benchmark、UI full regression、Windows/macOS desktop package 全部通过。

### 0.2.22 RD3 HEVC / H.265 Media Foundation Probe（已合并 PR #76）

- 新增 `H265Probe`，沿用 H.264 probe 的硬件/软件 encoder/decoder 计数语义，并可转换为内部 `DesktopCodecCapability{Codec:"h265"}` 供后续 RD3 使用。
- Windows 新增 `MFVideoFormat_HEVC` 对应的 Media Foundation MFT 枚举：分别探测 `NV12 → HEVC` encoder 与 `HEVC → NV12` decoder，硬件和软件 transform 分开计数；探测只枚举 activation，不占用实际 GPU encoder session。
- Windows Host 启动时额外记录 H.265 probe 日志，便于 Intel / NVIDIA / AMD 实机矩阵直接确认 `hwEnc/hwDec/swEnc/swDec`，但在线 capability snapshot 仍然只广告已经具备完整运行链的 H.264。
- `NormalizeCodecPreference` 暂不接受 `h265/hevc`；新增回归测试固定“探测能力 ≠ 用户可选择能力”，防止 Viewer decoder / Host stream path 尚未实现时误开放半成品选项。
- 非 Windows 平台提供明确 unavailable stub；现有 H.264/JPEG 运行逻辑、Auto capture 策略和 GUI 均不改变。
- 下一步将在此 probe 基础上参数化 Media Foundation transform 层，增加 HEVC encoder/decoder 与 Annex-B VPS/SPS/PPS / codec string 处理，再完成 Viewer generation-aware H.265 解码后才开放 capability/选择器。

- PR #76 已合并到 `main`，merge `c008e1fb0ed5a28cdd10913ce4aafcd1310f800c`；Go CI、race、benchmark、UI full regression、Windows/macOS desktop package 全部通过。

### 0.2.23 RD3 HEVC Encoder Core（已合并 PR #77）

- Media Foundation encoder transform 从 H.264 写死输出改为内部 `mfVideoEncoderSpec`：codec 名称、输出 subtype、profile、level、同步/异步输出标记和 D3D11 surface input 均由 spec 驱动；现有 `MFH264Transform` / `MFH264Encoder` 对外 API 保持兼容。
- 新增隐藏 `MFH265Encoder`，支持 CPU RawFrame → NV12 → HEVC 和 D3D11 NV12 surface → HEVC 两条路径，并复用现有异步 `IMFMediaEventGenerator`、ForceKeyFrame、动态 mean bitrate、stats 与安全 Close 生命周期。
- H.265 output media type 使用 `MFVideoFormat_HEVC`、Main 4:2:0 8-bit profile，并按当前 Width/Height/FPS/bitrate 选择最低满足约束的 HEVC Main-tier level；4K60/100 Mbps 会自动提升到可承载该码率的 level，而不是固定 5.1。
- 新增 HEVC Annex-B 工具：识别 VPS(32) / SPS(33) / PPS(34)，并可在 IDR 前补独立 sequence header；后续 Host generation 切换可直接沿用 H.264 的“新 generation + 独立参数集 + IDR”事务。
- 同步和异步 MFT output 现在按 encoder spec 写入 `EncodedPacket.Codec`，H.264 继续为 `h264`，隐藏 HEVC encoder 输出为 `h265`。
- 新增 HEVC level、VPS/SPS/PPS、sequence-header 注入和 Encoder / D3D11Encoder 接口契约测试；非 Windows 提供 HEVC encoder unavailable stub。
- 本分支不修改 `NormalizeCodecPreference`、Host capability 广告、Controller/GUI 选择器、RD/1 会话协商或 Viewer decoder；因此 H.265 仍不可被用户选择，避免 encoder 单边就绪造成半链路状态。
- 下一步：实现 Media Foundation HEVC decoder（含 D3D11 NV12 output）与 HEVC codec string / generation-aware Viewer rebuild，再接入 Host H.265 generation 和 capability negotiation。

- PR #77 已合并到 `main`，merge `89218c686a9696c340203328bcd39ef1bb320123`；Go CI 重跑通过 format/vet/full test/race/benchmark，UI full regression、Windows/macOS desktop package 全部通过。首轮 Go CI 唯一失败为既有 `internal/tunnel/TestTLSTunnelMultiplexing` 的本机 QUIC timeout，重跑即通过，与 HEVC 变更无关。

### 0.2.24 RD3 HEVC Decoder Core（已合并 PR #78）

- Media Foundation decoder 从 H.264 写死输入改为内部 `mfVideoDecoderSpec`，由 spec 指定 codec 标签与压缩输入 subtype；现有 `OpenMFH264Decoder` / `OpenMFH264DecoderWithD3D11` API 保持兼容。
- 新增隐藏 `OpenMFH265Decoder` / `OpenMFH265DecoderWithD3D11`，使用 `MFVideoFormat_HEVC → NV12` transform，并复用现有同步/异步 MFT 处理、flush、stream-change 与生命周期。
- HEVC decoder 直接复用 H.264 已验证的 D3D11 decoder manager：硬件 MFT 可输出 NV12 DXGI surface；使用 Viewer 共享 D3D11 device 时继续返回零拷贝 `D3D11Surface`，否则可按需 staging readback 为紧凑 NV12。
- decoder info 增加 codec identity，异步 ProcessInput/ProcessOutput、空 access unit 和 stream 错误不再写死 H.264 文案。
- 非 Windows 增加 H.265 decoder unavailable stub，并新增 H.264/H.265 CPU + D3D11 decoder entry-point 编译契约测试。
- 本分支仍不开放 `NormalizeCodecPreference("h265")`、Host capability、Controller/GUI selector 或 RD/1 会话协商；下一步先在 Viewer 增加 HEVC codec string/参数集解析与 generation-aware decoder rebuild，再接 Host H.265 generation。

- PR #78 已合并到 `main`，merge `ae9d25eabac7a7d5ec9398a3c7f60bbe5a695206`；Go format/vet/full test/race/benchmark、UI full regression、Windows/macOS desktop package 全部通过。

### 0.2.25 RD3 HEVC Viewer Generation（已合并 PR #79）

- Controller 的压缩帧快照从 H.264-only 扩展为 codec-aware：`h264 → video/h264`、`h265 → video/h265`，保留 `CodecString`、generation、尺寸、timestamp 与 keyframe 元数据；未知已配置 codec 不再误按 JPEG 解析。
- Controller 的 scene-aware ABR、runtime resolution control 与 keyframe/IDR loss recovery 扩展到 H.265；JPEG 等 intra/legacy 路径仍不进入这些 inter-frame 状态机。
- Windows native viewer 新增 MIME → codec 映射，并按 codec 选择 `OpenMFH264Decoder*` 或 `OpenMFH265Decoder*`；共享 Viewer D3D11 device 优先，失败后仍回退到普通 Media Foundation decoder。
- native viewer pipeline 现在把 codec identity 与 generation/尺寸一起作为 decoder rebuild 条件；即使尺寸不变，只要 generation 或 H.264↔H.265 发生变化，也会创建新 decoder，成功后再原子替换并关闭旧 decoder。
- H.265 decoder 输出继续复用已合并的 NV12 CPU/D3D11 surface 渲染、GPU cursor、staging readback 和性能统计路径，不复制一套 renderer。
- 初次打开 native viewer 与后续轮询均接受 `video/h264` / `video/h265`；decode/重建日志改为带 codec，不再写死 H.264。
- 新增 H.265 Controller snapshot/ABR/recovery 测试，以及 Windows MIME mapping、codec-switch/same-generation rebuild 测试。
- `NormalizeCodecPreference("h265")`、Host capability 广告和 GUI codec selector 仍保持关闭；下一步实现 Host H.265 generation（CPU + WGC D3D11 zero-copy）、HEVC codec string，并在端到端验证后再开放协商。
- PR #79 已合并到 `main`，merge `1e3afb98d42e77999709c37a8d6d5eca996696a8`；Go CI 与 UI CI 均通过。

### 0.2.26 RD3 HEVC Host Generation（已合并 PR #80）

- 新增隐藏 Host H.265 generation pipeline，保持用户侧 H.265 选择/能力广告关闭；先让 Host 可以稳定产生 generation-aware HEVC，再开放端到端协商。
- CPU 路径复用现有 RawCaptureSource / RGBA → NV12 生命周期，使用 `MFH265Encoder` 输出 HEVC Annex-B，并在每个新 generation 的首个关键帧前补 VPS/SPS/PPS sequence header。
- WGC GPU 路径复用 `D3D11CaptureSource → D3D11 VideoProcessor BGRA→NV12 → EncodeD3D11` 零拷贝链，仅将编码器切换为 `OpenMFH265EncoderWithD3D11`，保留 runtime GPU→CPU HEVC generation 迁移。
- H.265 generation 沿用现有 bitrate/FPS/resolution ABR、IDR 请求、统计、generation rollover 与资源关闭顺序，不复制网络/ABR 状态机。
- 新增 `H265CodecString`：从 Annex-B SPS 的 `profile_tier_level` 生成 RFC 6381 `hvc1.<profile>.<compat>.<tier+level>.<constraints>`，并处理 emulation-prevention byte。
- 新增 Host generation/codec metadata 与 HEVC codec-string 回归测试。
- 本分支仍不修改 `NormalizeCodecPreference`、Host capability advertisement、Controller/GUI codec selector 或默认协商；待 Windows 实机端到端验证后再开放 H.265。
- PR #80 已合并到 `main`，merge `a76cc133a180982b29e5332beb13c4d4096835f7`；Go format/vet/full test/race/benchmark、UI full regression、Windows/macOS desktop package 全部通过。

### 0.2.27 RD3 HEVC End-to-End Validation Negotiation（已合并 PR #81）

- 新增仅供实机验证的内部 codec sentinel：`h265-validation`。它不出现在 GUI、Host capability advertisement 或公开 `NormalizeCodecPreference` 中，普通 `auto/h264/jpeg` 行为保持不变。
- Controller 显式携带该 sentinel 时，Host 优先启动已合并的 H.265 generation pipeline；成功后沿现有 RD/1 Datagram、Controller H.265 snapshot/ABR/recovery 与 Windows native HEVC decoder 路径完成端到端验证。
- 若 HEVC 在发送首个 VideoConfig 前不可用，则自动回退到已广告的 H.264，再由现有逻辑回退 JPEG；若 HEVC 已经广告 generation 后发生运行时失败，则使用下一 generation 直接回退 JPEG，避免 generation 倒退。
- Windows GUI 增加隐藏环境变量触发：启动前设置 `RELAYPROXY_DESKTOP_HEVC_VALIDATION=1` 时，只覆盖下一次 Remote Desktop 连接参数为 `backend=relay` + `codec=h265-validation`；界面本身仍不显示 H.265 选项，取消环境变量后恢复原行为。
- 隐藏 H.265 会话状态下允许现有 runtime resolution 控件继续触发 generation rebuild；嵌入式 WebView 不尝试把 `video/h265` 当 JPEG/WebCodecs H.264 解码，而是明确提示使用 Windows 原生 Media Foundation 查看器。
- 该入口的目的仅是 Intel/NVIDIA/AMD 实机兼容性和零拷贝链路验证；通过实机矩阵前不开放 H.265 GUI 选项，也不把 H.265 加入自动协商。
- PR #81 已合并到 `main`，merge `a2e291c6d9ee55987fd55eb87af24a933bb382c8`；Go CI 首轮仅命中既有 `TestTLSTunnelMultiplexing` flaky，重跑 full test/race/benchmark 通过；UI full regression 与 Windows/macOS desktop package 全部通过。

### 0.2.28 RD3 HEVC Validation Diagnostics Summary（已合并 PR #82）

- 诊断报告 schema 升级到 v3，仅当连接请求使用内部 `h265-validation` sentinel 时增加 `hevcValidation` 汇总；普通 H.264/JPEG 报告保持无该字段。
- 汇总直接统计实际 `h265` 样本数、H.264/JPEG fallback 样本数、HEVC 硬编/硬解样本数，避免实机测试后人工扫描最多 1200 条时间序列。
- HEVC 样本单独聚合 capture backend/format、encoder backend、decoder backend，可直接区分 WGC D3D11 zero-copy、CPU fallback 与不同 Media Foundation decoder 路径。
- 现有逐样本网络/ABR/时延数据和通用 summary 保持不变；该汇总只做验证结果压缩，不改变媒体策略或能力协商。
- PR #82 已合并到 `main`，merge `1784f870a5031be900f23e28d2f19c64134de7c2`；Go CI 首轮再次命中既有 `TestTLSTunnelMultiplexing` flaky，重跑后 full test/race/benchmark 通过；UI full regression 与 Windows/macOS desktop package 全部通过。

### 0.2.29 CI Reliability：TLS Tunnel Multiplexing Flake（已合并 PR #83）

- 连续两个 HEVC PR 的 Linux Go CI 都偶发失败在既有 `internal/tunnel/TestTLSTunnelMultiplexing`；实际失败点是客户端首个 stream write 收到 `session shutdown`，与 HEVC 代码无关。
- 原测试使用 `net.Pipe + 手工 TLS + yamux`，只开一条 stream，却以“Multiplexing”命名；服务端提前退出时客户端断言看不到服务端 accept/read/write 的根因。
- 测试改为真实 loopback TCP + TLS 1.3，并通过生产 `DialTLS` / `ServerTLS` 建立会话；一次保持 4 条 yamux stream 同时存活，再逐条 echo，覆盖真正的 multiplexing。
- 客户端失败时同步附带服务端错误上下文，并为每条 stream 设置有界 I/O deadline；不使用 sleep 放宽时序，也不修改生产 tunnel 实现。
- PR #83 已合并到 `main`，merge `6be4fa4d577bd10aad5b1178032e01aba68f4919`；修正 stale import/gofmt 后 Go format/vet/full test/race/benchmark 全部通过。
- #85 首轮 CI 仍复现最后一条 stream 的 `session shutdown`，确认 #83 还存在“server final Write 成功后立即 defer Close、client 尚未消费完”的生命周期竞态；PR #86 增加 client-completion barrier 后 Go CI 全绿并已合并 `f769d91862e0fe5ecc475553936e29b621e3c8f0`。

### 0.2.30 RD3 Audio Media Foundation（已合并 PR #85）

- 为 RD/1 预留固定媒体 stream ID：video=1、audio=2、cursor=3；音频使用独立 stream/sequence 域，避免与视频丢包统计互相污染。
- 新增 `DesktopAudioConfig` / `audio_config` session message，描述 generation、codec、sample rate、channels、bit depth、frame duration 与 target bitrate；先建立稳定 wire model，再接具体 Windows capture/decoder。
- 现有 `PacketizeFrame` 保持 video-only 兼容接口；新增 typed `PacketizeMediaFrame`，可复用同一 RD/1 header/MTU fragmentation 发送 audio。
- Reassembler 新增 packet-type scope，默认仍只接受 video；未来 Controller 将为 audio 使用独立 reassembler，禁止把 audio fragment 混入 video generation/frame 状态。
- 现有 ABR/loss tracker 继续只计算 video packet sequence；audio datagram 不会制造假的 video packet gap。
- 新增 audio out-of-order fragmentation/reassembly、video/audio type isolation、audio config JSON round-trip 与 sequence-domain 隔离测试。
- 本阶段不启用音频采集或播放，也不修改默认 GUI；下一步接 Controller audio demux/buffer，再实现 Windows WASAPI loopback capture/native playback。
- PR #85 已合并到 `main`，merge `8ca93fac7d59705334c1df43ddf50b826ef606d3`；基于 PR #86 的生命周期修复后 Go CI、UI full regression、Windows/macOS desktop package 全部通过。

### 0.2.31 RD3 Audio Controller Demux / Realtime Queue（已合并 PR #88）

- Controller control loop 接收 `audio_config`，要求 generation 单调递增；同一 generation 内 codec/sample-rate/channel/bit-depth/frame-duration 等格式字段不可变化，格式切换必须 rollover。
- 媒体 read loop 先解析 RD/1 header，再按 packet type 分流到独立 video/audio Reassembler；audio 使用 250 ms partial-frame TTL 和独立容量，不再经过 video JPEG/H.26x generation/recovery 路径。
- audio 只接受保留的 stream ID=2 且 generation 必须匹配当前 AudioConfig；旧 generation 和错误 stream 的音频直接丢弃。
- Controller 增加最多 8 帧的 realtime audio queue。消费者落后时丢最旧帧保留最新尾部，避免音频像可靠队列一样持续积压延迟。
- generation 切换会清空旧 audio queue；`NextAudioFrame(ctx)` 提供阻塞式消费接口，并在返回时复制 payload，供 Windows native playback goroutine 直接使用，不走 WebView 轮询。
- 新增 invalid/stale config、同 generation 格式变更、stream/generation 隔离、queue overflow、generation clear、payload copy 与 context cancellation 测试。
- PR #88 已合并到 `main`，merge `1766022d708b8be9164ed7b1843f68c006a6a370`；Go CI、UI full regression、Windows/macOS desktop package 全部通过。

### 0.2.32 RD3 Audio Native Bridge（已合并 PR #90）

- `Agent.NextRemoteDesktopAudioFrame(ctx)` 暴露当前 ControllerSession 的阻塞式 audio frame 消费接口；没有活动 Relay Desktop session 时立即返回错误。
- `UIBridge.NextRemoteDesktopAudioFrame(ctx)` 仅供 Go/native viewer 使用；刻意不绑定到 WailsService/JavaScript，避免约 20 ms 一帧的音频进入 JSON/WebView polling。
- native bridge 保持 context-aware，后续 Windows playback goroutine 可以直接阻塞消费 `NextAudioFrame`，无需建立第二套音频队列。
- 新增 Agent/Bridge unavailable regression tests，覆盖未连接 session 与 nil bridge。
- PR #90 已合并到 `main`，merge `f6620e412eaf4944ca3fb0d0adbd3f81e2e2734e`；Go CI 通过。

### 0.2.33 RD3 Windows WASAPI PCM Player Core（已合并 PR #91）

- 新增独立 `agent/desktop/audio` 播放层，公开 `PCMConfig` / `Player` / `OpenPCMPlayer`；非 Windows 平台提供 unavailable stub。
- 首轮 transport/playback 固定为 signed PCM S16LE，支持 8–192 kHz、1–8 channels；默认 bit depth=16，并严格校验 block alignment。
- Windows 使用现有 `go-bindings-win32` 的 `IMMDeviceEnumerator → IAudioClient → IAudioRenderClient`，默认 multimedia render endpoint、shared mode 与 Windows PCM/SRC 自动转换，不新增第三方音频依赖。
- COM/WASAPI 生命周期固定在 `runtime.LockOSThread` 的专用 goroutine；Write 跨 goroutine 复制 payload，避免上层复用接收 buffer 引入数据竞争。
- 根据 `GetCurrentPadding` 计算真实 render capacity，必要时拆分网络 PCM frame；buffer 满时短周期等待并响应 context/Close，避免无界队列和长时间不可取消阻塞。
- 修正 `go-bindings-win32` runtime import 路径后，Go CI、UI regression、Windows/macOS desktop package 全部通过。
- PR #91 已合并到 `main`，merge `7982b90aeb186a6f8cb2e7e84e81463db45caeea`；旧 PR #89 已关闭，由 #91 替代。

### 0.2.34 RD3 Native Viewer Audio Playback（已合并 PR #92）

- 固定首个 wire codec 名称为 `pcm_s16le`，Controller audio config/tests 与 native playback 共用协议常量，避免 Host/Controller/Viewer 出现字符串漂移。
- `ControllerSession.AudioEnabled()` 默认启用音频，并严格遵守显式 `RemoteDesktopConnectOptions.Audio=false`；Agent/Bridge 只向 Go/native viewer 暴露这一状态，不增加 WebView 音频轮询。
- Windows native viewer 在 session 生命周期内启动独立 audio goroutine，阻塞消费 #90 的 `NextRemoteDesktopAudioFrame`，首次 PCM frame 到达后再惰性打开 WASAPI player。
- audio generation 或格式变化时关闭并重建 player；每帧在写入前复用 `PCMConfig.ValidatePayload` 做 block alignment 校验，WASAPI 写失败时丢弃旧 player 并允许下一帧重建。
- viewer run 退出时主动 cancel session context，确保 input/audio goroutine 一并退出。
- PR #92 已合并到 `main`，merge `eefcb76b2e87c6ace104af5a754fe51fccf9436f`；Go CI、UI regression、Windows/macOS desktop package 全部通过。

### 0.2.35 RD3 WASAPI Loopback Capture / PCM E2E（已合并 PR #93）

- 新增 `audio.Capture` 与 `OpenLoopbackCapture`；Windows 使用默认 multimedia render endpoint + WASAPI shared-mode loopback，将系统播放音频转换为 S16LE PCM。
- 首轮 Host 音频固定为 48 kHz / stereo / 16-bit / 20 ms，每帧 3840 bytes；capture worker 在锁定 OS thread 的 COM apartment 内读取 `IAudioCaptureClient`，支持 silent packet、context cancellation 与 Close。
- Host 在目标平台声明 audio capability 后，默认随 Relay Desktop session 启动音频；显式 `Audio=false` 保持关闭。audio 初始化/设备失败只禁用当前 session 音频，不主动中断视频。
- Host 先通过可靠控制流发送 `audio_config`，再用独立 stream ID=2 / sequence domain 将 PCM 经 `PacketizeMediaFrame` 发送，Controller/Native Viewer 复用既有 #88/#90/#92 链路接收播放。
- `MediaConn.Send` 增加 datagram 写串行化，避免视频与音频并发 direct-path 发送时相互覆盖 socket write deadline。
- Windows host capability snapshot 现在声明 loopback audio 支持；非 Windows 继续保持 unavailable stub。
- PR #93 已合并到 `main`，merge `797729c73f21894530c39cc7288f3147714301a1`；Go CI、UI regression、Windows/macOS desktop package 全部通过。CI 已覆盖编译与回归，但仍需 Windows 实机确认实际 loopback 播放链。

### 0.2.36 RD3 Audio Runtime Diagnostics（已合并 PR #94）

- Controller 为 audio 单独维护运行计数，不并入 video ABR/loss domain：记录接收/消费帧与字节、8 帧 realtime queue 当前深度、queue overflow drop、generation rollover 丢弃、错误 generation/stream 拒绝，以及最后接收/消费时间。
- `AudioDiagnosticsSnapshot` 同时暴露当前 `audio_config`、queue capacity、最后 FrameID 与媒体时间戳，可区分 Host 无数据、网络/重组无数据、Controller queue 堵塞和 native viewer 未消费。
- 诊断导出 schema 从 v3 升级到 v4；每个 500 ms sample 增加 audio snapshot，report 增加 `currentAudio`，summary 增加 audio codec 分布、最大/percentile queue depth 与累计 received/consumed/drop/reject 指标。
- generation 切换时被清理的旧队列与实时队列满导致的 drop 分开统计，避免把正常格式切换误判成网络/播放拥塞。
- PR #94 已合并到 `main`，merge `e7696ba957683d850d1af890dc25552051f37ccc`；Go test/race、UI regression、Windows/macOS desktop package 全部通过。

### 0.2.37 RD3 Audio Transport Integration Coverage（已合并 PR #95）

- 新增 synthetic loopback capture + recording direct datagram path 集成测试，不依赖真实声卡即可驱动 Host 的 `streamSessionAudio` 主链。
- 测试验证可靠控制流首先产生 `audio_config`，并校验 PCM S16LE / 48 kHz / stereo / 16-bit / 20 ms / target bitrate 元数据。
- 强制使用较小 RD/1 packet size 把 3840-byte PCM frame 分成多个 datagram，逐包校验 type=audio、stream ID=2、generation/frame ID 与独立 sequence 连续性。
- 所有 datagram 再通过真正的 audio `Reassembler` 还原，最终 payload 必须与 synthetic capture 原始 PCM byte-for-byte 一致，同时确认 capture 生命周期正确关闭。
- PR #95 已合并到 `main`，merge `a057810cfe9aae35ca77844d0383cc246523077b`；Go CI 与 UI CI 全部通过。

### 0.2.38 RD3 Bounded Audio Jitter / Playout Buffer（已合并 PR #96）

- Controller audio queue 从单纯 arrival-order FIFO 改为同 generation 内按 FrameID 有序插入；轻微 datagram/frame completion 乱序在进入 native player 前被重排。
- 保持 8 帧 realtime 上限；队列达到 2 帧即可立即播放，正常 20 ms PCM 因此只增加约 1 帧启动缓存。若缺少相邻帧，单帧最长只等待基于 frame duration 的 20–80 ms 有界 playout deadline，随后继续播放而不是无限等待。
- 已消费 FrameID 之后才到达的旧帧直接记为 late/rejected；queue 内相同 FrameID 直接记为 duplicate/rejected，避免 WASAPI 重复播放旧声音。
- generation 切换继续清空旧队列，并重置该 generation 的 consumed head；原有 queue overflow 仍丢最旧帧，优先保持实时性。
- Audio diagnostics 增加 reordered / duplicate / late / playout-timeout 与 last-consumed-frame 指标，便于 Windows 实机区分网络乱序、真正丢帧和播放器跟不上。
- PR #96 已合并到 `main`，merge `bba4a4cc0fe111bff9d0e7dfb7c5045d739e8031`；Go CI、UI regression、Windows/macOS desktop package 全部通过。

### 0.2.39 RD3 Pure-Go Opus Codec Foundation（已合并 PR #97）

- 协议新增 `DesktopAudioCodecOpus = "opus"`，但本阶段不改变 Host 当前默认 PCM wire codec；先把 codec core 单独做稳，下一阶段再做能力协商与 PCM fallback。
- 引入 Pion Opus 2026-08 encoder 提交线的纯 Go module，不使用 CGO/libopus，保持 Windows amd64/arm64 与 macOS 的现有 Go 构建模型。
- 新增 `OpusConfig` / `OpusEncoder` / `OpusDecoder`：首轮严格固定 48 kHz、mono/stereo、S16LE、20 ms；默认 96 kbps，合法 bitrate 6–510 kbps。
- 现有 48 kHz stereo PCM 一帧为 3840 bytes；96 kbps / 20 ms Opus 的目标 payload 约 240 bytes 级别，可在完成协商后显著降低当前 1.536 Mbps PCM 数据面带宽。
- Encoder 输入必须是完整单个 20 ms PCM frame；Decoder 输出固定恢复为与 config 对应的 PCM frame，继续复用现有 WASAPI Player、bounded jitter queue 与 generation 模型。
- 新增 config validation、S16LE 440 Hz stereo encode/decode round-trip、压缩尺寸与错误输入测试。
- PR #97 已合并到 `main`，merge `0d896e891a9020ee74989c967f5f2a95122dd231`；Go format/vet/test/race/benchmark、UI regression、Windows/macOS desktop package 全部通过。

### 0.2.40 RD3 Opus Negotiation / End-to-End Data Path（已合并 PR #98）

- `DesktopCapabilities` 新增 `audioCodecs`，新 Host 在 loopback audio 可用时声明 `[opus, pcm_s16le]`；server gateway/session 对该 slice 做独立拷贝，继续保持 capability snapshot 隔离。
- `RemoteDesktopConnectOptions` 新增内部协商字段 `audioCodec`。Controller 在 Relay Desktop dial 前自动选择：新目标优先 Opus；legacy 目标只有 `Audio=true` 且没有 codec list 时严格按 PCM-only 处理；显式 `Audio=false` 不携带 codec。
- Host capture 仍统一使用现有 48 kHz/stereo/S16LE/20 ms WASAPI loopback；选择 Opus 时仅在 RD/1 packetize 前编码为 96 kbps Opus，PCM fallback 路径保持原样。
- `audio_config` 通过现有 generation 模型声明实际 codec 与 target bitrate；Opus payload 继续使用独立 audio stream ID=2 / sequence domain，并进入现有 bounded jitter queue。
- Windows native viewer 按 `audio_config.codec` 创建 Opus decoder；Opus frame 在进入 WASAPI player 前恢复为 PCM，原有 PCM alignment 校验、player rebuild 与错误恢复路径继续复用。
- 新增 Controller 协商测试、legacy PCM fallback、显式 codec 拒绝、Host Opus transport/reassembly/decode 集成测试，以及 audio codec capability copy 测试。
- PR #98 已合并到 `main`，merge `7d0499dfe7a764df157e8d33abe33ce0fd8621a4`；Go CI、UI regression、Windows/macOS desktop package 全部通过。

### 0.2.41 RD3 Opus Packet-Loss Concealment（已合并 PR #99）

- Controller 在 Opus playout 中根据 FrameID 明确识别网络缺口；缺失帧不再直接跳过，而是产生 `Concealment` playout event，Native Viewer 调用 Pion Opus decoder 的 PLC 生成完整 20 ms PCM 后继续交给 WASAPI。
- 单次连续 concealment 严格限制为 3 帧（60 ms）。超过上限的大缺口直接推进到最新可播放真实帧，并记录 `gapSkippedFrames`，防止长断网后用 PLC 回放历史时间、造成音频持续落后。
- 本地 8 帧 realtime queue overflow 与网络丢帧分开处理：overflow 主动丢掉的旧 FrameID 会同步推进 playout cursor，不再被 PLC 补回；这保持了“宁可丢旧音频也不增加长期延迟”的实时策略。
- Opus decoder wrapper 新增 `DecodePLC()`，把 Pion 的 signed-int16 PLC 输出恢复为现有 S16LE byte frame；未收到任何真实 Opus packet 前的 PLC 由底层 decoder 输出静音，已 prime 后使用 codec concealment 状态。
- Audio diagnostics 新增 `concealmentFrames` 与 `gapSkippedFrames`，summary 同步导出 `audioConcealmentFrames` / `audioGapSkippedFrames`，可区分网络缺帧被平滑掩盖与大缺口主动追实时。
- 新增单帧缺失、连续大缺口 3 帧 PLC 上限、queue-overflow 不触发 PLC、Opus codec PLC PCM 输出与诊断汇总测试。
- 新增真实 RD/1 packet-loss 集成链：Host 连续生成 4 帧 Opus datagram，确定性丢弃 FrameID=2 的实际 packet，再经 audio Reassembler → Controller gap detector → PLC event → Opus decoder，验证恢复后的三段 PCM 均保持完整 20 ms 帧长。
- PR #99 已合并到 `main`，merge `866014812867ab7217ac05c123415ccd0ec3dc3a`；Go format/vet/test/race/benchmark、UI regression、Windows/macOS desktop package 全部通过。

### 0.2.42 RD3 Opus Loss Feedback Control（已合并 PR #100）

- 协议新增独立 `audio_control` / `DesktopAudioControl.ExpectedLossPercent`，与 video ABR control 分离；只有已协商 Opus 的 session 才会产生该反馈，legacy PCM 不发送新 control message。
- Controller 每秒根据 audio playout 的累计 `receivedFrames`、`concealmentFrames`、`gapSkippedFrames` 计算增量丢帧比例；本地 realtime queue overflow 不计入网络丢帧反馈，避免把播放器跟不上误判为链路 loss。
- 丢帧率以 5 个百分点为步长量化，并要求至少 10 帧样本再更新，减少单个丢包导致 encoder 参数抖动；纯净窗口会恢复到 0%。
- Host 控制循环校验 0–100% 后只保留最新 loss target；Opus stream 在下一帧 encode 前调用 Pion `SetLossRate`，不重建 encoder、不切 generation、不影响 video ABR。
- `OpusEncoder` wrapper 新增 `SetLossRate`，并保留 Pion 参数合法性校验；当前只使用其 packet-loss resilience control，不启用尚未在当前 pinned encoder 路径验证的 FEC。
- 新增 loss quantization / baseline / recovery / PCM reset、Opus encoder loss control 与 Host live update 测试。
- PR #100 已合并到 `main`，merge `4d23d2771c69f91881f40c422e2650cbc64a2dec`；Go CI、UI regression、Windows/macOS desktop package 全部通过。

### 0.2.43 RD3 Audio Validation Diagnostics Summary（已合并 PR #101）

- Desktop diagnostics schema 升级到 v5，新增 `audioValidation` 汇总，不新增第二套导出入口；现有诊断 JSON 即可直接用于 Windows 双机实测。
- 汇总记录 requested/actual codec、Opus/PCM sample 数和 Opus→PCM fallback 次数，能直接确认协商结果是否真的落到 Opus，而不是仅看连接参数。
- 汇总当前 48 kHz / channel / bit depth / frame duration / target bitrate，并根据诊断窗口内 `receivedBytes` 增量计算实际 audio payload bitrate；同时给出相对 raw PCM bitrate 的观测压缩比。
- 根据 `receivedFrames + concealmentFrames + gapSkippedFrames` 计算估算网络 audio loss%，并直接汇总 queue drop、PLC concealment、large-gap skip、reorder、duplicate、late、playout timeout 与最大 queue 深度。
- 显式 `Audio=false` 的会话不生成 `audioValidation`；启用音频但尚未拿到 `audio_config` 时会保留 requested 状态且 `active=false`，方便定位 Host 无 loopback/capability 的问题。
- 新增 Opus runtime bitrate/loss/queue 汇总、PCM fallback 与 Audio=false 回归测试。
- 新增 `scripts/analyze-desktop-audio.ps1` 与 Windows 实机验证清单：直接读取 GUI 导出的 schema v5 diagnostics JSON，输出 codec/bitrate/compression/loss/PLC/queue 指标，并支持 Opus、loss、queue、gap、timeout、compression ratio 阈值作为可重复测试 gate；Windows CI 解析检查该脚本语法。
- PR #101 已合并到 `main`，merge `da6dfaec3fd4e01ec473af5dfc09ae845b08c7d2`；Go CI、UI regression、Windows/macOS desktop package 与 PowerShell analyzer syntax 全部通过。

### 0.2.44 RD3 Live Audio Diagnostics UI（已合并 PR #102）

- 新增轻量 `GetRemoteDesktopAudioDiagnostics` binding，只返回当前 `DesktopAudioDiagnostics` snapshot；不会每 2 秒拉取包含最多 1200 条样本的完整 diagnostics report，也不会把 20 ms audio frame 暴露给 WebView。
- Relay Desktop 预览区新增独立“音频”状态行，实时显示实际 codec、sample rate、channels、target bitrate、queue depth 与基于 received/concealment/skip 的估算 network loss。
- 只有异常/有事件的指标才追加展示 PLC concealment、large-gap skip、local queue drop、reorder 与 playout timeout，避免正常状态下信息过载。
- 非 Relay / 未连接状态显示 `音频：--`；音频已启用但尚未收到 `audio_config` 时显示等待音频流，显式关闭时显示已关闭。
- 新增 Agent/Bridge unavailable 回归与 GUI binding/render token 测试。
- PR #102 已合并到 `main`，merge `98e508a2847707106a86c78d91ce33c4382381e1`。
- 下一步：代码侧音频主链先进入实机验证阶段；在拿到 Windows 双机 diagnostics 前不继续盲调 Opus bitrate/FEC。

### 0.2.45 RD3 Follow Viewport Resolution（已进入 main）

- 连接设置中的“自动”分辨率明确为“跟随窗口（自动）”：Relay Desktop 嵌入预览根据 Viewer stage 的 CSS 尺寸与 `devicePixelRatio` 计算目标像素，并按当前 session 的 `MaxWidth / MaxHeight` 保持源画面纵横比、偶数尺寸和最低 320×180 边界。
- 不新增 wire message，继续复用既有 `SetRemoteDesktopResolution → DesktopVideoControl(TargetWidth/TargetHeight) → generation rebuild` 链路，因此 H.264 / 隐藏 H.265 validation 与现有 ABR/decoder generation 语义保持一致。
- 使用 `ResizeObserver` + window resize/fullscreen 事件，300 ms debounce 后更新 viewport；正常窗口缩小时及时降低不必要的编码像素。
- 为避免和 scene-aware ABR 抢控制权，viewport 只有在当前媒体尺寸仍等于上一次 viewport 请求时才允许自动升档；如果 ABR 已因 loss/queue/jitter 把 generation 降到更低分辨率，窗口放大不会强制把它顶回高分辨率。
- 运行中手动选择固定分辨率/“最高”会关闭 viewport follow；下拉框新增“跟随窗口”可重新启用。打开原生 Win32 Viewer 时会暂停嵌入 WebView 的 viewport follow，避免用较小的 WebView 尺寸限制独立原生窗口；后续可由原生 Viewer 自己上报 viewport。
- 修复 PR #102 页面模板中网络统计与音频统计之间残留的字面 `\\n`，避免界面显示异常文本。
- UI 实现已直接提交 `main`：`5141bf8edbf8788571f613c15e52f3dd65403a77`；回归测试：`fea60f4c679b7b28529eaa2666f1544e4c34079c`。
- 下一步：等待主线 CI，并在 Windows 实机确认窗口缩放 / 全屏 / ABR 降档 / generation rebuild 组合行为；音频参数仍等待双机 diagnostics 后再标定。

### 0.2.46 RD3 Native Viewer Viewport（已进入 main）

- 原生 Win32/D3D11 Viewer 窗口从固定尺寸改为可缩放窗口，启用 `WS_THICKFRAME / WS_MAXIMIZEBOX`；Viewer 新增独立 `Viewport` 契约和 `OnViewport` 回调，媒体编码尺寸与本地窗口客户区尺寸不再混为同一个概念。
- `WM_SIZE` 现在实时更新 Native Viewer 客户区尺寸；鼠标绝对坐标归一化改为基于实际 viewport，而不是旧的编码分辨率，窗口缩放后输入映射不会继续使用旧尺寸。
- Native Viewer 增加独立 viewport worker：窗口变化先做 300 ms debounce，再根据当前媒体纵横比、session `MaxWidth / MaxHeight` 和实际客户区计算偶数目标尺寸，最低仍保持 320×180。
- viewport 请求与手动分辨率请求在 Controller 内正式拆分：`RequestResolution` 会关闭 follow，`RequestViewportResolution` 会开启 follow；WebView 也改用新的 viewport-aware binding，避免手动分辨率和自动跟随在 Go/JS 两侧状态不一致。
- Native Viewer 的自动升档继续服从 ABR：只有当前 generation 仍等于上一次 viewport 请求时才允许因窗口放大而升分辨率；如果 ABR 已因 loss/queue/jitter 主动降档，viewport 不会立即把它顶回高分辨率。窗口缩小时仍可继续降到更合适的编码尺寸。
- generation 导致 Viewer pipeline 重建时会保留旧窗口的客户区尺寸，再以该 viewport 初始化新 Viewer，避免媒体分辨率变化把用户刚调整的窗口大小直接覆盖。
- 新增 viewport 比例/上限/最小尺寸、ABR grow gate、resolution mode 状态以及 GUI viewport binding 回归测试。
- 主线 CI 调整：`UI CI` 现在也在 `main` push 上执行 Windows/macOS desktop package；Go/UI 的 gofmt 检查改为仓库全量文件，消除连续直接提交 main 时 `origin/main...HEAD` shallow merge-base 竞态。
- Windows/macOS desktop package、Linux UI scope/full regression 已验证通过；功能提交从 `4ef0a1a` 延续至本轮 main。
- 下一步：Native Viewer 仍会在媒体尺寸 generation 改变时重建 D3D11 Viewer pipeline；后续可继续把 renderer 改造成同一 HWND 内 resize/reconfigure，减少动态分辨率切换时的窗口重建闪烁。H.265 正式公开仍等待 Intel/NVIDIA/AMD 实机验证。

### 0.2.47 RD3 Native Viewer In-place Generation Reconfigure（已进入 main）

- Native Viewer 新增 `Reconfigure(width, height)` 契约；H.264 / H.265 generation 在分辨率变化时不再关闭旧 Viewer、创建新 HWND，而是在当前窗口内原地切换媒体尺寸。
- Win32 Viewer 使用独立 `WM_APP` reconfigure 消息把 D3D11 resize 调度回 Viewer 所属 OS thread；媒体线程只提交同步请求并等待结果，不直接跨线程操作 swap chain。
- D3D11 renderer 保留现有 device/context/swap chain，只重建与媒体尺寸相关的 upload texture、backbuffer 引用、video processor、output view 与 GPU cursor view，并通过 `IDXGISwapChain::ResizeBuffers` 切换新 generation 尺寸。
- resize 前执行 `ID3D11DeviceContext::ClearState + Flush` 并释放旧 backbuffer/view 引用，满足 DXGI `ResizeBuffers` 对 outstanding references 的要求；如果新尺寸资源创建失败，会尝试回滚到旧媒体尺寸。
- Viewer 将本地 viewport 尺寸与当前媒体尺寸拆成两个原子状态；`Submit / SubmitD3D11` 按当前 media size 校验，不再把初始 `Config.Width / Height` 当成永久固定尺寸。
- generation rebuild 现在只替换 decoder；Media Foundation 仍复用 Viewer 的同一个 D3D11 device，窗口位置、窗口大小、最大化状态和焦点都不会因媒体分辨率变化而被重建。
- 如果新 decoder 创建失败，Controller 会请求 Viewer 回滚到旧媒体尺寸，避免 renderer 与仍在工作的旧 decoder 尺寸永久失配。
- 新增 Windows viewport packing / media-size / headless reconfigure no-op 回归；Windows desktop package 已覆盖新的 Native interface 与 Win32/D3D11 编译路径。
- 功能提交：`7d0c44c`（Viewer contract）、`d6a2aab`（D3D11 in-place resize）、`607a089`（Win32-thread reconfigure）、`082f1f2`（generation 保持同 HWND）、`621bf7b`（ClearState/Flush 稳定性）、`a249437`（格式修复）。
- 验证：Go CI #793 的 gofmt/vet/test/race/benchmark 全部通过；UI CI #483 的 Linux UI/full regression、Windows desktop package、macOS desktop package 全部通过。
- 下一步：当前 swap-chain backbuffer 会随 media generation 尺寸变化，但任意宽高比的 Native Viewer 客户区仍可能由 DXGI 拉伸画面；后续应补 D3D11 letterbox / aspect-fit viewport，以及对应的鼠标输入可视区域坐标映射。

### 0.2.48 RD3 Native Viewer Aspect-fit Letterbox（已进入 main）

- Native Viewer 的顶层窗口继续负责键盘/鼠标输入与 viewport 管理；新增一个禁用的 `STATIC` child render surface，D3D11 swap chain 改为绑定该 child HWND，而不是直接绑定整个顶层 client area。
- child render surface 根据当前 `viewport × media` 实时计算 aspect-fit 矩形；窗口比例与媒体比例不一致时只移动/缩放 child，不改变媒体 generation，也不改变 decoder / swap chain 的媒体分辨率。
- 顶层窗口背景固定为黑色并启用 `WS_CLIPCHILDREN`，因此未被 render surface 覆盖的区域自然形成 letterbox / pillarbox，不需要额外 shader 或 CPU 缩放路径。
- 该结构同时覆盖零拷贝 D3D11 decoder 和 CPU BGRA fallback：两条路径仍向媒体尺寸 swap chain 输出，Windows 只在等比例 child surface 内做最终窗口缩放，因此不会出现非等比拉伸。
- child surface 使用 `WS_DISABLED`；Win32 会把原本命中 disabled child 的鼠标输入转交给父窗口，因此现有父窗口统一输入、capture、按键释放逻辑无需复制到第二个 WndProc。
- `normalizedPointer` 改为基于实际可见媒体矩形，而不是整个 client viewport；黑边区域的鼠标位置会 clamp 到最近的画面边缘，再归一化为 0..65535，避免 pillarbox / letterbox 导致远端鼠标横纵坐标偏移。
- media generation 改变时，现有 in-place `Reconfigure` 完成后会重新布局 child render surface；窗口大小与位置保持不变，新的媒体宽高比会立即重新居中。
- 新增 `aspectFitRect` 纯函数和 Windows 回归测试，覆盖同宽高比、16:9→方窗、4:3→宽窗以及左右黑边 clamp 到远端画面边缘。
- 实现提交：`a12e044`；测试提交：`9d2c0a0`；格式修复：`337baf7`。
- 验证：Go CI #797 gofmt/vet/test/race/benchmark 通过；UI CI #487 Linux UI/full regression、macOS desktop package 与 Windows desktop package 均通过。
- 下一步：✅ borderless fullscreen / `Alt+Enter` 已在 0.2.49 完成；后续继续做多显示器窗口 placement 持久化与 H.265 正式产品化验证。

### 0.2.49 RD3 Native Viewer Borderless Fullscreen（已进入 main）

- Native Win32 Viewer 新增 `Alt+Enter` 本地快捷键；仅拦截带 Alt context 的 `WM_SYSKEYDOWN / WM_SYSKEYUP + VK_RETURN`，普通 Enter、F11 与其他远端按键继续按既有输入链路发送。
- 全屏不创建新窗口、不重建 renderer/decoder，也不触发媒体 generation：同一个顶层 HWND 仅把 window style 从 overlapped/resizable 切为 `WS_POPUP | WS_VISIBLE | WS_CLIPCHILDREN`，D3D11 child render surface 和当前媒体 pipeline 原样保留。
- 进入全屏前保存 `WINDOWPLACEMENT`；使用 `MonitorFromWindow(..., MONITOR_DEFAULTTONEAREST)` 与 `MONITORINFO.RcMonitor` 覆盖当前 Viewer 所在显示器，而不是固定主屏。
- 退出全屏时恢复原 window style 与 `WINDOWPLACEMENT`，因此普通窗口的位置、大小以及进入全屏前的最大化状态都可以恢复。
- style 改变通过 `SetWindowLong + SetWindowPos(SWP_FRAMECHANGED)` 生效；全屏切换后重新前置并设置键盘焦点，不改变已有 viewport/letterbox/鼠标归一化逻辑。
- 对按住 `Alt+Enter` 的系统键自动重复做 repeat-bit 过滤，只在首次 keydown 时切换一次，避免长按导致窗口在全屏/窗口模式之间快速来回跳。
- 修复 Go 对 `WS_POPUP (0x80000000)` 的常量转换限制：先落到运行时 `uint32` style 再传入 `SetWindowLong(int32)`，兼容 Win32 最高位 style bit。
- 新增 fullscreen shortcut 判定测试，覆盖 Alt+Enter down/up、普通 Enter、无 Alt context 以及其他 system key。
- 实现提交：`e766997`；Win32 style 修复：`cda76fd`；测试：`ecc3e71`；格式修复：`a38eed3`。
- 验证：Go CI #802 的 gofmt/vet/test/race/benchmark 全部通过；UI CI #492 的 Linux UI/full regression、Windows desktop package、macOS desktop package 全部通过。
- 下一步：增加 Native Viewer 的窗口 placement 持久化（跨 Viewer 重开记忆显示器/位置/尺寸），然后开始 H.265 capability negotiation 与 UI 正式公开。

### 0.2.50 RD3 Native Viewer Window Placement Persistence（已进入 main）

- Native Viewer 的普通窗口位置、外框尺寸与最大化状态现在持久化到既有 `relay-agent.yaml -> gui.native_viewer`，不新增第二套 UI 状态文件。
- Viewer 公共契约新增 `WindowPlacement`；Windows 实现通过 `GetWindowPlacement` 在 `WM_MOVE / WM_SIZE / WM_CLOSE` 时缓存普通窗口 placement，支持负 X/Y 坐标，因此左侧或上方副屏也能正确记忆。
- 全屏状态不会覆盖普通窗口 placement；进入 borderless fullscreen 前缓存 `WINDOWPLACEMENT`，即使用户直接在全屏中关闭 Viewer，下次仍恢复进入全屏前的普通窗口位置/大小。
- 恢复使用 `SetWindowPlacement` 而不是把 `RcNormalPosition` 直接传给 `CreateWindowEx`，由 Windows 正确处理 workspace 坐标、任务栏偏移和最大化语义。
- GUI 关闭 Native Viewer 时通过现有 `persistGUI -> bridge.SaveConfig` 增量保存 placement；不会覆盖 server/proxy/routing 等无关配置，也不会产生 restart/reload pending。
- 配置新增尺寸校验：placement 一旦存在，宽高必须在 320×180 到 16384×16384 范围；YAML round-trip、Bridge 增量保存和 Win32 placement 转换均有回归测试。
- 主要提交：`75dc512`、`0e8a0bd`、`f876779`、`b9bd2d7`、`65d8916`、`875798a`，测试与格式收尾至 `61c8c7b`。
- 验证：Go CI #818 全部通过；UI CI #508 的 Linux UI/full regression、Windows desktop package、macOS desktop package 全部通过。
- 下一步：正式公开 H.265/HEVC capability 与显式 codec 选择；`auto` 暂继续使用成熟 H.264 路径，待 Intel/NVIDIA/AMD 两机验证完成后再考虑默认 HEVC。

### 0.2.51 RD3 Public H.265 / HEVC Negotiation（已进入 main）

- H.265 不再仅能通过 `h265-validation` 隐藏哨兵触发：公共 codec normalizer 现在识别 `h265 / hevc / hvc1 / hev1`，同时继续保证 diagnostics-only `h265-validation` 不会泄露为普通 capability。
- Windows `NewSystemHost` 同时探测 Media Foundation H.264 与 H.265 encoder/decoder；只要本机存在 HEVC Encode 或 Decode，就把真实 `DesktopCodecCapability{Codec:"h265", Encode, Decode, Hardware,...}` 纳入认证阶段上报。
- Server 的 `DesktopCapabilitiesForTarget` 已确认完整复制 `Codecs`，因此目标 HEVC capability 会原样到达控制端，不需要数据库 schema 或额外 server 协议改造。
- 公共 Host 会话新增 H.265 分支，直接复用既有 HEVC Media Foundation encoder、D3D11 zero-copy generation、ABR、IDR、动态码率/FPS/分辨率和 generation hot-switch 数据面。
- H.265 显式请求在连接前做双端能力校验：目标必须上报 `Encode=true`，本机必须通过自身 Host capability 上报 `Decode=true`；仅有 encoder 或仅有 decoder 都不会误启用 HEVC。
- 运行时容错继续保留：显式 H.265 在 Host encoder capability 缺失或 encoder 启动失败时先尝试 H.264，再落到 JPEG；若已经发送 HEVC CONFIG 后发生 generation runtime failure，则使用新的 generation 安全回退，避免旧 HEVC 帧与 fallback 帧混代。
- Windows GUI 视频编码下拉框正式增加 “H.265 / HEVC（原生 Viewer）”；目标卡片展示其上报的 H.264/H.265 Encode 能力，连接前也会做目标 capability 预检查。
- Embedded WebView 不宣称可移植的 HEVC WebCodecs 支持：H.265 帧明确提示使用 Native Viewer；浏览器管理页没有原生 Viewer binding 时会禁用 H.265 选项。Native MF/D3D11 Viewer 已支持 H.265 decode、letterbox、follow viewport、ABR generation 和全屏。
- `auto` 策略保持不变：当前仍优先成熟 H.264（WebCodecs 不可用时 JPEG），不会因为本轮 capability 公开就自动切 HEVC；待 Intel/NVIDIA/AMD 双机验证数据稳定后再评估默认策略。
- 新增 codec normalizer、Host H.265 encode capability、双端 negotiation、GUI capability gating 回归；旧 HEVC validation env/sentinel 继续保留用于强制诊断。
- 主要提交：`3823ec4`、`1c86e50`、`d2437f0`、`c111059`、`1f95772`、`c972c7e`、`5c78d12`、`285f7e0`、`2fa32af`，文档/注释收尾至 `d925dae`。
- 验证：Go CI #832 全部通过；UI CI #522 的 Linux UI/full regression、Windows desktop package、macOS desktop package 全部通过。
- 下一步：进入真实多显示器增强——运行中切换显示器、显示器热插拔/布局变化刷新，以及后续独立多窗口/多流设计。

### 0.2.52 RD3 Runtime Multi-Display Switching（已进入 main）

- Relay Desktop 运行中切换显示器已经从“断开重连”升级为媒体会话内热切换：`DesktopVideoControl.DisplayID *string` 使用指针语义区分“没有显示器控制请求”和“显式切回全部显示器（空字符串）”。
- Controller 新增 `RequestDisplay`，Agent / Bridge / Wails / 前端完整暴露 `SetRemoteDesktopDisplay`；连接后 Viewer 工具栏直接显示当前目标的显示器选择框。
- Host 收到切屏请求后不会把它当媒体故障：当前 H.264 / H.265 / JPEG generation 正常结束，事务式切换 capture + input geometry，再在同一 RD/1 媒体连接上启动下一 generation。
- Windows capture 切换已改为事务式：先成功打开新 stream 再替换旧 stream；新显示器或新 backend 打开失败时，旧画面继续保留。
- Windows input 映射同步事务化；新显示器输入几何初始化失败时，Host 会把 capture 回滚到旧显示器，避免“画面在新屏、鼠标还映射旧屏”的半切换状态。
- `auto / GDI` 会话允许运行中切回虚拟桌面；显式 `DXGI / WGC` 仍要求具体显示器。Session status 公开请求的 capture backend preference，GUI 会直接禁用不合法的“全部显示器”，而不是等 Host 返回错误。
- 切屏前 GUI 主动释放已按下键鼠状态；新 `VideoConfig.DisplayID + Generation` 是切换完成的最终权威状态，ABR、IDR、P2P/Relay path 与 audio side-channel 不需要重建。
- 新增 Host capture/input rollback、latest display control、DXGI/WGC 约束和 GUI/Wails binding 回归。
- 代表提交：`3611dce`、`a3ad4ed`、`83e0155`、`3413d7b`、`c662a36`、`f0c435b`、`e5a5467`、`e0e9edb`、`217c2b7`，测试/格式收尾至 `56e2f64`。
- 验证：Go CI #849 全部通过；UI CI #539 的 Linux UI/full regression、Windows desktop package、macOS desktop package 全部通过。

### 0.2.53 RD3 Live Display Topology + Display-Loss Recovery（已进入 main）

- 活动 Relay Desktop session 新增可靠控制消息 `DesktopSessionDisplays`；Host 通过现有 `CaptureCapabilitySource` 每 2 秒轻量刷新一次显示器拓扑，仅在 ID / 名称 / 尺寸 / RefreshHz / Primary / HDR 等快照真正变化时发送。
- 热插拔刷新完全走现有媒体 side-channel，不依赖重新认证或 Server 数据库更新：连接前仍使用登录阶段 capability snapshot，连接后 Controller 保存实时 display snapshot，Agent status 与 GUI 优先使用该快照。
- `RemoteDesktopStatus` 新增 `Displays / DisplaysReady`。独立 ready 标志明确区分“还没收到实时列表”和“实时列表确实为空”，避免 JSON `omitempty` 让空拓扑错误回退到旧登录快照。
- GUI 当前会话显示器下拉框因此会自动响应显示器插拔、分辨率变化和主屏变化；无需断开或手工刷新设备。
- 当前正在观看的显示器被拔掉时，媒体错误路径会立即重新枚举拓扑并尝试自愈，而不是先降级 codec：`auto / GDI` 回到虚拟桌面；显式 `DXGI / WGC` 优先切到剩余主屏，否则选择首个可用显示器。
- 丢屏恢复沿用运行时切屏的事务和 generation boundary：capture/input 成功切换后，同一 codec 用下一 generation 继续；不会把旧屏残留帧混到新屏，也不会因为单纯的显示器移除误触发 H.265→H.264→JPEG 降级链。
- 如果显式 DXGI/WGC 时系统暂时没有任何显示器，恢复策略不会伪造目标；会保留错误并等待后续拓扑/会话处理。
- 新增 topology change、defensive snapshot copy、empty topology、wire-level empty `displayId`、丢屏恢复策略等回归。
- 代表提交：`a6d6d09`、`9aaf838`、`b9bfdea`、`0e9b36c`、`9bbe355`、`2eaa109`、`caec356`、`278cbd3`、`bbec42f`、`b7909a4`、`c79cc3e`，测试/格式收尾至 `1d90233`。
- 验证：Go CI #884 的 gofmt / vet / 全量 test / race / benchmark 全部通过；UI CI #574 的 frontend / UI full regression、Windows desktop package、macOS desktop package 均已通过。
- 下一步：Windows 双机实测显示器热插拔与跨 DPI/负坐标布局；随后进入“同一远端同时打开多个显示器”的独立多窗口 / 多媒体流设计，而不是继续把单流切屏模型无限扩展。

### 0.2.54 RD3 Independent Multi-Stream / Multi-Window Foundation（已进入 main）

- Windows System Host 不再把整个 Relay Desktop 媒体生命周期锁成单实例：新增 `HostSessionFactory`，每条媒体关联独立创建 `windowsCapture + windowsInputSink`，因此 DXGI / WGC stream、显示器几何、输入坐标和 cursor 状态不会在多个窗口间共享可变状态；旧自定义 Host 未配置 factory 时继续保持原串行语义。
- `DesktopCapabilities` 新增 `MultiStream`；只有具备真正 per-session isolation 的 Host 才上报，避免“协议允许多流但底层 capture 仍共享”的假能力。Server 的在线 capability snapshot 使用整结构复制并深拷贝切片，`MultiStream` 会从 Host 登录快照完整透传到 Controller。
- `RemoteDesktopSessionInfo / RemoteDesktopStatus` 新增本地逻辑 `SessionID`；Agent 从单一 `desktopConnection` 演进为 primary + `desktopConnections[sessionID]` registry，连接、状态、Frame、Cursor、Input、Viewport ABR、IDR、Stats 与 Audio 均提供 session-scoped API。
- 旧 API 保持兼容：`ConnectRemoteDesktop / GetRemoteDesktopStatus` 继续指向 primary；primary 结束时可提升剩余活动 session，Tunnel replacement、global disconnect、Agent shutdown 会去重并关闭全部媒体 session。
- Native Viewer 从单窗口指针扩展为 `desktopViewers[sessionID]`；每个 Win32/D3D11 Viewer 独立读取自己的媒体 generation、decoder、cursor、input 和 viewport resolution，不再间接读取 primary 会话。
- GUI 的运行中显示器控件新增“独立窗口”。它复制 primary 的 codec / scene / quality / FPS / resolution / capture backend，仅替换具体 `DisplayID`；secondary 默认关闭 Audio、Clipboard、AutoLaunch，避免多个窗口重复采集系统音频或互相竞争剪贴板。
- secondary Viewer 关闭时自动释放对应媒体 session；primary Viewer 仍保持原语义——关闭窗口不会主动断开 primary Remote Desktop。
- 当前 P2P rendezvous 仍按 `ControllerID` 关联 target-side desktop media，无法区分同一 Controller 的两条并发媒体流。为保证正确性，只要活动 desktop stream > 1，就主动关闭 primary desktop P2P、停止 P2P retry，并让所有并发窗口使用 Relay QUIC Datagram；不会冒险把 direct socket 绑到错误窗口。
- 单流模式仍保留原 P2P 行为；从多流降回单流后，本阶段不自动复用旧 direct association，重新连接 primary 即可恢复 P2P。下一阶段会把逻辑 Desktop SessionID 带进 P2P rendezvous 后再消除这一限制。
- 新增 SessionID/registry 去重与 primary promotion、P2P eligibility、Host isolation / MultiStream capability、Server capability passthrough、Wails/GUI multi-window 静态回归。
- 代表提交：`9263a8a`、`74235bf`、`09f274b`、`7c351ff`、`b0a67c6`、`fe1642d`、`4616c52`、`29bbea6`、`9bf945d`、`549d327`、`a3c5aff`、`32e886b`、`74616f0`、`dc70595`、`f7eb952`、`d67dce6`，格式/测试收尾至 `e757b57`。
- 验证：Go CI #919 的 gofmt / vet / 全量 test / race / benchmark 全部通过；UI CI #609 的 frontend / UI full regression、Windows desktop package、macOS desktop package 全部通过。
- 下一步：给 P2P rendezvous / `RDPControlMessage` / target media association 增加逻辑 Desktop SessionID，以 `(ControllerID, SessionID)` 唯一标识并发 direct media；随后让每个 Viewer independently upgrade/fallback Relay ↔ P2P。

### 0.2.55 RD3 Independent Multi-Stream P2P Binding（已进入 main）

- Relay Desktop 媒体握手与 P2P control plane 新增向后兼容的 `DesktopSessionID`；Controller 在创建逻辑桌面 session 后即把同一个 ID 带入 Relay media request 与 `desktop_media` rendezvous，旧单流调用保持空 ID 兼容。
- P2P `Session / Lease` 保存并在 connect request/response/notify、candidate update、lease renew 与 session close 中完整透传 `DesktopSessionID`，避免后续重协商或候选更新丢失窗口身份。
- Target 侧 Relay media 与 direct application path 不再只按 `ControllerID` 关联，改为 `(ControllerID, DesktopSessionID)` 复合键；同一 Controller 的多个窗口可以并行建立 direct path，不会把 P2P socket 绑定到错误媒体流。
- Agent Controller 侧从单一 `desktopP2PSession` 演进为 `desktopP2PSessions[sessionID]`；每个 Relay Desktop session 独立执行 P2P 建连、质量监测、Relay ↔ P2P 切换、退避重试和关闭清理。
- 多流场景不再主动关闭 primary P2P，也不再因为活动 stream > 1 停止 P2P retry；primary/secondary Viewer 均可独立升级为 `udp_p2p`，某一窗口 direct path 丢失或质量降级只回退该窗口的 Relay Datagram。
- primary 替换、单 session 关闭、Tunnel replacement、global disconnect 与 Agent shutdown 均按 session 去重关闭 direct leases，旧 direct lease 的异步 OnClose 不会误删同一窗口已经重连的新 lease。
- Target Host 新增可选 `HostSessionHandler` 扩展，将逻辑 session ID 暴露给多流 Host wrapper；旧 `HostHandler` 实现无需修改即可继续工作。
- 新增 per-session P2P eligibility、Relay media SessionID、P2P manager SessionID 与 session-aware Host handler 回归。
- 代表实现 PR：#104；merge `ffceef7`。
- 验证：Go CI #922 的 gofmt / vet / 全量 test / race / benchmark 全部通过；UI CI #612 的 frontend / UI full regression、Windows desktop package、macOS desktop package 全部通过。
- 下一步：进入 RD3 剩余的 4:4:4 / 高色彩质量链路设计与实现，同时保留 Windows 双机多显示器、多窗口 P2P 与跨 DPI/负坐标布局实测作为发布前验证项。

### 0.2.56 RD3 4:4:4 Negotiation / I444 Media Foundation（已进入 main）

- `RemoteDesktopConnectOptions` 新增 `Chroma=auto|420|444`，Controller 在发起连接前进行严格 capability negotiation：显式 4:4:4 仅允许 H.264 / H.265，并要求 Target 同时上报 `Encode + Chroma444`、Controller 本机上报 `Decode + Chroma444`；任一端缺失都会直接拒绝，不静默降成 4:2:0。
- `VideoConfig / RemoteDesktopStatus / RemoteDesktopFrame / Controller FrameSnapshot` 全链路携带 chroma 与 bit depth；native Viewer 的 generation rebuild 条件扩展为 codec + chroma + bit-depth + resolution，因此未来同分辨率 4:2:0 ↔ 4:4:4 切换也会 flush/reopen decoder。
- Codec 层新增 `PixelFormatI444` 与 8-bit planar I444 surface，并实现 RGBA→I444、padded BGRA→I444、I444→BGRA 的 BT.709 limited-range 转换；每个像素保留独立 U/V sample，为彩色文字/UI 边缘的 4:4:4 backend 提供公共 CPU 输入/验证边界。
- Host session policy 新增 `Chroma / BitDepth`；H.264/H.265 CONFIG 不再硬编码 `420/8`，而是从 generation config 产生，旧 peer/旧调用仍默认 4:2:0 8-bit。
- Windows 内置 Media Foundation 路径明确保持 8-bit 4:2:0：MF H.264/H.265 encoder/decoder 遇到 4:4:4 会返回 unavailable，Host 在没有真正 4:4:4 encoder backend 时也会拒绝 session，不允许“NV12 实际 4:2:0、协议却标 4:4:4”的假能力。
- GUI 新增“色彩采样”选择（自动 / 4:2:0 / 4:4:4），并在目标未上报相应 Chroma444 encode capability 时前置阻止连接；帮助文案同步修正多窗口现在已支持每个媒体流独立 Relay ↔ P2P。
- 新增 chroma negotiation、VideoConfig 格式校验、Host chroma policy、I444 stride/彩色边缘 round-trip、native Viewer chroma/bit-depth rebuild 与 GUI contract 回归。
- 代表实现 PR：#105；merge `98829f1`。
- 验证：Go CI #928 的 gofmt / vet / 全量 test / race / benchmark 全部通过；UI CI #618 的 frontend / UI full regression、Windows desktop package、macOS desktop package 全部通过。
- 下一步：接入第一个真正能输出/解码 4:4:4 的 Windows codec backend。优先评估 NVENC / QSV / AMF 的 vendor-native 能力与部署成本；只有实际 probe 成功的 backend 才允许把 `Chroma444=true` 写入在线 capability snapshot。

### 0.2.57 RD3 Intel oneVPL HEVC 4:4:4 Probe（已进入 main）

- Windows amd64 新增 oneVPL dispatcher 动态探测，不引入编译期 oneVPL SDK 依赖；运行时按需加载 `libvpl.dll`。
- Probe 不依据 GPU 型号/代际推断能力，而是使用 oneVPL dispatcher property filters 真实筛选 Intel Hardware + D3D11 implementation。
- HEVC 4:4:4 encode / decode 独立探测：分别要求 HEVC codec + AYUV ColorFormats，并只在两侧都满足时认为 `HEVC444EndToEnd=true`。
- Win64 `mfxVariant` ABI 显式建模，并增加 size/offset 回归，锁定 `MFXSetConfigFilterProperty` 的 16-byte indirect argument 布局。
- 非 Windows/非 amd64 平台提供安全 unsupported fallback，不影响现有跨平台编译。
- Probe 已接入 Windows Host 启动诊断，输出 dispatcher / hardware runtime / HEVC444 encode / decode / end-to-end 状态。
- 本阶段刻意保持 `advertised=false`：驱动/硬件能力与 RelayProxy 已实现 codec backend 分离，避免仅检测到 oneVPL 就错误上报 `Chroma444=true`。
- 代表实现 PR：#106；merge `a2f87dd`。
- 验证：Go CI #934 的 gofmt / vet / 全量 test / race / benchmark 全部通过；UI CI #624 的 frontend / UI full regression、Windows desktop package、macOS desktop package 全部通过。
- 下一步：实现 oneVPL HEVC AYUV encoder/decoder backend，接入现有 generation-aware Host / native Viewer；只有实现层和 runtime probe 同时可用时才开放公开 4:4:4 capability。

### 0.2.58 RD3 oneVPL HEVC 4:4:4 End-to-End MVP（已进入 main）

- oneVPL HEVC 8-bit 4:4:4 encoder 已接入 Host generation：Windows amd64 运行时动态加载 `libvpl.dll`，要求 HEVC RExt + AYUV，使用 oneVPL internal system-memory surface；Relay Desktop 的 RGBA/BGRA/I444 输入统一转换为 I444/AYUV 后进入硬件编码器。
- oneVPL encoder 支持 IDR、bitrate-only reset、HEVC VPS/SPS/PPS 提取与现有 generation-aware CONFIG/ABR 生命周期；4:2:0 H.265 继续使用 Media Foundation，不改变成熟路径。
- native Viewer 新增 I444 CPU frame 渲染路径；NV12 + D3D11 zero-copy 路径保持不变。
- oneVPL HEVC 4:4:4 decoder 已接入原生 Viewer：首次 generation 通过 `MFXVideoDECODE_DecodeHeader` 解析码流，再严格确认 HEVC RExt / AYUV / 8-bit 4:4:4；输出 surface 经 Synchronize + Map(read) 拆成 I444 后进入 Viewer。
- Decoder 维护未消费 bitstream、处理 device-busy / more-data / generation 参数变化，并在 Flush 后关闭 decoder component、下一次关键帧懒初始化，保持现有 Viewer 恢复语义。
- 在线 codec capability 新增 `encodeChroma / decodeChroma` 方向级列表；新 peer 优先按方向判断 4:2:0 / 4:4:4，旧 peer 继续使用 `chroma420 / chroma444` 共享字段。
- 只有本机 oneVPL probe 同时确认 HEVC 4:4:4 encode + decode，且 RelayProxy encoder/decoder 实现均已存在时才公开 `Chroma444=true`；不按 Intel GPU 型号猜能力。
- H.265 `auto` 明确等价于现有 4:2:0 路径并要求对应方向的 `420` capability；oneVPL-only 4:4:4 设备不会被误用于 H.265 4:2:0 session。
- 混合 MF/oneVPL 场景保留方向精度，同时对旧客户端保持保守共享 capability；Host 与 Server session snapshot 对新增嵌套 chroma slice 做深拷贝，避免 capability alias。
- 代表实现 PR：#107（encoder）、#108（I444 Viewer）、#109（decoder）、#110（runtime capability advertisement）；#110 merge `80dd506`。
- 验证：PR #110 Go CI #954 的 gofmt / vet / 全量 test / race / benchmark 全部通过；UI CI #644 的 frontend / UI full regression、Windows desktop package、macOS desktop package 全部通过。
- CPU-surface MVP 仍保留为兼容回退；PR #111–#113 已在其上补齐 D3D11 AYUV GPU 编解码路径。支持条件满足时不再执行两端 CPU packing/readback；实际 Intel GPU/driver 可用性仍必须由 runtime probe 与双机实测确认。

### 0.2.59 RD3 Typed GPU Surface / oneVPL D3D11 Decode（已进入 main）

- 新增共享 GPU frame abstraction：统一携带 backend、D3D11 device/resource/subresource、尺寸与 `NV12 / AYUV / P010 / BGRA` 格式，Native Viewer 新增 `SubmitGPU`，旧 `SubmitD3D11` 保持兼容。
- 现有 Media Foundation NV12 zero-copy 路径已真实迁移到该 abstraction；D3D11 VideoProcessor renderer 增加 NV12 / AYUV / P010 输入格式映射与 runtime format support 校验，为后续高色彩 surface 共用同一呈现层。
- GPU frame 增加 retain/release owner 生命周期，避免 oneVPL internal surface 在 Viewer 尚未呈现时被 decoder pool 回收；generation 切换会先清理 Viewer 当前 GPU frame，再关闭旧 decoder。
- oneVPL HEVC 4:4:4 decoder 新增 video-memory 模式：使用 Native Viewer 自己的 D3D11 device 调用 `MFXVideoCORE_SetHandle`，要求输出 AYUV video-memory surface。
- 解码 surface 通过 oneVPL native/device handle ABI 直接暴露为 D3D11 AYUV texture，并在 Viewer 持有期间对 mfx surface AddRef/Release；支持时数据链从 `oneVPL decode → Map(read) → I444 → BGRA` 改为 `oneVPL decode → D3D11 AYUV → VideoProcessor → swap chain`。
- Viewer 只有在同一 D3D11 device、AYUV input support 与 GPU cursor 条件都满足时才选择该路径，否则自动回到现有 system-memory I444 4:4:4 decoder。
- 协议增加 `DesktopGPUCapability` schema 以及 server snapshot 深拷贝，但本阶段没有因为 schema 存在就无条件上报 zero-copy，避免假能力。
- 代表实现 PR：#111（GPU abstraction，merge `d96f6ea`）、#112（oneVPL D3D11 AYUV decode，merge `14e24b4`）。
- 验证：#111 / #112 的 Go format/vet/full test/race/benchmark、UI full regression、Windows/macOS desktop package 均通过；Intel 实机 decode/display zero-copy 仍需 runtime 硬件验证。

### 0.2.60 RD3 oneVPL D3D11 AYUV Encode Zero-Copy（已进入 main）

- `D3D11EncodeFrame` 现在携带 device 与 pixel format；空 format 保持兼容地视为 NV12。Media Foundation H.264/H.265 D3D11 encoder 继续严格只接受 NV12，避免 AYUV surface 被错误送入 4:2:0 MFT。
- D3D11 VideoProcessor converter 从 NV12-only 扩展为 NV12 / AYUV 两种输出；4:4:4 Host generation 使用 `BGRA capture texture → AYUV texture`，4:2:0 继续使用现有 NV12。
- oneVPL HEVC 4:4:4 encoder 新增 video-memory 模式，并在 `Init` 前绑定 capture D3D11 device；system-memory I444 模式保持不变作为 fallback。
- GPU encode 不依赖实验性的 oneVPL surface import API：RelayProxy 先取得 oneVPL 自己分配的 AYUV encode surface，再通过同一 D3D11 immediate context 做 `CopySubresourceRegion`。这能兼容 oneVPL 内部 16 对齐纹理，例如 1920×1080 可见区与 1920×1088 内部分配。
- 支持条件满足时 Host 数据链变为 `DXGI/WGC D3D11 BGRA → VideoProcessor AYUV → GPU copy → oneVPL HEVC RExt 4:4:4`，不再执行 CPU `BGRA/RGBA → I444` 和 `Map(write)`。
- H.265 Host GPU probe 不再排除 Chroma444；generation rebuild、捕获几何变化与 runtime resolution change 均根据 chroma 选择 AYUV 或 NV12 converter。Stats/diagnostics 对 4:4:4 GPU 路径标记 `d3d11-ayuv`。
- D3D11 初始化或运行期失败时继续迁回 oneVPL system-memory I444 generation；4:2:0 H.265 仍使用 Media Foundation，不改变成熟路径。
- 代表实现 PR：#113；merge `ef87e16247c419b3cf8bb41d17c7159986318ec9`。
- 验证：最终 Go CI #971 的 format/vet/full test/race/benchmark 全部通过；UI CI #661 的 full regression、Windows desktop package、macOS desktop package 全部通过。
- 当前剩余：完成 Intel 双机实际 AYUV encode/decode/display 验证与驱动矩阵；根据实测结果继续校准公开策略，并进入 NVIDIA/AMD 4:4:4 vendor-native backend。

### 0.2.61 RD3 Runtime GPU Capability Snapshot（已进入 main）

- `Host` 新增线程安全的 `DesktopGPUCapability` snapshot 保存/深拷贝；`DesktopCapabilities()` 与 isolated multi-stream Host 会完整继承并上报 GPU capability，不再出现协议已有 `GPU` 字段但登录快照始终为空的情况。
- Windows runtime probe 不按 GPU 型号或“代码存在”推断 zero-copy。探测会先从真实 DXGI/WGC 会话取得当前 D3D11 capture frame/device，再在同一个 device 上验证后续媒体链。
- NV12 只有在 BGRA→NV12 VideoProcessor、Media Foundation H.264 D3D11 hardware encode（包含一次真实 `EncodeD3D11`）、D3D11 zero-copy decoder，以及 NV12→BGRA VideoProcessor display 四段均成立时，才进入 `GPU.Formats`。
- AYUV 只有在 BGRA→AYUV VideoProcessor、oneVPL HEVC RExt 4:4:4 D3D11 encode（包含一次真实 `EncodeD3D11`）、oneVPL D3D11 decoder `Query/Init`，以及 AYUV→BGRA VideoProcessor display 四段均成立时，才进入 `GPU.Formats`。
- `EncodeZeroCopy / DecodeZeroCopy / DisplayZeroCopy` 仅在至少一个格式完成端到端验证后上报；探测失败保持 `GPU=nil`，避免把 codec 枚举成功、DLL 存在或单段能力误报成整条 zero-copy 能力。
- D3D11 首帧探测对 `ErrNoFrame` / timeout 做有限重试，避免静态桌面或 capture 刚启动时产生瞬时假阴性；非瞬态错误仍立即退出并记录各格式 encode/decode/display 失败原因。
- oneVPL D3D11 decoder probe 在 Windows amd64 上真实执行 hardware+D3D11 session、`MFXVideoCORE_SetHandle`、HEVC 4:4:4 video-memory `Query` 与 `Init`；非 Windows / 非 amd64 提供 unavailable stub，保持 ARM64 与其他平台编译兼容。
- 代表实现 PR：#115；merge `dd6d6ce6cd8702869e82625e6326178a2bde44bf`；随后 `aeba81bd6c48e057094898ec3f85fcdd66d359d1` 修复新增 D3D11 probe 的 gofmt 对齐。
- 验证：Go CI #978 的 format/vet/full test/race/benchmark 全部通过；UI CI #668 的 frontend/full regression、Windows desktop package、macOS desktop package 全部通过。
- 下一步：在至少两台 Intel Windows 机器与多个驱动版本上完成真实 AYUV encode/decode/display 双机验证，核对 capability snapshot、实际 backend 与 diagnostics 一致性；随后依据矩阵结果进入 NVIDIA NVENC / AMD AMF 4:4:4 vendor-native backend。

### 0.2.62 RD3 GPU Zero-Copy Diagnostics Validation（已进入 main）

- Diagnostics schema 升级到 v6，并在每个 Relay Desktop session 建立时深拷贝目标端 `DesktopGPUCapability` 为 `targetGpu`；目标重新登录、离线或 capability 变化不会改写已经开始的会话验证基线。
- 新增 `gpuValidation` 汇总，按当前实际 codec/chroma 推导预期 GPU format：4:2:0 对应 NV12，HEVC 4:4:4 对应 AYUV。
- Host encode zero-copy、Viewer decode zero-copy、Viewer display zero-copy 分开统计，不再把“decoder 输出 GPU surface”自动等同于“最终 GPU 呈现成功”。
- `DesktopSessionStats` 新增 `RenderBackend`；Native Viewer GPU surface 成功提交到 D3D11 renderer 时上报 `d3d11-zero-copy`，GPU submit/readback 回退后的 CPU BGRA 呈现上报 `cpu-bgra`。
- AYUV 只有同一 diagnostics sample 同时满足 `captureFormat=d3d11-ayuv`、`encoderBackend=onevpl-hevc444-d3d11-zero-copy`、`decoderBackend=onevpl-hevc444-d3d11-zero-copy`、`renderBackend=d3d11-zero-copy` 时，才计入 `EndToEndZeroCopySamples`。
- 新增 `ViewerDisplayZeroCopySamples`、`RenderBackends` 与 `FallbackSamples`；因此 Intel 双机实测可以直接从导出报告区分“目标未声明 / Host encode fallback / Viewer decode fallback / Viewer display fallback”。
- 修复原生多窗口 Viewer stats 的 session 路由：secondary Viewer 不再把 decoder/render stats 写到 primary session，而是按逻辑 Desktop SessionID 独立上报。
- 代表实现 PR：#116；merge `832869f563b4d52b469b65a7783ebb5e62d04541`；gofmt 修复至 `80c6ebf21a0df81b697802ed34ec057aeb138dd4`。
- 验证：Go CI #990 的 format/vet/full test/race/benchmark 已通过；UI full regression 已通过；#116 原始合并提交的 Windows/macOS desktop package 均已通过。最新格式修复仅调整 gofmt 空白，不改变运行逻辑。
- Intel 双机验收条件：目标 `targetGpu.formats` 必须包含 `ayuv` 且 encode/decode/display 三个 zero-copy flag 均为 true；稳定 4:4:4 会话中 `EndToEndZeroCopySamples` 必须持续增长，并且 `FallbackSamples` 不应在正常稳态增长。若发生 runtime fallback，报告必须能明确定位到 encode/decode/display 中的具体断点。
- 下一步：完成 Intel GPU/driver 双机矩阵；代码侧开始评估 NVIDIA NVENC / AMD AMF 4:4:4 vendor-native backend，并继续保持“runtime probe 成功才公开 capability”的原则。

### 0.2.63 RD3 GPU Zero-Copy GUI Observability（已进入 main）

- 远程桌面目标卡片直接展示登录快照中的 GPU runtime capability，例如 `GPU D3D11 NV12/AYUV E/D/R`；E / D / R 分别代表 encode / decode / render(display) zero-copy。
- 活动会话 banner 同步展示目标 GPU capability，实测时无需切到日志或导出 JSON 即可确认目标是否声明 NV12 / AYUV。
- 运行中媒体统计新增实际 `RenderBackend` 展示，并基于当前 codec/chroma 自动推导期望 surface：4:2:0 为 NV12，H.265 4:4:4 为 AYUV。
- GUI 新增运行时链路摘要：`GPU AYUV E✓/D✓/R✓` 表示 Host encode、Viewer decode、Viewer D3D11 display 三段均命中；任一段回退会显示对应的 ×。
- 如果运行时实际命中 zero-copy 但目标登录快照没有声明相应 format，会明确显示“未声明”，便于发现 capability probe 与实际运行结果不一致的问题。
- Node UI 回归覆盖 target card capability 渲染、AYUV E2E、GPU decode 后 CPU display fallback、以及未声明 runtime path。
- 代表实现 PR：#117；merge `9d89ca8ac0d175ab06e828677b58a66332ec2ef0`。
- 验证：Go CI #993 的 format/vet/full test/race/benchmark 全部通过；UI CI #683 的 frontend/full regression、Windows desktop package、macOS desktop package 全部通过。
- 下一步：把 HEVC 4:4:4 capability / backend 选择从 Intel oneVPL 单实现抽象为 vendor backend registry，在不改变现有 Intel 行为的前提下，为 NVIDIA NVENC / AMD AMF runtime probe 与 codec backend 留出独立入口。

### 0.2.64 RD3 Vendor-Neutral HEVC 4:4:4 Backend Registry（已进入 main）

- 新增 vendor-neutral HEVC 8-bit 4:4:4 backend registry；Intel oneVPL 仍是当前唯一注册实现，现有 Intel 行为和 fallback 语义保持不变。
- Host 4:4:4 encoder、Native Viewer 4:4:4 decoder、Windows 启动 capability probe、AYUV D3D11 runtime validation 全部改走统一 backend helper，上层业务代码不再直接依赖 oneVPL opener。
- `H265CapabilityWith444Backends` 可聚合多个 vendor 的 runtime capability，同时保留旧 `H265Capability(mf, oneVPL)` API 兼容现有调用/测试。
- registry 保持确定性优先级；probe 会保留 backend 名称和失败原因，便于后续比较 NVENC / AMF / oneVPL。
- 增加 implementation gate：只有 runtime probe 成功且 RelayProxy 已注册对应 encoder / decoder opener，该方向才会进入可用 capability；“驱动/SDK 报告支持”仍不会自动变成用户可选能力。
- 公共 `Chroma444` 继续保守：本机必须同时存在可用的 4:4:4 encode 与 decode 方向才公开；方向可以来自不同已实现 vendor backend。
- AYUV diagnostics、GUI E/D/R 状态以及 GPU cursor 判断从 oneVPL 精确字符串改为受控的 hardware `*-d3d11-zero-copy` backend 约定，为未来 NVENC / AMF 接入避免重复修改上层逻辑。
- 新增 registry priority / name backfill / runtime gate / implementation gate、多 vendor capability 聚合和 vendor-neutral AYUV diagnostics/UI 回归。
- 代表实现 PR：#118；merge `eb6e10a1f64c7e12e428a2f420911344a64b791f`；gofmt 修复 `016b34c4867084b21aa88cc651d4513b3c4e65ba`。
- 验证：Go CI #999 的 format/vet/full test/race/benchmark 全部通过；UI CI #689 的 frontend/full regression、Windows desktop package、macOS desktop package 全部通过。
- 下一步：先实现 NVIDIA / AMD 的“candidate runtime probe”与诊断输出，但不注册 public opener、不公开 `Chroma444`；待各自 encode/decode backend 真正落地并通过 D3D11 实测后再加入正式 registry capability。

### 0.2.65 RD3 NVIDIA / AMD HEVC 4:4:4 Runtime Candidate Probe（已进入 main）

- 新增 diagnostic-only `H265444RuntimeCandidate`，与正式 `h265444BackendRegistry` 完全分离；candidate 结果不能直接影响 `Chroma444` negotiation。
- NVIDIA NVENC candidate：Windows amd64 动态加载 `nvEncodeAPI64.dll`，解析 `NvEncodeAPIGetMaxSupportedVersion` 与 `NvEncodeAPICreateInstance`，并记录驱动支持的 NVENC API 版本。
- NVIDIA NVDEC candidate：动态加载 `nvcuvid.dll` 并确认 `cuvidGetDecoderCaps` entry point 存在。当前不调用该函数，因为 NVIDIA 要求有效 CUDA context 才能安全查询 codec/chroma capability。
- AMD AMF candidate：动态加载 `amfrt64.dll`，解析 `AMFInit` / `AMFQueryVersion` 并记录 runtime version。
- `EncodeRuntime` / `DecodeRuntime` 仅表示未来 vendor integration 所需 runtime entry points 可加载，不代表当前 GPU 支持 HEVC 4:4:4。
- Windows Host 启动日志新增结构化 candidate 输出：vendor / backend / runtime / encodeRuntime / decodeRuntime / version / implemented / advertised=false / error。
- Windows ARM64、Linux、macOS 使用 stub；不会因 NVIDIA/AMD x64 probe 引入额外 DLL 或编译依赖。
- 代表实现 PR：#119；merge `fed484f6e64f79d6a2e681585a026089a137e8ce`。
- 验证：Go CI #1002 的 format/vet/full test/race/benchmark 全部通过；UI CI #692 的 frontend/full regression、Windows desktop package、macOS desktop package 全部通过。
- 下一步：在 candidate 层继续做真实 device/context capability probe。NVIDIA 需要创建受控 CUDA/D3D11 device context 后查询 NVENC HEVC/YUV444 encode caps 与 NVDEC HEVC/4:4:4 decode caps；AMD 需要通过 AMF factory/context 创建 HEVC component 并查询 surface/chroma 支持。即使 probe 成功，仍不注册 public opener，直到实际 encode/decode + D3D11 zero-copy 路径完成。

### 0.2.66 RD3 NVIDIA NVDEC HEVC 4:4:4 Device Capability Probe（已进入 main）

- NVIDIA candidate 从“runtime entry point 可加载”推进到真实设备级 decode capability 查询：Windows amd64 动态加载 `nvcuda.dll`，解析 CUDA Driver API，并枚举本机 CUDA devices。
- probe 在短生命周期 CUDA context 内调用 `cuvidGetDecoderCaps`，固定查询 HEVC / YUV 4:4:4 / 8-bit；只有真实 device query 返回 `IsSupported` 才设置 candidate `HEVC444Decode=true`。
- CUDA context 与 OS thread 生命周期绑定：probe 期间锁定 goroutine 到当前线程，结束前显式 `cuCtxSetCurrent(NULL)`，再销毁 context，避免把 CUDA current context 遗留到 Go 线程池。
- `H265444RuntimeCandidate` 新增 `DeviceProbe`、`DeviceCount`、`HEVC444Encode`、`HEVC444Decode`。这些字段仍然是 diagnostics-only；`Implemented=false` 且 `advertised=false`。
- 固定 `CUVIDDECODECAPS` ABI size 与 HEVC / 4:4:4 enum 值的 Windows 单测，降低 SDK ABI 手写绑定漂移风险。
- 本轮只验证 NVDEC decode capability；不会据此注册 production decoder、不会声明 D3D11 zero-copy output，也不会公开 `Chroma444`。
- 代表实现 PR：#120；merge `8deed5c5678e4f305069bfa27e827d2dd3fcdceb`；gofmt 修复 `8bc366fd2846c601cc92fddf64dd6f0904f8fe3e`。
- 验证：Go CI #1006 的 format/vet/full test/race/benchmark 全部通过；UI CI #696 的 frontend/full regression、Windows desktop package、macOS desktop package 全部通过。
- 下一步：把 vendor candidate diagnostics 作为机器可读 target snapshot 随远程桌面目标能力传递，并进入 diagnostics JSON；该字段只用于诊断，selector / negotiation 必须完全忽略。之后再实现真实 NVENC HEVC/YUV444 encode-capability probe。

### 0.2.67 RD3 Vendor GPU Candidate Target / Diagnostics Snapshot（已进入 main）

- 新增 protocol 层 diagnostic-only `DesktopGPUCandidateDiagnostics`，把 NVIDIA / AMD candidate runtime/device 结果随认证的 `DesktopCapabilities` 目标快照传递到控制端。
- candidate 数据链：Host probe -> DeviceHello -> Server authorized target snapshot -> ControllerSession -> diagnostics recorder。
- diagnostics schema 升级到 v7，导出 `targetGpuCandidates`；会话建立时固化快照，目标端之后重连或 capability 改变不会篡改当前会话的诊断证据。
- candidate 字段不参与 `SelectBackend`、codec selector 或 HEVC 4:4:4 registry；新增回归明确验证“即使 NVIDIA candidate 报告 HEVC444 decode，也不能凭 candidate 启用 Relay Desktop backend”。
- Server 继续以当前 `desktop.host` grant 为授权边界；未授权目标的 GPU / candidate / codec / display 动态细节全部剥离。
- 顺带修复 Remote Desktop target 原有浅拷贝边界：captures、codec chroma slices、GPU formats、GPU candidates、displays、audio codecs 统一走 protocol 深拷贝。
- 代表实现 PR：#121；merge `cac34f5b4634d999fb8c7479d262d3992245ee64`；gofmt 修复提交 `17bb28f3b7116f62b1cb86a38888999d4f15f7e1` / `65cd58b767676dc5b4506af91b825e4996239d8b` / `e1d3d2c93eec72ad2613e0a8bdd5e59cec7b2cbb`。
- 验证：Go CI #1013 全绿；UI CI #703 全绿，含 Windows/macOS desktop package。

### 0.2.68 RD3 NVIDIA NVENC HEVC 4:4:4 Device Capability Probe（已进入 main）

- NVIDIA candidate 新增真实 encode device capability：Windows amd64 使用 `NvEncodeAPICreateInstance` 获取完整 NVENC function table，在每个 CUDA device 的短生命周期 context 中调用 `nvEncOpenEncodeSessionEx`。
- probe 先枚举 encode codec GUID；只有设备公开 HEVC GUID 时，才调用 `nvEncGetEncodeCaps` 查询 `NV_ENC_CAPS_SUPPORT_YUV444_ENCODE`。只有该真实查询成功且返回非零，candidate 才设置 `HEVC444Encode=true`。
- 动态 ABI 固定到 Video Codec SDK / nv-codec-headers 13.1：API 13.1、function-list struct v2、open-session/caps struct v1、YUV444 caps enum 33、官方 HEVC GUID。
- Windows 测试固定 `NV_ENCODE_API_FUNCTION_LIST` 2552-byte size、关键 function offset、session/caps struct size、HEVC GUID 与枚举常量，降低手写 FFI ABI 漂移风险。
- 为避免旧驱动 ABI 猜测，驱动 `NvEncodeAPIGetMaxSupportedVersion` 低于 13.1 时只保留 runtime candidate，并记录 probe ABI 不兼容；不会强行创建设备 session。
- NVENC 与 NVDEC 的真实查询合并到同一 NVIDIA candidate：`HEVC444Encode` / `HEVC444Decode` 独立记录，`DeviceProbe` 表示至少有一个真实 device/context capability query 完成。
- candidate probe 总预算由 3 秒提高到 6 秒，容纳 NVENC + NVDEC 双查询；仍然是启动期一次性诊断，不进入媒体热路径。
- 仍保持 `Implemented=false` / `advertised=false`：没有 NVENC production encoder opener、没有 NVDEC production decoder opener、没有 D3D11/CUDA zero-copy 实测之前，不公开 `Chroma444`。
- 代表实现 PR：#122；merge `bf65a6f92ee4614a5a95672834e193c45e4ed871`。
- 验证：Go CI #1015 的 format/vet/full test/race/benchmark 全部通过；UI CI #705 的 frontend/full regression、Windows desktop package、macOS desktop package 全部通过。
- 下一步：明确 AMD AMF 的 HEVC 4:4:4 API 能力边界。当前 AMF HEVC public profile 仅 Main/Main10；若没有 FRExt/4:4:4 output profile，candidate 必须明确记录“AMF API 不提供 HEVC 4:4:4 encode”，不能把 AYUV/Y410 等输入 surface 格式误判为 4:4:4 bitstream 支持。

### 0.2.69 RD3 AMD AMF HEVC 4:4:4 Public API Boundary（已进入 main）

- candidate capability 新增 known-state：`EncodeCapabilityKnown` / `DecodeCapabilityKnown`，用于区分“明确支持/不支持”与“尚未有可靠查询方式”。
- 新增稳定 diagnostics limitation code；AMF encode 当前为 `amf_hevc_public_profiles_main_main10_only`。
- AMD AMF HEVC encoder public API 仅公开 Main / Main10 profile。虽然 AMF surface API 有 AYUV / Y410 / Y416 等 4:4:4 surface format，但这些是输入/输出 surface 能力，不能等价为 HEVC 4:4:4 bitstream profile。
- 因此 AMD candidate encode 明确记录：`EncodeCapabilityKnown=true`、`HEVC444Encode=false`、`Implemented=false`，而不是把 false 留成无法区分的 unknown。
- AMD decode 仍保持 `DecodeCapabilityKnown=false`：AMF decoder `GetOutputCaps()` 可以枚举 output surface formats，但 public decoder API 没有类似 NVDEC `chroma_format` 的压缩 HEVC bitstream chroma capability query。AYUV/Y410 output surface 不能作为 HEVC 4:4:4 decode 证明。
- 若后续要把 AMD decode 变成 known，需要真实 HEVC 4:4:4 validation stream 解码闭环，或使用能直接返回 bitstream profile/chroma 能力的更底层 AMD 接口。
- diagnostics schema 由 v7 升级到 v8，target candidate JSON 增加 encode/decode known-state 与 limitation。
- 代表实现 PR：#123；merge `8d46877291b9ecfc33be0e697c63cc361bc03b4e`；gofmt 修复 `83616916782571fe252e66b2014641d8d2ba7720` / `bd41520e3749a9f2eea5826ff056005f8af9d1a8` / `b491ef8de8d405eedcd9f2e682e3dcd15fdfc7f8`。
- 验证：Go CI #1021 全绿；UI CI #711 全绿，含 Windows/macOS desktop package。
- 下一步：candidate 能力发现阶段已基本闭环。转入 NVIDIA production backend scaffolding：定义 NVENC/NVDEC opener 生命周期、生产 registry gate、资源 ownership 与 D3D11/CUDA interop contract；只有真实 encode/decode + zero-copy 验证完成后才允许 `Implemented=true` 与 `Chroma444` 广告。

### 0.2.70 RD3 NVIDIA NVCodec HEVC 4:4:4 Production Backend Scaffold

- vendor-neutral HEVC 4:4:4 registry 新增显式 production gate：只有 `productionReady=true`、`zeroCopyValidated=true`、生命周期合约有效、D3D11 interop 合约有效并且对应 opener 已实现时，backend 才允许进入 capability / opener 选择。
- registry 的方向判定改为同时接受 system-memory opener 或 D3D11-only opener，为后续 NVENC/NVDEC 只实现 GPU zero-copy production path 留出正确边界。
- 定义 backend session 生命周期合约：codec context 与 interop registrations 由 session 持有；`Close()` 必须幂等，并在返回前释放所有 session-owned 资源。
- 定义 D3D11 zero-copy ownership：encoder 只在单次 `EncodeD3D11` 调用期间借用输入 texture；decoder 输出 surface 由 backend 持有到对应 `DecodedFrame.Close()`。
- NVIDIA scaffold 使用 `d3d11-cuda` interop contract，输入/输出均固定为 AYUV 8-bit 4:4:4，并要求 capture / codec / viewer 保持在同一 D3D11 device / adapter 边界。
- Windows amd64 registry 已挂入 `nvcodec-hevc444` production slot，但不重复执行 NVIDIA device probe；真实 NVENC/NVDEC device capability 继续只由 candidate diagnostics 负责，避免启动期双重探测。production opener 仍未挂接。
- NVIDIA backend 明确保持 `productionReady=false`、`zeroCopyValidated=false`；因此即使 NVENC/NVDEC capability probe 均为 true，也不会公开 `Chroma444`，也不会被 `OpenH265444*()` 选中。
- 新增测试固定 production gate、D3D11-only opener、lifecycle ownership、D3D11/CUDA interop ownership 以及 NVCodec 默认关闭行为。
- 代表实现 PR：#124。
- 下一步：实现 NVENC production encoder session opener。编码端优先采用 NVENC 原生 DirectX/D3D11 session + texture registration/map/unmap；CUDA interop 保留给后续确有需要的 NVDEC 路径。完成真实 encode 验证后仍保持 backend 总 gate 关闭，直到 NVDEC + zero-copy round trip 同样通过。

### 0.2.71 RD3 NVIDIA NVENC D3D11 Production Session Lifecycle

- 新增真实 NVENC production session loader：加载 `nvEncodeAPI64.dll`，查询 driver max API version，并严格要求当前固定 ABI 13.1；旧驱动不会进入 production session。
- production function table 增加完整性校验，要求 initialize、bitstream buffer、register/map/unmap/unregister resource、encode、lock/unlock bitstream、sequence params、reconfigure 与 destroy 等后续编码热路径所需 entry point 全部存在。
- Windows NVENC session 改为直接使用 `NV_ENC_DEVICE_TYPE_DIRECTX` + Relay Desktop 现有 D3D11 device 打开，不为编码端额外创建 CUDA context。NVIDIA 官方 API 支持 Windows D3D11 device 直接建 session 与注册外部分配的 D3D11 resource。
- session 打开后再次在该真实 encode session 上确认 HEVC codec 与 YUV444 encode capability；没有真实 4:4:4 capability 时立即关闭，不留下半可用 session。
- `nvencD3D11Session` 明确拥有 NVENC encoder handle 与 runtime DLL lifetime，但只借用上层 D3D11 device；`Close()` 幂等并保证先 destroy encoder 再卸载 runtime。
- NVIDIA backend interop 标识从过窄的 `d3d11-cuda` 调整为 `d3d11-nvcodec`：NVENC 输入可以原生注册 D3D11 texture，后续 NVDEC 仍可在同一 adapter/device 边界内通过 CUDA interop 输出。
- 当前仍未把 session opener 挂入通用 `OpenH265444EncoderWithD3D11`，也未打开 production gate；尚缺 encoder initialize、AYUV resource register/map、bitstream encode/lock、sequence header、IDR/reconfigure 实现与真实硬件验证。
- 下一步：基于该 session 实现 `SequenceHeaderEncoder + D3D11Encoder`，完成一帧 AYUV D3D11 texture 的 register → map → encode → lock bitstream → unmap/unregister 闭环。

### 0.2.72 RD3 NVIDIA NVENC D3D11 Resource + Bitstream Lifecycle

- 新增 NVENC D3D11 external resource ABI：`NV_ENC_REGISTER_RESOURCE`、`NV_ENC_MAP_INPUT_RESOURCE`、`NV_ENC_CREATE_BITSTREAM_BUFFER` 按当前固定的 Video Codec SDK 13.1 布局定义，并增加 Windows amd64 size/offset 测试，降低手写 FFI 漂移风险。
- NVENC 输入格式固定为 `NV_ENC_BUFFER_FORMAT_AYUV`，resource type 使用 DirectX，usage 使用 input image；注册前继续要求 Relay Desktop 的 `D3D11EncodeFrame` 为 AYUV、有效 texture、正偶数尺寸且 device 与 encoder session 一致。
- 完成 D3D11 texture `register -> map -> unmap -> unregister` 生命周期；映射结果会再次检查 NVENC 返回的 mapped buffer format，若不是 AYUV 立即解映射并拒绝进入 encode。
- input resource `Close()` 幂等；如果调用方忘记显式 `Unmap()`，`Close()` 会先 unmap 再 unregister，避免长期保留 mapped/registered resource。
- 新增 system-memory bitstream output buffer 的 create/destroy 生命周期，为下一步 `NvEncEncodePicture -> NvEncLockBitstream` 提供稳定 output handle。
- 当前 resource helper 仍只作为 NVENC encoder 内部 building block，尚未挂入 `OpenH265444EncoderWithD3D11`，production gate 保持关闭。
- 下一步：补 `NV_ENC_INITIALIZE_PARAMS/NV_ENC_CONFIG_HEVC` 初始化，再把 mapped AYUV resource 与 bitstream buffer 接进 `NV_ENC_PIC_PARAMS`，完成第一帧 HEVC 4:4:4 encode + bitstream lock 闭环。

### 0.2.73 RD3 NVIDIA NVENC HEVC 4:4:4 Initialization

- 新增 NVENC 13.1 initialization ABI 固定层：`NV_ENC_CONFIG=3584`、`NV_ENC_PRESET_CONFIG=5128`、`NV_ENC_INITIALIZE_PARAMS=1808`，全部强制 8-byte alignment；关键 offset 使用当前 SDK/bindgen layout 固化。
- 初始化先调用 `nvEncGetEncodePresetConfigEx` 获取 P1 preset，再在 preset 基础上覆盖 Relay Desktop 所需参数，避免手工从零构造整个 3.5KB codec config。
- HEVC profile 固定为 `NV_ENC_HEVC_PROFILE_FREXT_GUID`，用于 HEVC Main 4:2:2/4:4:4 8/10-bit family；当前 Relay Desktop 仍限定 8-bit 4:4:4。
- HEVC config 明确设置 `chromaFormatIDC=3` 与 `repeatSPSPPS=1`；IDR/GOP 周期跟随 `KeyframeEvery × FPS`，默认 30 FPS / 2 秒即 60 帧。
- rate control 使用 CBR，目标码率来自 `VideoConfig.TargetBitrate`；默认低延迟模式关闭 lookahead、禁止 B frame（`frameIntervalP=1`）、开启 zero-reorder，并使用约一帧码率大小的 VBV buffer / initial delay。
- `DisableLowLatency=true` 时切换到 high-quality tuning，并清除 zero-reorder/single-frame VBV，但仍保持无 B frame，避免当前同步 D3D11 resource 生命周期出现跨帧引用。
- session 在初始化成功后保留 config/init blob 与规范化 `VideoConfig`，为后续 bitrate reconfigure / sequence header / encode picture 复用；重复相同配置初始化幂等，不同配置返回 rebuild-required。
- production function table 现在强制要求 `nvEncGetEncodePresetConfigEx`；旧/不完整 runtime 不进入 production initializer。
- 当前仍未挂入通用 encoder opener，NVCodec production gate 保持关闭。
- 下一步：实现 `NV_ENC_PIC_PARAMS` + `NV_ENC_LOCK_BITSTREAM` ABI，并把已完成的 AYUV map + bitstream buffer 接入第一帧 `nvEncEncodePicture`，拿到真实 HEVC Annex-B 输出。

### 0.2.74 RD3 NVIDIA NVENC HEVC 4:4:4 First D3D11 Frame

- 新增真实 `nvencH265Encoder` 内部实现，并保持独立 opener `OpenNVENCH265EncoderWithD3D11`；当前仍未注册到通用 HEVC 4:4:4 backend，因此不会改变线上选择结果。
- 增加 `NV_ENC_PIC_PARAMS`（3360 bytes）、`NV_ENC_LOCK_BITSTREAM`（1552 bytes）、`NV_ENC_SEQUENCE_PARAM_PAYLOAD`（1544 bytes）固定 ABI blob，均保持 8-byte alignment，并对关键 size/offset/version 做 Windows 测试。
- opener 现在完成完整准备链：D3D11 NVENC session → HEVC 4:4:4 initialize → system-memory bitstream buffer → `nvEncGetSequenceParams`；只有拿到包含 VPS/SPS/PPS 的 HEVC sequence header 才返回 encoder。
- 单帧 GPU 路径完成：AYUV D3D11 texture → register → map → `NvEncEncodePicture` → blocking `NvEncLockBitstream` → copy Annex-B bytes → unlock → unmap/unregister。
- 默认首帧和显式 `ForceIDR` 使用 `FORCEIDR | OUTPUT_SPSPPS`，并同时通过 NVENC picture type 与 HEVC IRAP NAL type 判断 keyframe；编码流中若再次出现 VPS/SPS/PPS，会刷新 encoder 的 sequence header。
- output bitstream 增加边界检查：空输出、NULL pointer、超过 64 MiB 的异常返回均拒绝复制，避免手写 FFI 下的无界内存读取。
- encoder stats 已记录 hardware/backend、frames、bytes、last encode time；raw CPU frame 明确拒绝，只接受同尺寸 AYUV D3D11 frame。
- `Close()` 顺序固定为先 destroy bitstream buffer、再 destroy NVENC encoder/session/runtime，避免 output buffer 生命周期越过 encoder。
- bitrate reconfigure 本阶段仍返回 `ErrEncoderControlUnsupported`；下一阶段单独实现 `NV_ENC_RECONFIGURE_PARAMS`，避免与首帧编码闭环混在同一风险面。
- 当前 production gate 仍关闭，通用 `OpenH265444EncoderWithD3D11` 仍不会选择 NVENC；需要 Windows NVIDIA 真机 encode 验证以及后续 NVDEC/zero-copy round trip 后才允许打开。

### 0.2.75 RD3 NVIDIA NVENC Bitrate Reconfigure

- 新增 `NV_ENC_RECONFIGURE_PARAMS` ABI：固定 1824 bytes / 8-byte alignment，内嵌当前 SDK 13.1 的 `NV_ENC_INITIALIZE_PARAMS`；version 使用 reserved-bit v2。
- NVENC encoder 的 `Reconfigure` 现在支持真正的 bitrate-only 动态调整，不再统一返回 unsupported。
- 重配置边界与现有 oneVPL 路径一致：只允许 `TargetBitrate` 改变；width、height、FPS、keyframe interval、chroma、bit-depth、low-latency mode 任一变化都返回 `ErrEncoderRebuildRequired`。
- 动态码率仍使用 CBR；重新计算 average/max bitrate 与低延迟单帧 VBV，同时保留原 preset/config 中其他 NVENC 参数。
- `NvEncReconfigureEncoder` 失败时不修改 session 的 active config；只有驱动返回成功后才原子更新保存的 `VideoConfig`、config blob 与 init blob。
- 重配置成功后 encoder 设置 pending IDR，下一帧走 `FORCEIDR | OUTPUT_SPSPPS`，确保码率切换后立即建立新的可独立解码边界。
- 当前仍不开放 resolution/FPS/GOP/tuning 在线重配；这些变化继续走 generation rebuild，避免把尚未真机验证的 NVENC reconfigure 能力暴露到生产路径。
- production gate 继续关闭；下一步增加 NVIDIA 真机诊断入口，输出 session/init/sequence/first-frame/reconfigure 各阶段结果，然后再进入 NVDEC 4:4:4 zero-copy 解码闭环。

### 0.2.76 RD3 NVIDIA NVDEC D3D11/CUDA Session Foundation

- 新增 NVDEC session 基座，继续保持 production gate 关闭；本阶段只解决 D3D11 adapter 到 CUDA/NVDEC runtime 的可靠绑定与生命周期，不提前暴露 decoder。
- Windows amd64 运行时链固定为 `nvcuda.dll + nvcuvid.dll`；NVDEC function table 强制包含 decoder caps/create/destroy/decode/map/unmap、video parser create/parse/destroy，以及 CUVID context lock create/destroy/lock/unlock。
- D3D11 与 CUDA 设备绑定改用当前 CUDA Driver API 的 `cuD3D11GetDevices(..., CU_D3D11_DEVICE_LIST_ALL)`，直接从 session 的 `ID3D11Device` 获取 CUDA device，避免依赖 GPU 型号或独立枚举顺序。
- zero-copy session 当前要求一个 D3D11 device 精确映射到一个 CUDA device；多 GPU / linked-adapter 返回 unavailable，不在未验证条件下猜测 primary device。
- CUDA context 创建后立即创建 `CUvideoctxlock`，为后续 NVDEC parser callback / decoder surface 映射提供 NVIDIA 推荐的 floating-context 同步边界；session 返回前清除当前线程 CUDA context。
- CUDA context 是线程相关状态：创建、清理均使用 `runtime.LockOSThread`；后续实际 decode/map 阶段也必须在 context lock + current-context 规则内执行。
- session `Close()` 幂等，清理顺序为 context lock → CUDA context → `nvcuvid.dll` → CUDA driver module；初始化中途失败也只有一条资源所有权清理路径，避免 DLL/context 双释放。
- 新增 function-table 缺失检测与 Close 幂等测试；Windows CI 只验证 ABI/编译边界，真机 CUDA/NVDEC runtime 仍需 NVIDIA 主机执行。
- 下一步：补 `CUVIDPARSERPARAMS / CUVIDSOURCEDATAPACKET / CUVIDDECODECREATEINFO` ABI，完成 HEVC 4:4:4 parser sequence callback → `cuvidCreateDecoder`，随后再接 decode/map 与 D3D11 AYUV 输出。

### 0.2.77 RD3 NVIDIA NVDEC HEVC 4:4:4 Parser + Decoder Create

- 固定 Windows x64 NVDEC parser/decode-create ABI：`CUVIDPARSERPARAMS=136`、`CUVIDSOURCEDATAPACKET=24`、`CUVIDEOFORMAT=64`、`CUVIDDECODECREATEINFO=112`；特别按 Windows ABI 的 32-bit `unsigned long` 建模，避免误用 Linux 下 64-bit `unsigned long` 的 32/176-byte packet/create-info 布局。
- 新增 NVDEC parser callback handle registry：传给 `pUserData` 的是 RelayProxy 自己分配的整数 handle，而不是长期保留 Go heap pointer；sequence/decode/display callback 通过 `sync.Map` 找回 parser owner。
- parser 使用 HEVC codec、10 MHz timestamp clock、零 display delay；callback 与 `cuvidParseVideoData` 保持同步调用边界，`Parse` 与 `Close` 通过独立 call mutex 串行化，避免 parser destroy 与 callback 并发。
- sequence callback 严格验证 codec=HEVC、chroma=4:4:4、8-bit、progressive、有效 coded/display dimensions，且 display size 必须与当前 `VideoConfig` generation 一致；格式变化不在本阶段静默重配。
- sequence callback 在创建 decoder 前重新调用 `cuvidGetDecoderCaps`，检查 HEVC 8-bit 4:4:4 支持和 max width/height；decoder output 固定为 `cudaVideoSurfaceFormat_YUV444`，progressive 使用 weave，decode path 选择 `cudaVideoCreate_PreferCUVID`。
- `CUVIDDECODECREATEINFO` 绑定上一阶段创建的 `CUvideoctxlock`；parser 返回 sequence 所需 DPB surface 数给 NVDEC，以覆盖初始 parser surface hint。
- decode callback 不需要在 Go 侧复制巨大的 `CUVIDPICPARAMS` ABI，直接把 NVDEC callback 给出的 opaque pointer 传回 `cuvidDecodePicture`；display callback 当前只记录 ready-frame 事件，尚未 map/copy/interop 输出 surface。
- parser/decoder/session 关闭顺序固定为 parser → decoder → NVDEC session；callback registry 会在 destroy 前移除，`Close()` 保持幂等。
- 当前仍没有 `Decoder` production opener，也不会返回 `DecodedFrame`；NVIDIA backend 的 `productionReady=false / zeroCopyValidated=false` 不变。
- 下一步：补 `CUVIDPARSERDISPINFO / CUVIDPROCPARAMS` 与 `cuvidMapVideoFrame64`，拿到 planar YUV444 CUDA output，并实现 CUDA YUV444 → D3D11 AYUV 同 adapter 输出 surface 生命周期。

### 0.2.78 RD3 NVIDIA NVDEC Display Queue + CUDA Frame Map

- 新增 `CUVIDPARSERDISPINFO=24` 与 `CUVIDPROCPARAMS=264` 的 Windows x64 ABI 固定层，并对 timestamp/output_stream 等关键 offset 做测试。
- display callback 不再只计数：现在复制 NVDEC 给出的 `picture_index / progressive_frame / top_field_first / repeat_first_field / timestamp`，进入有界 display queue；队列上限 32，避免 viewer 停止消费时无限积压。
- 当前 NVIDIA 4:4:4 路径继续只接受 progressive frame；display callback 若收到 interlaced frame 直接终止该 parser 回调，不把未经验证的 field processing 带入 zero-copy 链路。
- 新增 `MapNextDisplay`：对 ready picture 调用 `cuvidMapVideoFrame64`，返回内部 `nvdecMappedFrame`，持有 CUDA device pointer、pitch、picture index、输出尺寸与按 10 MHz parser clock 还原的 presentation timestamp。
- mapped frame 生命周期严格一一对应 `cuvidMapVideoFrame64 / cuvidUnmapVideoFrame64`；显式 `Close()` 幂等，parser `Close()` 也会先统一 unmap 所有仍存活 frame，再 destroy parser/decoder/session。
- 即使 session/API 在关闭阶段异常，Go 侧 mapped frame 也会立即失效，后续不再暴露旧 CUDA pointer，避免 use-after-free 风险。
- 修复上一阶段 parser 真机风险：`cuvidCreateDecoder` 现在明确在 `CUvideoctxlock` 边界内执行，匹配 NVIDIA floating CUDA context 的使用要求；CI 只能验证编译，真机初始化行为仍需 NVIDIA Windows 主机确认。
- 本阶段故意不把 planar YUV444 CUDA pointer 伪装成 `D3D11Surface`。NVDEC 4:4:4 输出是 CUDA YUV444 surface，而 Relay Desktop viewer 的 zero-copy contract 要求 D3D11 AYUV；二者之间仍需 GPU-side pack/interop。
- production gate 继续保持 `productionReady=false / zeroCopyValidated=false`，通用 decoder opener 仍不会选择 NVDEC。
- 下一步：创建同 adapter 的 D3D11 AYUV output texture，使用 CUDA-D3D11 graphics interop 注册并映射 texture，再通过 CUDA kernel 将 NVDEC planar YUV444 打包为 AYUV；完成 GPU-only surface 转换后再封装成 `DecodedFrame.D3D11`。

### 0.2.79 RD3 NVIDIA NVDEC CUDA ↔ D3D11 AYUV Interop Foundation

- 新增 CUDA Driver graphics interop function table：强制解析 `cuGraphicsD3D11RegisterResource`、register/unregister、map/unmap、subresource mapped-array 与 map-flags API；缺任一入口即不进入该 interop 层。
- 新增 `nvdecD3D11AYUVInteropSurface`：只在 NVDEC session 已绑定的同一个 `ID3D11Device` 上创建 `DXGI_FORMAT_AYUV` texture，不创建第二张跨 adapter 设备。
- AYUV texture 使用 D3D11 default usage / render-target bind，与现有 Relay Desktop D3D11 video-processor AYUV 路径保持兼容；CUDA registration 使用 `CU_GRAPHICS_REGISTER_FLAGS_SURFACE_LDST`，为后续 GPU kernel surface write 预留能力。
- graphics resource map flags 使用 write-discard；`Map()` 调 `cuGraphicsMapResources` + `cuGraphicsSubResourceGetMappedArray`，当前只暴露内部 `CUarray` handle，不返回 `DecodedFrame`。
- 所有 CUDA graphics register/map/unmap/unregister 都通过 session 的 `CUvideoctxlock` 执行；新增 session 内部 `withCUDAContextLock`，并在调用期间持有 session mutex，避免 CUDA context 与 graphics 操作并发销毁。
- interop surface `Close()` 幂等；若仍处于 mapped 状态，顺序固定为 unmap → unregister → release D3D11 texture。
- 本阶段没有 CPU readback，也没有把 NVDEC planar YUV444 pointer 伪装成 AYUV；只有 graphics interop 资源基座，纹理内容在 pack kernel 写入前不视为有效帧。
- production gate 继续保持 `productionReady=false / zeroCopyValidated=false`。
- 下一步：补 CUDA module/kernel API，加载固定 PTX pack kernel，将 NVDEC planar Y/U/V 8-bit surface 按 pitch/surface height 读取并写入 mapped AYUV `CUarray`；kernel + stream 同步成功后才封装为 `D3D11Surface`。

### 0.2.80 RD3 NVIDIA NVDEC Planar YUV444 → D3D11 AYUV PTX Pack

- 新增 driver-only CUDA pack module，不引入 NVRTC / CUDA Toolkit 运行时依赖；PTX 作为 RelayProxy 内置字符串交给 `nvcuda.dll` 的 `cuModuleLoadData` JIT。
- kernel API 强制解析 `cuModuleLoadData / cuModuleGetFunction / cuModuleGetSurfRef / cuSurfRefSetArray / cuLaunchKernel / cuCtxSynchronize / cuModuleUnload`，缺任一入口即不进入该路径。
- pack kernel 固定输入为 NVDEC `cudaVideoSurfaceFormat_YUV444` 的 8-bit planar Y/U/V surface；按 `pitch * surfaceHeight` 计算 U/V plane 起点，并读取三个等尺寸平面。
- Relay Desktop 的 `DXGI_FORMAT_AYUV` 内存布局固定为 V,U,Y,A，因此 kernel 每像素直接写 `V,U,Y,255`；无颜色空间变换、无 CPU readback，只做 GPU 内存布局转换。
- 输出继续使用上一阶段映射得到的 D3D11 AYUV `CUarray`；PTX 使用 surface reference + `sust.b.2d.v4.b8` 写入，x 坐标按 CUDA surface 指令要求转换为 byte offset（`x * 4`）。
- kernel launch 使用 16×16 thread block；grid 按输出尺寸向上取整，越界线程在 PTX 内提前退出。
- source `nvdecMappedFrame` 在 kernel launch + `cuCtxSynchronize` 全程持有 frame mutex 与所属 NVDEC session 引用，阻止 `cuvidUnmapVideoFrame64` 在 GPU 读取完成前执行。
- destination interop surface 的 `Map → Pack → Unmap` 与 `Close` 通过独立 call mutex 串行化；pack 成功也必须先 `cuGraphicsUnmapResources`，D3D11 才重新取得 texture 所有权。
- PTX module/surface reference/function 由 `nvdecAYUVPacker` 生命周期管理，`Close()` 幂等并在 CUDA context lock 内 unload module。
- 当前仍不返回 production `DecodedFrame`；下一阶段将把成功 pack 的 AYUV texture 封装成有引用计数的 `D3D11Surface`，再实现独立 NVDEC `Decoder` opener 与 NVENC→NVDEC 真机 round trip。
- NVIDIA backend 的 `productionReady=false / zeroCopyValidated=false` 保持不变。

### 0.3 本轮进度（2026-09-22）

本轮继续完成四项 RD2 网络路径与自适应能力，并全部合并到 `main`：

- PR #43：路径评分与切换滞回，merge `275f78d717befb5aafa241db915fc1dfc2c8d915`。
- PR #44：真实 direct-path RTT/Jitter 探针，merge `bd241b1daf7a60396559002b6a87d303938e6b81`。
- PR #46：确定性弱网 transport shim 与路径切换场景测试，merge `c8ba2163a708c51d4232a3da0c40f660c069e91e`。
- PR #47：send-queue ABR 拥塞闭环与默认路径策略场景标定，merge `35a69ac6b1900ad2a53a6c6718c5d8cc4e902354`。

已完成：

- 集中式 `PathScorePolicy` 与 `PathSwitchGate`，统一管理 RTT、Jitter、Loss、Send Queue Delay、Relay penalty、升级 margin、稳定窗口和紧急切换阈值。
- 每次 Relay ↔ P2P 切换重置媒体路径本地 packet-order 边界，避免跨路径 Sequence 跳变被误判为大规模丢包。
- P2P 切换前保存 Relay 媒体质量基线；`udp_p2p` 激活后持续比较路径质量，并在连续劣化超过滞回窗口时主动降级回 Relay。
- 主动降级继续复用 2/4/8/16/30 秒有界指数退避与 20 秒稳定窗口。
- Relay → P2P 和 P2P → Relay 切换都会触发 H.264 IDR 恢复。
- direct-path RTT/Jitter 不再借用可靠 Relay 控制流：复用现有认证 `PunchKeep/PunchAck`，在同一 `udp_p2p` socket 上直接测量。
- `desktop_media` direct probe cadence 为 1 秒；Native RDP 保持原 10 秒 keepalive，不改变原 RDP 行为。
- `PunchAck` 在媒体 datagram 解码前被内部消费，不会泄漏给 RD/1 Reassembler。
- P2P Session 维护 RTT/Jitter EWMA；路径评分器现在可以同时使用真实 P2P RTT、Jitter、媒体丢包和 Host send-queue delay。
- Relay 基线使用 Relay session probe；P2P 使用 direct socket probe，明确隔离两个测量域。
- 增加认证 direct RTT、RTT/Jitter 平滑、评分滞回与 path-boundary 统计测试。
- 新增确定性 Datagram transport shim：delay、jitter、随机丢包、burst loss、写侧 bandwidth limit 均可独立配置，固定 seed 可复现相同场景。
- 新增 Relay → P2P 稳定晋升、瞬时 P2P 劣化后取消回退、持续 P2P 劣化后回退 Relay、P2P 不可用立即 failover 等确定性策略测试。
- 新增真实 `desktop_media` probe + impairment 集成测试，验证 `PunchKeep/PunchAck` RTT 观测包含网络 shim 注入的延迟。
- H.264 与 JPEG Host 发送路径新增媒体 `Send()` 阻塞时间 EWMA，并通过 Stats 上报真实 `SendQueueDelayMs`；此前字段存在但 Host 未实际填充的问题已修复。
- bitrate-only ABR 新增 send-queue 拥塞输入：30/60/120 ms 分别进入 mild/normal/severe queue 降级档，避免限速场景必须等到丢包后才响应。
- queue delay 超过稳定阈值时阻止码率恢复；链路恢复健康后仍沿用连续稳定窗口慢速升码率，避免带宽反复抖动。
- transport shim 新增运行时 `SetProfile`，可在不重建 UDP socket 的前提下切换带宽/时延/丢包阶段，并重新开始固定 seed 的确定性序列。
- 新增媒体发送阻塞实测单测，以及带宽骤降/恢复 ABR 场景；同时用 clean direct、lossy direct、queued direct、near-equal paths 固化当前默认 `PathScorePolicy` 行为。

验证结果：

```text
Go CI
  ✓ gofmt
  ✓ go vet
  ✓ go test ./...
  ✓ race core data path
  ✓ benchmark smoke

UI CI
  ✓ frontend / UI full regression
  ✓ Windows desktop packages
  ✓ macOS desktop packages
```

下一轮重点：

1. ✅ 组合弱网确定性场景、stale-frame/drop 实时性保护、bitrate/FPS/resolution 三层 ABR、generation-aware Encoder/Decoder rebuild 与可导出 diagnostics schema v2 均已进入 `main`。
2. 使用“导出诊断”完成同 LAN、IPv4 NAT、IPv6、Relay-only、Wi-Fi 抖动等实机矩阵；每次测试保留 raw 500 ms samples 与 Summary，重点比较 p50/p95 RTT/Jitter/Loss/Queue、DroppedFrames、PathSwitches、GenerationChanges、ABRChanges 与 ResolutionChanges。
3. 完成 Intel / NVIDIA / AMD 编码与解码路径验证，并通过 `CaptureBackends / EncoderBackends / DecoderBackends`、硬件样本数确认实际媒体链路，而不是仅依据日志或 FPS 推测。
4. 在真实样本完成前保持当前 `PathScorePolicy`、ABR pressure/recovery window 和 P2P retry/path-switch 默认参数，不用纯模拟结果直接改生产权重。
5. 根据实机诊断 Summary 对 ABR 阈值、resolution hold、PathScorePolicy 权重、upgrade/emergency margin 与 P2P retry/path-switch 参数做定向校准，并继续用确定性弱网场景防止回归。

### 0.3.1 组合弱网联动验证（本轮新增）

- `internal/testnetem` 增加组合 profile 确定性测试：30 ms 基线延迟、±12 ms jitter、18% random loss、周期 burst loss 与 12 KB/s 写侧限速同时启用，并验证固定 seed 下 drop/delay/queue 序列可重复。
- `agent/desktop/path_policy_scenario_test.go` 增加 ABR 与路径切换联动场景：短时组合劣化立即触发 bitrate 降级，但不会绕过 `PathSwitchGate`；健康窗口会取消 fallback probation，ABR 仍按稳定窗口慢恢复。
- 持续组合劣化必须经过完整 `UpgradeHold` 才从 `udp_p2p` 回退 Relay；回退后的健康 direct path 也必须重新经历 promotion hold，防止 P2P / Relay 来回振荡。
- 该轮只固化现有默认参数行为，不因纯模拟结果调整生产权重；权重调整继续等待跨 NAT / Wi-Fi 实机数据。

- Viewer 网络统计条现已补充媒体 Path、Send Queue Delay、Dropped Frames、Capture/Encode 耗时，便于跨 NAT / Wi-Fi 实机验证时直接观察拥塞与 stale-frame 行为。

### 0.3.2 Scene-aware Adaptive FPS（已合并 PR #51）

- `DesktopVideoControl` 新增向后兼容的 `TargetFPS` 字段；Host 只调整采集 ticker，不重建 H.264 Encoder、不改变 resolution/generation。
- Office / Auto / Quality 只有在 severe queue、持续 stale/drop、严重丢包或严重 jitter 连续多个 500 ms 窗口后才降低采集 FPS，避免单次抖动造成画面节拍变化。
- Gaming / Performance 的 adaptive minimum FPS 等于 negotiated FPS，因此 ABR 继续只降码率，不牺牲高帧率交互目标。
- 网络恢复时先把 bitrate 按既有稳定窗口逐步恢复到上限；之后再用更长的稳定窗口慢速恢复 FPS，避免 bitrate 与 FPS 同时上冲重新制造队列积压。
- Host Stats 新增 `TargetFPS`，Viewer 网络统计条同步展示目标 FPS，方便实机校准 pressure/recovery window。

### 0.3.3 在线 Host Capability Snapshot（已合并 PR #52）

- Agent 握手的 `DeviceHello` 新增可选 `DesktopCapabilities` 快照；只有本次实例实际装载 Relay Desktop Host 时才上报，不改变数据库中的管理员授权模型。
- Windows Host 在握手时重新枚举当前显示器，把会话级 HMONITOR ID、设备名、像素尺寸、主屏标记以及 GDI / DXGI capture backend 汇总到 `Displays / Captures`；显示器 ID 明确不持久化，布局变化或重连后重新获取。
- Media Foundation H.264 codec 能力、Host 最大分辨率/FPS、Clipboard 等现有能力一并进入同一 snapshot，为 Controller 在连接前展示真实能力提供数据源。
- Server 不把动态显示器/GPU 信息写入数据库；Gateway 只把快照保存在当前认证 `DeviceSession`，避免离线后继续暴露过期硬件状态。
- Controller 获取 `RemoteDesktopTargets` 时仍先通过数据库验证所有权、显式 grant 与 backend 权限；只有目标当前在线且该认证 Session 的有效 grant 仍包含 `desktop.host`，才合并显示器/codec/capture 等动态详情。
- Server 返回动态切片前再次复制，避免认证快照被 Controller 侧 DTO 修改；没有当前 `desktop.host` grant 或仅允许 Native RDP 时不会泄露显示器详情。
- 该能力快照是后续 GUI 显示器下拉框、`DisplayID` 选屏、选中显示器 DXGI/GDI Capture、光标/输入坐标几何校正的基础层。

### 0.4 当前实现与最终设计的差异

为了优先验证 Windows Home 的端到端链路，RD1 中间增加了一个功能验证阶段：

```text
当前验证：
DXGI Desktop Duplication（不可用时 GDI）→ 原生尺寸优先 BGRA direct → CPU NV12（缩放时回退 RGBA）→ Media Foundation H.264
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
RD1  Windows Relay Desktop Relay-only MVP                ✅ 已完成
RD2  P2P + ABR + 性能统计                                🧪 direct probe + transport shim + queue ABR 闭环已完成，组合弱网 / 实机验证中
RD3  H.265 / 4:4:4 / 音频 / 多显示器                    🧪 H.265 + Opus + 多显示器/P2P + oneVPL HEVC 4:4:4 CPU-surface MVP 已完成，D3D11 zero-copy / 实机验证中
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