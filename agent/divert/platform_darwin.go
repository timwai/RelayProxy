//go:build darwin

package divert

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
)

const darwinExtensionBundleID = "com.relayproxy.agent.network-extension"

func platformCapabilities() Capabilities {
	caps := Capabilities{
		Platform: "darwin", TCP: true, UDP: true, IPv6: true, Hostnames: true, HostnameSource: "network-extension",
		ProcessIdentity: true, OriginalDestination: true, ReplyInjection: true, LoopBypass: true,
	}
	if err := darwinPlatformReadiness(); err != nil {
		caps.UnavailableReason = err.Error()
	}
	return caps
}

func darwinIPCPaths() (string, string, error) {
	socketPath := strings.TrimSpace(os.Getenv("RELAYPROXY_NE_SOCKET"))
	tokenPath := strings.TrimSpace(os.Getenv("RELAYPROXY_NE_TOKEN_FILE"))
	if socketPath != "" && tokenPath != "" {
		return socketPath, tokenPath, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	base := filepath.Join(home, "Library", "Group Containers", "group.com.relayproxy.shared")
	if socketPath == "" {
		socketPath = filepath.Join(base, "agent.sock")
	}
	if tokenPath == "" {
		tokenPath = filepath.Join(base, "ipc.token")
	}
	return socketPath, tokenPath, nil
}

func darwinPlatformReadiness() error {
	_, tokenPath, err := darwinIPCPaths()
	if err != nil {
		return err
	}
	info, err := os.Stat(tokenPath)
	if err != nil {
		return fmt.Errorf("Network Extension IPC token 不可用: %w", err)
	}
	if info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("Network Extension IPC token 权限必须为 0600: %s", tokenPath)
	}
	if _, err := readDarwinToken(tokenPath); err != nil {
		return err
	}
	if os.Getenv("RELAYPROXY_NE_SKIP_STATUS") != "1" {
		output, err := exec.Command("/usr/bin/systemextensionsctl", "list").CombinedOutput()
		if err != nil {
			return fmt.Errorf("检查 Network Extension 状态失败: %w", err)
		}
		status := string(output)
		if !strings.Contains(status, darwinExtensionBundleID) ||
			(!strings.Contains(status, "activated enabled") && !strings.Contains(status, "[activated enabled]")) {
			return fmt.Errorf("Network Extension %s 尚未安装并获用户批准", darwinExtensionBundleID)
		}
	}
	return nil
}

func readDarwinToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(data))
	if len(token) < 32 || len(token) > 512 {
		return "", errors.New("Network Extension IPC token 长度无效")
	}
	return token, nil
}

type darwinInterceptor struct {
	server     *Server
	listener   *net.UnixListener
	socketPath string
	token      string
	ctx        context.Context
	cancel     context.CancelFunc
	running    atomic.Bool
	ready      chan struct{}
	readyOnce  sync.Once
	closeOnce  sync.Once
	connMu     sync.Mutex
	conns      map[*net.UnixConn]struct{}
	wg         sync.WaitGroup
}

func startPlatformInterceptor(s *Server) (systemInterceptor, error) {
	if err := darwinPlatformReadiness(); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrPlatformNotReady, err)
	}
	if err := prepareLoopGuard(s); err != nil {
		return nil, err
	}
	socketPath, tokenPath, err := darwinIPCPaths()
	if err != nil {
		return nil, err
	}
	token, err := readDarwinToken(tokenPath)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(socketPath), 0700); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(socketPath); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("拒绝覆盖非 socket 路径 %s", socketPath)
		}
		if err := os.Remove(socketPath); err != nil {
			return nil, fmt.Errorf("清理旧 IPC socket 失败: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("监听 Network Extension IPC 失败: %w", err)
	}
	if err := os.Chmod(socketPath, 0600); err != nil {
		_ = listener.Close()
		_ = os.Remove(socketPath)
		return nil, err
	}
	ctx, cancel := context.WithCancel(s.ctx)
	i := &darwinInterceptor{
		server: s, listener: listener, socketPath: socketPath, token: token,
		ctx: ctx, cancel: cancel, ready: make(chan struct{}), conns: make(map[*net.UnixConn]struct{}),
	}
	i.running.Store(true)
	i.wg.Add(1)
	go i.accept()

	// The provider opens an authenticated control connection from startProxy.
	// Waiting here prevents a configured-but-disabled extension from being
	// advertised as an active transparent proxy.
	select {
	case <-i.ready:
		return i, nil
	case <-time.After(10 * time.Second):
		i.Close()
		return nil, fmt.Errorf("%w: Network Extension 未连接 Agent IPC", ErrPlatformNotReady)
	case <-s.ctx.Done():
		i.Close()
		return nil, s.ctx.Err()
	}
}

func (i *darwinInterceptor) Running() bool { return i != nil && i.running.Load() }
func (i *darwinInterceptor) ListenAddr() string {
	if i == nil {
		return ""
	}
	return i.socketPath
}

func (i *darwinInterceptor) accept() {
	defer i.wg.Done()
	for {
		conn, err := i.listener.AcceptUnix()
		if err != nil {
			if i.ctx.Err() == nil {
				log.Printf("[divert] macOS IPC 已停止: %v", err)
			}
			return
		}
		if !i.trackConnection(conn) {
			_ = conn.Close()
			return
		}
		i.wg.Add(1)
		go func() {
			defer i.wg.Done()
			defer i.untrackConnection(conn)
			if err := i.handle(conn); err != nil && i.ctx.Err() == nil && !errors.Is(err, io.EOF) {
				log.Printf("[divert] macOS flow IPC: %v", err)
			}
			_ = conn.Close()
		}()
	}
}

