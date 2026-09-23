package codec

import "testing"

func h265NAL(nalType byte, payload ...byte) []byte {
	nal := []byte{0, 0, 0, 1, nalType << 1, 1}
	return append(nal, payload...)
}

func TestH265NALUnitType(t *testing.T) {
	for _, tc := range []struct {
		nalType byte
		want    uint8
	}{
		{32, 32},
		{33, 33},
		{34, 34},
		{19, 19},
	} {
		nal := h265NAL(tc.nalType)
		units := annexBNALUnits(nal)
		if len(units) != 1 {
			t.Fatalf("type=%d units=%d", tc.nalType, len(units))
		}
		if got := H265NALUnitType(units[0]); got != tc.want {
			t.Fatalf("type=%d got=%d want=%d", tc.nalType, got, tc.want)
		}
	}
}

func TestH265HasParameterSets(t *testing.T) {
	header := append(h265NAL(32), h265NAL(33)...)
	header = append(header, h265NAL(34)...)
	if !H265HasParameterSets(header) {
		t.Fatal("complete VPS/SPS/PPS was not recognized")
	}
	if H265HasParameterSets(append(h265NAL(33), h265NAL(34)...)) {
		t.Fatal("SPS/PPS without VPS was accepted")
	}
}

func TestH265WithSequenceHeader(t *testing.T) {
	header := append(h265NAL(32), h265NAL(33)...)
	header = append(header, h265NAL(34)...)
	idr := h265NAL(19, 1, 2, 3)

	got := H265WithSequenceHeader(idr, header)
	if len(got) != len(header)+len(idr) {
		t.Fatalf("len=%d want=%d", len(got), len(header)+len(idr))
	}
	already := append(append([]byte(nil), header...), idr...)
	got = H265WithSequenceHeader(already, header)
	if len(got) != len(already) {
		t.Fatalf("duplicated HEVC sequence header: %d != %d", len(got), len(already))
	}
}


func TestH265MediaFoundationLevel(t *testing.T) {
	tests := []struct {
		name string
		cfg  VideoConfig
		want uint32
	}{
		{
			name: "720p30",
			cfg:  VideoConfig{Width: 1280, Height: 720, FPS: 30, TargetBitrate: 6_000_000},
			want: 4,
		},
		{
			name: "1080p30",
			cfg:  VideoConfig{Width: 1920, Height: 1080, FPS: 30, TargetBitrate: 12_000_000},
			want: 5,
		},
		{
			name: "1080p60",
			cfg:  VideoConfig{Width: 1920, Height: 1080, FPS: 60, TargetBitrate: 12_000_000},
			want: 6,
		},
		{
			name: "4k60",
			cfg:  VideoConfig{Width: 3840, Height: 2160, FPS: 60, TargetBitrate: 20_000_000},
			want: 8,
		},
		{
			name: "4k60 high bitrate",
			cfg:  VideoConfig{Width: 3840, Height: 2160, FPS: 60, TargetBitrate: 100_000_000},
			want: 11,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := h265MediaFoundationLevel(tc.cfg); got != tc.want {
				t.Fatalf("level=%d want=%d", got, tc.want)
			}
		})
	}
}
