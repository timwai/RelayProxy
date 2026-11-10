package browsersync

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"relayproxy/internal/acl"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
	"relayproxy/server/session"
)

const networkLeaseTTL = 15 * time.Minute

type networkLease struct {
	deviceID string
	expires  time.Time
}
type NetworkProxy struct {
	Store       *Store
	Sessions    *session.Manager
	relayPolicy *acl.Policy
	mu          sync.Mutex
	leases      map[[32]byte]networkLease
	slots       chan struct{}
	shutdown    chan struct{}
	closeOnce   sync.Once
}

func NewNetworkProxy(store *Store, sessions *session.Manager) *NetworkProxy {
	return &NetworkProxy{Store: store, Sessions: sessions,
		leases: make(map[[32]byte]networkLease),
		slots:  make(chan struct{}, 128), shutdown: make(chan struct{})}
}

// Bind the same Relay target ACL as ordinary proxy streams. Agent Exit
// intersects this with its own host/IP policy before making a DNS connection.
func (p *NetworkProxy) SetRelayPolicy(policy acl.Policy) { p.relayPolicy = &policy }

func (p *NetworkProxy) AllowedExit(browserIdentity, exitID string) bool {
	if browserIdentity == "" {
		return false
	}
	exit, ok := p.Sessions.Get(exitID)
	// Browser network binding is explicitly approved at BOTH ends of the
	// pairing. Any online Agent with the identity's approved proxy.exit grant
	// is eligible, including remote exits without a local browser-share flag.
	// Keep the identity, relay tunnel and proxy.exit checks on every CONNECT
	// and on the established stream watchdog (not only when listing exits).
	return ok && exit != nil && exit.IsExit() && exit.IdentityID == browserIdentity &&
		exit.Tunnel != nil
}

// A locally opted-in Agent understands the dedicated Browser Network frame.
// Other identity-authorized proxy.exit Agents already handle ordinary TCP
// egress with the SAME Agent ACL and Relay target policy. The Server has
// authenticated the source browser and the target-approved binding before
// sending either frame, never offering unapproved browser traffic.
func browserExitStreamType(exit *session.DeviceSession) protocol.FrameType {
	if exit != nil {
		for _, capability := range exit.Capabilities {
			if capability == protocol.CapabilityBrowserNetworkExit {
				return protocol.FrameTypeBrowserNetworkTCP
			}
		}
	}
	return protocol.FrameTypeOpenTCP
}

