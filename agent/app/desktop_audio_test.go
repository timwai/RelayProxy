package app

import (
	"testing"

	"relayproxy/internal/protocol"
)

func audioTarget(codecs ...string) protocol.RemoteDesktopTarget {
	return protocol.RemoteDesktopTarget{
		DeviceID: "target",
		Online:   true,
		Capabilities: protocol.DesktopCapabilities{
			RelayDesktop: true,
			Audio:        true,
			AudioCodecs:  codecs,
		},
	}
}

func TestNegotiateRemoteDesktopAudioPrefersOpus(t *testing.T) {
	options, err := negotiateRemoteDesktopAudio(
		audioTarget(protocol.DesktopAudioCodecOpus, protocol.DesktopAudioCodecPCMS16LE),
		protocol.RemoteDesktopConnectOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if options.AudioCodec != protocol.DesktopAudioCodecOpus {
		t.Fatalf("audio codec=%q want opus", options.AudioCodec)
	}
}

func TestNegotiateRemoteDesktopAudioFallsBackToPCMForLegacyTarget(t *testing.T) {
	options, err := negotiateRemoteDesktopAudio(audioTarget(), protocol.RemoteDesktopConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if options.AudioCodec != protocol.DesktopAudioCodecPCMS16LE {
		t.Fatalf("audio codec=%q want PCM fallback", options.AudioCodec)
	}
}

func TestNegotiateRemoteDesktopAudioHonorsDisable(t *testing.T) {
	disabled := false
	options, err := negotiateRemoteDesktopAudio(
		audioTarget(protocol.DesktopAudioCodecOpus),
		protocol.RemoteDesktopConnectOptions{Audio: &disabled, AudioCodec: protocol.DesktopAudioCodecOpus},
	)
	if err != nil {
		t.Fatal(err)
	}
	if options.AudioCodec != "" {
		t.Fatalf("disabled audio retained codec %q", options.AudioCodec)
	}
}

func TestNegotiateRemoteDesktopAudioRejectsUnsupportedExplicitCodec(t *testing.T) {
	_, err := negotiateRemoteDesktopAudio(
		audioTarget(protocol.DesktopAudioCodecPCMS16LE),
		protocol.RemoteDesktopConnectOptions{AudioCodec: protocol.DesktopAudioCodecOpus},
	)
	if err == nil {
		t.Fatal("unsupported explicit Opus request was accepted")
	}
}

func TestTargetSupportsDesktopAudioCodecLegacyPCMOnly(t *testing.T) {
	caps := audioTarget().Capabilities
	if !targetSupportsDesktopAudioCodec(caps, protocol.DesktopAudioCodecPCMS16LE) {
		t.Fatal("legacy audio target did not imply PCM")
	}
	if targetSupportsDesktopAudioCodec(caps, protocol.DesktopAudioCodecOpus) {
		t.Fatal("legacy audio target incorrectly implied Opus")
	}
}
