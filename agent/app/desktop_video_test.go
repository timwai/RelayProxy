package app

import (
	"testing"

	"relayproxy/internal/protocol"
)

func videoTarget(codecs ...protocol.DesktopCodecCapability) protocol.RemoteDesktopTarget {
	return protocol.RemoteDesktopTarget{
		DeviceID: "target",
		Online:   true,
		Capabilities: protocol.DesktopCapabilities{
			RelayDesktop: true,
			Codecs:       codecs,
		},
	}
}

func TestNegotiateRemoteDesktopVideoAcceptsH265WhenBothSidesSupportIt(t *testing.T) {
	target := videoTarget(protocol.DesktopCodecCapability{Codec: "h265", Encode: true})
	local := []protocol.DesktopCodecCapability{{Codec: "h265", Decode: true}}
	options, err := negotiateRemoteDesktopVideo(
		target,
		local,
		protocol.RemoteDesktopConnectOptions{Codec: "HEVC"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if options.Codec != "h265" {
		t.Fatalf("codec=%q want h265", options.Codec)
	}
}

func TestNegotiateRemoteDesktopVideoRejectsH265WithoutTargetEncoder(t *testing.T) {
	target := videoTarget(protocol.DesktopCodecCapability{Codec: "h265", Decode: true})
	local := []protocol.DesktopCodecCapability{{Codec: "h265", Decode: true}}
	if _, err := negotiateRemoteDesktopVideo(
		target,
		local,
		protocol.RemoteDesktopConnectOptions{Codec: "h265"},
	); err == nil {
		t.Fatal("H.265 request without target encoder was accepted")
	}
}

func TestNegotiateRemoteDesktopVideoRejectsH265WithoutLocalDecoder(t *testing.T) {
	target := videoTarget(protocol.DesktopCodecCapability{Codec: "h265", Encode: true})
	local := []protocol.DesktopCodecCapability{{Codec: "h265", Encode: true}}
	if _, err := negotiateRemoteDesktopVideo(
		target,
		local,
		protocol.RemoteDesktopConnectOptions{Codec: "h265"},
	); err == nil {
		t.Fatal("H.265 request without local decoder was accepted")
	}
}

func TestNegotiateRemoteDesktopVideoKeepsAutoOnEstablishedCodecPath(t *testing.T) {
	options, err := negotiateRemoteDesktopVideo(
		videoTarget(),
		nil,
		protocol.RemoteDesktopConnectOptions{Codec: "auto"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if options.Codec != "auto" {
		t.Fatalf("auto codec=%q", options.Codec)
	}
}

func TestNegotiateRemoteDesktopVideoPreservesHEVCValidationSentinel(t *testing.T) {
	options, err := negotiateRemoteDesktopVideo(
		videoTarget(),
		nil,
		protocol.RemoteDesktopConnectOptions{Codec: protocol.DesktopCodecH265Validation},
	)
	if err != nil {
		t.Fatal(err)
	}
	if options.Codec != protocol.DesktopCodecH265Validation {
		t.Fatalf("validation codec=%q", options.Codec)
	}
}
