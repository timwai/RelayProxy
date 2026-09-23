package codec

import "testing"

func TestH265CodecStringFromSPS(t *testing.T) {
	// Synthetic Main-profile SPS profile_tier_level:
	// profile_space=0, tier=main, profile_idc=1,
	// compatibility flags reverse to 0x6, level_idc=93, constraint=B0.
	header := []byte{
		0, 0, 0, 1, 0x42, 0x01,
		0x01,
		0x01,
		0x60, 0x00, 0x00, 0x00,
		0xB0, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x5D,
	}
	if got, want := H265CodecString(header), "hvc1.1.6.L93.B0"; got != want {
		t.Fatalf("codec string=%q want=%q", got, want)
	}
}

func TestH265CodecStringProfileSpaceTierAndConstraints(t *testing.T) {
	header := []byte{
		0, 0, 1, 0x42, 0x01,
		0x01,
		0xA2, // profile_space=B, high tier, profile_idc=2
		0x80, 0x00, 0x00, 0x00, // reverse -> 1
		0x90, 0x01, 0, 0, 0, 0,
		0x78, // level_idc=120
	}
	if got, want := H265CodecString(header), "hvc1.B2.1.H120.90.01"; got != want {
		t.Fatalf("codec string=%q want=%q", got, want)
	}
}

func TestH265CodecStringFallsBackWithoutSPS(t *testing.T) {
	if got, want := H265CodecString(nil), "hvc1.1.6.L93.B0"; got != want {
		t.Fatalf("codec string=%q want=%q", got, want)
	}
}
