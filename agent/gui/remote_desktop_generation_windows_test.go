//go:build windows

package gui

import (
	"testing"

	desktopviewer "relayproxy/agent/desktop/viewer"
	"relayproxy/internal/protocol"
)

func TestNativeDesktopFrameCodec(t *testing.T) {
	for _, tc := range []struct {
		mime string
		want string
	}{
		{mime: "video/h264", want: "h264"},
		{mime: "video/h265", want: "h265"},
		{mime: "image/jpeg", want: ""},
	} {
		frame := protocol.RemoteDesktopFrame{MimeType: tc.mime}
		if got := nativeDesktopFrameCodec(frame); got != tc.want {
			t.Fatalf("mime=%q codec=%q want=%q", tc.mime, got, tc.want)
		}
	}
}

func TestNativeDesktopFrameNeedsRebuild(t *testing.T) {
	tests := []struct {
		name       string
		generation uint32
		codec      string
		width      int
		height     int
		frame      protocol.RemoteDesktopFrame
		want       bool
	}{
		{
			name:       "same generation and size",
			generation: 3,
			codec:      "h264",
			width:      1920,
			height:     1080,
			frame:      protocol.RemoteDesktopFrame{Generation: 3, MimeType: "video/h264", Width: 1920, Height: 1080},
		},
		{
			name:       "new generation same size",
			generation: 3,
			codec:      "h264",
			width:      1920,
			height:     1080,
			frame:      protocol.RemoteDesktopFrame{Generation: 4, MimeType: "video/h264", Width: 1920, Height: 1080},
			want:       true,
		},
		{
			name:       "codec switch same generation and size",
			generation: 3,
			codec:      "h264",
			width:      1920,
			height:     1080,
			frame:      protocol.RemoteDesktopFrame{Generation: 3, MimeType: "video/h265", Width: 1920, Height: 1080},
			want:       true,
		},
		{
			name:       "same generation new size",
			generation: 3,
			codec:      "h264",
			width:      1920,
			height:     1080,
			frame:      protocol.RemoteDesktopFrame{Generation: 3, MimeType: "video/h264", Width: 1280, Height: 720},
			want:       true,
		},
		{
			name:   "legacy zero generation stable",
			codec:  "h264",
			width:  1280,
			height: 720,
			frame:  protocol.RemoteDesktopFrame{MimeType: "video/h264", Width: 1280, Height: 720},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nativeDesktopFrameNeedsRebuild(tt.generation, tt.codec, tt.width, tt.height, tt.frame); got != tt.want {
				t.Fatalf("nativeDesktopFrameNeedsRebuild()=%t want=%t", got, tt.want)
			}
		})
	}
}


func TestNativeDesktopViewportResolution(t *testing.T) {
	tests := []struct {
		name     string
		viewport desktopviewer.Viewport
		status   protocol.RemoteDesktopStatus
		width    int
		height   int
		ok       bool
	}{
		{
			name:     "same aspect shrinks to viewport",
			viewport: desktopviewer.Viewport{Width: 1280, Height: 720},
			status:   protocol.RemoteDesktopStatus{Width: 1920, Height: 1080, MaxWidth: 3840, MaxHeight: 2160},
			width:    1280,
			height:   720,
			ok:       true,
		},
		{
			name:     "square viewport preserves media aspect",
			viewport: desktopviewer.Viewport{Width: 1000, Height: 1000},
			status:   protocol.RemoteDesktopStatus{Width: 1920, Height: 1080, MaxWidth: 3840, MaxHeight: 2160},
			width:    1000,
			height:   562,
			ok:       true,
		},
		{
			name:     "viewport cannot exceed session ceiling",
			viewport: desktopviewer.Viewport{Width: 3000, Height: 2000},
			status:   protocol.RemoteDesktopStatus{Width: 1920, Height: 1080, MaxWidth: 1920, MaxHeight: 1080},
			width:    1920,
			height:   1080,
			ok:       true,
		},
		{
			name:     "tiny viewport rejected",
			viewport: desktopviewer.Viewport{Width: 200, Height: 100},
			status:   protocol.RemoteDesktopStatus{Width: 1920, Height: 1080, MaxWidth: 1920, MaxHeight: 1080},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			width, height, ok := nativeDesktopViewportResolution(tt.viewport, tt.status)
			if width != tt.width || height != tt.height || ok != tt.ok {
				t.Fatalf("viewport resolution=%dx%d ok=%v want=%dx%d ok=%v", width, height, ok, tt.width, tt.height, tt.ok)
			}
		})
	}
}

func TestNativeDesktopViewportGrowRequiresLastRequestedGeneration(t *testing.T) {
	if nativeDesktopViewportMayGrow(1280, 720, 0, 0) {
		t.Fatal("viewport grew before any stable/requested baseline")
	}
	if !nativeDesktopViewportMayGrow(1280, 720, 1280, 720) {
		t.Fatal("matching viewport generation did not allow growth")
	}
	if !nativeDesktopViewportMayGrow(1279, 721, 1280, 720) {
		t.Fatal("small generation rounding difference blocked growth")
	}
	if nativeDesktopViewportMayGrow(960, 540, 1280, 720) {
		t.Fatal("ABR-downshifted generation was allowed to grow over adaptation")
	}
}
