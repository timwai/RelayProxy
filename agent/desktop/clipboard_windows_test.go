//go:build windows

package desktop

import (
	"testing"

	"relayproxy/internal/protocol"
)

func TestWindowsClipboardUTF16Sizing(t *testing.T) {
	text := "RelayProxy 中文 😀"
	normalized, err := validateClipboardText(text)
	if err != nil {
		t.Fatal(err)
	}
	if normalized != text {
		t.Fatalf("normalized=%q", normalized)
	}
	if len([]byte(text)) > protocol.MaxDesktopClipboardBytes {
		t.Fatal("test text unexpectedly exceeds clipboard limit")
	}
}
