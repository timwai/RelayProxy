//go:build windows

package gui

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	desktopcodec "relayproxy/agent/desktop/codec"
	desktopgpu "relayproxy/agent/desktop/gpu"
	desktopviewer "relayproxy/agent/desktop/viewer"
	"relayproxy/internal/protocol"
)

type rebuildTestDecoder struct {
	name  string
	calls *[]string
}

func (d *rebuildTestDecoder) Decode(context.Context, []byte, time.Duration) ([]desktopcodec.DecodedFrame, error) {
	return nil, nil
}
func (d *rebuildTestDecoder) Flush(context.Context) error { return nil }
func (d *rebuildTestDecoder) Hardware() bool              { return true }
func (d *rebuildTestDecoder) Backend() string             { return d.name }
func (d *rebuildTestDecoder) Close() error {
	if d.calls != nil {
		*d.calls = append(*d.calls, d.name+"-close")
	}
	return nil
}

type rebuildTestViewer struct {
	calls          *[]string
	reconfigureErr error
}

func (v *rebuildTestViewer) Submit(desktopviewer.Frame) error                 { return nil }
func (v *rebuildTestViewer) SubmitGPU(desktopgpu.Frame) error                 { return nil }
func (v *rebuildTestViewer) SubmitD3D11(desktopviewer.D3D11Frame) error       { return nil }
func (v *rebuildTestViewer) ClearFrame() {
	if v.calls != nil {
		*v.calls = append(*v.calls, "clear")
	}
}
func (v *rebuildTestViewer) D3D11Device() uintptr                             { return 1 }
func (v *rebuildTestViewer) SupportsGPUFormat(desktopgpu.Format) bool         { return true }
func (v *rebuildTestViewer) SupportsGPUCursor() bool                          { return false }
func (v *rebuildTestViewer) SetCursor(desktopviewer.CursorOverlay) error      { return nil }
func (v *rebuildTestViewer) Reconfigure(width, height int) error {
	if v.calls != nil {
		*v.calls = append(*v.calls, "resize")
	}
	return v.reconfigureErr
}
func (v *rebuildTestViewer) Viewport() desktopviewer.Viewport                 { return desktopviewer.Viewport{Width: 1280, Height: 720} }
func (v *rebuildTestViewer) WindowPlacement() desktopviewer.WindowPlacement   { return desktopviewer.WindowPlacement{} }
func (v *rebuildTestViewer) Focus()                                           {}
func (v *rebuildTestViewer) Done() <-chan struct{}                            { return make(chan struct{}) }
func (v *rebuildTestViewer) Close() error                                     { return nil }

func TestNativeDesktopRebuildPreparesDecoderBeforeResize(t *testing.T) {
	var calls []string
	viewer := &rebuildTestViewer{calls: &calls}
	oldDecoder := &rebuildTestDecoder{name: "old", calls: &calls}
	nextDecoder := &rebuildTestDecoder{name: "next", calls: &calls}
	session := &nativeDesktopSession{
		viewer:          viewer,
		decoder:         oldDecoder,
		generation:      1,
		decoderCodec:    "h264",
		decoderChroma:   "420",
		decoderBitDepth: 8,
		decoderWidth:    1280,
		decoderHeight:   720,
	}
	session.decoderOpener = func(
		context.Context,
		desktopviewer.Native,
		string,
		string,
		int,
		int,
		int,
	) (desktopcodec.Decoder, error) {
		calls = append(calls, "open")
		return nextDecoder, nil
	}

	frame := protocol.RemoteDesktopFrame{
		Generation: 2,
		MimeType:   "video/h264",
		Chroma:     "420",
		BitDepth:   8,
		Width:      1024,
		Height:     576,
	}
	if err := session.rebuildMediaPipeline(context.Background(), frame); err != nil {
		t.Fatal(err)
	}
	want := []string{"open", "resize", "clear", "old-close"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%v want=%v", calls, want)
	}
	if session.decoder != nextDecoder || session.decoderWidth != 1024 || session.decoderHeight != 576 || session.generation != 2 {
		t.Fatalf("pipeline not switched: decoder=%T size=%dx%d generation=%d", session.decoder, session.decoderWidth, session.decoderHeight, session.generation)
	}
}

func TestNativeDesktopRebuildResizeFailureKeepsOldPipeline(t *testing.T) {
	var calls []string
	resizeErr := errors.New("resize failed")
	viewer := &rebuildTestViewer{calls: &calls, reconfigureErr: resizeErr}
	oldDecoder := &rebuildTestDecoder{name: "old", calls: &calls}
	nextDecoder := &rebuildTestDecoder{name: "next", calls: &calls}
	session := &nativeDesktopSession{
		viewer:          viewer,
		decoder:         oldDecoder,
		generation:      7,
		decoderCodec:    "h264",
		decoderChroma:   "420",
		decoderBitDepth: 8,
		decoderWidth:    1920,
		decoderHeight:   1080,
	}
	session.decoderOpener = func(
		context.Context,
		desktopviewer.Native,
		string,
		string,
		int,
		int,
		int,
	) (desktopcodec.Decoder, error) {
		calls = append(calls, "open")
		return nextDecoder, nil
	}

	err := session.rebuildMediaPipeline(context.Background(), protocol.RemoteDesktopFrame{
		Generation: 8,
		MimeType:   "video/h264",
		Chroma:     "420",
		BitDepth:   8,
		Width:      1280,
		Height:     720,
	})
	if !errors.Is(err, resizeErr) {
		t.Fatalf("error=%v want resize failure", err)
	}
	want := []string{"open", "resize", "next-close"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%v want=%v", calls, want)
	}
	if session.decoder != oldDecoder || session.decoderWidth != 1920 || session.decoderHeight != 1080 || session.generation != 7 {
		t.Fatalf("old pipeline was modified: decoder=%T size=%dx%d generation=%d", session.decoder, session.decoderWidth, session.decoderHeight, session.generation)
	}
}
