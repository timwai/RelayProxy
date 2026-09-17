# M1: UDP 隧道（DialUDP）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在现有 TCP 隧道旁增加端到端 UDP：协议帧、`TunnelDialer.DialUDP`、Relay 路由、Exit 转发，可用单测验证。

**Architecture:** 镜像 `OpenTCP` 握手：客户端开 stream → `StreamHeader(Type=OpenUDP)` → JSON `OpenUDPRequest` → Exit dial UDP → JSON `OpenUDPResponse` → 之后在同一 stream 上用 **长度前缀 datagram**（`uint32 BE length + payload`）双向传包。一个 `DialUDP` 关联绑定单一目标 `host:port`（connected UDP 语义）。

**Tech Stack:** Go；现有 `internal/protocol`、`agent/client`、`agent/exit`、`server/gateway`；测试用 `net.ListenPacket("udp", "127.0.0.1:0")` 作回显目标。

## Global Constraints

- 见索引计划；本计划不改 divert/TUN/GUI
- 新 FrameType 取值：`OpenUDP=0x08`，`OpenUDPResp=0x09`（勿复用 0x02/0x03）
- Datagram 最大 payload：65535；长度字段为 payload 字节数；`length==0` 表示关联正常结束（可选，实现用 Close 即可，读到 EOF 结束）
- 空闲超时：Exit 与 Client 侧默认 **60s** 无包则关闭 stream（可用 `SetReadDeadline` 滑动）
- 提交格式：`<type>(<scope>): <中文说明>`（仅用户要求时提交）

---

### Task 1: 协议类型与编解码单测

**Files:**
- Modify: `internal/protocol/message.go`
- Create: `internal/protocol/udp_frame.go`
- Create: `internal/protocol/udp_frame_test.go`
- Modify: `internal/protocol/codec_test.go`（若需覆盖新 FrameType 写入 header）

**Interfaces:**
- Consumes: 现有 `WriteStreamHeader` / `ReadStreamHeader` / `WriteJSON` / `ReadJSON`
- Produces:
  - `const FrameTypeOpenUDP FrameType = 0x08`
  - `const FrameTypeOpenUDPResp FrameType = 0x09`
  - `type OpenUDPRequest struct { RequestID string; Host string; Port uint16; TimeoutMs int }`
  - `type OpenUDPResponse struct { RequestID string; Success bool; RemoteIP string; ErrorCode string; ErrorMessage string }`
  - `func WriteUDPDatagram(w io.Writer, payload []byte) error`
  - `func ReadUDPDatagram(r io.Reader) ([]byte, error)`

- [ ] **Step 1: Write the failing test**

```go
// internal/protocol/udp_frame_test.go
package protocol

import (
	"bytes"
	"testing"
)

func TestUDPDatagramRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	payload := []byte("hello-udp")
	if err := WriteUDPDatagram(&buf, payload); err != nil {
		t.Fatal(err)
	}
	got, err := ReadUDPDatagram(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("got %q want %q", got, payload)
	}
}

func TestUDPDatagramRejectsTooLarge(t *testing.T) {
	big := make([]byte, 65536)
	if err := WriteUDPDatagram(ioDiscard{}, big); err == nil {
		t.Fatal("expected error for payload > 65535")
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }
```

（若 `ioDiscard` 不便，可对 `WriteUDPDatagram` 在编码前检查长度，测试不写盘。）

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/protocol/ -run UDPDatagram -count=1`

Expected: FAIL（`WriteUDPDatagram` undefined）

- [ ] **Step 3: Write minimal implementation**

在 `message.go` 增加：

```go
FrameTypeOpenUDP     FrameType = 0x08
FrameTypeOpenUDPResp FrameType = 0x09
```

```go
type OpenUDPRequest struct {
	RequestID string `json:"requestId"`
	Host      string `json:"host"`
	Port      uint16 `json:"port"`
	TimeoutMs int    `json:"timeout"`
}

