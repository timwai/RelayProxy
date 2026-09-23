package audio

import (
	"encoding/binary"
	"errors"
	"fmt"

	pionopus "github.com/pion/opus"
)

const (
	DefaultOpusBitrate     = 96_000
	DefaultOpusFrameMs     = 20
	MaxOpusPacketBytes     = 1_276
	minRelayOpusBitrate    = 6_000
	maxRelayOpusBitrate    = 510_000
	relayOpusSampleRate    = 48_000
	relayOpusBitsPerSample = 16
)

// OpusConfig describes the first compressed Relay Desktop audio format.
// The codec foundation intentionally matches the existing PCM bring-up:
// 48 kHz, mono/stereo, signed 16-bit PCM input/output, and 20 ms packets.
type OpusConfig struct {
	SampleRate      int
	Channels        int
	BitsPerSample   int
	FrameDurationMs int
	Bitrate         int
}

func NormalizeOpusConfig(cfg OpusConfig) (OpusConfig, error) {
	if cfg.SampleRate == 0 {
		cfg.SampleRate = relayOpusSampleRate
	}
	if cfg.SampleRate != relayOpusSampleRate {
		return OpusConfig{}, fmt.Errorf("unsupported Opus sample rate %d", cfg.SampleRate)
	}
	if cfg.Channels == 0 {
		cfg.Channels = 2
	}
	if cfg.Channels < 1 || cfg.Channels > 2 {
		return OpusConfig{}, fmt.Errorf("unsupported Opus channel count %d", cfg.Channels)
	}
	if cfg.BitsPerSample == 0 {
		cfg.BitsPerSample = relayOpusBitsPerSample
	}
	if cfg.BitsPerSample != relayOpusBitsPerSample {
		return OpusConfig{}, fmt.Errorf("unsupported Opus PCM bit depth %d", cfg.BitsPerSample)
	}
	if cfg.FrameDurationMs == 0 {
		cfg.FrameDurationMs = DefaultOpusFrameMs
	}
	if cfg.FrameDurationMs != DefaultOpusFrameMs {
		return OpusConfig{}, fmt.Errorf("unsupported Opus frame duration %d ms", cfg.FrameDurationMs)
	}
	if cfg.Bitrate == 0 {
		cfg.Bitrate = DefaultOpusBitrate
	}
	if cfg.Bitrate < minRelayOpusBitrate || cfg.Bitrate > maxRelayOpusBitrate {
		return OpusConfig{}, fmt.Errorf("invalid Opus bitrate %d", cfg.Bitrate)
	}
	return cfg, nil
}

func (c OpusConfig) PCMConfig() PCMConfig {
	return PCMConfig{
		SampleRate:    c.SampleRate,
		Channels:      c.Channels,
		BitsPerSample: c.BitsPerSample,
	}
}

func (c OpusConfig) PCMFrameBytes() int {
	if c.SampleRate <= 0 || c.Channels <= 0 || c.BitsPerSample <= 0 || c.FrameDurationMs <= 0 {
		return 0
	}
	return c.SampleRate * c.FrameDurationMs / 1000 * c.Channels * (c.BitsPerSample / 8)
}

type OpusEncoder struct {
	cfg OpusConfig
	enc *pionopus.Encoder
}

func NewOpusEncoder(cfg OpusConfig) (*OpusEncoder, error) {
	cfg, err := NormalizeOpusConfig(cfg)
	if err != nil {
		return nil, err
	}
	enc, err := pionopus.NewEncoder(
		pionopus.WithChannels(cfg.Channels),
		pionopus.WithBitrate(cfg.Bitrate),
	)
	if err != nil {
		return nil, fmt.Errorf("create Opus encoder: %w", err)
	}
	return &OpusEncoder{cfg: cfg, enc: enc}, nil
}

func (e *OpusEncoder) Config() OpusConfig {
	if e == nil {
		return OpusConfig{}
	}
	return e.cfg
}

func (e *OpusEncoder) SetLossRate(percent int) error {
	if e == nil || e.enc == nil {
		return errors.New("Relay Desktop Opus encoder is unavailable")
	}
	if err := e.enc.SetLossRate(percent); err != nil {
		return fmt.Errorf("set Opus loss rate: %w", err)
	}
	return nil
}

func (e *OpusEncoder) EncodePCM(pcm []byte) ([]byte, error) {
	if e == nil || e.enc == nil {
		return nil, errors.New("Relay Desktop Opus encoder is unavailable")
	}
	if len(pcm) != e.cfg.PCMFrameBytes() {
		return nil, fmt.Errorf("Opus PCM frame size %d does not match expected %d", len(pcm), e.cfg.PCMFrameBytes())
	}
	if err := e.cfg.PCMConfig().ValidatePayload(pcm); err != nil {
		return nil, err
	}
	packet := make([]byte, MaxOpusPacketBytes)
	n, err := e.enc.Encode(pcm, packet)
	if err != nil {
		return nil, fmt.Errorf("encode Opus frame: %w", err)
	}
	if n <= 0 || n > len(packet) {
		return nil, fmt.Errorf("Opus encoder returned invalid packet size %d", n)
	}
	return packet[:n], nil
}

type OpusDecoder struct {
	cfg OpusConfig
	dec pionopus.Decoder
}

func NewOpusDecoder(cfg OpusConfig) (*OpusDecoder, error) {
	cfg, err := NormalizeOpusConfig(cfg)
	if err != nil {
		return nil, err
	}
	dec, err := pionopus.NewDecoderWithOutput(cfg.SampleRate, cfg.Channels)
	if err != nil {
		return nil, fmt.Errorf("create Opus decoder: %w", err)
	}
	return &OpusDecoder{cfg: cfg, dec: dec}, nil
}

func (d *OpusDecoder) Config() OpusConfig {
	if d == nil {
		return OpusConfig{}
	}
	return d.cfg
}

func (d *OpusDecoder) DecodePacket(packet []byte) ([]byte, error) {
	if d == nil {
		return nil, errors.New("Relay Desktop Opus decoder is unavailable")
	}
	if len(packet) == 0 {
		return nil, errors.New("Opus packet is empty")
	}
	if len(packet) > MaxOpusPacketBytes {
		return nil, fmt.Errorf("Opus packet size %d exceeds %d", len(packet), MaxOpusPacketBytes)
	}
	pcm := make([]byte, d.cfg.PCMFrameBytes())
	if _, _, err := d.dec.Decode(packet, pcm); err != nil {
		return nil, fmt.Errorf("decode Opus frame: %w", err)
	}
	return pcm, nil
}

func (d *OpusDecoder) DecodePLC() ([]byte, error) {
	if d == nil {
		return nil, errors.New("Relay Desktop Opus decoder is unavailable")
	}
	samplesPerChannel := d.cfg.SampleRate * d.cfg.FrameDurationMs / 1000
	pcm16 := make([]int16, samplesPerChannel*d.cfg.Channels)
	if err := d.dec.DecodePLC(pcm16); err != nil {
		return nil, fmt.Errorf("decode Opus PLC frame: %w", err)
	}
	pcm := make([]byte, len(pcm16)*2)
	for i, sample := range pcm16 {
		binary.LittleEndian.PutUint16(pcm[i*2:i*2+2], uint16(sample))
	}
	return pcm, nil
}
