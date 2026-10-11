package browsersync

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"relayproxy/internal/acl"
	"relayproxy/internal/protocol"
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
	Advertise   string
	allowExits  map[string]bool
	relayPolicy *acl.Policy
	mu          sync.Mutex
	leases      map[[32]byte]networkLease
	server      *http.Server
	listener    net.Listener
	slots       chan struct{}
}

func NewNetworkProxy(store *Store, sessions *session.Manager, advertise string, exitIDs []string) *NetworkProxy {
	allowed := make(map[string]bool)
	for _, id := range exitIDs {
		allowed[id] = true
	}
	return &NetworkProxy{Store: store, Sessions: sessions, Advertise: advertise, allowExits: allowed,
		leases: make(map[[32]byte]networkLease), slots: make(chan struct{}, 128)}
}

// Bind the same Relay target ACL as ordinary proxy streams. Agent Exit
// intersects this with its own host/IP policy before making a DNS connection.
func (p *NetworkProxy) SetRelayPolicy(policy acl.Policy) { p.relayPolicy = &policy }

func (p *NetworkProxy) AllowedExit(browserIdentity, exitID string) bool {
	if !p.allowExits[exitID] || browserIdentity == "" {
		return false
	}
	exit, ok := p.Sessions.Get(exitID)
	return ok && exit != nil && exit.IsExit() && exit.IdentityID == browserIdentity &&
		exit.Tunnel != nil && browserExitOptedIn(exit)
}
// Only the Agent's signed, per-connection transport declaration proves its
// local permission to share egress. A Server allowlist alone is insufficient.
func browserExitOptedIn(exit *session.DeviceSession) bool {
	if exit == nil {
		return false
	}
	for _, capability := range exit.Capabilities {
		if capability == protocol.CapabilityBrowserNetworkExit {
			return true
		}
	}
	return false
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
	host, port, err := net.SplitHostPort(p.Advertise)
	if err != nil || host == "" || port == "" {
		return nil, ErrRuleDenied
	}
	random := make([]byte, 32)
	if _, err = rand.Read(random); err != nil {
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
	n, _ := strconv.Atoi(port)
	return map[string]any{"type": "NETWORK_TOKEN", "host": host, "port": n,
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
	if r.TLS == nil || r.Method != http.MethodConnect || r.ProtoMajor != 1 {
		http.Error(w, "HTTPS CONNECT only", http.StatusMethodNotAllowed)
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
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil || port != "443" || !validNetworkHost(host) || r.URL == nil || r.URL.Host != "" && r.URL.Host != r.Host {
		http.Error(w, "site is not allowed", http.StatusForbidden)
		return
	}
	exit, err := p.authorizedExit(r.Context(), lease.deviceID, host)
	if err != nil {
		http.Error(w, "network binding denied or exit offline", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	stream, err := exit.Tunnel.OpenStream(ctx)
	if err != nil {
		http.Error(w, "exit unavailable", http.StatusBadGateway)
		return
	}
	defer stream.Close()
	reqID := uuid.NewString()
	_ = stream.SetDeadline(time.Now().Add(12 * time.Second))
	header := &protocol.StreamHeader{Type: protocol.FrameTypeBrowserNetworkTCP, RequestID: reqID,
		ClientDeviceID: lease.deviceID, ExitDeviceID: exit.DeviceID}
	if err = protocol.WriteStreamHeader(stream, header); err == nil {
		err = protocol.WriteJSON(stream, protocol.OpenTCPRequest{
			RequestID: reqID, Host: host, Port: 443, TimeoutMs: 10000, RelayPolicy: p.relayPolicy})
	}
	if err != nil {
		http.Error(w, "exit handshake failed", http.StatusBadGateway)
		return
	}
	var reply protocol.OpenTCPResponse
	if err = protocol.ReadJSON(stream, &reply); err != nil || !reply.Success || reply.RequestID != reqID {
		http.Error(w, "exit refused connection", http.StatusBadGateway)
		return
	}
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
	// Authorization is not a one-time handshake: revoking either browser
	// device, its rule, the Exit grant, or the local opt-in must terminate
	// established CONNECT streams too. Fail closed on DB errors.
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
			case <-r.Context().Done():
			case <-exit.Tunnel.Done():
			case <-ticker.C:
				current, checkErr := p.authorizedExit(r.Context(), lease.deviceID, host)
				if checkErr == nil && current == exit && time.Now().Before(lease.expires) {
					continue
				}
			}
			_ = client.Close()
			_ = stream.Close()
			return
		}
	}()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(stream, buffer); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, stream); done <- struct{}{} }()
	<-done
	_ = client.Close()
	_ = stream.Close()
	<-done
	close(stopWatch)
	<-watchStopped
}
func (p *NetworkProxy) Start(listen string, tlsConfig *tls.Config) error {
	if tlsConfig == nil || len(tlsConfig.Certificates) == 0 {
		return errors.New("network proxy requires a TLS certificate")
	}
	cfg := tlsConfig.Clone()
	cfg.MinVersion = tls.VersionTLS13
	cfg.NextProtos = []string{"http/1.1"}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	p.listener = ln
	p.server = &http.Server{Handler: p, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second,
		MaxHeaderBytes: 8192}
	go func() { _ = p.server.Serve(tls.NewListener(ln, cfg)) }()
	return nil
}
func (p *NetworkProxy) Close(ctx context.Context) error {
	p.mu.Lock()
	p.leases = make(map[[32]byte]networkLease)
	p.mu.Unlock()
	if p.server == nil {
		return nil
	}
	return p.server.Shutdown(ctx)
}
func (p *NetworkProxy) Address() string {
	if p.listener == nil {
		return ""
	}
	return fmt.Sprint(p.listener.Addr())
}
