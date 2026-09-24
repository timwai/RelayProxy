package codec

import (
	"fmt"
	"strings"
)

func annexBNALUnits(data []byte) [][]byte {
	var units [][]byte
	findStart := func(from int) (int, int) {
		for i := from; i+3 <= len(data); i++ {
			if i+4 <= len(data) && data[i] == 0 && data[i+1] == 0 && data[i+2] == 0 && data[i+3] == 1 {
				return i, 4
			}
			if data[i] == 0 && data[i+1] == 0 && data[i+2] == 1 {
				return i, 3
			}
		}
		return -1, 0
	}
	offset := 0
	for {
		start, prefix := findStart(offset)
		if start < 0 {
			break
		}
		payloadStart := start + prefix
		next, _ := findStart(payloadStart)
		if next < 0 {
			if payloadStart < len(data) {
				units = append(units, data[payloadStart:])
			}
			break
		}
		if next > payloadStart {
			units = append(units, data[payloadStart:next])
		}
		offset = next
	}
	return units
}

func H264CodecString(sequenceHeader []byte) string {
	for _, nal := range annexBNALUnits(sequenceHeader) {
		if len(nal) >= 4 && nal[0]&0x1f == 7 {
			return fmt.Sprintf("avc1.%02X%02X%02X", nal[1], nal[2], nal[3])
		}
	}
	return "avc1.42E01F"
}

func H264HasParameterSets(data []byte) bool {
	hasSPS, hasPPS := false, false
	for _, nal := range annexBNALUnits(data) {
		if len(nal) == 0 {
			continue
		}
		switch nal[0] & 0x1f {
		case 7:
			hasSPS = true
		case 8:
			hasPPS = true
		}
	}
	return hasSPS && hasPPS
}

func H264WithSequenceHeader(data, sequenceHeader []byte) []byte {
	if len(sequenceHeader) == 0 || H264HasParameterSets(data) {
		return append([]byte(nil), data...)
	}
	out := make([]byte, 0, len(sequenceHeader)+len(data))
	out = append(out, sequenceHeader...)
	out = append(out, data...)
	return out
}

func NormalizeCodecPreference(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "auto":
		return "auto"
	case "jpeg", "jpg":
		return "jpeg"
	case "h264", "avc", "avc1":
		return "h264"
	case "h265", "hevc", "hvc1", "hev1":
		return "h265"
	default:
		return "auto"
	}
}
