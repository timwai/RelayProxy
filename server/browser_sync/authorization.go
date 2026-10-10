// Package browsersync provides isolated Browser Device authorization
// primitives. No proxy.use or rdp.connect capability authorizes this feature.
package browsersync

import (
	"errors"

	protocol "relayproxy/internal/browser_sync"
)

const (
	CapabilitySend    = "browser.sync.send"
	CapabilityReceive = "browser.sync.receive"
)

// VerifiedDevice must be created from a successfully authenticated browser
// session, NEVER directly from request JSON.
type VerifiedDevice struct {
	ID       string
	Approved bool
	Revoked  bool
	Send     bool
	Receive  bool
}

type ApprovedRule struct {
	ID                      string
	SourceBrowserDeviceID   string
	TargetBrowserDeviceID   string
	SourceApproved          bool
	TargetApproved          bool
	KeyFingerprintsVerified bool
	Active                  bool
}

// AuthorizeRelay denies by default. It authorizes message routing, not the
// cryptographic signature or policy-specific cookie contents.
func AuthorizeRelay(sender, recipient VerifiedDevice, rule ApprovedRule, msg protocol.Envelope) error {
	if !sender.Approved || sender.Revoked || !sender.Send ||
		!recipient.Approved || recipient.Revoked || !recipient.Receive {
		return errors.New("browser device not authorized")
	}
	if sender.ID == "" || recipient.ID == "" ||
		sender.ID == recipient.ID || msg.SourceBrowserDeviceID != sender.ID ||
		msg.TargetBrowserDeviceID != recipient.ID {
		return errors.New("authenticated browser device mismatch")
	}
	if !rule.Active || !rule.SourceApproved || !rule.TargetApproved ||
		!rule.KeyFingerprintsVerified || rule.ID != msg.RuleID ||
		rule.SourceBrowserDeviceID != sender.ID ||
		rule.TargetBrowserDeviceID != recipient.ID {
		return errors.New("browser sync rule not mutually authorized")
	}
	return nil
}
