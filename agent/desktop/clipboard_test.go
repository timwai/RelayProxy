package desktop

import (
	"strings"
	"testing"

	"relayproxy/internal/protocol"
)

func TestValidateClipboardText(t *testing.T) {
	got, err := validateClipboardText("hello\x00ignored")
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello" {
		t.Fatalf("text=%q", got)
	}
	_, err = validateClipboardText(strings.Repeat("a", protocol.MaxDesktopClipboardBytes+1))
	if err == nil {
		t.Fatal("oversized clipboard was accepted")
	}
}

func TestClipboardSyncStateDeduplicates(t *testing.T) {
	var state clipboardSyncState
	state.Seed("a")
	if state.Changed("a") {
		t.Fatal("seeded clipboard was reported as changed")
	}
	if !state.Changed("b") {
		t.Fatal("new clipboard was not reported as changed")
	}
	if state.Changed("b") {
		t.Fatal("duplicate clipboard was reported as changed")
	}
	if !state.IsCurrent("b") {
		t.Fatal("current clipboard was not tracked")
	}
}

func TestClipboardEnabledDefaultsOn(t *testing.T) {
	if !clipboardEnabled(protocol.RemoteDesktopConnectOptions{}) {
		t.Fatal("clipboard should default to enabled")
	}
	disabled := false
	if clipboardEnabled(protocol.RemoteDesktopConnectOptions{Clipboard: &disabled}) {
		t.Fatal("explicitly disabled clipboard was enabled")
	}
}
