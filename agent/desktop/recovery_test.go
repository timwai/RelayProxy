package desktop

import (
	"testing"

	desktopmedia "relayproxy/internal/desktop"
	"relayproxy/internal/protocol"
)

func h264TestConfig() protocol.DesktopVideoConfig {
	return protocol.DesktopVideoConfig{Generation: 1, Codec: "h264"}
}

func TestH264RecoveryRequiresInitialKeyFrame(t *testing.T) {
	var state h264RecoveryState
	accept, request := state.Observe(&desktopmedia.EncodedFrame{Generation: 1, FrameID: 1}, h264TestConfig())
	if accept || !request {
		t.Fatalf("accept=%v request=%v", accept, request)
	}
	accept, request = state.Observe(&desktopmedia.EncodedFrame{Generation: 1, FrameID: 2, KeyFrame: true}, h264TestConfig())
	if !accept || request {
		t.Fatalf("keyframe accept=%v request=%v", accept, request)
	}
}

func TestH264RecoveryDropsDeltaFramesAfterGap(t *testing.T) {
	var state h264RecoveryState
	if accept, _ := state.Observe(&desktopmedia.EncodedFrame{Generation: 1, FrameID: 1, KeyFrame: true}, h264TestConfig()); !accept {
		t.Fatal("initial keyframe rejected")
	}
	if accept, request := state.Observe(&desktopmedia.EncodedFrame{Generation: 1, FrameID: 3}, h264TestConfig()); accept || !request {
		t.Fatalf("gap accept=%v request=%v", accept, request)
	}
	if accept, request := state.Observe(&desktopmedia.EncodedFrame{Generation: 1, FrameID: 4}, h264TestConfig()); accept || request {
		t.Fatalf("waiting delta accept=%v request=%v", accept, request)
	}
	if accept, request := state.Observe(&desktopmedia.EncodedFrame{Generation: 1, FrameID: 5, KeyFrame: true}, h264TestConfig()); !accept || request {
		t.Fatalf("recovery keyframe accept=%v request=%v", accept, request)
	}
}

func TestH264RecoveryRejectsStaleFrame(t *testing.T) {
	var state h264RecoveryState
	state.Observe(&desktopmedia.EncodedFrame{Generation: 1, FrameID: 5, KeyFrame: true}, h264TestConfig())
	if accept, request := state.Observe(&desktopmedia.EncodedFrame{Generation: 1, FrameID: 4, KeyFrame: true}, h264TestConfig()); accept || request {
		t.Fatalf("stale frame accept=%v request=%v", accept, request)
	}
}

func TestH264RecoveryRetriesIDR(t *testing.T) {
	var state h264RecoveryState
	state.Observe(&desktopmedia.EncodedFrame{Generation: 1, FrameID: 1}, h264TestConfig())
	var requested bool
	for id := uint32(2); id <= 1+h264RecoveryRetryFrames; id++ {
		_, requested = state.Observe(&desktopmedia.EncodedFrame{Generation: 1, FrameID: id}, h264TestConfig())
	}
	if !requested {
		t.Fatal("IDR request was not retried")
	}
}

func TestH264RecoveryForceAndFailure(t *testing.T) {
	var state h264RecoveryState
	if !state.ForceRecovery() {
		t.Fatal("first forced recovery did not request IDR")
	}
	if state.ForceRecovery() {
		t.Fatal("duplicate forced recovery requested another IDR")
	}
	state.RequestFailed()
	if !state.ForceRecovery() {
		t.Fatal("failed IDR request was not retryable")
	}
}


func TestH265RecoveryUsesKeyFrameGate(t *testing.T) {
	var state h264RecoveryState
	config := protocol.DesktopVideoConfig{Generation: 2, Codec: "h265"}
	accept, request := state.Observe(
		&desktopmedia.EncodedFrame{Generation: 2, FrameID: 1},
		config,
	)
	if accept || !request {
		t.Fatalf("initial HEVC delta accept=%v request=%v", accept, request)
	}
	accept, request = state.Observe(
		&desktopmedia.EncodedFrame{Generation: 2, FrameID: 2, KeyFrame: true},
		config,
	)
	if !accept || request {
		t.Fatalf("HEVC keyframe accept=%v request=%v", accept, request)
	}
}
