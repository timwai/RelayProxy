package codec

func H265NALUnitType(nal []byte) uint8 {
	if len(nal) == 0 {
		return 0
	}
	return (nal[0] >> 1) & 0x3f
}

func H265HasParameterSets(data []byte) bool {
	hasVPS, hasSPS, hasPPS := false, false, false
	for _, nal := range annexBNALUnits(data) {
		switch H265NALUnitType(nal) {
		case 32:
			hasVPS = true
		case 33:
			hasSPS = true
		case 34:
			hasPPS = true
		}
	}
	return hasVPS && hasSPS && hasPPS
}

func H265WithSequenceHeader(data, sequenceHeader []byte) []byte {
	if len(sequenceHeader) == 0 || H265HasParameterSets(data) {
		return append([]byte(nil), data...)
	}
	out := make([]byte, 0, len(sequenceHeader)+len(data))
	out = append(out, sequenceHeader...)
	out = append(out, data...)
	return out
}
