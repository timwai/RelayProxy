package desktop

import (
	"context"
	"errors"
	"fmt"
	"time"

	desktopaudio "relayproxy/agent/desktop/audio"
	desktopmedia "relayproxy/internal/desktop"
	"relayproxy/internal/protocol"
)

const hostAudioFrameDuration = 20 * time.Millisecond

var hostAudioPCMConfig = desktopaudio.PCMConfig{
	SampleRate:    48_000,
	Channels:      2,
	BitsPerSample: 16,
}

type AudioCapabilitySource interface {
	DesktopAudioAvailable() bool
}

type audioCaptureFactory func(context.Context, desktopaudio.PCMConfig, time.Duration) (desktopaudio.Capture, error)

func openDefaultAudioCapture(ctx context.Context, cfg desktopaudio.PCMConfig, duration time.Duration) (desktopaudio.Capture, error) {
	return desktopaudio.OpenLoopbackCapture(ctx, cfg, duration)
}

func desktopAudioEnabled(options protocol.RemoteDesktopConnectOptions) bool {
	return options.Audio == nil || *options.Audio
}

func (h *Host) desktopAudioAvailable() bool {
	if h == nil || h.source == nil {
		return false
	}
	provider, ok := h.source.(AudioCapabilitySource)
	return ok && provider.DesktopAudioAvailable()
}

func (h *Host) streamSessionAudio(ctx context.Context, conn *desktopmedia.MediaConn) error {
	if h == nil || conn == nil || h.audioOpen == nil {
		return errors.New("Relay Desktop audio capture is unavailable")
	}
	cfg, duration, frameBytes, err := desktopaudio.NormalizeFrameDuration(hostAudioPCMConfig, hostAudioFrameDuration)
	if err != nil {
		return err
	}
	capture, err := h.audioOpen(ctx, cfg, duration)
	if err != nil {
		return fmt.Errorf("open Relay Desktop loopback capture: %w", err)
	}
	defer capture.Close()

	generation := uint32(1)
	audioConfig := protocol.DesktopAudioConfig{
		Generation:      generation,
		Codec:           protocol.DesktopAudioCodecPCMS16LE,
		SampleRate:      cfg.SampleRate,
		Channels:        cfg.Channels,
		BitsPerSample:   cfg.BitsPerSample,
		FrameDurationMs: int(duration / time.Millisecond),
		TargetBitrate:   cfg.BytesPerSecond() * 8,
	}
	if err := conn.SendSessionMessage(ctx, protocol.DesktopSessionMessage{
		Type:        protocol.DesktopSessionAudioConfig,
		AudioConfig: &audioConfig,
	}); err != nil {
		return fmt.Errorf("send Relay Desktop audio config: %w", err)
	}

	sessionID, err := newMediaSessionID()
	if err != nil {
		return err
	}
	var frameID uint32 = 1
	var sequence uint32 = 1
	started := time.Now()

	for {
		data, err := capture.Read(ctx)
		if err != nil {
			return err
		}
		if len(data) != frameBytes {
			return fmt.Errorf("Relay Desktop audio capture frame size=%d want=%d", len(data), frameBytes)
		}
		if err := cfg.ValidatePayload(data); err != nil {
			return err
		}
		frame := desktopmedia.EncodedFrame{
			Type:       desktopmedia.MediaPacketAudio,
			SessionID:  sessionID,
			StreamID:   desktopmedia.MediaStreamAudioID,
			Generation: generation,
			FrameID:    frameID,
			Timestamp:  uint64(time.Since(started).Microseconds()),
			Data:       data,
		}
		packets, next, err := desktopmedia.PacketizeMediaFrame(frame, h.cfg.PacketSize, sequence)
		if err != nil {
			return err
		}
		for _, packet := range packets {
			if err := conn.Send(ctx, packet); err != nil {
				return err
			}
		}
		frameID++
		sequence = next
	}
}
