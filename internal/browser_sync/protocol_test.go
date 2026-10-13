package browsersync

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func validEnvelope(now time.Time) Envelope {
	return Envelope{
		Protocol:              ProtocolVersion,
		Type:                  SessionSnapshot,
		MessageID:             "69064be2-ea88-4e8d-9aa1-a0c55e7a0d5e",
		RuleID:                "d75b3c83-415f-4a69-aea4-a78a937b3d16",
		SourceBrowserDeviceID: "browser_a",
		TargetBrowserDeviceID: "browser_b",
		Sequence:              1,
		CreatedAt:             now.Add(-time.Second),
		ExpiresAt:             now.Add(time.Minute),
		Encryption: EncryptionHeader{
			Suite: SessionCipherSuite, KeyID: "key_b1",
			Salt: base64.RawURLEncoding.EncodeToString(make([]byte, 32)),
			IV:   base64.RawURLEncoding.EncodeToString(make([]byte, 12)),
			Enc:  base64.RawURLEncoding.EncodeToString([]byte("enc")),
		},
		Ciphertext: base64.RawURLEncoding.EncodeToString([]byte("encrypted payload")),
		Signature:  base64.RawURLEncoding.EncodeToString(make([]byte, 64)),
	}
}

func TestValidateEnvelope(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	if err := ValidateEnvelope(validEnvelope(now), now); err != nil {
		t.Fatalf("valid envelope rejected: %v", err)
	}
	for _, tc := range []struct {
		name string
		edit func(*Envelope)
	}{
		{"wrong-version", func(e *Envelope) { e.Protocol = "v0" }},
		{"wrong-type", func(e *Envelope) { e.Type = "CONTROL" }},
		{"same-device", func(e *Envelope) { e.TargetBrowserDeviceID = e.SourceBrowserDeviceID }},
		{"zero-sequence", func(e *Envelope) { e.Sequence = 0 }},
		{"expired", func(e *Envelope) { e.ExpiresAt = now.Add(-time.Millisecond) }},
		{"future", func(e *Envelope) { e.CreatedAt = now.Add(2 * time.Minute); e.ExpiresAt = now.Add(3 * time.Minute) }},
		{"bad-key", func(e *Envelope) { e.Encryption.Enc = "*invalid*" }},
		{"oversized-ciphertext", func(e *Envelope) {
			e.Ciphertext = strings.Repeat("A", base64.StdEncoding.EncodedLen(MaxCiphertextBytes)+4)
		}},
		{"missing-signature", func(e *Envelope) { e.Signature = "" }},
		{"invalid-transfer-request", func(e *Envelope) { e.TransferRequestID = "untrusted-text" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := validEnvelope(now)
			tc.edit(&e)
			if err := ValidateEnvelope(e, now); err == nil {
				t.Fatal("wanted validation failure")
			}
		})
	}
}

func TestExpandedSnapshotCiphertextEnvelopeLimit(t *testing.T) {
	now := time.Now().UTC()
	env := validEnvelope(now)
	env.Ciphertext = base64.RawURLEncoding.EncodeToString(make([]byte, 768*1024))
	if err := ValidateEnvelope(env, now); err != nil {
		t.Fatalf("large but bounded encrypted snapshot rejected: %v", err)
	}
	env.Ciphertext = base64.RawURLEncoding.EncodeToString(make([]byte, MaxCiphertextBytes+1))
	if err := ValidateEnvelope(env, now); err == nil {
		t.Fatal("snapshot larger than the wire limit was accepted")
	}
}

func TestReceiverTransferRequestIsAuthenticatedInEnvelopeHeader(t *testing.T) {
	now := time.Now().UTC()
	base := validEnvelope(now)
	oldHeader := SignedHeader(base)
	const token = "01234567-89ab-4cde-a321-112233445566"
	base.TransferRequestID = token
	if err := ValidateEnvelope(base, now); err != nil {
		t.Fatalf("valid B request token rejected: %v", err)
	}
	signed := SignedHeader(base)
	if signed == oldHeader || !strings.HasSuffix(signed, "\n"+token) {
		t.Fatalf("receiver token not authenticated in signed header")
	}
	base.TransferRequestID = "11111111-89ab-4cde-a321-112233445566"
	if SignedHeader(base) == signed {
		t.Fatal("changing request token did not invalidate the signed header")
	}
	base.TransferRequestID = ""
	if SignedHeader(base) != oldHeader {
		t.Fatal("legacy unrequested snapshot wire header changed")
	}
}