func (p *NetworkProxy) AvailableExits(ctx context.Context, browserID string) []map[string]string {
	browser, err := p.Store.deviceAllowed(ctx, browserID)
	if err != nil {
		return nil
	}
	result := []map[string]string{}
	for _, agent := range p.Sessions.GetExits() {
		if agent != nil && p.AllowedExit(browser.IdentityID, agent.DeviceID) {
			result = append(result, map[string]string{"id": agent.DeviceID, "name": agent.DeviceName})
		}
	}
	return result
}
func (p *NetworkProxy) Issue(ctx context.Context, deviceID string) (map[string]any, error) {
	if !p.Store.HasActiveNetworkBinding(ctx, deviceID) {
		return nil, ErrRuleDenied
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(random)
	digest := sha256.Sum256([]byte(token))
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	for key, value := range p.leases {
		if !now.Before(value.expires) {
			delete(p.leases, key)
		}
	}
	if len(p.leases) >= 4096 {
		return nil, ErrRuleDenied
	}
	expires := now.Add(networkLeaseTTL)
	p.leases[digest] = networkLease{deviceID: deviceID, expires: expires}
	return map[string]any{"type": "NETWORK_TOKEN",
		"token": token, "expiresAt": expires.UnixMilli()}, nil
}
func (p *NetworkProxy) checkToken(header string) (networkLease, error) {
	if !strings.HasPrefix(header, "Basic ") {
		return networkLease{}, ErrRuleDenied
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(header[6:]))
	if err != nil || len(raw) > 180 {
		return networkLease{}, ErrRuleDenied
	}
	username, token, ok := strings.Cut(string(raw), ":")
	if !ok || username != "browser" || len(token) > 100 {
		return networkLease{}, ErrRuleDenied
	}
	digest := sha256.Sum256([]byte(token))
	p.mu.Lock()
	item, found := p.leases[digest]
	p.mu.Unlock()
	if !found || !time.Now().Before(item.expires) || subtle.ConstantTimeCompare([]byte(username), []byte("browser")) != 1 {
		return networkLease{}, ErrRuleDenied
	}
	return item, nil
}
func (p *NetworkProxy) authorizedExit(ctx context.Context, deviceID, host string) (*session.DeviceSession, error) {
	receiver, err := p.Store.deviceAllowed(ctx, deviceID)
	if err != nil || !receiver.Receive {
		return nil, ErrRuleDenied
	}
	exitID, err := p.Store.NetworkDestination(ctx, deviceID, host)
	if err != nil || !p.AllowedExit(receiver.IdentityID, exitID) {
		return nil, ErrRuleDenied
	}
	exit, ok := p.Sessions.Get(exitID)
	if !ok || exit == nil {
		return nil, ErrRuleDenied
	}
	return exit, nil
}
func (p *NetworkProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	forwardHTTP := r.Method != http.MethodConnect && r.URL != nil && r.URL.IsAbs() && r.URL.Scheme == "http"
	if r.TLS == nil || (!forwardHTTP && r.Method != http.MethodConnect) || r.ProtoMajor != 1 {
		http.Error(w, "authenticated HTTPS proxy required", http.StatusMethodNotAllowed)
		return
	}
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	default:
		http.Error(w, "busy", http.StatusServiceUnavailable)
		return
	}
	lease, err := p.checkToken(r.Header.Get("Proxy-Authorization"))
	if err != nil {
		w.Header().Set("Proxy-Authenticate", `Basic realm="RelayProxy Browser Sync"`)
		http.Error(w, "proxy authentication required", http.StatusProxyAuthRequired)
		return
	}
	authority := r.Host
	if forwardHTTP && r.URL.Port() == "" {
		authority = net.JoinHostPort(r.URL.Hostname(), "80")
	}
	host, rawPort, err := net.SplitHostPort(authority)
	if err != nil || r.URL == nil || r.URL.User != nil || r.URL.Fragment != "" ||
		(r.URL.Host != "" && !strings.EqualFold(r.URL.Host, r.Host)) {
		http.Error(w, "site is not allowed", http.StatusForbidden)
		return
	}
	target, err := normalizeNetworkTarget(net.JoinHostPort(strings.ToLower(host), rawPort))
	if err != nil {
		http.Error(w, "site is not allowed", http.StatusForbidden)
		return
	}
	exit, err := p.authorizedExit(r.Context(), lease.deviceID, target)
	if err != nil {
		http.Error(w, "network binding denied or exit offline", http.StatusForbidden)
		return
	}
	if forwardHTTP {
		p.serveForwardHTTP(w, r, lease, target, exit)
		return
	}
	stream, err := p.openNetworkStream(r.Context(), lease, target, exit)
	if err != nil {
		http.Error(w, "exit unavailable", http.StatusBadGateway)
		return
	}
	defer stream.Close()
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "CONNECT unsupported", http.StatusInternalServerError)
		return
	}
	client, buffer, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer client.Close()
	// Don't honor extra data buffered after CONNECT headers unless forwarding it.
	// A lost TLS ClientHello otherwise causes a misleading browser hang.
	_ = buffer.Flush()
	if _, err = io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	deadline := lease.expires
	_ = client.SetDeadline(deadline)
	_ = stream.SetDeadline(deadline)
	stopWatch := p.watchNetworkStream(r.Context(), lease, target, exit, func() {
		_ = client.Close()
		_ = stream.Close()
	})
	defer stopWatch()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(stream, buffer); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, stream); done <- struct{}{} }()
	<-done
	_ = client.Close()
	_ = stream.Close()
	<-done
}

