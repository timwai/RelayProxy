package desktop

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
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

func testClipboardPNG(t *testing.T, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestValidateClipboardContentSupportsLegacyTextAndPNG(t *testing.T) {
	text, err := validateClipboardContent(protocol.DesktopClipboardState{Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if text.Kind != protocol.DesktopClipboardKindText || text.Text != "hello" || len(text.PNG) != 0 {
		t.Fatalf("legacy text normalized=%+v", text)
	}

	payload := testClipboardPNG(t, color.RGBA{R: 10, G: 20, B: 30, A: 255})
	imageContent, err := validateClipboardContent(protocol.DesktopClipboardState{
		Kind: protocol.DesktopClipboardKindPNG,
		PNG:  payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	if imageContent.Kind != protocol.DesktopClipboardKindPNG || len(imageContent.PNG) == 0 || imageContent.Text != "" {
		t.Fatalf("PNG normalized=%+v", imageContent)
	}
	payload[0] ^= 0xff
	if imageContent.PNG[0] == payload[0] {
		t.Fatal("validated PNG aliases caller payload")
	}

	if _, err := validateClipboardContent(protocol.DesktopClipboardState{
		Kind: protocol.DesktopClipboardKindPNG,
		PNG:  make([]byte, protocol.MaxDesktopClipboardImageBytes+1),
	}); err == nil {
		t.Fatal("oversized PNG clipboard was accepted")
	}
	if _, err := validateClipboardContent(protocol.DesktopClipboardState{
		Kind: protocol.DesktopClipboardKindPNG,
		PNG:  []byte("not a png"),
	}); err == nil {
		t.Fatal("invalid PNG clipboard was accepted")
	}
}

func TestClipboardSyncStateDeduplicatesPNG(t *testing.T) {
	var state clipboardSyncState
	first := protocol.DesktopClipboardState{
		Kind: protocol.DesktopClipboardKindPNG,
		PNG:  testClipboardPNG(t, color.RGBA{R: 1, G: 2, B: 3, A: 255}),
	}
	state.SeedContent(first)
	if state.ChangedContent(first) {
		t.Fatal("seeded PNG clipboard was reported as changed")
	}
	second := protocol.DesktopClipboardState{
		Kind: protocol.DesktopClipboardKindPNG,
		PNG:  testClipboardPNG(t, color.RGBA{R: 9, G: 8, B: 7, A: 255}),
	}
	if !state.ChangedContent(second) {
		t.Fatal("changed PNG clipboard was not reported")
	}
	if state.ChangedContent(second) {
		t.Fatal("duplicate PNG clipboard was reported as changed")
	}
	if !state.IsCurrentContent(second) {
		t.Fatal("current PNG clipboard was not tracked")
	}
}
