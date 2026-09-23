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

func TestRemoteDesktopAudioDiagnosticsRequiresConnectedSession(t *testing.T) {
	agent := &Agent{}
	got := agent.RemoteDesktopAudioDiagnostics()
	if got.Config.Generation != 0 || got.ReceivedFrames != 0 || got.QueueFrames != 0 {
		t.Fatalf("disconnected audio diagnostics=%+v", got)
	}
}
