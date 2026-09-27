# Windows 系统透明代理

Windows x64 客户端通过 WinDivert 截获新建 TCP 连接和 UDP 数据报。透明代理、SOCKS5 与 HTTP 使用同一套组合路由规则，按进程、域名/IP、端口和协议选择 DIRECT / PROXY / REJECT。PROXY 直接进入 Relay 隧道，并保留规则选定的出口。

## 使用

Windows x64 的 `relay-agent-gui.exe` 和 `relay-agent.exe` 已内嵌官方 WinDivert 2.2.2，单独分发一个 EXE 即可。构建脚本也会生成 `dist/RelayProxy-agent-windows-amd64.zip`，附带示例配置、可选外置运行库、许可证和 SHA256 校验清单。

> Windows ARM64 产物不包含 x64 WinDivert，因此不支持系统透明代理，但仍可使用 Agent、SOCKS5/HTTP 和本地 Web 管理页。ARM64 桌面窗口还需要安装匹配的 Microsoft Edge WebView2 Runtime；缺失时程序会提示并回退到 Web 管理页。

1. 将 `relay-agent-gui.exe` 放在固定位置。日常启动不需要管理员权限，也无需手动下载 WinDivert。
2. 在“本地代理服务 → 系统透明代理”中首次启用并保存。客户端会弹出一次 UAC，用于安装/更新 `RelayProxy Network Service`。
3. UAC 完成后，GUI/Agent 继续以当前普通用户身份运行；透明代理的数据包捕获和注入由 LocalSystem 网络服务承担。
4. 打开“路由分流”，选择“按规则分流”，填写组合规则并保存。规则更新影响新建 TCP 连接及新的 UDP 关联，已建立的连接保留原决定。
5. 点击左侧“实时连接 ↗”打开独立窗口，按进程、PID、域名/IP、端口、协议、动作或规则筛选，查看双向速率、累计流量和连接详情。

`RelayProxy Network Service` 只负责 WinDivert 收包和注入，不建立第二条 Relay 会话，也不读取路由规则。普通 Agent 仍负责进程识别、规则判断、Relay 隧道和实时连接统计；Agent 与 SYSTEM 服务之间通过只允许 SYSTEM、Administrators 和安装用户访问的本机命名管道交换报文。

为避免把用户目录中的可替换程序以 SYSTEM 身份长期运行，安装程序会把当前客户端复制到 `%ProgramData%\RelayProxy-Network-Service\<程序校验前缀>\RelayProxyNetwork.exe`。目录仅允许 Administrators 和 SYSTEM 写入。SYSTEM 服务也不会加载 EXE 旁的第三方 WinDivert DLL，而是只释放并加载内嵌、校验过的 WinDivert 2.2.2 到 `%ProgramData%\RelayProxy-WinDivert\...`。

普通启动和开机自启都不需要管理员 token。只有首次安装/客户端升级需要更新 Network Service 时才会请求一次 UAC。如果用户取消 UAC，GUI 仍会正常启动，但本次不会启用系统透明代理。管理员直接运行旧模式仍保留 WinDivert 直连回退，主要用于兼容和诊断。

外置 `WinDivert.dll` / `WinDivert64.sys` 仅用于管理员直接运行的兼容路径；LocalSystem Network Service 永远使用内嵌可信版本。

## 组合路由规则

不同字段同时满足（AND）；同一字段的多个值任选其一（OR）。按从上到下的顺序使用第一条匹配的启用规则，未命中时使用 `routing.default_action`。空列表或 `*` 不限制该字段；四个字段全空时匹配所有连接。

**进程与目标地址可以分别独立成为一条规则的匹配条件。** 只配置进程时，规则不限制目标地址；只配置目标 IP/CIDR/域名时，规则不要求进程身份；两者同时配置时按 AND 关系同时满足。端口和协议同样是可选附加条件。

**进程条件是可选条件，不是透明代理的前置条件。** 如果规则只填写目标 IP/CIDR、端口或协议，即使 Windows 在该数据包到达时暂时无法从 OWNER_PID 表解析出进程（例如 SMB/445、System 或部分受保护服务的早期 SYN），仍会使用原始目标五元组执行规则。只有规则显式填写了进程条件时，才要求该连接具备可匹配的进程身份。

```yaml
routing:
  mode: rule
  default_action: DIRECT
  rules:
    - name: 浏览器 HTTPS
      enabled: true
      processes: ["chrome*", "*msedge.exe", "*firefox*"]
      targets: ["*.example.*", "203.0.113.*", "2001:db8:*"]
      ports: ["443", "8000-9000"]
      protocols: [tcp]
      action: PROXY
      exit_id: "office-exit"
network:
  mode: divert
  exclude_processes: [updater.exe]
```

进程名、域名和 IP 均支持前面、后面或两端使用通配符：`*` 匹配任意长度（包括空字符串），`?` 匹配单个字符。

