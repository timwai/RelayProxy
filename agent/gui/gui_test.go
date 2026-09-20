package gui

import "testing"

func TestDesktopWindowDimensionsRemainResponsive(t *testing.T) {
	if DefaultWindowWidth <= MinimumWindowWidth || DefaultWindowHeight <= MinimumWindowHeight {
		t.Fatalf("default window must be larger than minimum: default=%dx%d min=%dx%d",
			DefaultWindowWidth, DefaultWindowHeight, MinimumWindowWidth, MinimumWindowHeight)
	}
	if MinimumWindowWidth > 900 || MinimumWindowHeight > 600 {
		t.Fatalf("minimum window is too large for compact desktop layouts: %dx%d",
			MinimumWindowWidth, MinimumWindowHeight)
	}
}

func TestDefaultWindowTitleIsProductNameOnly(t *testing.T) {
	if DefaultWindowTitle != "RelayProxy" {
		t.Fatalf("default window title = %q, want RelayProxy", DefaultWindowTitle)
	}
}
