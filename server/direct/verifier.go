package direct

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"time"

	directtransport "relayproxy/internal/direct"
	"relayproxy/internal/protocol"
)

type Verifier struct {
	Timeout time.Duration
}

func NewVerifier(timeout time.Duration) *Verifier {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &Verifier{Timeout: timeout}
}

func (v *Verifier) Verify(ctx context.Context, target VerificationTarget) error {
	if target.Endpoint.Protocol != protocol.PublicDirectEndpointProtocolUDP ||
		target.Endpoint.Address == "" ||
		target.RegistrationID == "" ||
		len(target.Secret) != directtransport.VerificationSecretSize {
		return errors.New("Public Direct verification target is incomplete")
	}
	timeout := v.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	verifyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Endpoint identity is proven by the HMAC challenge bound to the secret that
	// arrived over the already-authenticated Relay session. The probe certificate
	// therefore does not use PKI hostname verification; TLS still provides QUIC
	// confidentiality and TLS 1.3 integrity for the challenge exchange.
	tlsConfig := &tls.Config{
		MinVersion:         tls.VersionTLS13,
		NextProtos:         []string{protocol.PublicDirectALPN},
		InsecureSkipVerify: true,
	}
	session, err := directtransport.DialAddr(
		verifyCtx,
		target.Endpoint.Address,
		tlsConfig,
		directtransport.QUICConfig(),
	)
	if err != nil {
		return fmt.Errorf("Public Direct endpoint dial failed: %w", err)
	}
	defer session.Close()

	stream, err := session.OpenStream(verifyCtx)
	if err != nil {
		return fmt.Errorf("Public Direct verification stream failed: %w", err)
	}
	defer stream.Close()
	if deadline, ok := verifyCtx.Deadline(); ok {
		_ = stream.SetDeadline(deadline)
	}

	nonce := make([]byte, directtransport.VerificationNonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("Public Direct verification nonce failed: %w", err)
	}
	if err := protocol.WriteJSON(stream, protocol.PublicDirectHandshake{
		Type:           protocol.PublicDirectHandshakeVerify,
		RegistrationID: target.RegistrationID,
		Nonce:          nonce,
	}); err != nil {
		return fmt.Errorf("Public Direct verification request failed: %w", err)
	}

	var response protocol.PublicDirectHandshake
	if err := protocol.ReadJSON(stream, &response); err != nil {
		return fmt.Errorf("Public Direct verification response failed: %w", err)
	}
	if response.Type != protocol.PublicDirectHandshakeVerifyResponse ||
		response.RegistrationID != target.RegistrationID {
		return errors.New("Public Direct verification response is invalid")
	}
	if !directtransport.VerifyProof(target.Secret, target.RegistrationID, nonce, response.Proof) {
		return errors.New("Public Direct verification proof is invalid")
	}
	return nil
}
