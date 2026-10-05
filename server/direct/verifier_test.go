package direct_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"sync/atomic"
	"testing"
	"time"

	agentdirect "relayproxy/agent/direct"
	directtransport "relayproxy/internal/direct"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
	serverdirect "relayproxy/server/direct"
)

type probeHandler struct {
	calls atomic.Int32
}

func (h *probeHandler) HandleStream(context.Context, tunnel.TunnelStream) {
	h.calls.Add(1)
}

func verificationServerTLS(t *testing.T) *tls.Config {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(7),
		Subject:      pkix.Name{CommonName: "relayproxy-public-direct-verification"},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
	}
}

func TestVerifierProvesEndpointBelongsToRegisteredExit(t *testing.T) {
	secret := make([]byte, directtransport.VerificationSecretSize)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	const registrationID = "registration-test"
	handler := &probeHandler{}
	listener, err := agentdirect.ListenPublicQUIC(
		context.Background(),
		"127.0.0.1:0",
		verificationServerTLS(t),
		agentdirect.VerificationAuthenticator(registrationID, secret),
		handler,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	target := serverdirect.VerificationTarget{
		DeviceID:       "exit-test",
		RegistrationID: registrationID,
		Generation:     1,
		Endpoint: protocol.PublicDirectEndpoint{
			Protocol: protocol.PublicDirectEndpointProtocolUDP,
			Address:  listener.Addr().String(),
			Source:   protocol.PublicDirectEndpointSourceManual,
		},
		Secret: secret,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := serverdirect.NewVerifier(3*time.Second).Verify(ctx, target); err != nil {
		t.Fatal(err)
	}
	if got := handler.calls.Load(); got != 0 {
		t.Fatalf("verification connection reached proxy handler %d times", got)
	}
}

func TestVerifierRejectsWrongSecret(t *testing.T) {
	secret := make([]byte, directtransport.VerificationSecretSize)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	listener, err := agentdirect.ListenPublicQUIC(
		context.Background(),
		"127.0.0.1:0",
		verificationServerTLS(t),
		agentdirect.VerificationAuthenticator("registration-test", secret),
		&probeHandler{},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	wrong := append([]byte(nil), secret...)
	wrong[0] ^= 0xff
	err = serverdirect.NewVerifier(3*time.Second).Verify(context.Background(), serverdirect.VerificationTarget{
		DeviceID:       "exit-test",
		RegistrationID: "registration-test",
		Generation:     1,
		Endpoint: protocol.PublicDirectEndpoint{
			Protocol: protocol.PublicDirectEndpointProtocolUDP,
			Address:  listener.Addr().String(),
			Source:   protocol.PublicDirectEndpointSourceManual,
		},
		Secret: wrong,
	})
	if err == nil {
		t.Fatal("verifier accepted proof under the wrong registration secret")
	}
}
