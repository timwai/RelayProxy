# 身份授权与 Android 分流自审记录

日期：2026-10-03，身份/设备能力模型及历史设备迁移补充审查：2026-10-04。审查起点：`889912a`。对应 [实施规划](identity-access-routing-plan.md)。

修复提交：`3f1246e`（身份授权与凭据传输）、`52692b4`（Android 归属、规则更新与回归）。

本轮完成身份接入、跨身份代理/RDP 授权、Android 原始流归属、规则更新和设置保存的源码审查，修复下列缺陷并补充回归测试。整个规划仍处于实施中，不能据本记录宣称 M0–M6 全部交付。

结构导航使用 codebase-memory Tier 2，索引代次为 `2026-10-02T05:53:08Z`。覆盖检查发现新增身份/Android 文件尚未跟踪、部分旧文件已变化；结论以直接读取的当前源码、固定版本 Hev/lwIP 源码及实际测试为依据，不以旧图索引证明完整性。

## 已修复问题

| 优先级 | 问题及影响 | 修复 | 回归证据 |
| --- | --- | --- | --- |
| P1 | Hev 补丁两处 hunk 行数错误，干净源码无法应用，应用分流无法构建 | 修正补丁计数及 JNI `AttachCurrentThread` 参数类型 | 从固定源码重新应用补丁，构建 ARM32/ARM64，检查原生库 16 KB 对齐 |
| P1 | lwIP pretend UDP 回调的 `addr/port` 是目标端，原实现作为源端查询 UID；查询元组错误可使 UDP 应用规则失效 | 源端使用 `pcb.remote_ip/remote_port`，目标端使用回调参数 | 对照固定版本 lwIP `udp_input` 与 Hev 回调；双 ABI 原生编译。真实 Android UID 结果尚待真机验证 |
| P1 | v4 与 legacy 设备可能因为旧 owner 相同而被 Relay/P2P 快速路径放行；同身份快照也绕过实时策略 | 混合身份模式直接拒绝；v4 同身份和跨身份都调用实时策略 | `server/gateway/identity_boundary_test.go`、`server/p2p/identity_boundary_test.go` 覆盖双向混合模式及策略撤销 |
| P1 | challenge 与设备登记之间撤销密钥、到期或禁用身份，登记仍可能使用旧快照 | 在登记事务内重读凭据与身份状态；代理/RDP 功能判断读取当前设备能力 | `server/repository/identity_admission_test.go` 覆盖三种 challenge 变更、无残留设备、设备能力收紧与两类功能拒绝 |
| P1 | 身份接入密钥可与明文 TCP/跳过证书验证配置组合，失去凭据传输保护 | Agent/移动核心拒绝此配置，Android 保存前校验；服务端无 TLS 时拒绝 v4 接入 | `agent/app/identity_transport_test.go`、移动核心 `TestIdentityKeyRequiresVerifiedTLS`；Go 全量回归 |
| P1 | 历史已批准设备仍可通过 v3 连接，并以审批管理员 `owner_user_id` 参与旧授权；该字段不等于连接身份 | Gateway 默认统一拒绝 v3；管理页显示、筛选并编辑设备身份归属，迁移时复用原设备 ID、保留设备能力且禁止清空归属 | Gateway 默认旧协议拒绝测试；仓储迁移测试覆盖原设备复用、能力保留及错误身份密钥拒绝；管理 API 覆盖禁止清空身份 |
| P1 | 身份创建与编辑携带能力范围，使身份状态与设备能力耦合，历史设备迁移还会被身份能力隐式降权 | 身份 API、数据模型和管理页面移除能力字段；代理/RDP 仅检查设备能力，身份归属迁移保留设备能力，重连只能使用已批准能力 | API 测试覆盖拒绝身份能力字段；仓储测试覆盖身份元数据与设备能力解耦、重连不自行扩权及历史能力保留 |
| P1 | 共享 UID 包名组被截断后可能漏掉本应命中的规则；系统 `android` 包也会被错误拒绝 | 超限/无效组整体标为未知，保留完整合法组；Kotlin/Go 包名校验保持一致并支持 `android` | Kotlin 三项单元测试；移动核心包名组认证测试 |
| P2 | 修改规则或默认出口会重建 Core/TUN，中断存量连接；无默认出口还会阻止显式出口/DIRECT 规则启动 VPN | 热更新移动规则引擎与默认出口；VPN 就绪检查依赖控制连接、能力和内部监听 | 移动核心真实 TCP 回环测试：旧连接仍可收发、新连接执行 REJECT、无效更新保留旧策略 |
| P2 | 规则页旧快照可覆盖其他页保存，规则编辑/应用选择在旋转后丢失草稿；部分列表操作未校验完整配置 | 单独保存规则并使用 revision 拒绝过期编辑；通用设置保存不再写规则；保存前统一校验，保存/恢复规则及应用草稿 | Kotlin 编译、lint；UI 生命周期和多窗口操作仍需设备验收 |
| P2 | 设备到身份授权的 PATCH/DELETE 可以省略 revision 绕过冲突检测；重复删除返回 404 与规划幂等约定不符 | 强制正整数 revision，重复删除返回 `deleted:false` 且不重复通知 | 管理 API 生命周期测试覆盖缺失 revision、旧版本 PATCH、重复 DELETE；API race 测试 |
| P2 | API 26 默认主题使用 API 27 的浅色导航栏属性 | 以 `values-v27` 覆盖该属性，保留 API 26 基础主题 | Android lint 与 APK 构建 |

