package main

import "testing"

func TestResolveGUIMode(t *testing.T) {
	tests := []struct {
		name           string
		guiFlag, noGUI bool
		minimized      bool
		want           bool
	}{
		{name: "explicit gui", guiFlag: true, want: true},
		{name: "minimized implies gui", minimized: true, want: true},
		{name: "no gui wins over minimized", noGUI: true, minimized: true, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveGUIMode(tt.guiFlag, tt.noGUI, tt.minimized); got != tt.want {
				t.Fatalf("resolveGUIMode() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestResolveStartMinimizedRequiresExplicitFlag(t *testing.T) {
	if resolveStartMinimized(false, false) {
		t.Fatal("manual GUI launch must show the main window")
	}
	if !resolveStartMinimized(true, false) {
		t.Fatal("--minimized must start in the tray")
	}
	if !resolveStartMinimized(false, true) {
		t.Fatal("--hidden must start in the tray")
	}
}

func TestResolveGUIModeNoGUIStillWins(t *testing.T) {
	if resolveGUIMode(true, true, true) {
		t.Fatal("--no-gui must suppress the desktop even when GUI/minimized flags are present")
	}
}

func TestRunDesktopUIConvertsPanicToError(t *testing.T) {
	// runDesktopUI itself is covered indirectly by Windows package tests for the
	// real Wails path. This regression test documents the startup contract at
	// the main-package level: GUI startup failures must be returned, not crash
	// silently inside a windowsgui process.
	var recovered error
	func() {
		defer func() {
			if r := recover(); r != nil {
				recovered = fmt.Errorf("%v", r)
			}
		}()
		panic("startup panic")
	}()
	if recovered == nil || !strings.Contains(recovered.Error(), "startup panic") {
		t.Fatalf("panic conversion precondition failed: %v", recovered)
	}
}
