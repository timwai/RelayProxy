package desktop

import (
	"context"
	"errors"
	"image"
	"log"
	"time"

	desktopcodec "relayproxy/agent/desktop/codec"
	desktopmedia "relayproxy/internal/desktop"
	"relayproxy/internal/protocol"
)

func (h *Host) canEncodeH264() bool {
	for _, capability := range h.CodecCapabilities() {
		if capability.Codec == "h264" && capability.Encode {
			return true
		}
	}
	return false
}

func sendVideoConfig(ctx context.Context, conn *desktopmedia.MediaConn, cfg protocol.DesktopVideoConfig) error {
	return conn.SendSessionMessage(ctx, protocol.DesktopSessionMessage{
		Type:        protocol.DesktopSessionVideoConfig,
		VideoConfig: &cfg,
	})
}

func (h *Host) streamSessionFrames(
	ctx context.Context,
	conn *desktopmedia.MediaConn,
	cfg HostConfig,
	options protocol.RemoteDesktopConnectOptions,
	idrRequests <-chan struct{},
	bitrateUpdates <-chan int,
	fpsUpdates <-chan int,
) error {
	preference := desktopcodec.NormalizeCodecPreference(options.Codec)
	if preference == "h264" && h.canEncodeH264() {
		if err := h.streamH264Frames(ctx, conn, cfg, idrRequests, bitrateUpdates, fpsUpdates); err == nil || errors.Is(err, context.Canceled) {
			return err
		} else {
			log.Printf("[Desktop] H.264 session unavailable, falling back to JPEG: %v", err)
		}
	}
	if err := sendVideoConfig(ctx, conn, protocol.DesktopVideoConfig{
		Generation:    1,
		Codec:         "jpeg",
		Width:         cfg.MaxWidth,
		Height:        cfg.MaxHeight,
		FPS:           cfg.MaxFPS,
		TargetBitrate: cfg.MaxBitrate,
	}); err != nil {
		return err
	}
	return h.streamFrames(ctx, conn, cfg, fpsUpdates)
}

func fitRGBAEven(src *image.RGBA, maxWidth, maxHeight int) *image.RGBA {
	frame := fitRGBA(src, maxWidth, maxHeight)
	if frame == nil {
		return nil
	}
	b := frame.Bounds()
	width, height := b.Dx()&^1, b.Dy()&^1
	if width < 2 || height < 2 {
		return frame
	}
	if width == b.Dx() && height == b.Dy() {
		return frame
	}
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		srcOffset := frame.PixOffset(b.Min.X, b.Min.Y+y)
		dstOffset := dst.PixOffset(0, y)
		copy(dst.Pix[dstOffset:dstOffset+width*4], frame.Pix[srcOffset:srcOffset+width*4])
	}
	return dst
}

func sendEncodedDesktopFrame(
	ctx context.Context,
	conn *desktopmedia.MediaConn,
	frame desktopmedia.EncodedFrame,
	packetSize int,
	sequence *uint32,
	sendQueueDelayMs *float64,
) error {
	packets, next, err := desktopmedia.PacketizeFrame(frame, packetSize, *sequence)
	if err != nil {
		return err
	}
	for _, packet := range packets {
		started := time.Now()
		err := conn.Send(ctx, packet)
		if sendQueueDelayMs != nil {
			*sendQueueDelayMs = smoothSendQueueDelayMs(*sendQueueDelayMs, time.Since(started))
		}
		if err != nil {
			return err
		}
	}
	*sequence = next
	return nil
}

func reconfigureH264Bitrate(
	ctx context.Context,
	encoder desktopcodec.Encoder,
	current desktopcodec.VideoConfig,
	targetBitrate int,
	maxBitrate int,
) (desktopcodec.VideoConfig, error) {
	if encoder == nil {
		return current, desktopcodec.ErrEncoderUnavailable
	}
	if targetBitrate < 250_000 {
		targetBitrate = 250_000
	}
	if maxBitrate > 0 && targetBitrate > maxBitrate {
		targetBitrate = maxBitrate
	}
	if targetBitrate == current.TargetBitrate {
		return current, nil
	}
	next := current
	next.TargetBitrate = targetBitrate
	if err := encoder.Reconfigure(ctx, next); err != nil {
		return current, err
	}
	return next, nil
}

