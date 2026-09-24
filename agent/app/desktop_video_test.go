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
	target := videoTarget(protocol.DesktopCodecCapability{Codec: "h265", Encode: true, Chroma420: true})
	local := []protocol.DesktopCodecCapability{{Codec: "h265", Decode: true, Chroma420: true}}
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
	target := videoTarget(protocol.DesktopCodecCapability{Codec: "h265", Decode: true, Chroma420: true})
	local := []protocol.DesktopCodecCapability{{Codec: "h265", Decode: true, Chroma420: true}}
	if _, err := negotiateRemoteDesktopVideo(
		target,
		local,
		protocol.RemoteDesktopConnectOptions{Codec: "h265"},
	); err == nil {
		t.Fatal("H.265 request without target encoder was accepted")
	}
}

func TestNegotiateRemoteDesktopVideoRejectsH265WithoutLocalDecoder(t *testing.T) {
	target := videoTarget(protocol.DesktopCodecCapability{Codec: "h265", Encode: true, Chroma420: true})
	local := []protocol.DesktopCodecCapability{{Codec: "h265", Encode: true, Chroma420: true}}
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

func TestNegotiateRemoteDesktopVideoNormalizesChroma(t *testing.T) {
	options, err := negotiateRemoteDesktopVideo(
		videoTarget(),
		nil,
		protocol.RemoteDesktopConnectOptions{Codec: "h264", Chroma: ""},
	)
	if err != nil {
		t.Fatal(err)
	}
	if options.Chroma != protocol.DesktopChromaAuto {
		t.Fatalf("chroma=%q want auto", options.Chroma)
	}
	if _, err := negotiateRemoteDesktopVideo(
		videoTarget(),
		nil,
		protocol.RemoteDesktopConnectOptions{Codec: "h264", Chroma: "422"},
	); err == nil {
		t.Fatal("unsupported chroma was accepted")
	}
}

func TestNegotiateRemoteDesktopVideoAccepts444OnlyWhenBothSidesAdvertiseIt(t *testing.T) {
	target := videoTarget(protocol.DesktopCodecCapability{
		Codec: "h264", Encode: true, Chroma420: true, Chroma444: true, BitDepth8: true,
	})
	local := []protocol.DesktopCodecCapability{{
		Codec: "h264", Decode: true, Chroma420: true, Chroma444: true, BitDepth8: true,
	}}
	options, err := negotiateRemoteDesktopVideo(
		target,
		local,
		protocol.RemoteDesktopConnectOptions{Codec: "h264", Chroma: protocol.DesktopChroma444},
	)
	if err != nil {
		t.Fatal(err)
	}
	if options.Chroma != protocol.DesktopChroma444 {
		t.Fatalf("chroma=%q want 444", options.Chroma)
	}
}

func TestNegotiateRemoteDesktopVideoRejects444WithoutTargetSupport(t *testing.T) {
	target := videoTarget(protocol.DesktopCodecCapability{
		Codec: "h264", Encode: true, Chroma420: true, BitDepth8: true,
	})
	local := []protocol.DesktopCodecCapability{{
		Codec: "h264", Decode: true, Chroma420: true, Chroma444: true, BitDepth8: true,
	}}
	if _, err := negotiateRemoteDesktopVideo(
		target,
		local,
		protocol.RemoteDesktopConnectOptions{Codec: "h264", Chroma: protocol.DesktopChroma444},
	); err == nil {
		t.Fatal("4:4:4 request without target support was accepted")
	}
}

func TestNegotiateRemoteDesktopVideoRejects444WithoutLocalSupport(t *testing.T) {
	target := videoTarget(protocol.DesktopCodecCapability{
		Codec: "h264", Encode: true, Chroma420: true, Chroma444: true, BitDepth8: true,
	})
	local := []protocol.DesktopCodecCapability{{
		Codec: "h264", Decode: true, Chroma420: true, BitDepth8: true,
	}}
	if _, err := negotiateRemoteDesktopVideo(
		target,
		local,
		protocol.RemoteDesktopConnectOptions{Codec: "h264", Chroma: protocol.DesktopChroma444},
	); err == nil {
		t.Fatal("4:4:4 request without local support was accepted")
	}
}

func TestNegotiateRemoteDesktopVideoRejects444WithAutoCodec(t *testing.T) {
	if _, err := negotiateRemoteDesktopVideo(
		videoTarget(),
		nil,
		protocol.RemoteDesktopConnectOptions{Codec: "auto", Chroma: protocol.DesktopChroma444},
	); err == nil {
		t.Fatal("4:4:4 request with auto codec was accepted")
	}
}


func TestNegotiateRemoteDesktopVideoRejectsH265AutoWithout420(t *testing.T) {
	target := videoTarget(protocol.DesktopCodecCapability{
		Codec: "h265", Encode: true, Decode: true, Chroma444: true, BitDepth8: true,
	})
	local := []protocol.DesktopCodecCapability{{
		Codec: "h265", Encode: true, Decode: true, Chroma444: true, BitDepth8: true,
	}}
	if _, err := negotiateRemoteDesktopVideo(
		target,
		local,
		protocol.RemoteDesktopConnectOptions{Codec: "h265", Chroma: protocol.DesktopChromaAuto},
	); err == nil {
		t.Fatal("H.265 auto accepted a 4:4:4-only target")
	}
}

func TestNegotiateRemoteDesktopVideoRejectsH265420WithoutLocal420(t *testing.T) {
	target := videoTarget(protocol.DesktopCodecCapability{
		Codec: "h265", Encode: true, Chroma420: true, BitDepth8: true,
	})
	local := []protocol.DesktopCodecCapability{{
		Codec: "h265", Decode: true, Chroma444: true, BitDepth8: true,
	}}
	if _, err := negotiateRemoteDesktopVideo(
		target,
		local,
		protocol.RemoteDesktopConnectOptions{Codec: "h265", Chroma: protocol.DesktopChroma420},
	); err == nil {
		t.Fatal("H.265 4:2:0 accepted a 4:4:4-only local decoder")
	}
}

func TestNegotiateRemoteDesktopVideoAcceptsH265444OneVPLStyleCapability(t *testing.T) {
	target := videoTarget(protocol.DesktopCodecCapability{
		Codec: "h265", Encode: true, Decode: true, Chroma444: true, BitDepth8: true,
	})
	local := []protocol.DesktopCodecCapability{{
		Codec: "h265", Encode: true, Decode: true, Chroma444: true, BitDepth8: true,
	}}
	options, err := negotiateRemoteDesktopVideo(
		target,
		local,
		protocol.RemoteDesktopConnectOptions{Codec: "h265", Chroma: protocol.DesktopChroma444},
	)
	if err != nil {
		t.Fatal(err)
	}
	if options.Codec != "h265" || options.Chroma != protocol.DesktopChroma444 {
		t.Fatalf("negotiated options=%+v", options)
	}
}


func TestNegotiateRemoteDesktopVideoPrefersDirectionalChromaOverLegacyFlags(t *testing.T) {
	target := videoTarget(protocol.DesktopCodecCapability{
		Codec:        "h265",
		Encode:       true,
		Decode:       true,
		Chroma420:    true,
		Chroma444:    true,
		EncodeChroma: []string{"444"},
		DecodeChroma: []string{"420", "444"},
		BitDepth8:    true,
	})
	local := []protocol.DesktopCodecCapability{{
		Codec:        "h265",
		Encode:       true,
		Decode:       true,
		Chroma420:    true,
		Chroma444:    true,
		EncodeChroma: []string{"420", "444"},
		DecodeChroma: []string{"420", "444"},
		BitDepth8:    true,
	}}
	if _, err := negotiateRemoteDesktopVideo(
		target,
		local,
		protocol.RemoteDesktopConnectOptions{Codec: "h265", Chroma: protocol.DesktopChroma420},
	); err == nil {
		t.Fatal("directional encode chroma was ignored in favor of legacy shared Chroma420")
	}
}

func TestNegotiateRemoteDesktopVideoAcceptsDirectionalH265444(t *testing.T) {
	target := videoTarget(protocol.DesktopCodecCapability{
		Codec:        "h265",
		Encode:       true,
		Decode:       true,
		EncodeChroma: []string{"444"},
		DecodeChroma: []string{"444"},
		BitDepth8:    true,
	})
	local := []protocol.DesktopCodecCapability{{
		Codec:        "h265",
		Encode:       true,
		Decode:       true,
		EncodeChroma: []string{"444"},
		DecodeChroma: []string{"444"},
		BitDepth8:    true,
	}}
	if _, err := negotiateRemoteDesktopVideo(
		target,
		local,
		protocol.RemoteDesktopConnectOptions{Codec: "h265", Chroma: protocol.DesktopChroma444},
	); err != nil {
		t.Fatal(err)
	}
}