| 条件 | 前面通配 | 后面通配 | 两端通配 |
| --- | --- | --- | --- |
| 进程名 | `*chrome.exe` | `chrome*` | `*chrome*` |
| 域名 | `*.example.com` | `example.*` | `*example*` |
| IPv4 | `*.100.7` | `192.168.*` | `*168.1*` |
| IPv6 | `*::abcd` | `2001:db8:*` | `*db8*` |

进程支持文件名和完整路径，Windows 路径忽略大小写；完整路径中的通配符不跨目录分隔符。Windows Service 进程还会记录其 SCM 服务名：当一个 PID 只托管一个服务时，可使用 `service:<ServiceName>` 作为进程规则，例如 `service:Dnscache`。如果一个 `svchost.exe` PID 同时托管多个服务，连接详情会列出这些服务，但不会猜测某条 socket 属于其中哪一个，因此不会生成可匹配单个服务的别名；仍可按 `svchost.exe` 或完整进程路径匹配。受保护的独立服务进程即使无法读取 EXE 路径，也会回退为稳定的 `service:<ServiceName>` 身份。PID 4 保持 `System`，并附带 `service:System` 别名。域名忽略大小写和末尾的点，`*.example.com` 包含根域名及其子域名，`*.example.*` 同样可以匹配 `example.com` 和 `api.example.net`。

域名/IP 栏的通配表达式同时检查已知域名和目标 IP，任一匹配即可。IP 按规范化地址文本匹配：IPv4 使用点分十进制，IPv6 使用小写压缩形式（如 `2001:db8::1`），IPv4 映射地址按 IPv4 格式匹配。通配符也可匹配部分数字，例如 `*168.1*` 同时匹配 `192.168.1.20` 和 `192.168.100.20`；IP 仍支持精确地址和 CIDR 网段。端口支持单端口和闭区间范围。`datagram_required: true` 要求代理 UDP 使用原生数据报。

进程排除列表优先于透明代理规则；系统防环与本地流量旁路仍先执行。全局代理和全局直连模式同样适用于三个入口。当前配置格式不保留旧的 `match/value` 字段或独立 `network.rules/default_action`，请使用上述格式或附带的示例配置。

SOCKS5/HTTP 的进程归属来自本机客户端至代理监听端口的实际 TCP 连接；远程客户端或无法确定的归属显示为未知，不能命中指定进程的条件。请求中的域名保持远端解析，不额外进行本机 DNS 查询；IP 条件只匹配已知目标 IP。

透明代理的域名来自启用后观察到的明文 UDP DNS 查询与应答。只有查询端点、事务 ID、问题类型和域名一致时才建立关联，支持 A/AAAA 及 CNAME，遵守最短 TTL（最多保留 5 分钟）。同一 IP 对应多个有效域名时不选择其中任意一个。加密 DNS、TCP DNS、系统缓存、回环 DNS 和启用前的解析可能只有 IP 信息，域名规则不会命中这些未知名称。DNS 关联不证明该应用请求了这个域名，窗口会明确标注来源。

## 实时连接窗口

每秒刷新，速率为近 2 秒的平均值。窗口显示进程名/PID、请求或 DNS 关联域名、已知目标 IP、端口、协议、入口、动作、命中规则、双向速率、累计上传/下载和时长。点选连接可查看完整路径、本地端点、出口与错误原因；可暂停显示并查看最近结束、失败或阻断的连接。

本次运行的累计计数不随窗口开关或筛选清零。最多保留 8192 条活跃明细和 512 条最近结束记录；达到明细上限时仍保留总计并显示省略数量。代理连接按实际传输载荷计数，透明直连按捕获的数据包载荷计数（包括重传），均不包含 IP/TCP/UDP 头或隧道封装开销。监控涵盖 Agent 处理的新连接，不包括启用前已有 TCP 连接、回环及防环旁路流量。

关闭监控窗口会隐藏并复用窗口，关闭客户端会同时结束它。未观察到的域名与出口解析的目标 IP 保持未知，不用中继服务器的地址代替目标地址。

## Network Service 管理与恢复

Windows GUI 的“系统透明代理”区域会显示 `RelayProxy Network Service` 的安装状态、运行状态、服务 PID、当前 broker 程序路径和自动恢复状态。状态分为“未安装 / 已停止 / 需要修复 / 运行中”；客户端升级后如果 broker 二进制版本与当前客户端不一致，会显示“需要修复”。

