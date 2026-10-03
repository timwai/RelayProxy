# RelayProxy Android

Android 支持作为 **Relay 网络出口**，也支持作为 **代理客户端**：本机应用可连接回环 SOCKS5 / HTTP 代理；可选启用 Android VPN，将其他应用的 TCP/UDP 流量转发到已授权出口。控制与授权经过 Relay Server。代理客户端与出口端都支持 P2P QUIC；未建立直连或直连失败时，新连接自动使用 Relay。

## 架构

- `mobile/androidcore`：Go + gomobile。复用设备认证、QUIC/TLS+yamux、`TunnelDialer`、TCP/UDP 转发与出口 ACL；同一个 Core 提供出口服务及经过 `proxy.client` 授权的本机代理入口。`-javapkg com.relayproxy.core` 生成的 Java 包为 `com.relayproxy.core.androidcore`。
- `android/app`：Kotlin 原生 UI + 前台 Service。`RelayExitService` 持有唯一 Go Core；`RelayVpnService` 管理 VPN 授权、TUN fd 和前台通知，启动时复用或重建该 Core。
- VPN 使用 `VpnService` + 固定版本 `hev-socks5-tunnel`，将 IPv4/IPv6 默认路由送入独立且需要认证的内部回环 SOCKS5。RelayProxy 自身应用从 VPN 路由中排除，避免隧道回环。
- VPN 可选择全部应用、仅选中应用或排除选中应用；DNS 使用 hev-socks5-tunnel Mapped DNS，在 TUN 内返回 Fake-IP，并在建立 SOCKS5 连接时恢复为原始域名交给所选出口解析，避免把明文 UDP/53 DNS 暴露给出口网络。当前物理网络同步给 Android VPN，Wi-Fi / 蜂窝切换时共享 Core 会重连。
- Android 10+ 会根据原始 TCP/UDP 四元组查询连接 UID，把共享 UID 映射为包名组，再交给共用路由引擎匹配应用、目标、端口和协议条件。Android 8/9 保留非应用规则，并拒绝启用含应用条件的规则。
- SOCKS5 UDP ASSOCIATE 按目标维护有界 UDP association；HTTP 代理支持普通 HTTP 与 HTTPS CONNECT。所有本机代理仅绑定回环地址。

## 一键打包 APK（Windows）

仓库已提供：

`scripts/build-android.ps1`

默认构建 Release APK：

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\build-android.ps1
```

清理后重新构建：

```powershell
.\scripts\build-android.ps1 -Clean
```

构建 Release：

```powershell
.\scripts\build-android.ps1 -Configuration Release -Clean
```

产物统一输出到：

`dist/android/`

Windows 脚本默认构建同时包含 `armeabi-v7a` 与 `arm64-v8a` 的 Release APK，并签名为 `RelayProxy-Android-release.apk`（未配置 `RELAY_ANDROID_*` 时使用本机 debug 密钥）。如需 Debug，可执行 `./scripts/build-android.ps1 -Configuration Debug`。正式发布请设置：

```powershell
$env:RELAY_ANDROID_KEYSTORE="D:\keys\relayproxy.jks"
$env:RELAY_ANDROID_KEY_ALIAS="relayproxy"
$env:RELAY_ANDROID_KEYSTORE_PASSWORD="your-store-password"
$env:RELAY_ANDROID_KEY_PASSWORD="your-key-password"
.\scripts\build-android.ps1 -Configuration Release -Clean
```

脚本会自动完成 gomobile AAR 构建、Android SDK/NDK 检查、Gradle 8.9 获取、Hev TUN 引擎构建、APK 构建、Release 签名（配置签名时）以及 SHA256 输出。Gradle 会检查打包的每个 `.so` 是否使用 16 KB ELF LOAD 对齐。构建期间为 gomobile 临时加入的 Go tool 依赖会在结束后恢复，不会永久修改 `go.mod` / `go.sum`。

## 一键打包 APK（Mac mini M4 / Apple Silicon）

推荐入口：

`scripts/build-android-macos.sh`

兼容旧入口：

`scripts/build-android-macos-arm64.sh`

宿主机要求 macOS arm64（Mac mini M4 / M1 / M2 / M3 均可）。默认一次构建两个独立 APK：

- `arm64-v8a`：Go Core 使用 `gomobile bind -target=android/arm64`
- `armeabi-v7a`：Go Core 使用 `gomobile bind -target=android/arm`

Gradle 同时使用 `-PrelayAbi=<ABI>` 限定 APK 中的原生库，因此两个 APK 都只包含各自架构，不会混入 x86 / x86_64。

默认构建两个 Release APK：

```bash
./scripts/build-android-macos.sh
```

清理后完整重建：

```bash
./scripts/build-android-macos.sh --clean
```

默认构建两个 **已签名** Release APK（未配置 `RELAY_ANDROID_*` 时使用本机 Android debug 密钥，可直接安装）：

```text
dist/android/RelayProxy-Android-arm64-release.apk
dist/android/RelayProxy-Android-arm32-release.apk
```

只构建 ARM64：

```bash
./scripts/build-android-macos.sh --arm64-only
```

只构建 ARM32：

```bash
./scripts/build-android-macos.sh --arm32-only
```

显式构建 Debug：

```bash
./scripts/build-android-macos.sh --debug --clean
```

默认 Release 配置签名后输出：

```text
dist/android/RelayProxy-Android-arm64-release.apk
dist/android/RelayProxy-Android-arm32-release.apk
```

未配置 `RELAY_ANDROID_*` 时，脚本会用本机 `~/.android/debug.keystore` 签名，APK 可直接安装。上架或正式发布请设置：

```bash
export RELAY_ANDROID_KEYSTORE="$HOME/keys/relayproxy.jks"
export RELAY_ANDROID_KEY_ALIAS="relayproxy"
export RELAY_ANDROID_KEYSTORE_PASSWORD="your-store-password"
export RELAY_ANDROID_KEY_PASSWORD="your-key-password"