func (p *NetworkProxy) openNetworkStream(ctx context.Context, lease networkLease, target string, exit *session.DeviceSession) (tunnel.TunnelStream, error) {
	host, port, err := parseNetworkTarget(target)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	stream, err := exit.Tunnel.OpenStream(ctx)
	if err != nil {
		return nil, err
	}
	_ = stream.SetDeadline(time.Now().Add(12 * time.Second))
	reqID := uuid.NewString()
	header := &protocol.StreamHeader{Type: browserExitStreamType(exit), RequestID: reqID,
		ClientDeviceID: lease.deviceID, ExitDeviceID: exit.DeviceID}
	if err = protocol.WriteStreamHeader(stream, header); err == nil {
		err = protocol.WriteJSON(stream, protocol.OpenTCPRequest{
			RequestID: reqID, Host: host, Port: port, TimeoutMs: 10000, RelayPolicy: p.relayPolicy})
	}
	var reply protocol.OpenTCPResponse
	if err == nil {
		err = protocol.ReadJSON(stream, &reply)
	}
	if err != nil || !reply.Success || reply.RequestID != reqID {
		_ = stream.Close()
		return nil, errors.New("exit handshake failed")
	}
	_ = stream.SetDeadline(lease.expires)
	return stream, nil
}

// Both CONNECT and HTTP streams close promptly when a binding is revoked.
func (p *NetworkProxy) watchNetworkStream(ctx context.Context, lease networkLease, target string, exit *session.DeviceSession, closeStream func()) func() {
	stopWatch := make(chan struct{})
	watchStopped := make(chan struct{})
	go func() {
		defer close(watchStopped)
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopWatch:
				return
			case <-p.shutdown:
			case <-ctx.Done():
			case <-exit.Tunnel.Done():
			case <-ticker.C:
				current, checkErr := p.authorizedExit(ctx, lease.deviceID, target)
				if checkErr == nil && current == exit && time.Now().Before(lease.expires) {
					continue
				}
			}
			closeStream()
			return
		}
	}()
	return func() { close(stopWatch); <-watchStopped }
}

type networkHTTPConn struct {
	net.Conn
	stop func()
	once sync.Once
}

func (c *networkHTTPConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.stop)
	return err
}

func (p *NetworkProxy) serveForwardHTTP(w http.ResponseWriter, r *http.Request, lease networkLease, target string, exit *session.DeviceSession) {
	transport := &http.Transport{
		DisableCompression: true, DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			// The transport must only dial the host/port authorized above.
			requested, err := normalizeNetworkTarget(strings.ToLower(address))
			if err != nil || requested != target {
				return nil, ErrRuleDenied
			}
			stream, err := p.openNetworkStream(ctx, lease, target, exit)
			if err != nil {
				return nil, err
			}
			stop := p.watchNetworkStream(r.Context(), lease, target, exit, func() { _ = stream.Close() })
			return &networkHTTPConn{Conn: tunnel.NewNetConnAdapter(stream, nil, nil), stop: stop}, nil
		},
	}
	defer transport.CloseIdleConnections()
	proxy := &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.Header.Del("Proxy-Authorization")
			pr.Out.Header.Del("Proxy-Connection")
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, "exit unavailable", http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, r)
}
func (p *NetworkProxy) Close(ctx context.Context) error {
	// net/http hijacked CONNECT streams are owned by the public Relay
	// gateway; signal active watchers and invalidate all leases on shutdown.
	p.closeOnce.Do(func() { close(p.shutdown) })
	p.mu.Lock()
	p.leases = make(map[[32]byte]networkLease)
	p.mu.Unlock()
	return nil
}
