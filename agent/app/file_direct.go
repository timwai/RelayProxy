package app

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/netip"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"relayproxy/internal/p2p/candidate"
	"relayproxy/internal/p2p/upnp"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

const fileDirectALPN = "relayproxy-file-direct-v1"

type fileDirectTicket struct {
	deviceID  string
	expiresAt int64
}
type fileDirectRuntime struct {
	a           *Agent
	listener    net.Listener
	fingerprint string
	mu          sync.Mutex
	tickets     map[string]fileDirectTicket
	upnpCancel  context.CancelFunc
	upnpRunning bool
	upnpAddress netip.AddrPort
	closed      bool
	accepted    atomic.Int64
	wg          sync.WaitGroup
}

type fileDirectStream struct{ *tls.Conn }

func (s *fileDirectStream) CloseWrite() error { return s.Conn.CloseWrite() }

func makeFileDirectTLS() (*tls.Config, string, error) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", err
	}
	now := time.Now()
	certTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(now.UnixNano()),
		Subject:      pkix.Name{CommonName: "RelayProxy File Direct"},
		NotBefore:    now.Add(-time.Minute), NotAfter: now.Add(24 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:    []string{"relayproxy-file-direct"},
	}
	der, err := x509.CreateCertificate(rand.Reader, certTemplate, certTemplate, private.Public(), private)
	if err != nil {
		return nil, "", err
	}
	fingerprint := sha256.Sum256(der)
	return &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: private}},
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{fileDirectALPN},
	}, hex.EncodeToString(fingerprint[:]), nil
}

func (a *Agent) getFileDirectRuntime() (*fileDirectRuntime, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed.Load() {
		return nil, errors.New("agent is closed")
	}
	if !slices.Contains(a.approvedCapabilities, protocol.CapabilityFilesHost) || a.fileShareStore == nil {
		return nil, errors.New("file host is not approved")
	}
	if a.fileDirect != nil {
		return a.fileDirect, nil
	}
	cfg, fingerprint, err := makeFileDirectTLS()
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		return nil, err
	}
	rt := &fileDirectRuntime{a: a, listener: tls.NewListener(listener, cfg), fingerprint: fingerprint, tickets: map[string]fileDirectTicket{}}
	a.fileDirect = rt
	rt.wg.Add(1)
	go rt.accept()
	return rt, nil
}

func (r *fileDirectRuntime) close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	r.tickets = map[string]fileDirectTicket{}
	if r.upnpCancel != nil {
		r.upnpCancel()
	}
	_ = r.listener.Close()
	r.mu.Unlock()
	r.wg.Wait()
}

