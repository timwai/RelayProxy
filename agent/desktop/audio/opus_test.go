package audio

import (
	"encoding/binary"
	"math"
	"testing"
)

func TestNormalizeOpusConfigDefaults(t *testing.T) {
	cfg, err := NormalizeOpusConfig(OpusConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SampleRate != 48_000 || cfg.Channels != 2 || cfg.BitsPerSample != 16 ||
		cfg.FrameDurationMs != 20 || cfg.Bitrate != DefaultOpusBitrate {
		t.Fatalf("Opus config=%+v", cfg)
	}
	if cfg.PCMFrameBytes() != 3_840 {
		t.Fatalf("PCM frame bytes=%d want=3840", cfg.PCMFrameBytes())
	}
}

func TestNormalizeOpusConfigRejectsUnsupportedShape(t *testing.T) {
	tests := []OpusConfig{
		{SampleRate: 44_100},
		{SampleRate: 48_000, Channels: 3},
		{SampleRate: 48_000, Channels: 2, BitsPerSample: 24},
		{SampleRate: 48_000, Channels: 2, BitsPerSample: 16, FrameDurationMs: 10},
		{SampleRate: 48_000, Channels: 2, BitsPerSample: 16, FrameDurationMs: 20, Bitrate: 5_999},
	}
	for _, cfg := range tests {
		if _, err := NormalizeOpusConfig(cfg); err == nil {
			t.Fatalf("invalid Opus config accepted: %+v", cfg)
		}
	}
}

func testOpusPCM(cfg OpusConfig) []byte {
	pcm := make([]byte, cfg.PCMFrameBytes())
	samples := cfg.SampleRate * cfg.FrameDurationMs / 1000
	for i := range samples {
		value := int16(10_000 * math.Sin(2*math.Pi*440*float64(i)/float64(cfg.SampleRate)))
		for channel := range cfg.Channels {
			offset := (i*cfg.Channels + channel) * 2
			binary.LittleEndian.PutUint16(pcm[offset:offset+2], uint16(value))
		}
	}
	return pcm
}

func TestOpusEncodeDecodeTwentyMillisecondStereo(t *testing.T) {
	cfg, err := NormalizeOpusConfig(OpusConfig{Bitrate: 96_000})
	if err != nil {
		t.Fatal(err)
	}
	encoder, err := NewOpusEncoder(cfg)
	if err != nil {
		t.Fatal(err)
	}
	decoder, err := NewOpusDecoder(cfg)
	if err != nil {
		t.Fatal(err)
	}

	pcm := testOpusPCM(cfg)
	packet, err := encoder.EncodePCM(pcm)
	if err != nil {
		t.Fatal(err)
	}
	if len(packet) == 0 || len(packet) >= len(pcm) {
		t.Fatalf("Opus packet size=%d PCM=%d", len(packet), len(pcm))
	}
	decoded, err := decoder.DecodePacket(packet)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) != len(pcm) {
		t.Fatalf("decoded PCM bytes=%d want=%d", len(decoded), len(pcm))
	}
	var energy int64
	for i := 0; i+1 < len(decoded); i += 2 {
		v := int64(int16(binary.LittleEndian.Uint16(decoded[i : i+2])))
		if v < 0 {
			v = -v
		}
		energy += v
	}
	if energy == 0 {
		t.Fatal("decoded Opus frame is silent")
	}
}

func TestOpusCodecRejectsWrongFrameAndPacket(t *testing.T) {
	encoder, err := NewOpusEncoder(OpusConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := encoder.EncodePCM(make([]byte, 128)); err == nil {
		t.Fatal("wrong PCM frame size was accepted")
	}

	decoder, err := NewOpusDecoder(OpusConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decoder.DecodePacket(nil); err == nil {
		t.Fatal("empty Opus packet was accepted")
	}
	if _, err := decoder.DecodePacket(make([]byte, MaxOpusPacketBytes+1)); err == nil {
		t.Fatal("oversized Opus packet was accepted")
	}
}

func TestOpusDecoderPLCProducesPCMFrame(t *testing.T) {
	cfg, err := NormalizeOpusConfig(OpusConfig{Bitrate: 96_000})
	if err != nil {
		t.Fatal(err)
	}
	encoder, err := NewOpusEncoder(cfg)
	if err != nil {
		t.Fatal(err)
	}
	decoder, err := NewOpusDecoder(cfg)
	if err != nil {
		t.Fatal(err)
	}
	packet, err := encoder.EncodePCM(testOpusPCM(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decoder.DecodePacket(packet); err != nil {
		t.Fatal(err)
	}
	plc, err := decoder.DecodePLC()
	if err != nil {
		t.Fatal(err)
	}
	if len(plc) != cfg.PCMFrameBytes() {
		t.Fatalf("PLC PCM bytes=%d want=%d", len(plc), cfg.PCMFrameBytes())
	}
	var energy int64
	for i := 0; i+1 < len(plc); i += 2 {
		v := int64(int16(binary.LittleEndian.Uint16(plc[i : i+2])))
		if v < 0 {
			v = -v
		}
		energy += v
	}
	if energy == 0 {
		t.Fatal("primed Opus PLC frame is silent")
	}
}
