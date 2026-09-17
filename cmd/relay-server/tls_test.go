package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"relayproxy/internal/config"
	"relayproxy/server/gateway"
	"relayproxy/server/session"
)

func TestConfiguredCertificateServedByAllListeners(t *testing.T) {
	certPath, keyPath, expectedLeaf, root := serverCertificateFixture(t)
	configPath := filepath.Join(t.TempDir(), "server.yaml")
	yaml := fmt.Sprintf("server:\n  cert_file: %q\n  key_file: %q\n", filepath.ToSlash(certPath), filepath.ToSlash(keyPath))
	if err := os.WriteFile(configPath, []byte(yaml), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadServerConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	serverTLS, err := loadServerTLS(cfg)
	if err != nil {
		t.Fatal(err)
	}
	gw := gateway.NewGateway(gateway.GatewayConfig{TCPAddr: "127.0.0.1:0", QUICAddr: "127.0.0.1:0", TLSConfig: serverTLS}, session.NewManager(), nil)
	if err := gw.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gw.Close() })
	roots := x509.NewCertPool()
	roots.AddCert(root)
	clientTLS := &tls.Config{ServerName: "relay.test", RootCAs: roots, MinVersion: tls.VersionTLS13}
	check := func(t *testing.T, state tls.ConnectionState) {
		t.Helper()
		if len(state.PeerCertificates) != 2 || !bytes.Equal(state.PeerCertificates[0].Raw, expectedLeaf) || len(state.VerifiedChains) == 0 {
			t.Fatal("listener did not serve the configured certificate and complete chain")
		}
	}
	t.Run("TCP", func(t *testing.T) {
		conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 3 * time.Second}, "tcp", gw.TCPAddr().String(), clientTLS.Clone())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		check(t, conn.ConnectionState())
	})
	t.Run("QUIC", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		clientQUIC := clientTLS.Clone()
		clientQUIC.NextProtos = []string{"relayproxy-quic"}
		conn, err := quic.DialAddr(ctx, gw.QUICAddr().String(), clientQUIC, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.CloseWithError(0, "test complete")
		check(t, conn.ConnectionState().TLS)
	})
	t.Run("admin HTTPS", func(t *testing.T) {
		admin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
		admin.TLS = serverTLS.Clone()
		admin.StartTLS()
		defer admin.Close()
		transport := &http.Transport{TLSClientConfig: clientTLS.Clone()}
		defer transport.CloseIdleConnections()
		client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
		response, err := client.Get(admin.URL)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusNoContent || response.TLS == nil {
			t.Fatal("admin HTTPS request failed")
		}
		check(t, *response.TLS)
	})
}

func TestServerTLSRejectsMissingConfiguredFiles(t *testing.T) {
	cfg := &config.ServerConfig{}
	cfg.Server.CertFile = filepath.Join(t.TempDir(), "missing.pem")
	cfg.Server.KeyFile = filepath.Join(t.TempDir(), "missing.key")
	if tlsConfig, err := loadServerTLS(cfg); err == nil || tlsConfig != nil {
		t.Fatalf("missing configured pair was ignored: %v", err)
	}
	// An explicit plaintext mode is the only reason not to load configured files.
	cfg.Server.TLSEnabled = config.BoolPtr(false)
	if tlsConfig, err := loadServerTLS(cfg); err != nil || tlsConfig != nil {
		t.Fatalf("plaintext mode unexpectedly loaded certificates: %v", err)
	}
}

func TestHTTPAdminAndTLSTunnelServeIndependently(t *testing.T) {
	certPath, keyPath, _, root := serverCertificateFixture(t)
	cfg := &config.ServerConfig{}
	cfg.Server.CertFile, cfg.Server.KeyFile = certPath, keyPath
	cfg.Server.Admin.TLSEnabled = config.BoolPtr(false)
	loaded, err := loadServerTLS(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if adminTLSConfig(cfg, loaded) != nil || tunnelTLSConfig(cfg, loaded) == nil {
		t.Fatal("admin HTTP disabled the tunnel certificate")
	}
	admin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil {
			t.Error("HTTP management unexpectedly uses TLS")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	admin.Config.TLSConfig = adminTLSConfig(cfg, loaded)
	admin.Start()
	defer admin.Close()
	response, err := admin.Client().Get(admin.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent || response.TLS != nil {
		t.Fatal("HTTP IP management request failed")
	}
	gw := gateway.NewGateway(gateway.GatewayConfig{TCPAddr: "127.0.0.1:0", QUICAddr: "127.0.0.1:0", TLSConfig: tunnelTLSConfig(cfg, loaded)}, session.NewManager(), nil)
	if err := gw.Start(); err != nil {
		t.Fatal(err)
	}
	defer gw.Close()
	roots := x509.NewCertPool()
	roots.AddCert(root)
	clientTLS := &tls.Config{ServerName: "relay.test", RootCAs: roots, MinVersion: tls.VersionTLS13}
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 3 * time.Second}, "tcp", gw.TCPAddr().String(), clientTLS)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	clientQUIC := clientTLS.Clone()
	clientQUIC.NextProtos = []string{"relayproxy-quic"}
	quicConn, err := quic.DialAddr(ctx, gw.QUICAddr().String(), clientQUIC, nil)
	if err != nil {
		t.Fatal(err)
	}
	quicConn.CloseWithError(0, "test complete")
	cfg.Server.TLSEnabled, cfg.Server.Admin.TLSEnabled = config.BoolPtr(false), config.BoolPtr(true)
	if tunnelTLSConfig(cfg, loaded) != nil || adminTLSConfig(cfg, loaded) == nil {
		t.Fatal("inverse TLS independence failed")
	}
}

func serverCertificateFixture(t *testing.T) (string, string, []byte, *x509.Certificate) {
	t.Helper()
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rootTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "RelayProxy test CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTemplate, rootTemplate, &rootKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	root, err := x509.ParseCertificate(rootDER)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "relay.test"}, DNSNames: []string{"relay.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, root, &leafKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		t.Fatal(err)
	}
	chainPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})
	chainPEM = append(chainPEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER})...)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "fullchain.pem"), filepath.Join(dir, "privkey.pem")
	for path, contents := range map[string][]byte{certPath: chainPEM, keyPath: keyPEM} {
		if err := os.WriteFile(path, contents, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return certPath, keyPath, leafDER, root
}
