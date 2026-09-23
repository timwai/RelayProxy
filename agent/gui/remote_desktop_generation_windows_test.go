//go:build windows

package gui

import (
	"testing"

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
