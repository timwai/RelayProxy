package app

import (
	"context"
	"testing"
)

func TestNextRemoteDesktopAudioFrameRequiresConnectedSession(t *testing.T) {
	agent := &Agent{}
	if _, _, err := agent.NextRemoteDesktopAudioFrame(context.Background()); err == nil {
		t.Fatal("audio frame read without an active Relay Desktop session succeeded")
	}
}

func TestRemoteDesktopAudioEnabledRequiresConnectedSession(t *testing.T) {
	agent := &Agent{}
	if agent.RemoteDesktopAudioEnabled() {
		t.Fatal("disconnected agent reported Relay Desktop audio enabled")
	}
}