func (h *Host) streamH264Frames(
	ctx context.Context,
	conn *desktopmedia.MediaConn,
	cfg HostConfig,
	idrRequests <-chan struct{},
	bitrateUpdates <-chan int,
	fpsUpdates <-chan int,
) error {
	first, err := h.source.Capture(ctx)
	if err != nil {
		return err
	}
	first = fitRGBAEven(first, cfg.MaxWidth, cfg.MaxHeight)
	if first == nil || first.Bounds().Dx()%2 != 0 || first.Bounds().Dy()%2 != 0 {
		return errors.New("H.264 capture requires an even-sized frame")
	}

	bitrate := cfg.MaxBitrate
	if bitrate <= 0 {
		bitrate = 6_000_000
	}
	videoCfg := desktopcodec.VideoConfig{
		Width:         first.Bounds().Dx(),
		Height:        first.Bounds().Dy(),
		FPS:           cfg.MaxFPS,
		TargetBitrate: bitrate,
		KeyframeEvery: 2 * time.Second,
	}
	encoder, err := desktopcodec.OpenMFH264Encoder(ctx, videoCfg, true)
	if err != nil {
		return err
	}
	defer encoder.Close()

	_ = encoder.ForceIDR(ctx)
	sequenceHeader := encoder.SequenceHeader()
	codecString := desktopcodec.H264CodecString(sequenceHeader)
	if err := sendVideoConfig(ctx, conn, protocol.DesktopVideoConfig{
		Generation:    1,
		Codec:         "h264",
		CodecString:   codecString,
		Width:         videoCfg.Width,
		Height:        videoCfg.Height,
		FPS:           videoCfg.FPS,
		TargetBitrate: videoCfg.TargetBitrate,
		MaxBitrate:    videoCfg.TargetBitrate,
		Chroma:        "420",
		BitDepth:      8,
	}); err != nil {
		return err
	}

	sessionID, err := newMediaSessionID()
	if err != nil {
		return err
	}
	var frameID uint32 = 1
	var sequence uint32 = 1
	started := time.Now()
	lastIDR := started
	lastReportAt := started
	lastEncoderStats := encoder.Stats()
	var capturedFrames uint64
	var lastCapturedFrames uint64
	var lastCaptureMs float64
	var sendQueueDelayMs float64
	var droppedFrames uint64
	targetFPS := videoCfg.FPS
	frameInterval := frameIntervalForFPS(targetFPS)
	ticker := time.NewTicker(frameInterval)
	defer ticker.Stop()

	sendRGBA := func(frame *image.RGBA, now time.Time) error {
		capturedFrames++
		frame = fitRGBAEven(frame, videoCfg.Width, videoCfg.Height)
		if frame == nil || frame.Bounds().Dx() != videoCfg.Width || frame.Bounds().Dy() != videoCfg.Height {
			return errors.New("desktop capture dimensions changed during H.264 session")
		}
		if now.Sub(lastIDR) >= videoCfg.KeyframeEvery {
			if err := encoder.ForceIDR(ctx); err == nil {
				lastIDR = now
			}
		}
		packets, err := encoder.Encode(ctx, desktopcodec.RawFrame{
			Format:    desktopcodec.PixelFormatRGBA,
			Pix:       frame.Pix,
			Width:     videoCfg.Width,
			Height:    videoCfg.Height,
			Stride:    frame.Stride,
			Timestamp: now.Sub(started),
		})
		if err != nil {
			return err
		}
		for _, encoded := range packets {
			data := encoded.Data
			if encoded.KeyFrame {
				data = desktopcodec.H264WithSequenceHeader(data, sequenceHeader)
			}
			mediaFrame := desktopmedia.EncodedFrame{
				SessionID:  sessionID,
				StreamID:   1,
				Generation: 1,
				FrameID:    frameID,
				Timestamp:  uint64(encoded.Timestamp.Microseconds()),
				KeyFrame:   encoded.KeyFrame,
				Config:     encoded.Config,
				Data:       data,
			}
			if err := sendEncodedDesktopFrame(ctx, conn, mediaFrame, cfg.PacketSize, &sequence, &sendQueueDelayMs); err != nil {
				return err
			}
			frameID++
		}
		return nil
	}

	reportStats := func(now time.Time) error {
		elapsed := now.Sub(lastReportAt)
		if elapsed < time.Second {
			return nil
		}
		seconds := elapsed.Seconds()
		current := encoder.Stats()
		stats := protocol.DesktopSessionStats{
			CaptureFPS:       float64(capturedFrames-lastCapturedFrames) / seconds,
			EncodeFPS:        float64(current.Frames-lastEncoderStats.Frames) / seconds,
			ActualBitrate:    int64(float64((current.Bytes-lastEncoderStats.Bytes)*8) / seconds),
			TargetBitrate:    int64(videoCfg.TargetBitrate),
			TargetFPS:        targetFPS,
			CaptureMs:        lastCaptureMs,
			EncodeMs:         float64(current.LastEncodeTime.Microseconds()) / 1000,
			SendQueueDelayMs: sendQueueDelayMs,
			DroppedFrames:    droppedFrames,
			Path:             "relay",
		}
		if err := conn.SendSessionMessage(ctx, protocol.DesktopSessionMessage{
			Type:  protocol.DesktopSessionStatsReport,
			Stats: &stats,
		}); err != nil {
			return err
		}
		lastReportAt = now
		lastEncoderStats = current
		lastCapturedFrames = capturedFrames
		return nil
	}

	if err := sendRGBA(first, started); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-idrRequests:
			if err := encoder.ForceIDR(ctx); err != nil && !errors.Is(err, desktopcodec.ErrEncoderControlUnsupported) {
				log.Printf("[Desktop] H.264 IDR request failed: %v", err)
			} else {
				lastIDR = time.Now()
			}

		case targetBitrate := <-bitrateUpdates:
			nextConfig, err := reconfigureH264Bitrate(ctx, encoder, videoCfg, targetBitrate, cfg.MaxBitrate)
			if err != nil {
				if !errors.Is(err, desktopcodec.ErrEncoderControlUnsupported) {
					log.Printf("[Desktop] H.264 bitrate reconfigure failed target=%d: %v", targetBitrate, err)
				}
				continue
			}
			if nextConfig.TargetBitrate != videoCfg.TargetBitrate {
				videoCfg = nextConfig
				log.Printf("[Desktop] H.264 target bitrate updated=%d", videoCfg.TargetBitrate)
			}
		case nextFPS := <-fpsUpdates:
			nextFPS = clampInt(nextFPS, 1, cfg.MaxFPS)
			if nextFPS == targetFPS {
				continue
			}
			targetFPS = nextFPS
			frameInterval = frameIntervalForFPS(targetFPS)
			ticker.Reset(frameInterval)
			log.Printf("[Desktop] H.264 capture fps updated=%d", targetFPS)
		case scheduled := <-ticker.C:
			now := time.Now()
			if dropped := staleScheduledFrameCount(scheduled, now, frameInterval); dropped > 0 {
				droppedFrames += dropped
				if err := reportStats(now); err != nil {
					return err
				}
				continue
			}
			captureStarted := now
			frame, err := h.source.Capture(ctx)
			lastCaptureMs = float64(time.Since(captureStarted).Microseconds()) / 1000
			if err != nil {
				return err
			}
			if err := sendRGBA(frame, now); err != nil {
				return err
			}
			if err := reportStats(time.Now()); err != nil {
				return err
			}
		}
	}
}