“安装 / 修复服务”会请求一次 UAC，重新发布受保护的 broker 程序、更新 Windows Service 配置并启动服务。“卸载服务”会先关闭已保存的 `network.mode=divert`，再通过一次 UAC 停止并删除 `RelayProxyNetwork` 服务，同时清理 `%ProgramData%\RelayProxy-Network-Service\` 下的 broker 程序；因此下次启动不会自动把刚卸载的服务重新安装。WinDivert 的受保护运行库目录保留，供管理员直连诊断路径复用。

服务安装/修复时会配置 Windows SCM 故障恢复：异常退出后依次在 1 秒、5 秒、15 秒后自动重启，24 小时后重置失败计数。Agent 侧的命名管道 packet device 同时支持自动重新握手；因此 broker 被 SCM 拉起后，透明代理无需重启整个 GUI/Agent 即可恢复。手工“停止服务”或“卸载服务”属于正常停止，不会被故障恢复策略强行拉起。

## 登录后自动启用

首次启用透明代理时完成一次 Network Service 安装后，“开机自动启动”始终使用当前用户的普通登录项启动 GUI/Agent，不再因为 `network.mode=divert` 创建最高权限 GUI 任务，也不会在每次登录时弹 UAC。

`RelayProxy Network Service` 是自动启动的 LocalSystem Windows Service，可在用户登录前准备好 WinDivert broker。用户登录后，普通 Agent 连接本机命名管道并开始透明代理。旧版本已经创建的最高权限登录任务会在后续配置保存/自启动同步时迁移回普通用户登录项。

登录自启路径明确禁止主动弹 UAC：如果 Network Service 缺失、停止或版本与当前客户端不匹配，本次登录会先以 SOCKS5/HTTP 等非透明能力启动，并记录需要修复 Network Service；用户随后手动打开客户端并保存透明代理设置即可完成一次 UAC 修复。

## 构建

`scripts/build.ps1` 和 `scripts/build.sh` 会先下载并校验固定版本的官方压缩包，再编译内嵌 WinDivert 的客户端。源码中的 `agent/divert/windivert/WinDivert-2.2.2-A.zip` 保留完整官方发行包及许可证；普通 `go build ./cmd/relay-agent` 同样会内嵌它，Linux/macOS 构建不会包含该资源。

## 当前边界

- 支持 IPv4/IPv6 TCP 与 UDP；UDP 最大负载为 65507 字节。
- 本进程、中继端点、中继域名的 DNS 查询和本机代理入口强制直连，避免重捕获形成循环。本机、组播、广播和链路本地流量不经隧道。
- 开启前已经建立的 TCP 连接继续使用原路径。需要让目标应用重新建立连接。
- 不从网络层数据猜测 PID；通过 Windows TCP/UDP OWNER_PID 表取得归属。PID 4 直接映射为稳定的 `System` 身份，不再尝试 `OpenProcess(4)`，因此 System/内核服务拥有的 TCP/UDP 流量可参与透明代理规则。其他无法确定或存在多个 PID 的流仍拒绝并记录错误。
- 域名条件仅在存在请求域名或有效 DNS 关联时匹配，见上方说明。
- 不支持原始 IP 分片重组、源路由、IPsec AH/ESP 和 IPv6 jumbogram；无法分类的出站报文丢弃并记录原因。普通入站报文继续交给 Windows 处理，捕获范围的扩大不会阻断它们；透明重定向监听端口仍阻止外来连接。
- Linux 使用 NFQUEUE/iptables，macOS 使用签名后的 Network Extension；部署前提与验收方法分别见 `linux-transparent-proxy.md` 和 `../agent/divert/macos/README.md`。

## 验证状态

自动测试覆盖报文校验和、双向 TCP 地址还原、UDP 原始来源回复、进程匹配、防环、规则保留、端口复用、捕获失败与关闭清理。普通 `go test` 不安装或启动 WinDivert 驱动。

组合规则测试覆盖字段之间的 AND、字段内的 OR、进程/域名/IPv4/IPv6 的前后通配、未知身份、路径通配、排序及热更新。流量测试覆盖双向计数、部分读写、半关闭、并发采样、速率归零、容量边界，以及普通入站报文和 DNS 关联的 TTL/歧义处理。界面回归覆盖配置保存、筛选、详情、暂停/继续和深浅色渲染。

原生 WebView2 测试覆盖监控窗口创建、关闭隐藏、重开复用和客户端退出后的销毁。PowerShell 中设置 `$env:RELAYPROXY_GUI_SMOKE='1'` 后运行 `go test ./agent/gui -run TestConnectionsNativeWindowLifecycle -v -count=1 -timeout=60s`；测试在独立桌面进程中运行，不启动代理、隧道或驱动。

自动测试还覆盖内嵌文件完整性、并发释放、损坏修复、目录权限检查、自启动迁移和失败回滚。原生测试在临时目录释放内嵌 DLL，验证过滤器编译和求值；计划任务测试通过未注册的任务定义和 `TASK_VALIDATE_ONLY` 验证 Windows 接口，不创建自启动项。

可单独运行 `go test ./agent/divert -run TestWinDivertNativeFilter -v`，无需管理员权限，也不会加载驱动。如需验证外置 DLL，可将 `RELAYPROXY_WINDIVERT_DLL` 设置为其绝对路径。

需要在管理员会话运行驱动验收，核实真实应用的 IPv4/IPv6 TCP/UDP 流量、Windows 防火墙/驱动兼容性及重新登录后的自动启动。当前开发会话未提升权限，尚未执行这些实机验收；编译和模拟设备测试不能代替它。
