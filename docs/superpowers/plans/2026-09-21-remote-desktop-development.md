# RelayProxy Remote Desktop 开发实施文档

> 日期：2026-09-21  
> 状态：实施中 — RD0 / RD1 已完成；RD2 P2P / ABR / 弱网 / 诊断 / WGC / D3D11 zero-copy 主链已完成；RD3 HEVC probe、encoder/decoder core、generation-aware Viewer、Host generation、隐藏端到端验证入口与验证诊断均已合并，H.265 仍待 Intel/NVIDIA/AMD 实机验证后再公开；当前继续推进音频数据面基础。  
> 对应设计：`docs/superpowers/specs/2026-09-21-remote-desktop-design.md`  
> 基线：main 分支，现有 RDP M1–M5 已完成  
> 当前开发基线：`main`（PR #86 已合并，merge `f769d91862e0fe5ecc475553936e29b621e3c8f0`）

## 0. 当前进度

更新时间：**2026-09-23**

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

### 0.2.30 RD3 Audio Media Foundation（当前分支）

- 为 RD/1 预留固定媒体 stream ID：video=1、audio=2、cursor=3；音频使用独立 stream/sequence 域，避免与视频丢包统计互相污染。
- 新增 `DesktopAudioConfig` / `audio_config` session message，描述 generation、codec、sample rate、channels、bit depth、frame duration 与 target bitrate；先建立稳定 wire model，再接具体 Windows capture/decoder。
- 现有 `PacketizeFrame` 保持 video-only 兼容接口；新增 typed `PacketizeMediaFrame`，可复用同一 RD/1 header/MTU fragmentation 发送 audio。
- Reassembler 新增 packet-type scope，默认仍只接受 video；未来 Controller 将为 audio 使用独立 reassembler，禁止把 audio fragment 混入 video generation/frame 状态。
- 现有 ABR/loss tracker 继续只计算 video packet sequence；audio datagram 不会制造假的 video packet gap。
- 新增 audio out-of-order fragmentation/reassembly、video/audio type isolation、audio config JSON round-trip 与 sequence-domain 隔离测试。
- 本阶段不启用音频采集或播放，也不修改默认 GUI；下一步接 Controller audio demux/buffer，再实现 Windows WASAPI loopback capture/native playback。

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
RD3  H.265 / 4:4:4 / 音频 / 多显示器                    🚧 HEVC 验证链已就绪，音频数据面基础开发中
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