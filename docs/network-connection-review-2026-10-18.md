# RelayProxy 网络连接代码审查报告

- 日期：2026-10-18
- 审查对象：RelayProxy 网络连接与转发相关代码
- 审查方式：静态阅读 + 关键路径定向复现 + `go build` / `go vet` / `go test -race`
- 代码状态：第 1–8 节记录初审时的工作树；核验纠正与首批代码修复见第 9 节

> **复核提示**：本报告已于同日逐项核验。初审中的 UPnP 关闭滞留、QUIC socket 未调优及空 UDP 数据报导致忙循环等结论需要纠正；Public Direct 使用 Ticket 认证，并非双向 TLS。第 9 节是当前修复状态的依据。

## 1. 审查范围

| 模块 | 主要内容 |
| --- | --- |
| `internal/tunnel` | QUIC / TLS-yamux 会话、stream、pipe、DATAGRAM、Brutal/BBR、socket 调优 |
| `internal/proxy/{socks5,httpproxy}` | 本地 SOCKS5 / HTTP 代理入口、UDP associate、CONNECT |
| `internal/p2p` | punch、secure、resume、UPnP、candidate |
| `agent/client` | TunnelDialer、direct/relay 回退、resumable TCP、UDP 关联 |
| `agent/direct` | Public Direct listener/client、Ticket 认证 |
| `agent/exit` | Exit 目标拨号、ACL、UDP/TCP 转发 |
| `server/gateway` | 公网监听、设备握手、stream router、RDP/UDP 转发 |
| `server/session` | 设备会话注册、注销、撤销、心跳 |
| `server/direct` | Public Direct 端点注册、验证 |
| `server/api` | 管理端 SSE 实时更新 |
| `cmd/relay-server` | 监听器与 HTTP server 超时配置 |

## 2. 结论摘要

整体架构和安全边界设计较扎实：TLS 证书指纹固定、Ticket 防重放、Exit ACL 二次校验、Session 锁与代际检查均实现正确，未发现 TLS 校验绕过、Ticket 重放或典型的 DNS rebinding TOCTOU 漏洞。

主要风险集中在**连接生命周期、超时与资源上限、未认证入口**：

- 管理端 SSE 会被全局 `WriteTimeout` 在 15 秒切断（已复现）；
- Public Direct 的 probe 会话不需要 Ticket，且监听端从不关闭，存在未认证资源耗尽面；
- HTTP 代理 CONNECT 缺少目标校验，空主机名可落到本机回环并绕过按 IP/端口配置的 ACL；
- 本地 SOCKS5/HTTP 代理没有连接上限和空闲超时，且 accept 循环在 EMFILE 上忙等；
- TLS/yamux 的每流 16 MiB 窗口与 1024 backlog 组合，缺少会话级内存预算。

| 级别 | 数量 | 代表问题 |
| --- | --- | --- |
| 高 | 6 | SSE 超时、未认证 probe、CONNECT 校验、本地代理资源耗尽、yamux 内存放大、punch ACK 未绑定目的地址 |
| 中 | 9 | accept 忙等、15 秒 deadline 泄漏、请求头无上限、验证器无限并发、SOCKS5 UDP 卡死、resume 内存、QUIC socket 调优、UPnP 伪造 IGD、关闭竞态 |
| 低 | 11 | 初审列出的加固项数量；其中部分经复核不成立，详见第 9 节 |

## 3. 高优先级问题

### H-1 管理端 SSE 被 15 秒 `WriteTimeout` 切断

