//go:build windows

package gui

import (
	"testing"

	"relayproxy/internal/protocol"
)

func TestNativeDesktopFrameNeedsRebuild(t *testing.T) {
	tests := []struct {
		name       string
		generation uint32
		width      int
		height     int
		frame      protocol.RemoteDesktopFrame
		want       bool
	}{
		{
			name:       "same generation and size",
			generation: 3,
			width:      1920,
			height:     1080,
			frame:      protocol.RemoteDesktopFrame{Generation: 3, Width: 1920, Height: 1080},
		},
		{
			name:       "new generation same size",
			generation: 3,
			width:      1920,
			height:     1080,
			frame:      protocol.RemoteDesktopFrame{Generation: 4, Width: 1920, Height: 1080},
			want:       true,
		},
		{
			name:       "same generation new size",
			generation: 3,
			width:      1920,
			height:     1080,
			frame:      protocol.RemoteDesktopFrame{Generation: 3, Width: 1280, Height: 720},
			want:       true,
		},
		{
			name:       "legacy zero generation stable",
			width:      1280,
			height:     720,
			frame:      protocol.RemoteDesktopFrame{Width: 1280, Height: 720},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nativeDesktopFrameNeedsRebuild(tt.generation, tt.width, tt.height, tt.frame); got != tt.want {
				t.Fatalf("nativeDesktopFrameNeedsRebuild()=%t want=%t", got, tt.want)
			}
		})
	}
}