./scripts/build-android-macos.sh --release --clean
```

脚本会自动识别默认 Android SDK 路径 `$HOME/Library/Android/sdk`、Apple Silicon Homebrew JDK 17、Android SDK 35、Build Tools 35.0.0、NDK 27.2.12479018，并在本机没有 Gradle 时下载 Gradle 8.9。每个 ABI 都会单独重建 gomobile AAR 和 Android APK，避免不同架构之间复用旧的 Native Library。

构建脚本会从上游固定下载 `hev-socks5-tunnel 2.18.0` 源码，并在校验 SHA-256 后为 `arm64-v8a` 和 `armeabi-v7a` 编译 API 26 原生库。许可证及校验信息见 [third_party/hev-socks5-tunnel](../third_party/hev-socks5-tunnel/README.md)。

## 构建

需要 Go 1.27.1、Android SDK 35、JDK 17、Gradle 8.9 和 gomobile。

```bash
go get -tool golang.org/x/mobile/cmd/gobind@latest
go install golang.org/x/mobile/cmd/gomobile@latest
go install golang.org/x/mobile/cmd/gobind@latest
gomobile init
mkdir -p android/app/libs
gomobile bind -target=android -androidapi 26 \
  -javapkg com.relayproxy.core \
  -ldflags '-linkmode=external -extldflags=-Wl,-z,max-page-size=16384,-z,common-page-size=16384' \
  -o android/app/libs/mobilecore.aar \
  ./mobile/androidcore

