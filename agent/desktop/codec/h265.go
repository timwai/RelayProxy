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

type h265LevelLimit struct {
	value             uint32
	maxLumaPicture    int64
	maxLumaSampleRate int64
	maxBitrate        int64
}

var h265MainTierLevels = [...]h265LevelLimit{
	{value: 1, maxLumaPicture: 122_880, maxLumaSampleRate: 3_686_400, maxBitrate: 1_500_000},           // Level 2
	{value: 2, maxLumaPicture: 245_760, maxLumaSampleRate: 7_372_800, maxBitrate: 3_000_000},           // Level 2.1
	{value: 3, maxLumaPicture: 552_960, maxLumaSampleRate: 16_588_800, maxBitrate: 6_000_000},          // Level 3
	{value: 4, maxLumaPicture: 983_040, maxLumaSampleRate: 33_177_600, maxBitrate: 10_000_000},         // Level 3.1
	{value: 5, maxLumaPicture: 2_228_224, maxLumaSampleRate: 66_846_720, maxBitrate: 12_000_000},       // Level 4
	{value: 6, maxLumaPicture: 2_228_224, maxLumaSampleRate: 133_693_440, maxBitrate: 20_000_000},      // Level 4.1
	{value: 7, maxLumaPicture: 8_912_896, maxLumaSampleRate: 267_386_880, maxBitrate: 25_000_000},      // Level 5
	{value: 8, maxLumaPicture: 8_912_896, maxLumaSampleRate: 534_773_760, maxBitrate: 40_000_000},      // Level 5.1
	{value: 9, maxLumaPicture: 8_912_896, maxLumaSampleRate: 1_069_547_520, maxBitrate: 60_000_000},    // Level 5.2
	{value: 10, maxLumaPicture: 35_651_584, maxLumaSampleRate: 1_069_547_520, maxBitrate: 60_000_000},  // Level 6
	{value: 11, maxLumaPicture: 35_651_584, maxLumaSampleRate: 2_139_095_040, maxBitrate: 120_000_000}, // Level 6.1
	{value: 12, maxLumaPicture: 35_651_584, maxLumaSampleRate: 4_278_190_080, maxBitrate: 240_000_000}, // Level 6.2
}

func h265MediaFoundationLevel(cfg VideoConfig) uint32 {
	lumaPicture := int64(cfg.Width) * int64(cfg.Height)
	lumaSampleRate := lumaPicture * int64(cfg.FPS)
	bitrate := int64(cfg.TargetBitrate)
	for _, level := range h265MainTierLevels {
		if lumaPicture <= level.maxLumaPicture &&
			lumaSampleRate <= level.maxLumaSampleRate &&
			bitrate <= level.maxBitrate {
			return level.value
		}
	}
	return h265MainTierLevels[len(h265MainTierLevels)-1].value
}
