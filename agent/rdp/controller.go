package rdp

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os/exec"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"relayproxy/internal/tunnel"
)

type Target struct {
	DeviceID string `json:"deviceId"`
	Name     string `json:"name"`
	Online   bool   `json:"online"`
}

type ControllerOptions struct {
	DialTCP     func(context.Context, string) (net.Conn, error)
	DialUDP     func(context.Context, string) (net.PacketConn, error)
	SupportsUDP func() bool
	OnClose     func()
}

type Connection struct {
	Target      Target
	ListenAddr  string `json:"listenAddr"`
	UDPEnabled  bool   `json:"udpEnabled"`
	TCPListener net.Listener
	UDPConn     *net.UDPConn
	remoteUDP   net.PacketConn
	cancel      context.CancelFunc
	done        chan struct{}
	closeOnce   sync.Once
	mu          sync.Mutex
	localUDP    netip.AddrPort
	udpActive   atomic.Bool
	udpError    atomic.Value // string; the latest UDP association error
	onClose     func()
}

func (c *Connection) Addr() string { return c.ListenAddr }

// UDPStatus reports the local UDP listener capability and the state of the
// most recent remote association. UDPEnabled only means that the same-number
// loopback UDP socket was created; UDPActive means mstsc has sent a packet and
// the packet path was opened successfully.
func (c *Connection) UDPStatus() (enabled, active bool, lastError string) {
	if c == nil {
		return false, false, ""
	}
	enabled = c.UDPEnabled
	active = c.udpActive.Load()
	if value := c.udpError.Load(); value != nil {
		lastError, _ = value.(string)
	}
	return enabled, active, lastError
}

func (c *Connection) setUDPError(err error) {
	if err == nil {
		c.udpError.Store("")
		return
	}
	c.udpError.Store(err.Error())
}

func (c *Connection) Close() error {
	c.closeOnce.Do(func() {
		c.cancel()
		if c.TCPListener != nil {
			_ = c.TCPListener.Close()
		}
		if c.UDPConn != nil {
			_ = c.UDPConn.Close()
		}
		c.mu.Lock()
		if c.remoteUDP != nil {
			_ = c.remoteUDP.Close()
			c.remoteUDP = nil
		}
		c.mu.Unlock()
		if c.onClose != nil {
			c.onClose()
		}
		close(c.done)
	})
	return nil
}

// Start opens one loopback port for RDP TCP and, when the current transport
// supports native datagrams, the same port for RDP UDP.
func StartController(parent context.Context, target Target, options ControllerOptions, autoLaunch bool) (*Connection, error) {
	if target.DeviceID == "" {
		return nil, errors.New("RDP target device id is required")
	}
	if options.DialTCP == nil {
		return nil, errors.New("RDP TCP dialer is required")
	}
	ctx, cancel := context.WithCancel(parent)
	tcpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		cancel()
		return nil, fmt.Errorf("listen RDP controller TCP: %w", err)
	}
	port := tcpListener.Addr().(*net.TCPAddr).Port
	udpEnabled := options.DialUDP != nil && (options.SupportsUDP == nil || options.SupportsUDP())
	var udpConn *net.UDPConn
	if udpEnabled {
		udpConn, err = net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port})
		if err != nil {
			_ = tcpListener.Close()
			cancel()
			return nil, fmt.Errorf("listen RDP controller UDP: %w", err)
		}
		tunnel.TuneUDPConn(udpConn)
	}
	connection := &Connection{Target: target, ListenAddr: fmt.Sprintf("127.0.0.1:%d", port), UDPEnabled: udpEnabled, TCPListener: tcpListener, UDPConn: udpConn, cancel: cancel, done: make(chan struct{}), onClose: options.OnClose}
	go connection.serveTCP(ctx, options.DialTCP)
	if udpEnabled {
		go connection.serveUDP(ctx, options.DialUDP)
	}
	if autoLaunch && runtime.GOOS == "windows" {
		go func(ctx context.Context, address string) {
			timer := time.NewTimer(350 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			if err := exec.Command("mstsc.exe", "/v:"+address).Start(); err != nil {
				log.Printf("[RDP] 启动 mstsc.exe 失败: %v", err)
			}
		}(ctx, connection.ListenAddr)
	}
	return connection, nil
}

