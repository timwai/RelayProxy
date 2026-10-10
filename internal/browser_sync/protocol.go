// Package browsersync defines the versioned, agentless browser-session
// transport envelope. Parsing an envelope does NOT authorize its sender:
// the HTTP/WSS handler must bind the source ID to an authenticated browser
// device and verify an ACTIVE, mutually approved rule before relaying.
package browsersync

import (
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

const (
	ProtocolVersion     = "browser.sync.v1"
	MaxCiphertextBytes  = 256 * 1024
	MaxClockSkew        = 60 * time.Second
	MaxMessageLifetime  = 15 * time.Minute
	MaxIdentifierLength = 128
)

type EventType string

const (
	SessionSnapshot  EventType = "SESSION_SNAPSHOT"
	SessionDelta     EventType = "SESSION_DELTA"
	SessionTombstone EventType = "SESSION_TOMBSTONE"
)

type EncryptionHeader struct {
	Suite string `json:"suite"`
	KeyID string `json:"keyId"`
	Enc   string `json:"enc"`
}

type Envelope struct {
	Protocol              string           `json:"protocol"`
	Type                  EventType        `json:"type"`
	MessageID             string           `json:"messageId"`
	RuleID                string           `json:"ruleId"`
	SourceBrowserDeviceID string           `json:"sourceBrowserDeviceId"`
	TargetBrowserDeviceID string           `json:"targetBrowserDeviceId"`
	Sequence              uint64           `json:"sequence"`
	CreatedAt             time.Time        `json:"createdAt"`
	ExpiresAt             time.Time        `json:"expiresAt"`
	Encryption            EncryptionHeader `json:"encryption"`
	Ciphertext            string           `json:"ciphertext"`
	Signature             string           `json:"signature"`
}

// ValidateEnvelope checks wire-format and resource limits only. The caller
// must additionally verify the sender signature, E2EE suite, replay counter,
// per-device authorization, and the rule's site policy.
func ValidateEnvelope(e Envelope, now time.Time) error {
	if e.Protocol != ProtocolVersion {
		return fmt.Errorf("unsupported browser sync protocol: %q", e.Protocol)
	}
	switch e.Type {
	case SessionSnapshot, SessionDelta, SessionTombstone:
	default:
		return fmt.Errorf("unsupported event type: %q", e.Type)
	}
	if _, err := uuid.Parse(e.MessageID); err != nil {
		return errors.New("invalid messageId")
	}
	for name, value := range map[string]string{
		"ruleId": e.RuleID,
		"sourceBrowserDeviceId": e.SourceBrowserDeviceID,
		"targetBrowserDeviceId": e.TargetBrowserDeviceID,
	} {
		if value == "" || len(value) > MaxIdentifierLength {
			return fmt.Errorf("invalid %s", name)
		}
	}
	if e.SourceBrowserDeviceID == e.TargetBrowserDeviceID {
		return errors.New("source and target browser must be distinct")
	}
	if e.Sequence == 0 {
		return errors.New("sequence must be positive")
	}
	if e.CreatedAt.IsZero() || e.ExpiresAt.IsZero() ||
		!e.ExpiresAt.After(e.CreatedAt) || e.ExpiresAt.Sub(e.CreatedAt) > MaxMessageLifetime {
		return errors.New("invalid message lifetime")
	}
	if e.CreatedAt.After(now.Add(MaxClockSkew)) || !now.Before(e.ExpiresAt) {
		return errors.New("message outside validity window")
	}
	if e.Encryption.Suite != "HPKE-v1" || e.Encryption.KeyID == "" ||
		len(e.Encryption.KeyID) > MaxIdentifierLength || e.Encryption.Enc == "" {
		return errors.New("invalid encryption header")
	}
	if len(e.Encryption.Enc) > 2048 {
		return errors.New("encapsulated key too large")
	}
	if _, err := base64.StdEncoding.DecodeString(e.Encryption.Enc); err != nil {
		return errors.New("invalid encapsulated key encoding")
	}
	// Reject on encoded size *before* decoding to bound allocations.
	if len(e.Ciphertext) == 0 || len(e.Ciphertext) > base64.StdEncoding.EncodedLen(MaxCiphertextBytes) {
		return errors.New("ciphertext exceeds size limit or is empty")
	}
	ciphertext, err := base64.StdEncoding.DecodeString(e.Ciphertext)
	if err != nil || len(ciphertext) == 0 || len(ciphertext) > MaxCiphertextBytes {
		return errors.New("invalid ciphertext")
	}
	if len(e.Signature) == 0 || len(e.Signature) > 2048 {
		return errors.New("missing or oversized signature")
	}
	signature, err := base64.StdEncoding.DecodeString(e.Signature)
	if err != nil || len(signature) < 32 || len(signature) > 512 {
		return errors.New("invalid signature encoding or size")
	}
	return nil
}