type OpenUDPResponse struct {
	RequestID    string `json:"requestId"`
	Success      bool   `json:"success"`
	RemoteIP     string `json:"remoteIp,omitempty"`
	ErrorCode    string `json:"errorCode,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`
}
```

创建 `udp_frame.go`：

```go
package protocol

import (
	"encoding/binary"
	"fmt"
	"io"
)

const MaxUDPDatagramPayload = 65535

func WriteUDPDatagram(w io.Writer, payload []byte) error {
	if len(payload) > MaxUDPDatagramPayload {
		return fmt.Errorf("udp datagram payload %d exceeds %d", len(payload), MaxUDPDatagramPayload)
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func ReadUDPDatagram(r io.Reader) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > MaxUDPDatagramPayload {
		return nil, fmt.Errorf("udp datagram length %d exceeds %d", n, MaxUDPDatagramPayload)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/protocol/ -count=1`

Expected: PASS

- [ ] **Step 5: Commit**（仅当用户要求提交时）

```bash
git add internal/protocol/message.go internal/protocol/udp_frame.go internal/protocol/udp_frame_test.go
git commit -m "$(cat <<'EOF'
feat(protocol): 增加 OpenUDP 帧类型与 datagram 编解码

EOF
)"
```

---

### Task 2: 扩展 TunnelDialer 接口

**Files:**
- Modify: `internal/proxy/types.go`
- Modify: 所有实现 `TunnelDialer` 的类型，使编译通过：`agent/client/dialer.go`、`agent/routing/dialer.go`（可先 stub `return nil, fmt.Errorf("udp not implemented")`，Task 3 再实现 client）

**Interfaces:**
- Produces:
  ```go
  type TunnelDialer interface {
      DialTCP(ctx context.Context, exitNodeID string, host string, port uint16) (net.Conn, error)
      DialUDP(ctx context.Context, exitNodeID string, host string, port uint16) (net.PacketConn, error)
  }
  ```

- [ ] **Step 1: Write a compile-oriented test in routing（可选）或直接改接口并 `go build`**

优先：改接口后运行

Run: `go build ./...`

Expected: FAIL — `RoutingDialer` / 其它类型缺少 `DialUDP`

- [ ] **Step 2: Add DialUDP to interface**

```go
// internal/proxy/types.go
type TunnelDialer interface {
	DialTCP(ctx context.Context, exitNodeID string, host string, port uint16) (net.Conn, error)
	DialUDP(ctx context.Context, exitNodeID string, host string, port uint16) (net.PacketConn, error)
}
```

- [ ] **Step 3: Stub RoutingDialer.DialUDP**

在 `agent/routing/dialer.go`，按与 `DialTCP` 相同的 Match 逻辑：

```go
func (d *RoutingDialer) DialUDP(ctx context.Context, exitNodeID string, host string, port uint16) (net.PacketConn, error) {
	action, eid := d.engine.Match(host, int(port))
	switch action {
	case ActionDirect:
		return net.ListenPacket("udp", ":0") // 随后由调用方 Dial？ 
        // 正确做法：使用 net.DialUDP 得到 connected PacketConn
	case ActionReject:
		return nil, fmt.Errorf("routing: connection rejected by rule")
	case ActionProxy:
		if eid == "" {
			eid = exitNodeID
		}
		return d.tunnel.DialUDP(ctx, eid, host, port)
	default:
		return d.tunnel.DialUDP(ctx, exitNodeID, host, port)
	}
}
```

**DIRECT 分支必须用 connected UDP：**

```go
var dnet net.Dialer
c, err := dnet.DialContext(ctx, "udp", net.JoinHostPort(host, strconv.Itoa(int(port))))
if err != nil {
	return nil, err
}
pc, ok := c.(net.PacketConn)
if !ok {
	_ = c.Close()
	return nil, fmt.Errorf("udp dial did not return PacketConn")
}
return pc, nil
```

注意：标准库 `Dial("udp", ...)` 返回的是 `*UDPConn`，同时实现 `net.Conn` 与 `net.PacketConn`。

- [ ] **Step 4: Stub client TunnelDialer.DialUDP**（下一 Task 替换）

```go
func (d *TunnelDialer) DialUDP(ctx context.Context, exitNodeID string, host string, port uint16) (net.PacketConn, error) {
	return nil, fmt.Errorf("DialUDP not implemented")
}
```

- [ ] **Step 5: Build**

Run: `go build ./...`

Expected: PASS（或仅剩 intentional stub 测试失败——不应有编译错误）

- [ ] **Step 6: Commit**（用户要求时）

```bash
git commit -m "$(cat <<'EOF'
feat(proxy): TunnelDialer 增加 DialUDP 接口

EOF
)"
```

---

### Task 3: Client DialUDP + PacketConn 适配

**Files:**
- Create: `agent/client/udp_conn.go`
- Create: `agent/client/udp_conn_test.go`
- Modify: `agent/client/dialer.go`

**Interfaces:**
- Consumes: `protocol.WriteUDPDatagram` / `ReadUDPDatagram` / `FrameTypeOpenUDP` / `OpenUDPRequest` / `OpenUDPResponse`
- Produces: `(*TunnelDialer).DialUDP(...) (net.PacketConn, error)`；`udpTunnelConn` 实现 `net.PacketConn`

- [ ] **Step 1: Write failing unit test for datagram conn over pipe**

```go
// agent/client/udp_conn_test.go
package client

import (
	"io"
	"net"
	"testing"
	"time"

	"relayproxy/internal/protocol"
)

func TestUDPTunnelConnRoundTrip(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	client := newUDPTunnelConn(c1, &net.UDPAddr{IP: net.IPv4(1, 2, 3, 4), Port: 53})
	go func() {
		payload, err := protocol.ReadUDPDatagram(c2)
		if err != nil {
			t.Errorf("server read: %v", err)
			return
		}
		_ = protocol.WriteUDPDatagram(c2, append([]byte("echo:"), payload...))
	}()

	_ = client.SetDeadline(time.Now().Add(2 * time.Second))
	n, err := client.WriteTo([]byte("ping"), nil)
	if err != nil || n != 4 {
		t.Fatalf("WriteTo: n=%d err=%v", n, err)
	}
	buf := make([]byte, 64)
	n, _, err = client.ReadFrom(buf)
	if err != nil {
		t.Fatal(err)
	}
	if string(buf[:n]) != "echo:ping" {
		t.Fatalf("got %q", buf[:n])
	}
}
```

- [ ] **Step 2: Run test — expect fail**

Run: `go test ./agent/client/ -run TestUDPTunnelConnRoundTrip -count=1`

Expected: FAIL（`newUDPTunnelConn` undefined）

- [ ] **Step 3: Implement udp_conn.go**

```go
package client

import (
	"net"
	"sync"
	"time"

	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

type udpTunnelConn struct {
	stream     tunnel.TunnelStream // or net.Conn — use same as DialTCP adapter input
	remote     net.Addr
	local      net.Addr
	readMu     sync.Mutex
	writeMu    sync.Mutex
	idle       time.Duration
}

func newUDPTunnelConn(stream tunnel.TunnelStream, remote net.Addr) *udpTunnelConn {
	return &udpTunnelConn{
		stream: stream,
		remote: remote,
		local:  &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0},
		idle:   60 * time.Second,
	}
}

func (c *udpTunnelConn) ReadFrom(p []byte) (int, net.Addr, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	_ = c.stream.SetReadDeadline(time.Now().Add(c.idle))
	payload, err := protocol.ReadUDPDatagram(c.stream)
	if err != nil {
		return 0, nil, err
	}
	n := copy(p, payload)
	return n, c.remote, nil
}

func (c *udpTunnelConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.stream.SetWriteDeadline(time.Now().Add(c.idle))
	if err := protocol.WriteUDPDatagram(c.stream, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *udpTunnelConn) Close() error                       { return c.stream.Close() }
func (c *udpTunnelConn) LocalAddr() net.Addr                { return c.local }
func (c *udpTunnelConn) SetDeadline(t time.Time) error      { return c.stream.SetDeadline(t) }
func (c *udpTunnelConn) SetReadDeadline(t time.Time) error  { return c.stream.SetReadDeadline(t) }
func (c *udpTunnelConn) SetWriteDeadline(t time.Time) error { return c.stream.SetWriteDeadline(t) }
```

若 `TunnelStream` 与 `net.Pipe` 不兼容，测试改为 mock 实现 `tunnel.TunnelStream`，或让 `newUDPTunnelConn` 接受 `interface { io.ReadWriteCloser; SetDeadline; SetReadDeadline; SetWriteDeadline }`。

- [ ] **Step 4: Implement DialUDP mirroring DialTCP**

在 `dialer.go` 将 stub 替换为：与 `DialTCP` 相同步骤，但：

- `header.Type = protocol.FrameTypeOpenUDP`
- 写 `OpenUDPRequest`，读 `OpenUDPResponse`
- 成功后 `return newUDPTunnelConn(stream, remoteUDPAddr), nil`

- [ ] **Step 5: Run unit tests**

Run: `go test ./agent/client/ ./internal/protocol/ -count=1`

Expected: PASS

- [ ] **Step 6: Commit**（用户要求时）

```bash
git commit -m "$(cat <<'EOF'
feat(agent/client): 实现 DialUDP 与隧道 PacketConn

EOF
)"
```

---

### Task 4: Exit 处理 OpenUDP

**Files:**
- Modify: `agent/exit/handler.go`
- Create: `agent/exit/udp_pipe.go`
- Create: `agent/exit/handler_udp_test.go`

**Interfaces:**
- Consumes: `FrameTypeOpenUDP`、`OpenUDPRequest`/`Response`、`WriteUDPDatagram`/`ReadUDPDatagram`、现有 ACL `CheckHost`/`CheckIP`
- Produces: `HandleStream` 在 `header.Type == OpenUDP` 时走 UDP 路径

- [ ] **Step 1: Write failing test with net.Pipe + local UDP echo**

```go
func TestHandleOpenUDPEcho(t *testing.T) {
	// 1. Start UDP echo on 127.0.0.1:0
	echo, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil { t.Fatal(err) }
	defer echo.Close()
	go func() {
		buf := make([]byte, 2048)
		for {
			n, addr, err := echo.ReadFrom(buf)
			if err != nil { return }
			_, _ = echo.WriteTo(buf[:n], addr)
		}
	}()
	host, portStr, _ := net.SplitHostPort(echo.LocalAddr().String())
	port, _ := strconv.Atoi(portStr)

	clientEnd, exitEnd := net.Pipe()
	h := NewHandler(HandlerConfig{})
	go h.HandleStream(context.Background(), adaptPipe(exitEnd)) // adapt to TunnelStream

	// client writes header + OpenUDPRequest + datagram
	// ... 使用 protocol.WriteStreamHeader / WriteJSON / WriteUDPDatagram
	// 再 ReadUDPDatagram 期望回显
}
```

实现时把 `adaptPipe` 做成测试辅助，满足 `tunnel.TunnelStream`。

- [ ] **Step 2: Run — expect fail / unsupported frame**

Run: `go test ./agent/exit/ -run TestHandleOpenUDPEcho -count=1`

Expected: FAIL 或日志 `Unsupported frame type`

- [ ] **Step 3: Implement handleOpenUDP**

在 `HandleStream`：

```go
switch header.Type {
case protocol.FrameTypeOpenTCP:
	h.handleOpenTCP(ctx, stream, header) // 可将现有逻辑提取
case protocol.FrameTypeOpenUDP:
	h.handleOpenUDP(ctx, stream, header)
default:
	log.Printf("[ExitHandler] Unsupported frame type: %d", header.Type)
}
```

`handleOpenUDP`：

1. Read `OpenUDPRequest`
2. ACL（与 TCP 相同，协议语义按 host/IP）
3. `net.DialUDP` 到目标（或 `Dial("udp", addr)`）
4. 写 `OpenUDPResponse{Success:true, RemoteIP:...}`
5. `pipeUDP(stream, udpConn)`：两端循环 `ReadUDPDatagram`↔`WriteTo` / `ReadFrom`↔`WriteUDPDatagram`，任一侧错误则关闭；滑动 60s deadline

- [ ] **Step 4: Tests pass**

Run: `go test ./agent/exit/ -count=1`

Expected: PASS

- [ ] **Step 5: Commit**（用户要求时）

```bash
git commit -m "$(cat <<'EOF'
feat(agent/exit): 支持 OpenUDP 出口转发

EOF
)"
```

---

### Task 5: Relay StreamRouter 路由 OpenUDP

**Files:**
- Modify: `server/gateway/router.go`
- Modify: ACL 调用处：`CheckHostProtocol(..., "udp")`（若已有协议参数）
- Create or extend: `server/gateway/router_udp_test.go` / 现有 integration test

**Interfaces:**
- Consumes: 与 `handleOpenTCP` 相同的 `resolveExitSession` / `authChecker` / `aclChecker`
- Produces: `case protocol.FrameTypeOpenUDP: r.handleOpenUDP(...)`

- [ ] **Step 1: Mirror handleOpenTCP → handleOpenUDP**

差异仅：

- 读/写 `OpenUDPRequest` / `OpenUDPResponse`
- 向 exit 写 `StreamHeader{Type: FrameTypeOpenUDP}`
- Audit `Protocol: "udp"`
- ACL：`CheckHostProtocol(ctx, host, port, "udp")`（若方法不存在，扩展 `acl.Checker` 增加 protocol 参数或复用 CheckHost）

握手成功后 **不要** 用 TCP 的 raw `io.Copy`；应对 client↔exit 两 stream 做 **datagram 透明转发**：

```go
// 双向：ReadUDPDatagram from A → WriteUDPDatagram to B
```

或在握手成功后直接 `io.Copy` 两边 raw bytes（因为 datagram 帧已是长度前缀字节流，**raw Copy 等价于透明转发 datagram 流**）。优先 **raw `io.Copy` 双向**（与 TCP 相同），更简单且正确。

- [ ] **Step 2: Build + unit/integration test**

Run: `go test ./server/gateway/ ./agent/exit/ ./agent/client/ ./internal/protocol/ -count=1`

Expected: PASS

若已有端到端 tunnel test 目录 `test/`，增加 `TestUDPTunnelEcho`：client DialUDP → relay → exit → 本地 UDP echo。

- [ ] **Step 3: Commit**（用户要求时）

```bash
git commit -m "$(cat <<'EOF'
feat(server/gateway): 路由 OpenUDP 流到出口节点