func (i *darwinInterceptor) trackConnection(conn *net.UnixConn) bool {
	i.connMu.Lock()
	defer i.connMu.Unlock()
	if i.ctx.Err() != nil {
		return false
	}
	i.conns[conn] = struct{}{}
	return true
}

func (i *darwinInterceptor) untrackConnection(conn *net.UnixConn) {
	i.connMu.Lock()
	delete(i.conns, conn)
	i.connMu.Unlock()
}

func (i *darwinInterceptor) closeConnections() {
	i.connMu.Lock()
	connections := make([]*net.UnixConn, 0, len(i.conns))
	for conn := range i.conns {
		connections = append(connections, conn)
	}
	i.connMu.Unlock()
	for _, conn := range connections {
		_ = conn.Close()
	}
}

type darwinHello struct {
	Version int    `json:"version"`
	Token   string `json:"token"`
	Role    string `json:"role"`
}

func (i *darwinInterceptor) handle(conn *net.UnixConn) error {
	if err := verifyDarwinPeer(conn); err != nil {
		return err
	}
	kind, payload, err := readDarwinFrame(conn)
	if err != nil {
		return err
	}
	var hello darwinHello
	if kind != darwinFrameHello || json.Unmarshal(payload, &hello) != nil || hello.Version != darwinIPCVersion || !darwinTokenEqual(hello.Token, i.token) {
		return errors.New("拒绝未经认证的 Network Extension IPC")
	}
	if err := writeDarwinJSON(conn, darwinFrameHello, map[string]any{"version": darwinIPCVersion, "ok": true}); err != nil {
		return err
	}
	if hello.Role == "control" {
		i.readyOnce.Do(func() { close(i.ready) })
		// Keep one authenticated control stream open. It is both a cheap
		// readiness signal and immediate disconnect detection, avoiding a new
		// Unix connection and JSON handshake every second.
		var unexpected [1]byte
		if _, err := conn.Read(unexpected[:]); err != nil {
			return err
		}
		return errors.New("macOS IPC control stream carried unexpected data")
	}
	if hello.Role != "flow" {
		return errors.New("macOS IPC role 无效")
	}
	kind, payload, err = readDarwinFrame(conn)
	if err != nil {
		return err
	}
	if kind != darwinFrameOpen {
		return errors.New("macOS IPC 缺少 flow open frame")
	}
	var open darwinOpenFlow
	if err := json.Unmarshal(payload, &open); err != nil || open.Version != darwinIPCVersion {
		return errors.New("macOS IPC flow metadata 无效")
	}
	switch open.Protocol {
	case ProtoTCP:
		return i.handleTCP(conn, open)
	case ProtoUDP:
		return i.handleUDP(conn, open)
	default:
		return errors.New("macOS IPC protocol 无效")
	}
}

func verifyDarwinPeer(conn *net.UnixConn) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var credential *unix.Xucred
	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		credential, socketErr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	}); err != nil {
		return err
	}
	if socketErr != nil {
		return socketErr
	}
	if credential == nil || (credential.Uid != uint32(os.Geteuid()) && credential.Uid != 0) {
		return fmt.Errorf("拒绝 UID %d 的 Network Extension IPC", credential.Uid)
	}
	return nil
}

func darwinTokenEqual(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func (i *darwinInterceptor) handleTCP(conn *net.UnixConn, open darwinOpenFlow) error {
	destination, err := open.destination()
	if err != nil {
		return err
	}
	route, err := i.server.ClassifyFlow(open.flow(destination))
	if err != nil {
		_ = writeDarwinJSON(conn, darwinFrameError, map[string]string{"message": err.Error()})
		return err
	}
	decision := route.Decision()
	if err := writeDarwinJSON(conn, darwinFrameDecision, darwinFlowDecision{Action: decision.Action, ExitID: decision.ExitID, Reason: decision.Rule}); err != nil {
		return err
	}
	if decision.Action != ActionProxy {
		return nil
	}
	return i.server.ForwardTCP(i.ctx, route, conn)
}

func (i *darwinInterceptor) handleUDP(conn *net.UnixConn, open darwinOpenFlow) error {
	var writeMu sync.Mutex
	respond := func(ctx context.Context, key FlowKey, payload []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return writeDarwinDatagramFrame(conn, darwinFrameUDPReply, key.Destination, payload)
	}
	for {
		kind, payload, err := readDarwinFrame(conn)
		if err != nil {
			return err
		}
		if kind != darwinFrameUDPDatagram {
			return errors.New("macOS IPC UDP frame 类型无效")
		}
		destination, datagram, err := decodeDarwinDatagram(payload)
		if err != nil {
			return err
		}
		route, err := i.server.ClassifyFlow(open.flow(destination))
		if err != nil {
			return err
		}
		switch route.Decision().Action {
		case ActionProxy:
			if err := i.server.ForwardUDP(i.ctx, route, datagram, respond); err != nil {
				return err
			}
		case ActionDirect:
			writeMu.Lock()
			err = writeDarwinFrame(conn, darwinFrameUDPDirect, payload)
			writeMu.Unlock()
			if err != nil {
				return err
			}
		case ActionReject:
			writeMu.Lock()
			err = writeDarwinDatagramFrame(conn, darwinFrameUDPReject, destination, nil)
			writeMu.Unlock()
			if err != nil {
				return err
			}
		default:
			return errors.New("macOS IPC decision 无效")
		}
	}
}

func (i *darwinInterceptor) Close() {
	if i == nil {
		return
	}
	i.closeOnce.Do(func() {
		i.running.Store(false)
		i.cancel()
		_ = i.listener.Close()
		i.closeConnections()
		i.wg.Wait()
		if info, err := os.Lstat(i.socketPath); err == nil && info.Mode()&os.ModeSocket != 0 {
			_ = os.Remove(i.socketPath)
		}
	})
}
