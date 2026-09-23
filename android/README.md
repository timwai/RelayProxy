# RelayProxy Android

Android 第一阶段只实现 **网络出口节点**：手机加入 RelayProxy 后，其他已授权客户端可以把 TCP/UDP 流量经 Relay Server 转发到手机，再由手机当前网络访问目标。

## 架构

- `mobile/androidcore`：Go + gomobile。直接复用 RelayProxy 的设备认证、QUIC/TLS+yamux、出口 ACL、TCP/UDP 转发协议；`-javapkg com.relayproxy.core` 生成的 Java 包为 `com.relayproxy.core.androidcore`。
- `android/app`：Kotlin 原生 UI + 前台 Service。负责配置、生命周期、通知和可选的蜂窝网络进程绑定。
- Android 不创建 `VpnService`，第一期不是“把 Android 自己的流量送进 RelayProxy”，而是“把 Android 当作出口”。

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

Windows 脚本默认构建 Release。未配置签名时输出 `RelayProxy-Android-release-unsigned.apk`；配置签名后输出 `RelayProxy-Android-release.apk`。如需 Debug，可执行 `./scripts/build-android.ps1 -Configuration Debug`。若需要自动生成可安装的签名 Release APK，请设置：

```powershell
$env:RELAY_ANDROID_KEYSTORE="D:\keys\relayproxy.jks"
$env:RELAY_ANDROID_KEY_ALIAS="relayproxy"
$env:RELAY_ANDROID_KEYSTORE_PASSWORD="your-store-password"
$env:RELAY_ANDROID_KEY_PASSWORD="your-key-password"
.\scripts\build-android.ps1 -Configuration Release -Clean
```

脚本会自动完成 gomobile AAR 构建、Android SDK/NDK 检查、Gradle 8.9 获取、APK 构建、Release 签名（配置签名时）以及 SHA256 输出。构建期间为 gomobile 临时加入的 Go tool 依赖会在结束后恢复，不会永久修改 `go.mod` / `go.sum`。

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

默认输出：

```text
dist/android/RelayProxy-Android-arm64-release-unsigned.apk
dist/android/RelayProxy-Android-arm32-release-unsigned.apk
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

未配置签名时输出：

```text
dist/android/RelayProxy-Android-arm64-release-unsigned.apk
dist/android/RelayProxy-Android-arm32-release-unsigned.apk
```

自动签名使用与 Windows 脚本相同的环境变量：

```bash
export RELAY_ANDROID_KEYSTORE="$HOME/keys/relayproxy.jks"
export RELAY_ANDROID_KEY_ALIAS="relayproxy"
export RELAY_ANDROID_KEYSTORE_PASSWORD="your-store-password"
export RELAY_ANDROID_KEY_PASSWORD="your-key-password"

./scripts/build-android-macos.sh --release --clean
```

脚本会自动识别默认 Android SDK 路径 `$HOME/Library/Android/sdk`、Apple Silicon Homebrew JDK 17、Android SDK 35、Build Tools 35.0.0、NDK 27.2.12479018，并在本机没有 Gradle 时下载 Gradle 8.9。每个 ABI 都会单独重建 gomobile AAR 和 Android APK，避免不同架构之间复用旧的 Native Library。

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
  -o android/app/libs/mobilecore.aar \
  ./mobile/androidcore

gradle -p android :app:assembleDebug
```

APK 输出：

`android/app/build/outputs/apk/debug/app-debug.apk`

## 使用

1. 安装 APK，填写 Relay Server 域名/IP。
2. TLS 默认开启，端口默认 QUIC 443 / TCP 443，传输默认 `auto`。
3. 点击“启动网络共享”。
4. 第一次连接后，在 Relay Server 设备管理中批准该 Android 设备，并授予 `proxy.exit`。
5. Android 状态显示“已连接 / 已授权”后，即可被其他 RelayProxy 客户端选作网络出口。
6. 勾选“仅使用移动数据作为出口”时，应用通过 Android `ConnectivityManager.bindProcessToNetwork` 将 Relay 隧道和出口连接固定到蜂窝网络。

## 第一阶段边界

- 支持 TCP。
- 支持 UDP；QUIC 隧道可使用 RelayProxy 原生 UDP datagram，TLS/yamux 走 UDP stream 兼容模式。
- 支持自动重连和设备挑战签名认证。
- 支持公网目标；私网目标默认关闭，可在 UI 显式开启。
- 暂不提供流量统计图、分应用规则、SIM 卡选择、热点控制、Android 本机 VPN/透明代理入口。