gradle -p android :app:assembleDebug
```

APK 输出：

`android/app/build/outputs/apk/debug/app-debug.apk`

## 使用

1. 安装 APK，在“连接与身份”中填写 Relay Server 域名/IP、服务端签发的接入密钥和设备名称；客户端不填写身份 ID，服务端由密钥确定身份。
2. TLS 默认开启，端口默认 QUIC 443 / TCP 443，传输默认 `auto`。使用身份接入密钥时必须启用 TLS 并验证服务端证书，不能同时关闭 TLS 或跳过证书验证。
3. 从设置目录分别进入“出口选择”“分流规则”“VPN 与应用范围”或“本机代理”。出口和每条代理规则都可从已授权设备下拉选择；空白表示跟随默认出口，只有一个可用出口时会自动选择。
4. 点击“启动”会运行 Relay 服务。有效接入密钥会让新设备自动加入对应身份；同身份使用出口无需逐设备审批。出口设备仍需启用 `proxy.exit`，本机代理或 VPN 需要 `proxy.client`；跨身份出口由服务端管理员把目标设备授权给当前身份。
5. 显式代理应用时，SOCKS5 默认地址为 `127.0.0.1:1080`，HTTP 默认地址为 `127.0.0.1:8080`。保存新端口后，运行中的 Relay 服务会自动重建。
6. 在设置中选择 VPN 应用范围，并在“分流规则”中配置应用、IP/CIDR、域名、端口、协议、动作和出口。VPN DNS 默认启用 Mapped DNS，不再要求手动配置公网 DNS。点击独立的“启动 VPN”按钮，首次使用需在 Android 系统弹窗中授权。VPN 接管范围内应用的 IPv4/IPv6 默认流量；即使关闭用户 SOCKS5 开关，Core 仍会为 TUN 建立一个使用 Keystore 密钥认证的内部回环 SOCKS5 入口。RelayProxy 自身流量排除在 VPN 外。
7. 在“首选出口网络”中选择“Wi-Fi 优先”或“移动数据优先”。关闭“自动切换网络”时，Relay 隧道固定使用所选网络。
8. 开启“自动切换网络”后：
   - Wi-Fi 优先：Wi-Fi 具有已验证互联网连接时使用 Wi-Fi；Wi-Fi 断开或无互联网时自动切换到蜂窝，Wi-Fi 恢复后自动切回。
   - 移动数据优先：蜂窝网络可用时优先使用蜂窝；蜂窝不可用时自动切换到 Wi-Fi，蜂窝恢复后自动切回。
9. 每次实际出口网络发生变化时，Android 客户端会重新绑定进程网络并重建 Relay 隧道，避免旧连接继续停留在失效链路上。

VPN、网络出口和本机代理使用三个独立运行意图，共享一个 Go Core 和 Relay 会话。关闭其中一项不会中断仍启用的其他功能。首页显示授权、实际 Relay/P2P 路径、本机代理流量与 VPN TUN 流量。仅修改规则或默认出口时热更新，只影响新连接；修改连接、监听或 VPN 范围等配置时重建对应运行时。规则保存带 revision 校验，过期编辑会被拒绝。

## P2P 与省电策略

- Android Exit 和代理客户端都会按设置声明 `proxy_p2p_v1`。Relay Server 仅负责候选交换、租约和授权，直连数据不经过 Server。
- P2P 不可用、打洞失败、租约失效或进入冷却时，新连接会继续使用 Relay，不影响出口可用性。
- 首页会显示当前 P2P 状态、Direct Path 类型、RTT 和 P2P 电源策略。
- 屏幕关闭、Android Power Saver、Device Idle/Doze 或当前出口为蜂窝网络时自动进入省电模式。
- 省电模式关闭周期性 P2P QUIC keepalive、缩短空闲直连生命周期，并避免保留多余直连 Session；有真实业务时仍允许建立 P2P。
- Wi-Fi 恢复、设备重新交互或省电状态解除后自动恢复标准 P2P 策略。

## 当前实现边界

- 支持 TCP。
- 支持 UDP；QUIC 隧道可使用 RelayProxy 原生 UDP datagram，TLS/yamux 走 UDP stream 兼容模式。
- 支持自动重连、Wi-Fi / 蜂窝故障切换和设备挑战签名认证。
- 支持公网目标；私网目标默认关闭，可在 UI 显式开启。
- VPN 支持全局、仅选中应用和排除选中应用三种范围；RelayProxy 自身包始终排除。应用卸载后会在下次建立 TUN 时跳过；“仅选中”模式如果没有任何仍可用的应用会拒绝启动并提示重新选择。
- Android 10+ 已接入按应用分流代码链路，Android 8/9 仅支持非应用规则。双应用同目标、UDP、共享 UID、未知 UID、系统 Private DNS、IPv6-only/DNS64、长时稳定性和功耗尚未完成真机端到端验证；当前依赖的 hev-socks5-tunnel Mapped DNS 主要合成 IPv4 A 记录，IPv6-only 域名仍需要后续专项兼容。
- 共享 UID 按完整包名组匹配，组过大或查询失败时视为未知。按规则模式中存在启用的应用条件时，未知应用流量被拒绝；全局代理/直连模式保持所选动作。显式 SOCKS5/HTTP 代理不提供原始应用归属。
- 首页提供会话级累计代理字节数与 TUN 统计；当前不保存跨进程历史，也不提供统计图。
- 尚未实现域名解析后的真实 IP/CIDR 二次匹配，也不提供 HTTPS 解密、远端 ICMP、Always-on / 系统级断网保护、SIM 卡选择及热点流量接管。

最近的修复、自动化验证与待验收项见 [身份与分流自审记录](../docs/identity-access-routing-review.md)。