**位置**：[`cmd/relay-server/main.go:698-704`](../cmd/relay-server/main.go#L698-L704)、[`server/api/live_updates.go:92-150`](../server/api/live_updates.go#L92-L150)、[`server/api/router.go:314`](../server/api/router.go#L314)

管理端 `http.Server` 设置了：

```go
WriteTimeout: 15 * time.Second
```

但 `handleLiveEvents` 从未清除写截止时间，且保活心跳是 25 秒一次：

```go
pulse := time.NewTicker(25 * time.Second)
```

`net/http` 的 `WriteTimeout` 约束的是整个响应写入，不是两次写之间的空闲时间。15 秒后下一次 `Flush` 失败，SSE 连接被关闭。

**复现**：用独立最小 `http.Server`（`WriteTimeout: 300ms`）提供 SSE，客户端在第 403ms 左右观察到流结束。按同样语义，15 秒会切断管理端 SSE。

**影响**：15 秒 deadline 过期后的下一次写入会失败；空闲时一般要到第 25 秒的心跳才体现为断流，不能简单断言每 15 秒重连。前端 `EventSource` 重连的 `open` 事件还会触发一次全量刷新（[`live-updates.js:31-38`](../server/web/frontend/src/live-updates.js#L31-L38)），增加请求与日志噪声。

**建议**：

```go
rc := http.NewResponseController(w)
if err := rc.SetWriteDeadline(time.Time{}); err != nil {
    http.Error(w, "streaming deadline unsupported", http.StatusInternalServerError)
    return
}
```

如果仍需写保护，应在每次 `Flush` 前设置短期限，而不是依赖全局 `WriteTimeout`。补充一个使用真实 `http.Server` 的 SSE 集成测试（现有 `httptest` 默认无写超时，无法覆盖）。

### H-2 Public Direct 探针会话无需认证且永不关闭

**位置**：[`agent/direct/public_listener.go:164-196`](../agent/direct/public_listener.go#L164-L196)、[`agent/direct/public_listener.go:198-247`](../agent/direct/public_listener.go#L198-L247)

probe 分支不需要 Ticket，只需要 16–64 字节 nonce：

```go
if probe {
    // The verifier owns connection shutdown ...
    return          // 不关闭 session
}
```

“验证方会关闭连接”只对 Relay Server 的 verifier 成立。任意客户端都可以直接向 Exit 的公开 QUIC 端口发 probe，拿到响应后不关闭连接。初审时 `handleHandshake` 返回就释放 `slots`，该会话也不在 `PublicListener` 的跟踪集合中，长期运行时没有主动回收。监听器整体关闭会关闭共享 UDP socket，因此原文“Close 也无法关闭它”过强；主要问题是运行期间的无限存活和未计入握手额度。

攻击者可以持续发送 QUIC 包重置 idle timer，并在未被接受的 stream 上发送数据，占用连接接收窗口（Public Direct 初始 32 MiB，最大 256 MiB，[`internal/tunnel/direct_quic.go:20-23`](../internal/tunnel/direct_quic.go#L20-L23)）。限流器默认 120 次/分钟/IP（[`agent/direct/rate_limit.go:12`](../agent/direct/rate_limit.go#L12)），并不限制已建立会话的存活时间。

**影响**：未认证的连接/内存耗尽；每条被遗弃的连接可占用约 32 MiB 连接窗口，且可长期存活。

**建议**：

- probe 响应写完后最多等待 1 秒（优先等待 `session.Done()`），再由监听端主动 `session.Close()`；
- 将 probe 会话纳入有上限的集合，并设置很短的 idle timeout（例如 2–5 秒）；
- 如可行，为 probe 增加 Server 签发的一次性凭据，或至少限制来源为 Relay Server 地址。

### H-3 HTTP 代理 CONNECT 目标校验缺失

**位置**：[`internal/proxy/httpproxy/server.go:231-245`](../internal/proxy/httpproxy/server.go#L231-L245)、[`internal/proxy/httpproxy/server.go:264-280`](../internal/proxy/httpproxy/server.go#L264-L280)

```go
host, portStr, err := net.SplitHostPort(req.Host)
if err != nil {
    host = req.Host
    portStr = "443"
}
```

`CONNECT :22 HTTP/1.1` 会被 `http.ReadRequest` 解析为 `req.Host == ":22"`。`SplitHostPort(":22")` 成功返回 `host == ""`，随后直接 `DialTCP(ctx, exitID, "", 22)`。DIRECT 模式下 `net.JoinHostPort("", "22")` 就是 `":22"`，即本机回环的 22 端口。

由于路由/ACL 通常按 `127.0.0.1` 或 CIDR 匹配，空主机名不会命中这些规则，形成 **ACL 绕过 + 本机服务访问**。如果 HTTP 代理监听在 `0.0.0.0`（配置校验允许，[`internal/config/validation.go:216-217`](../internal/config/validation.go#L216-L217)），局域网客户端也可利用。

同一路径还存在：

- `CONNECT host:0` 被接受；SOCKS5 路径已拒绝端口 0（[`internal/proxy/socks5/server.go:319`](../internal/proxy/socks5/server.go#L319)）；
- 普通 HTTP 的 `Host: host:99999` 中 `ParseUint` 错误被忽略；范围溢出时返回的是 65535，语法错误才可能返回 0，均可能导致错误目标拨号（初审将其一概描述为 0 不准确）；
- `CONNECT [2001:db8::1]`（无端口）保留方括号，后续 `JoinHostPort` 变成 `[[2001:db8::1]]:443`，连接失败且主机名被当作域名送进 ACL。

**建议**：抽取共享 authority 校验函数：

- 主机非空，端口 1–65535（拒绝 0 和解析失败）；
- 无端口形式只允许裸主机名或可解析 IP，IPv6 先 `netip.ParseAddr(strings.Trim(host, "[]"))` 规范化；
- 拒绝既不是合法 IP、也不像合法主机名的输入。

### H-4 本地 SOCKS5/HTTP 代理无连接上限、无空闲超时

**位置**：[`internal/proxy/socks5/server.go:170-193`](../internal/proxy/socks5/server.go#L170-L193)、[`internal/proxy/socks5/server.go:840-864`](../internal/proxy/socks5/server.go#L840-L864)、[`internal/proxy/httpproxy/server.go:148-173`](../internal/proxy/httpproxy/server.go#L148-L173)、[`internal/proxy/httpproxy/server.go:320-349`](../internal/proxy/httpproxy/server.go#L320-L349)

两个 `pipe` 实现都是两个 `io.CopyBuffer` 加 `wg.Wait()`，没有设置或刷新任何 deadline：

```go
go copyOne(c1, c2)
go copyOne(c2, c1)
wg.Wait()
```

只要目标端 accept 后不主动关闭，而客户端半关闭或进程崩溃，另一个方向就可能一直阻塞到服务器关闭。每个 TCP 代理连接占两个 socket，初审实现的两个复制方向各取 128 KiB 缓冲，合计 256 KiB。代理默认只监听回环，但配置允许 `0.0.0.0`（[`internal/config/config.go:509-510`](../internal/config/config.go#L509-L510)），且 SOCKS5 认证是可选的，因此这是现实的本地/局域网资源耗尽面。

**建议**：

- 增加最大并发连接数；
- 复用已有的 [`internal/tunnel/pipe.go`](../internal/tunnel/pipe.go)（已实现 idle deadline、半关闭和取消），或为两个 copy 方向设置可刷新的 idle deadline；
- 连接关闭时确保两个方向都能被唤醒。

### H-5 TLS/yamux 每流 16 MiB 窗口 × 1024 流，缺少会话级内存预算

**位置**：[`internal/tunnel/tls.go:104-116`](../internal/tunnel/tls.go#L104-L116)

```go
config.AcceptBacklog = yamuxAcceptBacklog        // 1024
config.MaxStreamWindowSize = 16 << 20            // 16 MiB
```

yamux v0.1.2 的流初始窗口是 256 KiB，`OpenStream` / `AcceptStream` 阶段已经调用 `sendWindowUpdate`，不需要等到应用第一次 `Read` 才扩到 `MaxStreamWindowSize`（初审为 16 MiB）。网关单设备 stream 上限默认 1024（[`server/gateway/listener.go:80-93`](../server/gateway/listener.go#L80-L93)），因此一个已认证的恶意客户端/出口组合在目标端不读数据时，可使 Relay 侧有约 16 GiB 的理论窗口额度（1024 × 16 MiB，非实测 RSS）。QUIC 路径有 128 MiB 连接接收窗口兜底，TLS/yamux 没有。

**建议**：降低 `MaxStreamWindowSize`（例如 1–4 MiB），并为每个 yamux session 增加聚合字节预算；或在流空闲时调用 yamux 的 `Shrink`。

### H-6 P2P punch 的 ACK 未绑定到 nonce 的发送目的地址

**位置**：[`internal/p2p/punch/udp.go:99`](../internal/p2p/punch/udp.go#L99)、[`internal/p2p/punch/udp.go:145-184`](../internal/p2p/punch/udp.go#L145-L184)、[`internal/p2p/punch/udp.go:236-250`](../internal/p2p/punch/udp.go#L236-L250)

`pending` 只保存 `nonce -> time`，不保存该 nonce 发往哪个地址：

```go
pending := make(map[uint64]time.Time, ...)
...
case secure.PunchAck:
    if sentAt, ok := pending[packet.Nonce]; ok {
        delete(pending, packet.Nonce)
        observation.gotAck = true     // observation 由 ACK 的源地址决定
```

对端收到自己地址上的请求后知道 nonce；如果它能伪造源地址，就可以把该 nonce 的 ACK 从 `victimIP:victimPort` 发出，使 victim 的 observation 被标记为可用。对 `PunchResponder`（`symmetric=false`，RDP 直连使用，[`agent/rdp/p2p/manager.go:766`](../agent/rdp/p2p/manager.go#L766)），只有 ACK 就足以进入 ready；随后 `NewPacketConn` 会把 RDP UDP 全部发往 victim。QUIC 路径至少会把 Initial 包发过去，形成反射/流量重定向。

**建议**：

- `pending` 改为 `nonce -> netip.AddrPort`，收到 ACK 时要求 `remote == pending[nonce]`；
- 不要在 ACK 分支学习新地址（只从带 MAC 的 Request/Keep 学习，并限制数量）；
- 使用随机 nonce；RDP UDP 在发送应用数据前增加一次认证过的数据包/QUIC 往返确认。

## 4. 中优先级问题

### M-1 Accept 循环在 EMFILE/ENFILE 等持续错误上忙等

**位置**：[`internal/proxy/socks5/server.go:170-184`](../internal/proxy/socks5/server.go#L170-L184)、[`internal/proxy/httpproxy/server.go:148-164`](../internal/proxy/httpproxy/server.go#L148-L164)、[`server/gateway/listener.go:295-307`](../server/gateway/listener.go#L295-L307)、[`server/gateway/listener.go:364-380`](../server/gateway/listener.go#L364-L380)

四处 accept 循环在非关闭错误时都是 `log + continue`。Go 的 `syscall.Errno.Temporary()` 对 `EMFILE`/`ENFILE` 返回 true（`/usr/local/go/src/syscall/syscall_unix.go:134`），应退避重试；初审实现会在 fd 持续耗尽时忙转并大量记录日志，但 fd 释放后仍可能恢复，原文“永远不会恢复”不准确。

**建议**：检查 `net.ErrClosed`/ctx 后，对 `net.Error` 做指数退避（5ms→1s，带 jitter），并 `select` ctx；同时配合 H-4 的连接上限。

### M-2 15 秒握手 deadline 泄漏到后续拨号和请求体阶段

**位置**：[`internal/proxy/httpproxy/server.go:203-213`](../internal/proxy/httpproxy/server.go#L203-L213)、[`internal/proxy/httpproxy/server.go:290-316`](../internal/proxy/httpproxy/server.go#L290-L316)、[`internal/proxy/socks5/server.go:214-216`](../internal/proxy/socks5/server.go#L214-L216)、[`internal/proxy/socks5/server.go:334-340`](../internal/proxy/socks5/server.go#L334-L340)、[`internal/proxy/socks5/server.go:381-386`](../internal/proxy/socks5/server.go#L381-L386)

`handleConn` 一开始设置 15 秒 deadline 防 Slowloris，但只在 CONNECT 成功写回复之后才清除。于是：

- HTTP POST/PUT 请求体若在 accept 后 15 秒还没传完，`req.Write(targetConn)` 会因客户端连接 deadline 到期而失败，客户端得不到 HTTP 响应；
- CONNECT 拨号本身可能接近 15 秒（tunnel dialer 内部还有 15 秒 stream deadline，[`agent/client/dialer.go:421-425`](../agent/client/dialer.go#L421-L425)），`200 Connection Established` 可能写在已过期的 socket 上；
- SOCKS5 先花最多 15 秒解析请求，再拨号 15 秒，最后 `sendReply` 时客户端连接可能已过期，客户端只看到 EOF 而不是成功/失败回复。

**建议**：解析完成后立即 `SetDeadline(time.Time{})`；拨号/转发阶段使用独立的、按活动刷新的 idle deadline；握手只约束握手本身。

### M-3 本地 HTTP 代理请求头没有大小上限

**位置**：[`internal/proxy/httpproxy/server.go:207-208`](../internal/proxy/httpproxy/server.go#L207-L208)

```go
reader := bufio.NewReader(conn)
req, err := http.ReadRequest(reader)
```

`http.ReadRequest` 内部的 `textproto.ReadMIMEHeader` 使用 `math.MaxInt64` 作为内存/头数量上限（`/usr/local/go/src/net/textproto/reader.go:508`）。本地或局域网客户端可以在 15 秒 deadline 内持续发送超大头部，导致代理进程内存增长。网关的共享 HTTP 路径已通过 `MaxHeaderBytes: 16 << 10` 做了正确示范（[`server/gateway/http_mux.go:140-142`](../server/gateway/http_mux.go#L140-L142)）。

**建议**：用 `io.LimitedReader` 或计数 `Conn` 包裹解析阶段（如 16–64 KiB），超限返回 `431 Request Header Fields Too Large`。

### M-4 Public Direct 验证无并发上限、无去重，且 3 秒超时不约束流读取

**位置**：[`server/direct/verifier.go:52-61`](../server/direct/verifier.go#L52-L61)、[`server/direct/verifier.go:122-164`](../server/direct/verifier.go#L122-L164)、[`server/direct/control.go:163-193`](../server/direct/control.go#L163-L193)

`verify()` 为每个候选地址启动一个 goroutine，没有 single-flight、全局 semaphore 或每设备速率限制。一个已认证的 Exit 可以反复发送 `public_direct_register`，驱动 Server 对任意公网 IP:port 做 DNS 解析和 QUIC 探测（`resolvePublicEndpoint` 只拒绝私网/环回，不限制目标数量），形成扫描/反射原语和 goroutine 增长。

同时，`Verify` 的 3 秒 `verifyCtx` 只作用于传输拨号：`probeEndpoint` 中 `protocol.ReadJSON(stream, &response)` 没有设置 stream deadline。恶意端点可以保持流不响应，验证 goroutine 会一直待到 QUIC idle timeout（120 秒，[`internal/tunnel/quic.go:147`](../internal/tunnel/quic.go#L147)），甚至可以持续发包延长。

**建议**：为 probe stream 设置 `SetDeadline`；按 `(deviceID, address)` 做 single-flight；增加全局验证 semaphore 和每设备注册/验证速率限制；对 `StateFailed`/`StateExpired` 的地址不要每 2 分钟无脑重试。

### M-5 SOCKS5 UDP 关联可能永久卡住；新目标拨号阻塞收包循环

**位置**：[`internal/proxy/socks5/server.go:489-580`](../internal/proxy/socks5/server.go#L489-L580)、[`internal/proxy/socks5/server.go:600-650`](../internal/proxy/socks5/server.go#L600-L650)

两个问题：

1. UDP 读循环因任意读错误退出后，`association.close()` 不会关闭 `control`，随后：
   ```go
   select {
   case <-controlDone:
   case <-s.ctx.Done():
   }
   ```
   如果客户端仍保持 TCP 控制连接，`controlDone` 永远不会关闭，`s.ctx` 又是服务器级 context，handler 和关联会一直挂到整个 Server 关闭。Windows 上 UDP 收到 ICMP 时可能产生读错误，属于可触发场景。
2. `association.forward` 在收包循环中同步拨号（[`server.go:574`](../internal/proxy/socks5/server.go#L574)、[`server.go:640`](../internal/proxy/socks5/server.go#L640)）。某个新目标拨号卡住时，该关联的所有其他 UDP 包都会被阻塞，内核队列溢出丢包。

**建议**：循环退出时关闭 `control`（或把 control 纳入 association 的统一 `close`），并用有界等待；新目标拨号改为异步 worker，按目标维护有界队列，读循环只做入队。

### M-6 可续传流的 replay 缓冲缺少进程级预算

**位置**：[`internal/p2p/resume/replay.go:10`](../internal/p2p/resume/replay.go#L10)、[`internal/p2p/resume/replay.go:64-66`](../internal/p2p/resume/replay.go#L64-L66)、[`agent/app/agent.go:495`](../agent/app/agent.go#L495)、[`agent/exit/resume_registry.go:23-24`](../agent/exit/resume_registry.go#L23-L24)、[`internal/p2p/resume/endpoint.go:60`](../internal/p2p/resume/endpoint.go#L60)

`DefaultReplayLimit` 为 8 MiB/方向，接入端允许 128 个 resume session；每个 session 还有 512 KiB 的 inbound buffer。一个已认证客户端可以打开 128 条到“会持续产出但不读回”的目标的续传流，使 Exit 端约 1.1 GiB 内存被占满。单流有界，但进程级无界。

**建议**：增加全局 replay 预算（如 128 MiB），按 session 分配额度；超限时拒绝/中止新 session。

### M-7 中继 QUIC 的 UDP socket 未调优；DATAGRAM 发送可能阻塞整个会话

**位置**：[`internal/tunnel/socket.go:34-43`](../internal/tunnel/socket.go#L34-L43)、[`internal/tunnel/quic.go:361`](../internal/tunnel/quic.go#L361)、[`server/gateway/listener.go:198`](../server/gateway/listener.go#L198)、[`internal/tunnel/datagram.go:258-294`](../internal/tunnel/datagram.go#L258-L294)

- **此子结论不成立**：虽然业务路径没有调用 `TuneQUICUDPConn`，当前 quic-go 在 `wrapConn` 内部已调用 `setReceiveBuffer` / `setSendBuffer`，通常以 8 MiB 为目标（OpenBSD 为 2 MiB），并在 Linux 尝试 FORCE。实际值仍取决于内核上限和权限，需要运行时测量，不能按 OS 默认值断言未调优。
- quic-go 的 `datagramQueue.Add` 在发送队列（32 个 DATAGRAM）满时会阻塞，且没有 context/deadline。`sendLoop` 是每个 QUIC 会话唯一的 DATAGRAM 发送者，因此一个拥塞/慢对端会阻塞该会话上所有 UDP association；`UDPDatagramConn.WriteTo` 只在入队前检查 write deadline，无法取消已经阻塞的 `SendDatagram`。

**建议**：用 `quic.Dial`/`quic.Transport` 搭配自己创建并 `TuneQUICUDPConn` 的 `*net.UDPConn`（session 关闭时一并关闭）；网关监听也改为持有 UDP socket。对 `SendDatagram` 用超时或“队列满即丢”的策略，避免单发送者无限阻塞。

### M-8 UPnP 只校验“同网段”，局域网内可伪造 IGD

**位置**：[`internal/p2p/upnp/upnp.go:505-527`](../internal/p2p/upnp/upnp.go#L505-L527)、[`internal/p2p/upnp/upnp.go:620-631`](../internal/p2p/upnp/upnp.go#L620-L631)、[`internal/p2p/upnp/upnp.go:784-797`](../internal/p2p/upnp/upnp.go#L784-L797)、[`internal/p2p/upnp/upnp.go:224-247`](../internal/p2p/upnp/upnp.go#L224-L247)、[`agent/p2p/endpoint.go:164-168`](../agent/p2p/endpoint.go#L164-L168)

SSDP 响应只要求发送者在接口子网内（`onInterfaceSubnet`），不要求是默认网关。LAN 内被入侵的设备（或伪造源地址的攻击者）可以冒充 IGD：

- 可以冒充 IGD 提供描述及 SOAP 服务；但代码已经要求描述主机等于 SSDP 源地址，control URL 也固定到同一主机，禁用重定向并固定拨号 IP。因此不能笼统描述为“任意 LAN 主机 SSRF”；若要指向另一主机，还需要伪造 SSDP 源地址等额外条件；
- 通过 `GetExternalIPAddress` 返回任意公网 IPv4，`IsPublicWANIPv4` 通过后会被 Agent 当作自己的 reflexive candidate 上报，随后 Agent/对端会向该地址打 punch 包（UDP 反射）。

**建议**：只信任从路由表/DHCP 得到的默认网关地址；公网 WAN IP 需要从外网侧（STUN 或 Server 观测）交叉确认后才能上报；对 UPnP 描述和 control URL 再做白名单/源校验。

### M-9 关闭时 Proxy Exit 清单推送与 `Gateway.Close` 的竞态

**位置**：[`server/gateway/listener.go:773-790`](../server/gateway/listener.go#L773-L790)、[`server/gateway/listener.go:224-248`](../server/gateway/listener.go#L224-L248)、[`cmd/relay-server/main.go:732-733`](../cmd/relay-server/main.go#L732-L733)、[`cmd/relay-server/main.go:246-252`](../cmd/relay-server/main.go#L246-L252)

`authorizationExpiry` ticker 在 `gw.Close()` 之后才停止，因此每 2 秒仍可能调用 `RefreshProxyExitInventories`。`notifyProxyExitInventoryChanged` 先检查 `closed`，再调用 `g.wg.Add(1)`；如果 `Close` 在检查之后完成 `sessions.CloseAll()` 和 `wg.Wait()`，推送 goroutine 可能在 Close 返回后才运行，使用已关闭的 tunnel/DB，极端交错下还可能触发 WaitGroup misuse。窗口很小，但关闭契约不严格。

**建议**：先停 ticker 再 `gw.Close()`；在 `g.mu` 下同时完成 `closed` 检查和 `wg.Add(1)`；或为推送任务单独使用可 join 的 worker 集合。

## 5. 低优先级与加固项

| 问题 | 位置 | 说明 |
| --- | --- | --- |
| 遗留设备可绕过 server-exit 授权 | [`server/gateway/router.go:140-157`](../server/gateway/router.go#L140-L157) | 只有带 `IdentityID` 的客户端才检查 `authChecker(..., ServerExitDeviceID)`；`AllowLegacyDeviceAuth` 下 identity-less 设备可直接使用 server-local exit。应在所有情况下检查并 fail closed。 |
| SOCKS5 UDP 客户端地址可被抢占 | [`internal/proxy/socks5/server.go:549-568`](../internal/proxy/socks5/server.go#L549-L568) | `DST.PORT=0` 时第一个匹配回环来源的包会锁定 `clientAddr`；本机其他进程若先发包可劫持回包。建议要求非 0 客户端端口，或用 TCP 控制连接所属进程绑定。 |
| RDP UDP keepalive 遇到一次瞬时错误永久停止 | [`internal/p2p/punch/udp.go:359-377`](../internal/p2p/punch/udp.go#L359-L377) | `WriteToUDPAddrPort` 返回一次 `ENETUNREACH`/`EAGAIN` 就 `return`，NAT 映射随后过期，直连 UDP 静默失效。应退避重试，只在 `done` 或 socket 已关闭时退出。 |
| UPnP `refreshLoop` 在 Close 后最多滞留 30 分钟（复核不成立） | [`internal/p2p/upnp/upnp.go`](../internal/p2p/upnp/upnp.go) | `select` 已包含 `<-m.done`，关闭 channel 会立即唤醒等待；正在进行的 refresh 受 5 秒 context 和 `opMu` 约束，不存在等待下次 30 分钟 tick 的路径。 |
| UPnP Close 在所有权查询失败时不删除映射 | [`internal/p2p/upnp/upnp.go`](../internal/p2p/upnp/upnp.go) | 属于保守的所有权保护。可考虑有界重试；不能在无法确认所有权时兜底删除，以免误删已转让给其他设备的映射。 |
| DATAGRAM 通道关闭不联动所有者 | [`internal/tunnel/datagram.go:288-290`](../internal/tunnel/datagram.go#L288-L290)、[`internal/tunnel/udp_datagram.go:60-79`](../internal/tunnel/udp_datagram.go#L60-L79) | `SendDatagram` 失败只 `job.channel.Close()`，不会关闭 `UDPDatagramConn` 的 lifetime stream/读 goroutine；另外构造函数在 `setCleanup`/`setReaper` 失败时返回一个已关闭但非 nil 的 conn，调用方会把它当成功使用。 |
| `DialQUIC` 默认 TLS 不推断 `ServerName` | [`internal/tunnel/quic.go:338-352`](../internal/tunnel/quic.go#L338-L352)、[`internal/tunnel/manager.go:80-87`](../internal/tunnel/manager.go#L80-L87) | 该 fork 的 quic-go 把 `setTLSConfigServerName` 注释掉了；`NewTunnelManager` 的“nil TLSConfig 使用安全默认值”在 QUIC-only 下会因 `ServerName` 为空而握手失败（agent 生产路径显式设置了 ServerName，所以目前是潜在 API 问题）。 |
| 状态回调在 `stateEvents` 锁内执行 | [`internal/tunnel/manager.go:113-140`](../internal/tunnel/manager.go#L113-L140)、[`manager.go:143-166`](../internal/tunnel/manager.go#L143-L166) | 回调若同步调用 `Connect`/`MarkSessionLost` 会自死锁。当前 agent 回调通过新 goroutine 调用，属于潜在问题；建议回调移出锁。 |
| Linux socket buffer 诊断值翻倍/丢一半 | [`internal/tunnel/socket_buffer_linux.go:20-29`](../internal/tunnel/socket_buffer_linux.go#L20-L29) | `SO_RCVBUF`/`SO_SNDBUF` 在 Linux 返回内核翻倍值；一次 getsockopt 失败会丢弃另一个成功值；`Set*Buffer` 错误被忽略，诊断页会误导。 |
| RDP 本地 TCP accept 遇瞬时错误永久退出 | [`agent/rdp/controller.go:259-270`](../agent/rdp/controller.go#L259-L270) | 非 ctx 取消的 Accept 错误直接 return，监听器永久失效。应区分临时错误并退避重试。 |
| 0 长度 UDP 数据报返回 `(0, addr, nil)`（复核不构成缺陷） | [`internal/tunnel/udp_stream.go`](../internal/tunnel/udp_stream.go) | 空 UDP 数据报是合法数据。该实现已消费完整帧、重置 framing 并返回来源地址，下次读取会等待下一帧；已查调用方没有由此产生无输入忙循环的证据。 |

## 6. 已验证正常的关键点

- **TLS 固定**：P2P 与 Public Direct 使用 `InsecureSkipVerify` + `VerifyPeerCertificate` 校验叶子证书 SHA-256，使用 TLS 1.3。P2P 要求客户端证书；Public Direct 的客户端身份由 Ticket 校验，监听端没有要求双向 TLS，probe 也不需要 Ticket。已查配置未设置客户端 session cache。
- **Ticket**：签发/过期/时钟偏移/生命周期上限/issuer+nonce 重放缓存实现正确（[`agent/direct/ticket_auth.go:90-166`](../agent/direct/ticket_auth.go#L90-L166)）。
- **Exit ACL/TOCTOU**：Relay 用自己的 `acl.Policy` 覆盖客户端字段，要求 Exit 声明 `target_acl`，Exit 在 DNS 解析后逐个 IP 复查再拨号，未发现 DNS rebinding 绕过。
- **Session 管理**：`UnregisterSession` 有指针代际检查，`RegisterAuthenticated` 与撤销用 `authorizationMu` 串行化，map 访问均有锁。
- **Tunnel 基础部件**：`InterruptOnCancel`、`Pipe` 的 idle deadline、resume framebuffer 的边界检查、DATAGRAM 预算/关联上限等实现较扎实；`go test -race` 通过。

## 7. 验证记录

```text
go build ./...                                      PASS
go vet ./internal/tunnel ./agent/client \
  ./internal/p2p/... ./internal/proxy/... \
  ./server/gateway ./server/session ./server/direct \
  ./internal/traffic                                PASS
go test -race ./internal/tunnel ./agent/client \
  ./internal/p2p/... ./internal/proxy/... \
  ./server/gateway ./server/session ./server/direct \
  ./internal/traffic                                PASS
```

SSE 复现：独立最小 `http.Server`，`WriteTimeout: 300ms`，每 100ms 写一条 SSE 事件；客户端读取约 403ms 后流结束，确认 `WriteTimeout` 会切断长连接响应。

## 8. 建议修复顺序

1. SSE `WriteTimeout`（功能直接受损，改动最小）
2. Public Direct probe 会话关闭/上限（未认证 DoS）
3. HTTP CONNECT authority 校验（本机/ACL 绕过）
4. 本地代理连接上限 + idle deadline + accept 退避
5. yamux 窗口/会话内存预算
6. punch ACK 目的地址绑定
7. 15 秒 deadline 泄漏、HTTP 头部上限
8. verifier 超时/并发限制、QUIC socket 调优、UPnP 网关校验

## 9. 同日核验与首批修复

### 9.1 核验依据

先核对工作树和依赖源码，再添加针对性回归测试。知识图谱的快照日期为 2026-10-02，多数相关文件被覆盖检查标为已变更或未跟踪，因此图谱仅用于定位，结论以磁盘源码为准。没有分配多 GiB 内存来实测资源耗尽，内存数字均指窗口额度或配置允许的量级，不等于已分配 RSS。Windows、Linux/Android 的平台专有故障仍需实机验证。

修复前定向运行以下测试，四类问题均失败并被复现：

```text
TestLiveEventsSurviveServerWriteTimeout            SSE 在全局写截止时间后 unexpected EOF
TestPublicDirectClosesUncooperativeProbe           一秒后未认证会话仍存活
TestRejectInvalidAuthorityBeforeDial               空主机/零端口/溢出端口等进入 DialTCP
TestPunchRejectsAckFromDifferentAddress            未知地址及另一候选地址的 ACK 均被错误接受
```

### 9.2 逐项核验和当前状态

| 编号 | 核验结论 | 当前状态 |
| --- | --- | --- |
| H-1 | 成立。全局 `WriteTimeout` 限制整个 SSE 响应 | **已修复**：每次写入及 Flush 使用 10 秒预算，事件之间清空写 deadline，并检查 Flush 错误 |
| H-2 | 成立。probe 返回后释放握手 slot，却没有关闭 QUIC session。32 MiB 是初始接收信用额度，不能当作每连接必定预分配的内存 | **已修复**：probe 响应后最多等待 1 秒，优先响应 session 关闭；关闭前保留握手 slot。验证方读完有效 echo 后尽力发送 FIN，末尾 FIN 错误不否定已验证的响应；取消回调会等待关闭结束 |
| H-3 | 成立。HTTP 普通请求的端口溢出会饱和到 65535，而非一概变成 0 | **已修复**：CONNECT 与普通 HTTP 共用 authority 校验，拒绝空 host、0/非法端口和非法主机名，规范化 IPv6 与映射 IPv4；兼容 ASCII 下划线内网主机，IDN 在路由/ACL/拨号前转为 punycode；解析失败返回 400 |
| H-4 | 成立。Server.Close 已能回收活动连接，但长期运行时没有并发/空闲边界 | **已修复**：两种代理默认各 256 个客户端连接、5 分钟 TCP idle timeout，复用 `tunnel.Pipe`，超限即关闭新连接。YAML 暴露 `proxy.max_connections`、`idle_timeout_sec`、`udp_idle_timeout_sec`，重启生效。UDP 关联独立按双向有效报文计时，默认 5 分钟空闲后关闭 UDP/control 并释放名额，扫描间隔最多 30 秒 |
| H-5 | 成立。yamux 在建流/接受阶段即可扩大窗口；原 16 GiB 为理论额度 | **已加入接收聚合预算**：单流窗口 1 MiB，保留 1024 backlog；本地 yamux 补丁在接收分配前限制会话全部接收数组容量为 64 MiB，包含 Accept/Read 前及扩容时新旧数组。超限复位触发流、排空该帧并保留其他流。读取、完整关闭、RST 和显式会话关闭回收相应容量；这不是包含传输/应用缓冲和等待 GC 数组的进程 RSS 上限 |
| H-6 | 成立。不同地址可用同一个有效 nonce 通过 ACK 检查；原 nonce 有随机起点但后续递增 | **已修复**：保存 nonce 的目的地址和发送时间，严格匹配 ACK 源地址；仅从带 MAC 的 Request/Keep 学习地址，每次 challenge 独立随机，清理超过 5 秒的 challenge |
| M-1 | 成立。持续 accept 错误会忙等；“永远不能恢复”不成立 | **已修复**：两种代理、Gateway TCP/QUIC 共用 5ms → 1s 指数退避，带 jitter、成功重置及 context 取消 |
| M-2 | 成立。解析 deadline 可污染拨号回复或 HTTP 请求体 | **已修复**：解析结束清空 deadline；TCP 拨号独立 15 秒预算，请求体按活动刷新 idle deadline，隧道阶段由 Pipe 管理 |
| M-3 | 成立。`http.ReadRequest` 没有业务请求头大小上限 | **已修复**：请求行和头部合计上限 64 KiB，超限返回 431，解析后移除限读且保留预读字节；大请求体不受此上限影响 |
| M-4 | 成立；注册记录的 `StateVerifying` 能减少部分重复，但不是完整 single-flight 或全局并发上限 | **部分修复**：probe stream 设置 context deadline，取消时关闭 session；验证并发、去重和设备速率限制仍待处理。失败端点重试需保留网络恢复能力，不能直接永久停用 |
| M-5 | 两个子问题均成立 | **部分修复**：UDP 接收失败会关闭 TCP control，handler 可退出；按目标异步拨号及有界队列仍待处理 |
| M-6 | 成立。默认 Agent 配置允许 128 个 session，各方向 replay 上限 8 MiB，另有 inbound buffer；是约 GiB 级容量而非每条会话立即分配 8 MiB | 待增加进程 replay 总预算和可靠的释放/背压机制 |
| M-7 | “QUIC socket 未调优”不成立：当前依赖自动尝试设置收发缓冲；DATAGRAM 队列阻塞成立 | 保留 DATAGRAM 发送公平性/阻塞问题；不根据误判改写 socket 所有权。应用队列已有有界丢包策略，不能将其描述为 WriteTo 本身始终阻塞 |
| M-8 | IGD 来源/WAN IP 信任问题成立；“任意 LAN 主机 SSRF”表述过强 | 待确定跨平台可信网关和外部观测交叉校验方案；已有源地址、同主机 URL、固定 IP 与禁重定向保护 |
| M-9 | 成立。closed 检查与 WaitGroup.Add 之间存在关闭窗口 | **已修复**：在 Gateway 锁内完成最终 closed 检查和 Add；服务端关闭流程先停止授权过期生产者，再关闭 Gateway |

低优先级项目按原表顺序复核：

| 项目 | 核验结论与状态 |
| --- | --- |
| 遗留设备 server-exit 授权 | 成立，但限于显式启用 `AllowLegacyDeviceAuth` 的兼容路径，生产默认关闭；待在兼容测试中统一 fail closed |
| SOCKS5 UDP 客户端地址抢占 | 成立，攻击面为同机、同 loopback IP、且请求端口为 0；强制非零端口会影响标准客户端，需兼容方案 |
| RDP UDP keepalive 瞬时错误停止 | 成立；待按关闭错误/瞬时错误分类重试 |
| UPnP refreshLoop 关闭滞留 | **不成立**：done channel 已能立即唤醒 select |
| UPnP Close 查询失败不删除映射 | 行为存在，属于所有权保护；可做有界重试，**不采纳无所有权确认时兜底删除** |
| DATAGRAM 关闭不联动 owner | 成立。channel 先从 mux 删除后，公共 reaper 不能再看到 owner；构造阶段也存在已关闭非 nil 的返回；待完善级联关闭和调用端错误传播 |
| QUIC 默认 ServerName | 成立，当前 fork 的 `DialAddr` 不推断 ServerName；生产 Agent 显式配置，默认 API 路径待补齐 |
| TunnelManager 回调重入 | 成立。回调在 `stateEvents` 下执行，Connect 还涉及 `connectMu`；需有序事件派发方案，简单移出一个锁不足以支持任意重入 |
| Linux socket buffer 诊断 | 内核返回翻倍值且任一 getsockopt 失败会丢失另一个值的事实成立；原始内核容量是否错误取决于诊断口径，需明确后统一跨平台展示；本轮未修改 |
| RDP TCP accept 错误退出 | **已修复**：复用可取消 AcceptBackoff，临时错误后继续接受连接 |
| 空 UDP 数据报 | **不构成已证实缺陷**：完整消费帧后返回 `(0, addr, nil)` 符合数据报语义，不能据此推导忙循环 |

### 9.3 验证与后续顺序

新增覆盖真实 HTTP Server 写超时、未配合关闭的 QUIC probe、非法目标拨号拦截、跨地址 ACK、连接上限释放、TCP 半关闭回收、慢请求体、大请求体、UDP 失败退出、验证流读取超时，以及 yamux 慢读背压的回归测试。现有预读 CONNECT 数据与半关闭测试继续保留。

```text
go build ./...                                                     PASS
go vet ./internal/netutil ./internal/tunnel ./agent/client \
  ./agent/direct ./agent/exit ./agent/rdp ./internal/p2p/... \
  ./internal/proxy/... ./server/api ./server/gateway \
  ./server/session ./server/direct ./internal/traffic \
  ./cmd/relay-server                                               PASS
go test -race ./internal/netutil ./internal/tunnel ./agent/client \
  ./agent/direct ./agent/exit ./agent/rdp ./internal/p2p/... \
  ./internal/proxy/... ./server/api ./server/gateway \
  ./server/session ./server/direct ./internal/traffic \
  ./cmd/relay-server                                               PASS
git diff --check                                                  PASS
```

补充旧版 verifier 提前 FIN 的兼容等待后，再次定向运行 `go test -race ./agent/direct ./server/direct ./internal/p2p/punch ./internal/proxy/... ./internal/netutil ./internal/tunnel`，全部通过。最后补测“HTTP CONNECT 拨号时间同时超过短握手和短 idle 配置”场景并独立设置回复预算，HTTP 代理 race、vet、全量 build 及 diff 检查再次通过。尚未执行跨平台实机故障注入或高 BDP WAN 压测。

H-5 的接收聚合预算已补齐；接下来按 M-4 验证入口并发/去重/速率、M-5 异步 UDP 拨号、M-6 replay 总预算的顺序处理。yamux 窗口和聚合限额可能影响高 BDP 链路吞吐或突发连接成功率，需要跨网络压测；本轮本机测试不代表完成该项性能验证。


### 9.4 复审补充修复（2026-10-18）

- H-2：grace 提升到 1 秒；加入真实 QUIC 经双向各 350ms 延迟 UDP 中继（约 700ms RTT）、延迟消费响应，以及旧 verifier 提前 FIN 的测试。将同一测试临时恢复为 250ms grace 时，明确复现远端 `session closed`；恢复 1 秒后通过。验证成功后的 `CloseWrite` 改为尽力发送；监听端和验证端复用会等待取消回调结束的 `InterruptOnCancel`。
- H-5：保留 `hashicorp/yamux v0.1.2` 协议与 MPL-2.0 许可，在 `third_party/yamux` 维护接收层补丁，通过根模块 replace 接入。计费针对数组容量，部分读取仍计费，读空即释放；扩容先预留新数组，旧数组在替换后才归还额度。预算拒绝后复位该流、固定缓冲排空帧，不等待应用读取。补测 Accept 前后聚合超限、其他流存活、读取/关闭/RST/会话关闭归还，以及 SYN 记录清理。远端 EOF 保留已收到的数据供应用读完，显式本地关闭才丢弃；半关闭兼容测试重复运行 10 轮通过。
- H-4：默认 256 保持不变，可用 YAML 提高到 1024；并发范围 1–65536、空闲时间范围 1–86400 秒。UDP 关联空闲和目标空闲分别处理，活动双向刷新关联时钟，保持 TCP control 开启也会回收闲置关联。补测关联 socket/control/slot 回收、双向单边活动续期、配置加载/保存及 GUI 保存后仍保留参数并提示重启。
- H-3：ASCII 下划线与 IDN 兼容已补，既有端口、空主机、路径、userinfo、IPv6 zone 拒绝规则保留；真实 CONNECT 请求验证拨号前规范化结果。配置与主机名规则同步到 README 和 Agent 示例。

yamux 补丁须在升级时单独复核，CI 已增加该嵌套模块的独立 race 测试。64 MiB 是每会话接收数组预算，发送队列、流元数据、TLS/TCP、应用/replay 缓冲及等待 GC 的旧数组不在其中；不能将其写成进程 RSS 上限。超限策略会使触发流失败，需要结合业务负载观察连接突发和高 BDP 吞吐。

本轮最终验证（本机 macOS arm64）：

```text
go build ./...                                           PASS
go vet ./...                                             PASS
go test ./...                                            PASS
go test -race ./internal/tunnel ./agent/client \
  ./agent/direct ./agent/exit ./agent/rdp ./agent/rdp/p2p \
  ./server/direct ./server/session ./server/gateway \
  ./internal/proxy/... ./internal/config ./agent/bridge \
  ./cmd/relay-agent                                      PASS
(cd third_party/yamux && go vet ./...)                    PASS
(cd third_party/yamux && go test ./... -count=1)           PASS
(cd third_party/yamux && go test -race -short ./... \
  -count=1)                                              PASS
go mod tidy -diff                                        PASS
git diff --check                                         PASS
```

yamux 全量 race 在并行校验期间曾于大传输和多流用例触发测试配置的 250ms 写超时；这两个用例独立 race 复跑三轮均通过。上游大传输测试已有 `testing.Short` 跳过分支，并注明 race 下可能超时。CI 因此执行完整普通测试（保留大传输）加 short race（保留全部新增预算测试），不声称全量 race 从未失败。远端 EOF 保留已收数据、半关闭及预算用例另外连续 race 运行 10 轮通过。建流 benchmark smoke 通过，但不代表 WAN 吞吐验证。

DATAGRAM owner 级联关闭、默认 QUIC ServerName、TunnelManager 回调重入、同机 UDP 客户端地址抢占和 RDP keepalive 错误分类仍按 9.2 的待办状态处理。

---

*本报告基于当前工作树静态审查与定向验证，未进行 Windows WinDivert、macOS Network Extension 及 Android VPN 的实机/跨平台运行验证；相关平台特有路径仍需在对应环境复测。*
