package desktop

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"relayproxy/internal/protocol"
)

func TestSafeClipboardFileNameRejectsPaths(t *testing.T) {
	for _, name := range []string{"", ".", "..", "../x.txt", "a/b.txt", `a\b.txt`} {
		if _, err := safeClipboardFileName(name); err == nil {
			t.Fatalf("unsafe name %q accepted", name)
		}
	}
	if got, err := safeClipboardFileName("report.txt"); err != nil || got != "report.txt" {
		t.Fatalf("safe name got=%q err=%v", got, err)
	}
}

func TestClipboardFileReceiverRoundTrip(t *testing.T) {
	sourceDir := t.TempDir()
	source := filepath.Join(sourceDir, "hello.txt")
	payload := bytes.Repeat([]byte("relayproxy-file-clipboard\n"), 1000)
	if err := os.WriteFile(source, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	offer, err := buildClipboardFileOffer([]string{source})
	if err != nil {
		t.Fatal(err)
	}
	var receiver clipboardFileReceiver
	t.Cleanup(receiver.close)
	if err := receiver.offer(offer); err != nil {
		t.Fatal(err)
	}
	for offset := 0; offset < len(payload); offset += 777 {
		end := offset + 777
		if end > len(payload) {
			end = len(payload)
		}
		if err := receiver.chunk(protocol.DesktopClipboardFileChunk{
			TransferID: offer.TransferID,
			FileIndex:  0,
			Offset:     int64(offset),
			Data:       payload[offset:end],
		}); err != nil {
			t.Fatal(err)
		}
	}
	state, err := receiver.done(protocol.DesktopClipboardFileDone{TransferID: offer.TransferID})
	if err != nil {
		t.Fatal(err)
	}
	if state.Kind != protocol.DesktopClipboardKindFiles || len(state.LocalPaths) != 1 {
		t.Fatalf("state=%+v", state)
	}
	got, err := os.ReadFile(state.LocalPaths[0])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("staged clipboard file payload differs")
	}
}

func TestClipboardFileReceiverRejectsTraversalAndOffsets(t *testing.T) {
	var receiver clipboardFileReceiver
	t.Cleanup(receiver.reset)
	err := receiver.offer(protocol.DesktopClipboardFileOffer{
		TransferID: "0123456789abcdef",
		Files:      []protocol.DesktopClipboardFile{{Name: "../evil.txt", Size: 1, SHA256: strings.Repeat("0", 64)}},
	})
	if err == nil {
		t.Fatal("path traversal offer accepted")
	}
}
