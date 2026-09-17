# Divert 透明代理 — 计划索引

> **For agentic workers:** 本仓库将该规格拆成多个可独立交付的实现计划。每个里程碑计划自包含；开工前只加载对应文件。REQUIRED SUB-SKILL: `superpowers:subagent-driven-development` 或 `superpowers:executing-plans`。

**规格:** [`docs/superpowers/specs/2026-09-12-divert-transparent-proxy-design.md`](../specs/2026-09-12-divert-transparent-proxy-design.md)

**Goal:** 删除 fake-IP TUN，改为 ProxyBridge 式三端系统拦截；进程规则为主；TCP/UDP 均经 Relay 隧道直出。

## Global Constraints（所有子计划共用）

- 平台：Windows + macOS + Linux 同一产品版本交付
- 规则：按进程为主，可叠加 hosts/cidrs/ports/protocols
- 删除整包 `agent/tun`；旧 `network.mode: tun` 拒绝启动并提示迁移
- 出口：拦截层直接 `TunnelDialer.DialTCP` / `DialUDP`，不经本机 SOCKS
- UDP 与 TCP **同一产品版本**必须进隧道
- 本地 SOCKS5/HTTP 保留；对本机代理端口必须 DIRECT
- 提交信息格式：`<type>(<scope>): <中文说明>`（仅在用户要求提交时）

## 子计划与依赖

| 计划文件 | 里程碑 | 依赖 | 状态 |
|----------|--------|------|------|
| [`2026-09-12-divert-m1-udp-tunnel.md`](./2026-09-12-divert-m1-udp-tunnel.md) | M1 UDP 隧道协议 + DialUDP + 出口转发 | 无 | **已完成** |
| [`2026-09-12-divert-m2-m6-execute.md`](./2026-09-12-divert-m2-m6-execute.md) | M2–M6 divert 引擎/三端/删 TUN | M1 | **已完成** |

M3/M4/M5 可在 M2 完成后并行。

## 目标文件地图（全量）

| 路径 | 职责 |
|------|------|
| `internal/protocol/message.go` | `FrameTypeOpenUDP` / Req / 请求响应类型 |
| `internal/proxy/types.go` | `TunnelDialer` 增加 `DialUDP` |
| `agent/client/dialer.go` | 实现 `DialUDP` + datagram 帧编解码 |
| `agent/client/udp_conn.go` | `net.PacketConn` 适配隧道流 |
| `agent/exit/handler.go` | 处理 OpenUDP，出口侧 UDP 转发 |
| `server/gateway/router.go` | 路由 OpenUDP（镜像 OpenTCP） |
| `agent/divert/engine.go` | ProcessEngine（M2） |
| `agent/divert/server_*.go` | 各平台拦截（M3–M5） |
| `agent/tun/**` | M6 删除 |
| `internal/config/config.go` / GUI / bridge | divert 配置接线（M2+） |

## 执行顺序

1. 执行 **M1** 计划直至验收通过  
2. 再生成并执行 M2 →（M3∥M4∥M5）→ M6  
