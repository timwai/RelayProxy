package bridge

import (
	"context"
	"testing"
)

func TestNextRemoteDesktopAudioFrameRejectsUnavailableBridge(t *testing.T) {
	var bridge *UIBridge
	if _, _, err := bridge.NextRemoteDesktopAudioFrame(context.Background()); err == nil {
		t.Fatal("nil bridge audio frame read succeeded")
	}
}

func TestRemoteDesktopAudioEnabledRejectsUnavailableBridge(t *testing.T) {
	var bridge *UIBridge
	if bridge.RemoteDesktopAudioEnabled() {
		t.Fatal("nil bridge reported Relay Desktop audio enabled")
	}
}

func TestGetRemoteDesktopAudioDiagnosticsRejectsUnavailableBridge(t *testing.T) {
	var bridge *UIBridge
	got := bridge.GetRemoteDesktopAudioDiagnostics()
	if got.Config.Generation != 0 || got.ReceivedFrames != 0 {
		t.Fatalf("nil bridge audio diagnostics=%+v", got)
	}
}