同时处理了内部 VPN 认证密钥并发生成、JNI 类被混淆裁剪、加载原生库失败时清理回调再次抛错的问题。新增 SOCKS5 回归通过独立 TCP/UDP association 验证两个应用访问同一目标时元数据不会串用，并覆盖错误密钥、缺失认证方法、公共入口不能声明应用归属。

Android lint 的另一项错误来自 AGP 8.7.3 对 `systemExempted` VPN 资格检查的局限。清单已声明 `FOREGROUND_SERVICE_SYSTEM_EXEMPTED` 并使用 `BIND_VPN_SERVICE`；仅对该 VPN Service 添加有说明的 lint 豁免，没有申请无关的闹钟权限。VPN 属于可使用该类型的条件之一，依据 [Android 前台服务类型官方文档](https://developer.android.com/develop/background-work/services/fgs/service-types#system-exempted)。此静态豁免不代替设备上的授权、启动与撤销验证。

## 验证结果

| 检查 | 结果/范围 |
| --- | --- |
| `go test ./...` | 通过，全仓 Go 测试 |
| `go vet ./...` | 通过 |
| `go test -race ./server/repository ./server/gateway ./server/p2p ./server/api ./internal/proxy/socks5 ./mobile/androidcore` | 通过；随后重复执行 API race，覆盖最终幂等删除修改 |
| Gradle `:app:testDebugUnitTest :app:lintDebug` | 通过，应用归属编码 3 项单元测试，lint 无 error；仍有资源国际化、旧版本条件等 warning |
| `scripts/build-hev-android.sh` | ARM32/ARM64 原生构建通过，16 KB ELF LOAD 对齐检查通过 |
| `scripts/build-android-macos.sh --debug --skip-tests --skip-sdk-install --skip-tool-install` | 通过，生成 ARM32/ARM64 两个 Debug APK；构建脚本重建 gomobile、Hev 并检查打包原生库 16 KB 对齐 |
| `adb devices -l` | 无已连接设备；未运行真机或模拟器端到端测试 |

本轮曾实际遇到补丁应用失败、Android lint 错误和新增草稿恢复代码的 Kotlin 类型错误，均修复后重跑对应检查。测试中的密钥到期样本已统一为 UTC，授权测试桩也已显式提供实时设备出口策略，避免依赖已删除的快照放行行为。

最终本地产物（不提交二进制）：

| 路径 | SHA-256 |
| --- | --- |
| `dist/android/RelayProxy-Android-arm64-debug.apk` | `8afe0827942e484d4338cd1c049b9fc548eafb1670456532e0809a6ae19c18d5` |
| `dist/android/RelayProxy-Android-arm32-debug.apk` | `e6cb89dfd7800a507cf493f5b2c3615802cc206ea4112bdd79ac775a15f82ccf` |

## 尚未闭环的规划交付项

这些是实际缺口，保留为后续实现/验收门槛，不通过修改完成状态将其隐藏。

| 项目 | 当前边界 | 下一步与通过条件 |
| --- | --- | --- |
| Mapped DNS 与真实 IP/CIDR | Fake-IP 恢复为域名后，当前引擎不会解析真实 IP 再匹配；直接 IP 请求可用 IP 规则，普通域名连接不能声称有相同覆盖 | 完成获授权 DNS 出口、真实 IP 元数据、匹配地址与实际拨号地址绑定；覆盖 A/AAAA、多地址重试、REJECT、DNS64、缓存及故障时不静默直连 |
| 应用归属真实性与双栈 | Go 的认证/元数据隔离及 Kotlin 编码测试通过，不证明 Android 内核对真实 TCP/UDP 流返回正确 UID | Android 10+ 两应用同时访问同一 IPv4/IPv6 目标并选择不同出口；记录 UID、包名组、规则命中和实际出口；覆盖共享/未知 UID、包不可见、卸载、工作资料 |
| 内部凭据生命周期 | 当前使用 Keystore 保护的安装密钥。认证证明持有密钥，不绑定单条原始流或运行时代次 | 发布前补齐运行时代次/短期流凭据并验证旧代次重放、伪造包名、多个 UDP association 生命周期；不能将普通 SOCKS5 认证视为此项完成 |
| 设置页与配置生命周期 | 已拆连接、出口、规则、VPN、本机代理和网络策略；DNS、网络共享、诊断独立页未齐，通用设置页没有统一 revision/草稿机制 | 完成剩余页及分组并发保存契约；设备上验证快速保存、返回取消、旋转、后台恢复和运行意图不被覆盖 |
| Android 8/9 | 保留 API 26 最低版本和非应用规则，启用应用规则会被拒绝 | 真机验证安装、VPN、非应用规则和配置迁移；不能仅以 minSdk/编译通过代替兼容验收 |
| 全链路及发布 | Go 回归通过，尚无本轮 Windows/macOS GUI、真实 RDP、Relay/P2P 撤销时间边界和移动网络切换记录 | 按规划验收矩阵完成跨身份新加入设备、功能勾选、编辑/删除/过期、直连存量会话清理、迁移、长时负载、耗电与正式签名构建 |

以上缺口决定 M4/M5/M6 仍为未完成。当前可审阅的闭环是本轮已定位缺陷的代码修复与自动化回归；后续以真实验收记录逐项更新规划状态。
