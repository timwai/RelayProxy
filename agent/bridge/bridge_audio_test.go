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
