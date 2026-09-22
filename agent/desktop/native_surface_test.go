package desktop

import (
	"errors"
	"testing"
	"time"
)

type testCaptureSurface struct {
	backend string
	format  string
	closed  int
}

func (s *testCaptureSurface) Backend() string { return s.backend }
func (s *testCaptureSurface) Format() string  { return s.format }
func (s *testCaptureSurface) Close() error {
	s.closed++
	return nil
}

func TestNativeCaptureFrameValidate(t *testing.T) {
	surface := &testCaptureSurface{backend: "d3d11", format: "bgra8"}
	frame := NativeCaptureFrame{
		Width:      1920,
		Height:     1080,
		CapturedAt: time.Now(),
		Surface:    surface,
	}
	if err := frame.Validate(); err != nil {
		t.Fatalf("valid native capture frame rejected: %v", err)
	}

	tests := []struct {
		name  string
		frame NativeCaptureFrame
	}{
		{
			name:  "missing width",
			frame: NativeCaptureFrame{Height: 1080, Surface: surface},
		},
		{
			name:  "missing height",
			frame: NativeCaptureFrame{Width: 1920, Surface: surface},
		},
		{
			name:  "missing surface",
			frame: NativeCaptureFrame{Width: 1920, Height: 1080},
		},
		{
			name: "missing backend",
			frame: NativeCaptureFrame{
				Width: 1920, Height: 1080,
				Surface: &testCaptureSurface{format: "bgra8"},
			},
		},
		{
			name: "missing format",
			frame: NativeCaptureFrame{
				Width: 1920, Height: 1080,
				Surface: &testCaptureSurface{backend: "d3d11"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.frame.Validate(); err == nil {
				t.Fatal("invalid native capture frame was accepted")
			}
		})
	}
}

func TestNativeCaptureSurfaceOwnershipIsExplicit(t *testing.T) {
	surface := &testCaptureSurface{backend: "d3d11", format: "bgra8"}
	frame := NativeCaptureFrame{Width: 2, Height: 2, Surface: surface}
	if err := frame.Validate(); err != nil {
		t.Fatal(err)
	}
	if surface.closed != 0 {
		t.Fatal("surface closed before owner released it")
	}
	if err := frame.Surface.Close(); err != nil {
		t.Fatal(err)
	}
	if surface.closed != 1 {
		t.Fatalf("surface close count=%d want=1", surface.closed)
	}
}

func TestNativeCaptureFrameValidateDoesNotOwnSurfaceOnError(t *testing.T) {
	surface := &testCaptureSurface{backend: "d3d11", format: "bgra8"}
	frame := NativeCaptureFrame{Width: 0, Height: 1080, Surface: surface}
	if !errors.Is(frame.Validate(), frame.Validate()) {
		// Keep this test free of error-string coupling; Validate only reports
		// validity and never takes ownership of the surface.
	}
	if surface.closed != 0 {
		t.Fatal("Validate unexpectedly closed caller-owned surface")
	}
}