// syncUPnP never opens a router port without an explicit local option. A
// failed discovery or private CGNAT WAN leaves the existing Relay/LAN paths
// unchanged. Disabling the option withdraws an advertised address immediately.
func (r *fileDirectRuntime) syncUPnP() {
	r.a.mu.RLock()
	store := r.a.fileShareStore
	r.a.mu.RUnlock()
	enabled := false
	if store != nil {
		if state, err := store.Load(); err == nil {
			enabled = state.Enabled && state.DirectUPnPEnabled
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	if !enabled {
		r.upnpAddress = netip.AddrPort{}
		if r.upnpCancel != nil {
			r.upnpCancel()
		}
		return
	}
	if r.upnpRunning {
		return
	}
	parent := r.a.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	r.upnpCancel = cancel
	r.upnpRunning = true
	r.wg.Add(1)
	go r.runUPnP(ctx)
}

func (r *fileDirectRuntime) runUPnP(ctx context.Context) {
	defer r.wg.Done()
	defer func() {
		r.mu.Lock()
		r.upnpRunning = false
		r.upnpCancel = nil
		r.upnpAddress = netip.AddrPort{}
		r.mu.Unlock()
	}()
	port := r.listener.Addr().(*net.TCPAddr).Port
	discoveryCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	mapping, address, err := upnp.MapTCP(discoveryCtx, port)
	cancel()
	if err != nil {
		return
	}
	defer mapping.Close()
	// UPnP can succeed on a private WAN or CGNAT. Publishing such a
	// candidate would both leak local network topology and waste retries.
	if !upnp.IsPublicWANIPv4(address.Addr()) {
		return
	}
	r.mu.Lock()
	if !r.closed && ctx.Err() == nil {
		r.upnpAddress = address
	}
	r.mu.Unlock()
	for {
		select {
		case <-ctx.Done():
			return
		case state := <-mapping.Updates():
			r.mu.Lock()
			if !r.closed && ctx.Err() == nil && state.Healthy &&
				upnp.IsPublicWANIPv4(state.Address.Addr()) {
				r.upnpAddress = state.Address
			} else {
				r.upnpAddress = netip.AddrPort{}
			}
			r.mu.Unlock()
		}
	}
}

func (r *fileDirectRuntime) offer(clientID string, grant protocol.FileDirectGrant) protocol.FileDirectOffer {
	result := protocol.FileDirectOffer{}
	if clientID == "" || len(grant.Token) != 64 ||
		grant.ExpiresAt <= time.Now().UnixMilli() ||
		grant.ExpiresAt > time.Now().Add(30*time.Second).UnixMilli() {
		result.Error = "invalid direct authorization ticket"
		return result
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		result.Error = "file direct listener is closed"
		return result
	}
	for id, item := range r.tickets {
		if item.expiresAt <= time.Now().UnixMilli() {
			delete(r.tickets, id)
		}
	}
	if len(r.tickets) >= 64 {
		result.Error = "too many direct tickets"
		return result
	}
	r.tickets[grant.Token] = fileDirectTicket{deviceID: clientID, expiresAt: grant.ExpiresAt}
	port := r.listener.Addr().(*net.TCPAddr).Port
	endpoints := []string{}
	if r.upnpAddress.IsValid() && upnp.IsPublicWANIPv4(r.upnpAddress.Addr()) {
		endpoints = append(endpoints, r.upnpAddress.String())
	}
	for _, c := range candidate.Discover(0, port) {
		if c.Protocol == "tcp" {
			endpoints = append(endpoints, c.Address)
		}
	}
	// Same-host integration and localhost device pairing use loopback as a
	// *hint* only; the TLS certificate fingerprint and random ticket still
	// prevent another local process from impersonating the peer.
	if len(endpoints) > 15 {
		endpoints = endpoints[:15]
	}
	endpoints = append(endpoints, net.JoinHostPort("127.0.0.1", fmt.Sprint(port)))
	result.Endpoints = endpoints
	result.Fingerprint = r.fingerprint
	return result
}

func (r *fileDirectRuntime) accept() {
	defer r.wg.Done()
	quota := make(chan struct{}, 24)
	for {
		conn, err := r.listener.Accept()
		if err != nil {
			return
		}
		select {
		case quota <- struct{}{}:
			r.wg.Add(1)
			go func() {
				defer r.wg.Done()
				defer func() { <-quota }()
				r.serveConnection(conn)
			}()
		default:
			_ = conn.Close()
		}
	}
}

func (r *fileDirectRuntime) consume(token string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || len(token) != 64 {
		return "", false
	}
	item, ok := r.tickets[token]
	delete(r.tickets, token)
	if !ok || item.expiresAt <= time.Now().UnixMilli() {
		return "", false
	}
	return item.deviceID, true
}

func (r *fileDirectRuntime) serveConnection(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		return
	}
	if err := tlsConn.Handshake(); err != nil {
		return
	}
	if tlsConn.ConnectionState().NegotiatedProtocol != fileDirectALPN {
		return
	}
	var auth protocol.FileDirectAuth
	if err := protocol.ReadJSON(conn, &auth); err != nil {
		return
	}
	clientID, ok := r.consume(auth.Token)
	if !ok {
		_ = protocol.WriteJSON(conn, protocol.FileDirectAuthResult{Error: "file direct token rejected"})
		return
	}
	ctx, cancel := context.WithCancel(r.a.ctx)
	defer cancel()
	authorized := r.a.checkFileDirectGrant(ctx, clientID)
	if !authorized {
		_ = protocol.WriteJSON(conn, protocol.FileDirectAuthResult{Error: "file direct approval revoked"})
		return
	}
	if err := protocol.WriteJSON(conn, protocol.FileDirectAuthResult{Allowed: true}); err != nil {
		return
	}
	r.accepted.Add(1)
	_ = conn.SetDeadline(time.Time{})
	// Explicit live Server rechecks are essential: the local directory ACL
	// alone cannot observe Server-side device capability revocation.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				_ = conn.Close()
				return
			case <-ticker.C:
				if !r.a.checkFileDirectGrant(ctx, clientID) {
					_ = conn.Close()
					return
				}
			}
		}
	}()
	r.a.mu.RLock()
	store := r.a.fileShareStore
	r.a.mu.RUnlock()
	if store == nil {
		return
	}
	store.Serve(ctx, &fileDirectStream{tlsConn}, clientID)
}

func (a *Agent) serveFileDirectGrant(ctx context.Context, stream tunnel.TunnelStream, clientID string) {
	defer stream.Close()
	var grant protocol.FileDirectGrant
	if err := protocol.ReadJSON(stream, &grant); err != nil {
		return
	}
	runtime, err := a.getFileDirectRuntime()
	if err != nil {
		_ = protocol.WriteJSON(stream, protocol.FileDirectOffer{Error: "file host unavailable"})
		return
	}
	runtime.syncUPnP()
	_ = protocol.WriteJSON(stream, runtime.offer(clientID, grant))
}

func (a *Agent) checkFileDirectGrant(ctx context.Context, clientID string) bool {
	a.mu.RLock()
	session := a.readySession
	ok := a.handshakeOK.Load() && slices.Contains(a.approvedCapabilities, protocol.CapabilityFilesHost)
	a.mu.RUnlock()
	if !ok || session == nil {
		return false
	}
	checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	stream, err := session.OpenStream(checkCtx)
	if err != nil {
		return false
	}
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(3 * time.Second))
	if err := protocol.WriteStreamHeader(stream, &protocol.StreamHeader{
		Type: protocol.FrameTypeFileDirectCheck, ExitDeviceID: clientID,
	}); err != nil {
		return false
	}
	var response protocol.FileDirectAuthResult
	if err := protocol.ReadJSON(stream, &response); err != nil {
		return false
	}
	return response.Allowed
}

func validateFileDirectFingerprint(cert *x509.Certificate, expected string) bool {
	if cert == nil || len(expected) != 64 {
		return false
	}
	sum := sha256.Sum256(cert.Raw)
	actual := hex.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) == 1
}
