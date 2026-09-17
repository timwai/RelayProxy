# M2–M6 Divert 实现计划（压缩执行版）

> 与规格 `2026-09-12-divert-transparent-proxy-design.md` 对齐；本会话内联执行至完成。

**架构：** 本地 divert 监听 + 平台拦截把匹配流量重定向到本地端口；Go `ProcessEngine` 决策后 `DialTCP`/`DialUDP` 进隧道。

| 里程碑 | 交付 |
|--------|------|
| M2 | `agent/divert` ProcessEngine/规则/环路排除；config `network.mode=divert`；GUI；agent 接线 |
| M3 | Windows WinDivert 改写目的到本地 divert 端口 + PID→进程 |
| M4 | Linux iptables REDIRECT/TPROXY 或 NFQUEUE + /proc 进程识别 |
| M5 | macOS：Network Extension 未签名前提供清晰错误 + IPC 骨架；开发可用 pf/标注 |
| M6 | 删除 `agent/tun` 与 gvisor/wintun；拒绝 `mode: tun` |

**约束：** 无 git 不提交；旧 `tun` 不静默兼容。
