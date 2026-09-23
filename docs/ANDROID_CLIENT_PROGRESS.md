# RelayProxy Android 客户端开发进度

分支：`feature/android-client-exit`

## Phase 1：Android 作为网络出口

状态：**首版功能与 UI 已实现，GitHub Actions 构建已通过 / 待真机验收**

- [x] Android 独立工程骨架
- [x] gomobile Go Core
- [x] 复用 RelayProxy Device Protocol v3
- [x] Ed25519 设备身份与首次审批流程
- [x] 仅申请 `proxy.exit` 能力
- [x] QUIC / TLS+yamux 自动选择与重连
- [x] TCP 网络出口
- [x] UDP 网络出口
- [x] Relay ACL + Android 本地出口 ACL
- [x] 前台 Service 常驻
- [x] 后台运行意图持久化，系统回收后自动恢复
- [x] 开机 / App 更新后按上次运行状态自动恢复
- [x] 首页精简为状态卡 + 单按钮启停，配置迁移到独立设置页
- [x] 当前默认网络出口
- [x] 出口网络模式：自动 / 仅移动数据 / 仅 Wi-Fi
- [x] 状态、审批状态、设备 ID、活跃连接、延迟展示
- [x] Android 构建文档
- [x] GitHub Actions Debug APK 构建工作流
- [x] Android 现代化卡片式 UI
- [x] RelayProxy 品牌 Launcher / Round / 通知图标
- [x] macOS Apple Silicon ARM64 + ARM32 双 APK 打包脚本
- [x] Release 默认打包与自动签名支持

## Phase 1 验收

1. Android 首次启动生成独立设备身份。
2. 连接 Server 后，服务端可看到待审批设备。
3. 审批并授予 `proxy.exit` 后 Android 显示已授权。
4. PC Agent 选择 Android Device ID 作为出口后，HTTP/HTTPS 流量公网 IP 应变为 Android 当前出口网络公网 IP。
5. DNS、TCP 和 UDP 目标均能经过 Android 出口。
6. 切换飞行模式/移动数据或临时断网后，恢复网络可自动重新连接。
7. 开启“仅使用移动数据”后，即使 Wi-Fi 同时连接，RelayProxy 进程仍绑定蜂窝网络。

## 后续阶段建议

- Phase 2：多 SIM/指定 Subscription 与网络切换策略。
- Phase 2：流量速率、累计流量、连接列表与目标统计。
- Phase 2：电池优化白名单引导、开机启动、断线原因诊断。
- Phase 3：Android `VpnService` 客户端入口，让 Android 自身也能作为 RelayProxy Client。
- Phase 3：按应用路由、域名/IP 规则和分流。
