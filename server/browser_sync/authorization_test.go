package browsersync

import (
	"testing"

	protocol "relayproxy/internal/browser_sync"
)

func TestAuthorizeRelay(t *testing.T) {
	sender := VerifiedDevice{ID: "browser_a", Approved: true, Send: true}
	recipient := VerifiedDevice{ID: "browser_b", Approved: true, Receive: true}
	rule := ApprovedRule{ID: "rule_1", SourceBrowserDeviceID: "browser_a", TargetBrowserDeviceID: "browser_b", SourceApproved: true, TargetApproved: true, KeyFingerprintsVerified: true, Active: true}
	env := protocol.Envelope{RuleID: "rule_1", SourceBrowserDeviceID: "browser_a", TargetBrowserDeviceID: "browser_b"}
	if err := AuthorizeRelay(sender, recipient, rule, env); err != nil {
		t.Fatalf("expected allowed, got %v", err)
	}
	cases := []struct {
		name   string
		change func(*VerifiedDevice, *VerifiedDevice, *ApprovedRule, *protocol.Envelope)
	}{
		{"unapproved-sender", func(a, b *VerifiedDevice, r *ApprovedRule, e *protocol.Envelope) { a.Approved = false }},
		{"revoked-recipient", func(a, b *VerifiedDevice, r *ApprovedRule, e *protocol.Envelope) { b.Revoked = true }},
		{"no-send-capability", func(a, b *VerifiedDevice, r *ApprovedRule, e *protocol.Envelope) { a.Send = false }},
		{"no-receive-capability", func(a, b *VerifiedDevice, r *ApprovedRule, e *protocol.Envelope) { b.Receive = false }},
		{"target-not-approved", func(a, b *VerifiedDevice, r *ApprovedRule, e *protocol.Envelope) { r.TargetApproved = false }},
		{"key-not-confirmed", func(a, b *VerifiedDevice, r *ApprovedRule, e *protocol.Envelope) { r.KeyFingerprintsVerified = false }},
		{"source-spoofed", func(a, b *VerifiedDevice, r *ApprovedRule, e *protocol.Envelope) {
			e.SourceBrowserDeviceID = "browser_other"
		}},
		{"wrong-rule", func(a, b *VerifiedDevice, r *ApprovedRule, e *protocol.Envelope) { e.RuleID = "rule_other" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, b, r, e := sender, recipient, rule, env
			tc.change(&a, &b, &r, &e)
			if err := AuthorizeRelay(a, b, r, e); err == nil {
				t.Fatal("unexpected authorization")
			}
		})
	}
}
