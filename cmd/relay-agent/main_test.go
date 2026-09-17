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