func (c *Connection) serveTCP(ctx context.Context, dial func(context.Context, string) (net.Conn, error)) {
	for {
		conn, err := c.TCPListener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				log.Printf("[RDP] 本地 TCP 接受失败: %v", err)
				return
			}
		}
		tunnel.TuneTCPConn(conn)
		go c.handleTCP(ctx, conn, dial)
	}
}

func (c *Connection) handleTCP(ctx context.Context, local net.Conn, dial func(context.Context, string) (net.Conn, error)) {
	defer local.Close()
	openCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	remote, err := dial(openCtx, c.Target.DeviceID)
	if err != nil {
		log.Printf("[RDP] 打开目标 %s TCP 失败: %v", c.Target.DeviceID, err)
		return
	}
	defer remote.Close()
	_, _ = tunnel.Pipe(ctx, local, remote, 10*time.Minute, nil)
}

func (c *Connection) serveUDP(ctx context.Context, dial func(context.Context, string) (net.PacketConn, error)) {
	buffer := make([]byte, 64*1024)
	for {
		n, addr, err := c.UDPConn.ReadFromUDPAddrPort(buffer)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				return
			}
		}
		addr = netip.AddrPortFrom(addr.Addr().Unmap(), addr.Port())
		c.mu.Lock()
		c.localUDP = addr
		c.mu.Unlock()
		// Retry this same datagram once after replacing a stale association.
		// UDP socket writes can be the first operation to observe a closed QUIC
		// association; dropping that first RDP packet makes mstsc wait for its own
		// much slower retry timer and looks like a periodic desktop freeze.
		for attempt := 0; attempt < 2; attempt++ {
			remote, openErr := c.remoteUDPConn(ctx, dial)
			if openErr != nil {
				c.udpActive.Store(false)
				c.setUDPError(openErr)
				log.Printf("[RDP] 打开目标 %s UDP 失败: %v", c.Target.DeviceID, openErr)
				break
			}
			if _, writeErr := remote.WriteTo(buffer[:n], nil); writeErr == nil {
				break
			} else {
				c.discardRemoteUDP(remote, writeErr, ctx.Err() == nil)
			}
		}
	}
}

func (c *Connection) remoteUDPConn(ctx context.Context, dial func(context.Context, string) (net.PacketConn, error)) (net.PacketConn, error) {
	c.mu.Lock()
	remote := c.remoteUDP
	c.mu.Unlock()
	if remote != nil {
		return remote, nil
	}
	openCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	opened, err := dial(openCtx, c.Target.DeviceID)
	cancel()
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		_ = opened.Close()
		return nil, err
	}
	if c.remoteUDP == nil {
		c.remoteUDP = opened
		remote = opened
	} else {
		remote = c.remoteUDP
	}
	c.mu.Unlock()
	if remote != opened {
		_ = opened.Close()
		return remote, nil
	}
	c.setUDPError(nil)
	c.udpActive.Store(true)
	go c.readRemoteUDP(ctx, opened)
	return opened, nil
}

func (c *Connection) discardRemoteUDP(remote net.PacketConn, err error, report bool) {
	if remote == nil {
		return
	}
	c.mu.Lock()
	discarded := c.remoteUDP == remote
	if discarded {
		c.remoteUDP = nil
	}
	c.mu.Unlock()
	if !discarded {
		return
	}
	c.udpActive.Store(false)
	if report && err != nil {
		c.setUDPError(err)
	}
	_ = remote.Close()
}

func (c *Connection) readRemoteUDP(ctx context.Context, remote net.PacketConn) {
	buffer := make([]byte, 64*1024)
	for {
		n, _, err := remote.ReadFrom(buffer)
		if err != nil {
			c.discardRemoteUDP(remote, err, ctx.Err() == nil)
			return
		}
		c.mu.Lock()
		addr := c.localUDP
		c.mu.Unlock()
		if !addr.IsValid() {
			continue
		}
		if _, err := c.UDPConn.WriteToUDPAddrPort(buffer[:n], addr); err != nil {
			c.discardRemoteUDP(remote, err, ctx.Err() == nil)
			return
		}
		select {
		case <-ctx.Done():
			return
		default:
		}
	}
}
