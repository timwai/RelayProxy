package desktop

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
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

func TestClipboardDirectoryTransferRebuildsTree(t *testing.T) {
	sourceBase := t.TempDir()
	root := filepath.Join(sourceBase, "folder")
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	first := []byte("first")
	second := bytes.Repeat([]byte("second"), 20)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), first, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "b.bin"), second, 0o600); err != nil {
		t.Fatal(err)
	}

	plan, err := buildClipboardFileTransfer([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.offer.Roots) != 1 || !plan.offer.Roots[0].Directory || plan.offer.Roots[0].Name != "folder" {
		t.Fatalf("roots=%+v", plan.offer.Roots)
	}
	if len(plan.offer.Files) != 2 || len(plan.sources) != 2 {
		t.Fatalf("files=%+v sources=%v", plan.offer.Files, plan.sources)
	}

	var receiver clipboardFileReceiver
	t.Cleanup(receiver.close)
	if err := receiver.offer(plan.offer); err != nil {
		t.Fatal(err)
	}
	for i, source := range plan.sources {
		payload, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		if len(payload) > 0 {
			if err := receiver.chunk(protocol.DesktopClipboardFileChunk{
				TransferID: plan.offer.TransferID,
				FileIndex:  i,
				Offset:     0,
				Data:       payload,
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	state, err := receiver.done(protocol.DesktopClipboardFileDone{TransferID: plan.offer.TransferID})
	if err != nil {
		t.Fatal(err)
	}
	if len(state.LocalPaths) != 1 || filepath.Base(state.LocalPaths[0]) != "folder" {
		t.Fatalf("local paths=%v", state.LocalPaths)
	}
	gotFirst, err := os.ReadFile(filepath.Join(state.LocalPaths[0], "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	gotSecond, err := os.ReadFile(filepath.Join(state.LocalPaths[0], "nested", "b.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotFirst, first) || !bytes.Equal(gotSecond, second) {
		t.Fatal("reconstructed directory differs")
	}
}

func TestSafeClipboardRelativePathRejectsTraversal(t *testing.T) {
	for _, value := range []string{"", "/abs.txt", "../evil.txt", "a/../evil.txt", "a//b.txt", `a\..\evil.txt`} {
		if _, err := safeClipboardRelativePath(value); err == nil {
			t.Fatalf("unsafe relative path %q accepted", value)
		}
	}
	if got, err := safeClipboardRelativePath("folder/nested/file.txt"); err != nil || got != "folder/nested/file.txt" {
		t.Fatalf("safe relative path got=%q err=%v", got, err)
	}
}

func TestClipboardDirectoryTransferRejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated Windows privileges")
	}
	base := t.TempDir()
	root := filepath.Join(base, "folder")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(base, "target.txt")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := buildClipboardFileTransfer([]string{root}); err == nil {
		t.Fatal("directory symlink was accepted")
	}
}
