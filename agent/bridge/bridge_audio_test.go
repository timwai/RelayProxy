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
