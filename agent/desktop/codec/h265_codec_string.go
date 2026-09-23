package codec

import (
	"encoding/binary"
	"fmt"
	"math/bits"
	"strings"
)

func h265NALRBSP(nal []byte) []byte {
	if len(nal) <= 2 {
		return nil
	}
	src := nal[2:]
	out := make([]byte, 0, len(src))
	zeros := 0
	for _, b := range src {
		if zeros >= 2 && b == 0x03 {
			zeros = 0
			continue
		}
		out = append(out, b)
		if b == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out
}

// H265CodecString returns an RFC 6381 / ISO-BMFF HEVC codec string derived
// from the SPS profile_tier_level. Relay Desktop emits Annex-B HEVC, but the
// generation metadata uses the hvc1 form because it describes the decoder
// profile/level rather than the transport framing.
func H265CodecString(sequenceHeader []byte) string {
	for _, nal := range annexBNALUnits(sequenceHeader) {
		if len(nal) < 2 || H265NALUnitType(nal) != 33 {
			continue
		}
		rbsp := h265NALRBSP(nal)
		if len(rbsp) < 13 {
			continue
		}

		profileSpace := (rbsp[1] >> 6) & 0x03
		tierFlag := (rbsp[1] >> 5) & 0x01
		profileIDC := rbsp[1] & 0x1f
		compatibility := bits.Reverse32(binary.BigEndian.Uint32(rbsp[2:6]))
		levelIDC := rbsp[12]

		space := [...]string{"", "A", "B", "C"}[profileSpace]
		tier := "L"
		if tierFlag != 0 {
			tier = "H"
		}
		base := fmt.Sprintf(
			"hvc1.%s%d.%X.%s%d",
			space,
			profileIDC,
			compatibility,
			tier,
			levelIDC,
		)

		constraints := rbsp[6:12]
		last := len(constraints) - 1
		for last >= 0 && constraints[last] == 0 {
			last--
		}
		if last < 0 {
			return base + ".0"
		}
		var tail strings.Builder
		for i := 0; i <= last; i++ {
			fmt.Fprintf(&tail, ".%02X", constraints[i])
		}
		return base + tail.String()
	}
	return "hvc1.1.6.L93.B0"
}
