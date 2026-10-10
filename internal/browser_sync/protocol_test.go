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
		RuleID:                "rule_test",
		SourceBrowserDeviceID: "browser_a",
		TargetBrowserDeviceID: "browser_b",
		Sequence:              1,
		CreatedAt:             now.Add(-time.Second),
		ExpiresAt:             now.Add(time.Minute),
		Encryption: EncryptionHeader{
			Suite: "HPKE-v1", KeyID: "key_b1",
			Enc: base64.StdEncoding.EncodeToString([]byte("enc")),
		},
		Ciphertext: base64.StdEncoding.EncodeToString([]byte("encrypted payload")),
		Signature:  base64.StdEncoding.EncodeToString(make([]byte, 64)),
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
		{"oversized-ciphertext", func(e *Envelope) { e.Ciphertext = strings.Repeat("A", base64.StdEncoding.EncodedLen(MaxCiphertextBytes)+4) }},
		{"missing-signature", func(e *Envelope) { e.Signature = "" }},
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