EOF
)"
```

---

### Task 6: M1 验收

**Files:** 无新代码；文档勾选

- [ ] **Step 1: Full regression**

Run:

```bash
go test ./internal/protocol/ ./agent/client/ ./agent/exit/ ./server/gateway/ ./agent/routing/ -count=1
go build ./...
```

Expected: 全部 PASS；编译成功

- [ ] **Step 2: Manual checklist**

- [ ] `FrameType` 0x08/0x09 已定义且不与旧值冲突
- [ ] `DialUDP` 在 client / routing / proxy 接口一致
- [ ] Exit 对未知 type 不再误伤；OpenUDP 有 ACL
- [ ] Relay audit 记录 `udp`
- [ ] 无 divert/TUN 改动混入本里程碑

- [ ] **Step 3: Update design status line**（可选）

在规格文首将状态改为：`M1 实现中/已完成`

---

## M1 Spec coverage

| 规格项 | Task |
|--------|------|
| 扩展 DialUDP | 2, 3 |
| 协议帧 | 1 |
| 出口 UDP 转发 | 4 |
| Relay 路由 | 5 |
| 单测验收 | 1, 3, 4, 5, 6 |

## Placeholder scan

无 TBD；datagram 格式与 FrameType 数值已钉死。

## Type consistency

- `OpenUDPRequest`/`OpenUDPResponse` 字段与 TCP 对齐（`requestId`/`timeout` JSON 标签）
- `TunnelDialer.DialUDP(...) (net.PacketConn, error)` 全链路统一
