package codec

import "testing"

func TestH264CodecStringFromAnnexBSPS(t *testing.T) {
	header := []byte{0, 0, 0, 1, 0x67, 0x64, 0x00, 0x28, 0xaa, 0, 0, 1, 0x68, 0xee}
	if got := H264CodecString(header); got != "avc1.640028" {
		t.Fatalf("codec=%q", got)
	}
}

func TestH264WithSequenceHeaderAvoidsDuplicateParameterSets(t *testing.T) {
	header := []byte{0, 0, 0, 1, 0x67, 0x42, 0xe0, 0x1f, 0, 0, 0, 1, 0x68, 1}
	idr := []byte{0, 0, 0, 1, 0x65, 2, 3}
	got := H264WithSequenceHeader(idr, header)
	if len(got) != len(header)+len(idr) {
		t.Fatalf("len=%d", len(got))
	}
	already := append(append([]byte(nil), header...), idr...)
	got = H264WithSequenceHeader(already, header)
	if len(got) != len(already) {
		t.Fatalf("duplicated sequence header: %d != %d", len(got), len(already))
	}
}


func TestNormalizeCodecPreferenceDoesNotEnableH265BeforeSessionSupport(t *testing.T) {
	if got := NormalizeCodecPreference("h265"); got != "auto" {
		t.Fatalf("H.265 became selectable before session support: %q", got)
	}
	if got := NormalizeCodecPreference("hevc"); got != "auto" {
		t.Fatalf("HEVC became selectable before session support: %q", got)
	}
}
