package desktop

import (
	"context"
	"errors"
	"fmt"
	"image"
	"log"
	"strings"
	"time"

	desktopcodec "relayproxy/agent/desktop/codec"
	desktopmedia "relayproxy/internal/desktop"
	"relayproxy/internal/protocol"
)

func h265ValidationRequested(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), protocol.DesktopCodecH265Validation)
}

// h265RuntimeError mirrors the H.264 generation error boundary while H.265
// remains an internal-only Host path. It lets callers preserve the last
// advertised generation if an HEVC session fails after CONFIG was sent.
type h265RuntimeError struct {
	Generation uint32
	Err        error
}

func (e *h265RuntimeError) Error() string {
	if e == nil || e.Err == nil {
		return "H.265 runtime failure"
	}
	return e.Err.Error()
}

func (e *h265RuntimeError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type h265GenerationEncoder interface {
	desktopcodec.Encoder
	SequenceHeader() []byte
}

type h265GenerationEncoderOpener func(
	context.Context,
	desktopcodec.VideoConfig,
	bool,
) (h265GenerationEncoder, error)

func openMFH265GenerationEncoder(
	ctx context.Context,
	cfg desktopcodec.VideoConfig,
	preferHardware bool,
) (h265GenerationEncoder, error) {
	return desktopcodec.OpenMFH265Encoder(ctx, cfg, preferHardware)
}

func openH265GenerationEncoder(
	ctx context.Context,
	cfg desktopcodec.VideoConfig,
	opener h265GenerationEncoderOpener,
) (h265GenerationEncoder, desktopcodec.VideoConfig, []byte, error) {
	if opener == nil {
		return nil, desktopcodec.VideoConfig{}, nil, desktopcodec.ErrEncoderUnavailable
	}
	normalized, err := desktopcodec.NormalizeVideoConfig(cfg)
	if err != nil {
		return nil, desktopcodec.VideoConfig{}, nil, err
	}
	encoder, err := opener(ctx, normalized, true)
	if err != nil {
		return nil, desktopcodec.VideoConfig{}, nil, err
	}
	if encoder == nil {
		return nil, desktopcodec.VideoConfig{}, nil, desktopcodec.ErrEncoderUnavailable
	}
	if err := encoder.ForceIDR(ctx); err != nil && !errors.Is(err, desktopcodec.ErrEncoderControlUnsupported) {
		_ = encoder.Close()
		return nil, desktopcodec.VideoConfig{}, nil, err
	}
	return encoder, normalized, encoder.SequenceHeader(), nil
}

func openNextH265CPUGeneration(
	ctx context.Context,
	currentGeneration uint32,
	cfg desktopcodec.VideoConfig,
	opener h265GenerationEncoderOpener,
) (
	h265GenerationEncoder,
	desktopcodec.VideoConfig,
	[]byte,
	uint32,
	error,
) {
	nextGeneration, err := nextDesktopMediaGeneration(currentGeneration)
	if err != nil {
		return nil, desktopcodec.VideoConfig{}, nil, 0, err
	}
	encoder, normalized, sequenceHeader, err := openH265GenerationEncoder(ctx, cfg, opener)
	if err != nil {
		return nil, desktopcodec.VideoConfig{}, nil, 0, err
	}
	return encoder, normalized, sequenceHeader, nextGeneration, nil
}

func openH265D3D11Generation(
	ctx context.Context,
	cfg desktopcodec.VideoConfig,
	frame *D3D11CaptureFrame,
) (
	h265GenerationEncoder,
	desktopcodec.D3D11Encoder,
	*desktopcodec.D3D11NV12Converter,
	desktopcodec.VideoConfig,
	[]byte,
	error,
) {
	if frame == nil || !frame.Valid() {
		return nil, nil, nil, desktopcodec.VideoConfig{}, nil, desktopcodec.ErrInvalidFrame
	}
	normalized, err := desktopcodec.NormalizeVideoConfig(cfg)
	if err != nil {
		return nil, nil, nil, desktopcodec.VideoConfig{}, nil, err
	}
	converter, err := desktopcodec.OpenD3D11NV12Converter(frame.Device, desktopcodec.D3D11ConvertConfig{
		InputWidth:   frame.Width,
		InputHeight:  frame.Height,
		OutputWidth:  normalized.Width,
		OutputHeight: normalized.Height,
		FPS:          normalized.FPS,
	})
	if err != nil {
		return nil, nil, nil, desktopcodec.VideoConfig{}, nil, err
	}
	opener := func(
		ctx context.Context,
		cfg desktopcodec.VideoConfig,
		preferHardware bool,
	) (h265GenerationEncoder, error) {
		return desktopcodec.OpenMFH265EncoderWithD3D11(ctx, cfg, preferHardware, frame.Device)
	}
	encoder, normalized, sequenceHeader, err := openH265GenerationEncoder(ctx, normalized, opener)
	if err != nil {
		_ = converter.Close()
		return nil, nil, nil, desktopcodec.VideoConfig{}, nil, err
	}
	d3dEncoder, ok := encoder.(desktopcodec.D3D11Encoder)
	if !ok {
		_ = encoder.Close()
		_ = converter.Close()
		return nil, nil, nil, desktopcodec.VideoConfig{}, nil, desktopcodec.ErrEncoderUnavailable
	}
	return encoder, d3dEncoder, converter, normalized, sequenceHeader, nil
}

func h265DesktopVideoConfig(
	generation uint32,
	cfg desktopcodec.VideoConfig,
	maxWidth int,
	maxHeight int,
	maxBitrate int,
	displayID string,
	sequenceHeader []byte,
) protocol.DesktopVideoConfig {
	if maxBitrate <= 0 {
		maxBitrate = cfg.TargetBitrate
	}
	return protocol.DesktopVideoConfig{
		Generation:    generation,
		Codec:         "h265",
		CodecString:   desktopcodec.H265CodecString(sequenceHeader),
		Width:         cfg.Width,
		Height:        cfg.Height,
		MaxWidth:      maxWidth,
		MaxHeight:     maxHeight,
		FPS:           cfg.FPS,
		TargetBitrate: cfg.TargetBitrate,
		MaxBitrate:    maxBitrate,
		Chroma:        "420",
		BitDepth:      8,
		DisplayID:     displayID,
	}
}

func rawFrameFitsH265(frame desktopcodec.RawFrame, maxWidth, maxHeight int) bool {
	return rawFrameFitsH264(frame, maxWidth, maxHeight)
}

func h265ResolutionConfig(
	src *image.RGBA,
	target desktopResolutionTarget,
	current desktopcodec.VideoConfig,
) (*image.RGBA, desktopcodec.VideoConfig, error) {
	return h264ResolutionConfig(src, target, current)
}

func reconfigureH265Bitrate(
	ctx context.Context,
	encoder desktopcodec.Encoder,
	current desktopcodec.VideoConfig,
	targetBitrate int,
	maxBitrate int,
) (desktopcodec.VideoConfig, error) {
	return reconfigureH264Bitrate(ctx, encoder, current, targetBitrate, maxBitrate)
}

// streamH265Frames intentionally is not called from streamSessionFrames yet.
// HEVC remains hidden from user selection/capability advertisement until the
// Host + Controller + native Viewer path passes end-to-end validation.

func (h *Host) streamH265Frames(
	ctx context.Context,
	conn *desktopmedia.MediaConn,
	cfg HostConfig,
	captureBackend string,
	idrRequests <-chan struct{},
	bitrateUpdates <-chan int,
	fpsUpdates <-chan int,
	resolutionUpdates <-chan desktopResolutionTarget,
) (retErr error) {
	var advertised bool
	var advertisedGeneration uint32
	defer func() {
		if retErr != nil && advertised && !errors.Is(retErr, context.Canceled) {
			retErr = &h265RuntimeError{Generation: advertisedGeneration, Err: retErr}
		}
	}()
	var (
		first        *image.RGBA
		firstRaw     desktopcodec.RawFrame
		rawSource    RawCaptureSource
		rawAvailable bool
	)
	if source, ok := h.source.(RawCaptureSource); ok {
		rawSource = source
		candidate, available, rawErr := source.CaptureRaw(ctx)
		if rawErr != nil {
			return rawErr
		}
		if available && rawFrameFitsH265(candidate, cfg.MaxWidth, cfg.MaxHeight) {
			firstRaw = candidate
			rawAvailable = true
		}
	}
	if !rawAvailable {
		var err error
		first, err = h.source.Capture(ctx)
		if err != nil {
			return err
		}
		first = fitRGBAEven(first, cfg.MaxWidth, cfg.MaxHeight)
		if first == nil || first.Bounds().Dx()%2 != 0 || first.Bounds().Dy()%2 != 0 {
			return errors.New("H.265 capture requires an even-sized frame")
		}
	}

	width, height := 0, 0
	if rawAvailable {
		width, height = firstRaw.Width, firstRaw.Height
	} else {
		width, height = first.Bounds().Dx(), first.Bounds().Dy()
	}

	bitrate := cfg.MaxBitrate
	if bitrate <= 0 {
		bitrate = 6_000_000
	}
	videoCfg := desktopcodec.VideoConfig{
		Width:         width,
		Height:        height,
		FPS:           cfg.MaxFPS,
		TargetBitrate: bitrate,
		KeyframeEvery: 2 * time.Second,
	}
	var (
		encoder        h265GenerationEncoder
		d3dEncoder     desktopcodec.D3D11Encoder
		d3dConverter   *desktopcodec.D3D11NV12Converter
		d3dSource      D3D11CaptureSource
		firstD3D       *D3D11CaptureFrame
		sequenceHeader []byte
		normalizedCfg  desktopcodec.VideoConfig
		err            error
		gpuEnabled     bool
		gpuInputWidth  int
		gpuInputHeight int
	)
	if source, ok := h.source.(D3D11CaptureSource); ok {
		candidate, available, captureErr := source.CaptureD3D11(ctx)
		if captureErr != nil {
			log.Printf("[Desktop] D3D11 capture probe failed, keeping CPU H.265 path: %v", captureErr)
		} else if available && candidate != nil {
			encoder, d3dEncoder, d3dConverter, normalizedCfg, sequenceHeader, err =
				openH265D3D11Generation(ctx, videoCfg, candidate)
			if err == nil {
				d3dSource = source
				firstD3D = candidate
				gpuEnabled = true
				gpuInputWidth = candidate.Width
				gpuInputHeight = candidate.Height
				log.Printf("[Desktop] H.265 zero-copy path enabled capture=%dx%d encode=%dx%d",
					candidate.Width, candidate.Height, normalizedCfg.Width, normalizedCfg.Height)
			} else {
				candidate.Close()
				log.Printf("[Desktop] D3D11 H.265 initialization failed, keeping CPU path: %v", err)
			}
		}
	}
	if !gpuEnabled {
		encoder, normalizedCfg, sequenceHeader, err = openH265GenerationEncoder(
			ctx, videoCfg, openMFH265GenerationEncoder,
		)
		if err != nil {
			return err
		}
	}
	videoCfg = normalizedCfg
	defer func() {
		if firstD3D != nil {
			firstD3D.Close()
		}
		if encoder != nil {
			_ = encoder.Close()
		}
		if d3dConverter != nil {
			_ = d3dConverter.Close()
		}
	}()

	generation := uint32(1)
	sessionMaxWidth := videoCfg.Width
	sessionMaxHeight := videoCfg.Height
	sessionMaxBitrate := cfg.MaxBitrate
	if sessionMaxBitrate <= 0 {
		sessionMaxBitrate = videoCfg.TargetBitrate
	}
	if err := sendVideoConfig(ctx, conn, h265DesktopVideoConfig(
		generation, videoCfg, sessionMaxWidth, sessionMaxHeight, sessionMaxBitrate, cfg.DisplayID, sequenceHeader,
	)); err != nil {
		return err
	}
	advertised = true
	advertisedGeneration = generation

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
	captureFormat := "rgba"
	if rawAvailable {
		captureFormat = "bgra-direct"
	}
	if gpuEnabled {
		captureFormat = "d3d11-nv12"
	}
	needsGenerationKeyFrame := true
	targetFPS := videoCfg.FPS
	frameInterval := frameIntervalForFPS(targetFPS)
	ticker := time.NewTicker(frameInterval)
	defer ticker.Stop()

	sendRawFrame := func(frame desktopcodec.RawFrame, now time.Time) error {
		capturedFrames++
		if frame.Width != videoCfg.Width || frame.Height != videoCfg.Height {
			return errors.New("desktop capture dimensions changed during H.265 session")
		}
		frame.Timestamp = now.Sub(started)
		if needsGenerationKeyFrame || now.Sub(lastIDR) >= videoCfg.KeyframeEvery {
			if err := encoder.ForceIDR(ctx); err == nil {
				lastIDR = now
			}
		}
		packets, err := encoder.Encode(ctx, frame)
		if err != nil {
			return err
		}
		for _, encoded := range packets {
			if needsGenerationKeyFrame && !encoded.KeyFrame {
				continue
			}
			data := encoded.Data
			if encoded.KeyFrame {
				needsGenerationKeyFrame = false
				lastIDR = now
				data = desktopcodec.H265WithSequenceHeader(data, sequenceHeader)
			}
			mediaFrame := desktopmedia.EncodedFrame{
				SessionID:  sessionID,
				StreamID:   1,
				Generation: generation,
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

	sendD3D11Frame := func(frame *D3D11CaptureFrame, now time.Time) error {
		if !gpuEnabled || frame == nil || !frame.Valid() || d3dConverter == nil || d3dEncoder == nil {
			return desktopcodec.ErrEncoderUnavailable
		}
		capturedFrames++
		converted, err := d3dConverter.Convert(
			frame.Resource,
			frame.Subresource,
			now.Sub(started),
		)
		if err != nil {
			return err
		}
		if needsGenerationKeyFrame || now.Sub(lastIDR) >= videoCfg.KeyframeEvery {
			if err := encoder.ForceIDR(ctx); err == nil {
				lastIDR = now
			}
		}
		packets, err := d3dEncoder.EncodeD3D11(ctx, converted)
		if err != nil {
			return err
		}
		for _, encoded := range packets {
			if needsGenerationKeyFrame && !encoded.KeyFrame {
				continue
			}
			data := encoded.Data
			if encoded.KeyFrame {
				needsGenerationKeyFrame = false
				lastIDR = now
				data = desktopcodec.H265WithSequenceHeader(data, sequenceHeader)
			}
			mediaFrame := desktopmedia.EncodedFrame{
				SessionID:  sessionID,
				StreamID:   1,
				Generation: generation,
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

	sendRGBA := func(frame *image.RGBA, now time.Time) error {
		frame = fitRGBAEven(frame, videoCfg.Width, videoCfg.Height)
		if frame == nil || frame.Bounds().Dx() != videoCfg.Width || frame.Bounds().Dy() != videoCfg.Height {
			return errors.New("desktop capture dimensions changed during H.265 session")
		}
		return sendRawFrame(desktopcodec.RawFrame{
			Format: desktopcodec.PixelFormatRGBA,
			Pix:    frame.Pix,
			Width:  videoCfg.Width,
			Height: videoCfg.Height,
			Stride: frame.Stride,
		}, now)
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
			CaptureBackend:   captureBackendName(h.source, captureBackend),
			CaptureFormat:    captureFormat,
			EncoderBackend:   current.Backend,
			EncoderHardware:  current.Hardware,
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

	migrateGPUToCPU := func(now time.Time, cause error) error {
		if !gpuEnabled {
			return cause
		}
		nextEncoder, nextConfig, nextSequenceHeader, nextGeneration, openErr :=
			openNextH265CPUGeneration(ctx, generation, videoCfg, openMFH265GenerationEncoder)
		if openErr != nil {
			return errors.Join(cause, fmt.Errorf("CPU H.265 runtime fallback unavailable: %w", openErr))
		}
		nextProtocolConfig := h265DesktopVideoConfig(
			nextGeneration, nextConfig, sessionMaxWidth, sessionMaxHeight,
			sessionMaxBitrate, cfg.DisplayID, nextSequenceHeader,
		)
		if err := sendVideoConfig(ctx, conn, nextProtocolConfig); err != nil {
			_ = nextEncoder.Close()
			return errors.Join(cause, err)
		}
		advertisedGeneration = nextGeneration

		oldEncoder := encoder
		oldConverter := d3dConverter
		encoder = nextEncoder
		d3dEncoder = nil
		d3dConverter = nil
		d3dSource = nil
		gpuEnabled = false
		videoCfg = nextConfig
		sequenceHeader = nextSequenceHeader
		generation = nextGeneration
		frameID = 1
		needsGenerationKeyFrame = true
		lastIDR = now
		lastEncoderStats = encoder.Stats()
		lastCapturedFrames = capturedFrames
		lastReportAt = now
		captureFormat = "rgba"
		if rawSource != nil && videoCfg.Width == sessionMaxWidth && videoCfg.Height == sessionMaxHeight {
			captureFormat = "bgra-direct"
		}
		if oldEncoder != nil {
			if err := oldEncoder.Close(); err != nil {
				log.Printf("[Desktop] close failed D3D11 H.265 encoder during CPU migration: %v", err)
			}
		}
		if oldConverter != nil {
			_ = oldConverter.Close()
		}
		log.Printf(
			"[Desktop] H.265 GPU runtime failure migrated to CPU generation=%d encode=%dx%d bitrate=%d cause=%v",
			generation, videoCfg.Width, videoCfg.Height, videoCfg.TargetBitrate, cause,
		)
		return nil
	}

	if gpuEnabled {
		firstErr := sendD3D11Frame(firstD3D, started)
		firstD3D.Close()
		firstD3D = nil
		if firstErr != nil {
			if fallbackErr := migrateGPUToCPU(started, firstErr); fallbackErr != nil {
				return fallbackErr
			}
		}
	} else if rawAvailable {
		if err := sendRawFrame(firstRaw, started); err != nil {
			return err
		}
	} else if err := sendRGBA(first, started); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-idrRequests:
			if err := encoder.ForceIDR(ctx); err != nil && !errors.Is(err, desktopcodec.ErrEncoderControlUnsupported) {
				log.Printf("[Desktop] H.265 IDR request failed: %v", err)
			} else {
				lastIDR = time.Now()
			}

		case targetBitrate := <-bitrateUpdates:
			nextConfig, err := reconfigureH265Bitrate(ctx, encoder, videoCfg, targetBitrate, sessionMaxBitrate)
			if err != nil {
				if !errors.Is(err, desktopcodec.ErrEncoderControlUnsupported) {
					log.Printf("[Desktop] H.265 bitrate reconfigure failed target=%d: %v", targetBitrate, err)
				}
				continue
			}
			if nextConfig.TargetBitrate != videoCfg.TargetBitrate {
				videoCfg = nextConfig
				log.Printf("[Desktop] H.265 target bitrate updated=%d", videoCfg.TargetBitrate)
			}
		case nextFPS := <-fpsUpdates:
			nextFPS = clampInt(nextFPS, 1, cfg.MaxFPS)
			if nextFPS == targetFPS {
				continue
			}
			targetFPS = nextFPS
			frameInterval = frameIntervalForFPS(targetFPS)
			ticker.Reset(frameInterval)
			applyCaptureFPS(h.source, targetFPS)
			log.Printf("[Desktop] H.265 capture fps updated=%d", targetFPS)

		case target := <-resolutionUpdates:
			now := time.Now()
			if gpuEnabled && d3dSource != nil {
				captureStarted := now
				frame, available, captureErr := d3dSource.CaptureD3D11(ctx)
				lastCaptureMs = float64(time.Since(captureStarted).Microseconds()) / 1000
				if captureErr != nil {
					log.Printf("[Desktop] H.265 GPU resolution capture failed target=%dx%d: %v",
						target.MaxWidth, target.MaxHeight, captureErr)
					continue
				}
				if !available || frame == nil {
					continue
				}
				nextWidth, nextHeight, fitErr := fitEvenDimensions(
					frame.Width, frame.Height, target.MaxWidth, target.MaxHeight,
				)
				if fitErr != nil {
					frame.Close()
					log.Printf("[Desktop] H.265 GPU resolution update rejected target=%dx%d: %v",
						target.MaxWidth, target.MaxHeight, fitErr)
					continue
				}
				if nextWidth == videoCfg.Width && nextHeight == videoCfg.Height &&
					frame.Width == gpuInputWidth && frame.Height == gpuInputHeight {
					frame.Close()
					continue
				}
				nextConfig := videoCfg
				nextConfig.Width = nextWidth
				nextConfig.Height = nextHeight
				nextGeneration, generationErr := nextDesktopMediaGeneration(generation)
				if generationErr != nil {
					frame.Close()
					return generationErr
				}
				nextEncoder, nextD3DEncoder, nextConverter, nextConfig, nextSequenceHeader, openErr :=
					openH265D3D11Generation(ctx, nextConfig, frame)
				if openErr != nil {
					frame.Close()
					log.Printf("[Desktop] H.265 D3D11 generation rebuild failed target=%dx%d: %v",
						nextWidth, nextHeight, openErr)
					continue
				}
				nextProtocolConfig := h265DesktopVideoConfig(
					nextGeneration, nextConfig, sessionMaxWidth, sessionMaxHeight,
					sessionMaxBitrate, cfg.DisplayID, nextSequenceHeader,
				)
				if err := sendVideoConfig(ctx, conn, nextProtocolConfig); err != nil {
					frame.Close()
					_ = nextEncoder.Close()
					_ = nextConverter.Close()
					return err
				}
				advertisedGeneration = nextGeneration

				oldEncoder := encoder
				oldConverter := d3dConverter
				encoder = nextEncoder
				d3dEncoder = nextD3DEncoder
				d3dConverter = nextConverter
				videoCfg = nextConfig
				sequenceHeader = nextSequenceHeader
				gpuInputWidth = frame.Width
				gpuInputHeight = frame.Height
				captureFormat = "d3d11-nv12"
				generation = nextGeneration
				frameID = 1
				needsGenerationKeyFrame = true
				lastIDR = now
				lastEncoderStats = encoder.Stats()
				lastCapturedFrames = capturedFrames
				lastReportAt = now
				if oldEncoder != nil {
					if err := oldEncoder.Close(); err != nil {
						log.Printf("[Desktop] close previous D3D11 H.265 generation failed: %v", err)
					}
				}
				if oldConverter != nil {
					_ = oldConverter.Close()
				}
				log.Printf("[Desktop] H.265 D3D11 generation switched=%d capture=%dx%d encode=%dx%d bitrate=%d",
					generation, frame.Width, frame.Height, videoCfg.Width, videoCfg.Height, videoCfg.TargetBitrate)
				err := sendD3D11Frame(frame, now)
				frame.Close()
				if err != nil {
					if fallbackErr := migrateGPUToCPU(now, err); fallbackErr != nil {
						return fallbackErr
					}
				}
				continue
			}
			captureStarted := now
			rawFrame, err := h.source.Capture(ctx)
			lastCaptureMs = float64(time.Since(captureStarted).Microseconds()) / 1000
			if err != nil {
				return err
			}
			nextFrame, nextConfig, err := h265ResolutionConfig(rawFrame, target, videoCfg)
			if err != nil {
				log.Printf("[Desktop] H.265 resolution update rejected target=%dx%d: %v",
					target.MaxWidth, target.MaxHeight, err)
				continue
			}
			if nextConfig.Width == videoCfg.Width && nextConfig.Height == videoCfg.Height {
				continue
			}
			nextGeneration, err := nextDesktopMediaGeneration(generation)
			if err != nil {
				return err
			}
			nextEncoder, nextConfig, nextSequenceHeader, err := openH265GenerationEncoder(
				ctx, nextConfig, openMFH265GenerationEncoder,
			)
			if err != nil {
				log.Printf("[Desktop] H.265 encoder rebuild failed target=%dx%d: %v",
					nextConfig.Width, nextConfig.Height, err)
				continue
			}
			nextProtocolConfig := h265DesktopVideoConfig(
				nextGeneration, nextConfig, sessionMaxWidth, sessionMaxHeight, sessionMaxBitrate, cfg.DisplayID, nextSequenceHeader,
			)
			if err := sendVideoConfig(ctx, conn, nextProtocolConfig); err != nil {
				_ = nextEncoder.Close()
				return err
			}
			advertisedGeneration = nextGeneration

			oldEncoder := encoder
			encoder = nextEncoder
			videoCfg = nextConfig
			sequenceHeader = nextSequenceHeader
			captureFormat = "rgba"
			generation = nextGeneration
			frameID = 1
			needsGenerationKeyFrame = true
			lastIDR = now
			lastEncoderStats = encoder.Stats()
			lastCapturedFrames = capturedFrames
			lastReportAt = now
			if oldEncoder != nil {
				if err := oldEncoder.Close(); err != nil {
					log.Printf("[Desktop] close previous H.265 generation failed: %v", err)
				}
			}
			log.Printf("[Desktop] H.265 generation switched=%d size=%dx%d bitrate=%d",
				generation, videoCfg.Width, videoCfg.Height, videoCfg.TargetBitrate)
			if err := sendRGBA(nextFrame, now); err != nil {
				return err
			}

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
			if gpuEnabled && d3dSource != nil {
				frame, available, captureErr := d3dSource.CaptureD3D11(ctx)
				lastCaptureMs = float64(time.Since(captureStarted).Microseconds()) / 1000
				if captureErr != nil {
					if errors.Is(captureErr, context.DeadlineExceeded) {
						droppedFrames++
						if err := reportStats(time.Now()); err != nil {
							return err
						}
						continue
					}
					if fallbackErr := migrateGPUToCPU(time.Now(), captureErr); fallbackErr != nil {
						return fallbackErr
					}
					continue
				}
				if !available || frame == nil {
					droppedFrames++
					if err := reportStats(time.Now()); err != nil {
						return err
					}
					continue
				}
				if frame.Width != gpuInputWidth || frame.Height != gpuInputHeight {
					nextConverter, convertErr := desktopcodec.OpenD3D11NV12Converter(
						frame.Device,
						desktopcodec.D3D11ConvertConfig{
							InputWidth: frame.Width, InputHeight: frame.Height,
							OutputWidth: videoCfg.Width, OutputHeight: videoCfg.Height,
							FPS: videoCfg.FPS,
						},
					)
					if convertErr != nil {
						frame.Close()
						if fallbackErr := migrateGPUToCPU(time.Now(), convertErr); fallbackErr != nil {
							return fallbackErr
						}
						continue
					}
					oldConverter := d3dConverter
					d3dConverter = nextConverter
					gpuInputWidth = frame.Width
					gpuInputHeight = frame.Height
					if oldConverter != nil {
						_ = oldConverter.Close()
					}
					log.Printf("[Desktop] H.265 D3D11 capture geometry updated=%dx%d encode=%dx%d",
						gpuInputWidth, gpuInputHeight, videoCfg.Width, videoCfg.Height)
				}
				err := sendD3D11Frame(frame, now)
				frame.Close()
				if err != nil {
					if fallbackErr := migrateGPUToCPU(time.Now(), err); fallbackErr != nil {
						return fallbackErr
					}
					continue
				}
				captureFormat = "d3d11-nv12"
				if err := reportStats(time.Now()); err != nil {
					return err
				}
				continue
			}
			if rawSource != nil && videoCfg.Width == sessionMaxWidth && videoCfg.Height == sessionMaxHeight {
				rawFrame, available, rawErr := rawSource.CaptureRaw(ctx)
				lastCaptureMs = float64(time.Since(captureStarted).Microseconds()) / 1000
				if rawErr != nil {
					return rawErr
				}
				if available && rawFrame.Width == videoCfg.Width && rawFrame.Height == videoCfg.Height &&
					rawFrameFitsH265(rawFrame, videoCfg.Width, videoCfg.Height) {
					captureFormat = "bgra-direct"
					if err := sendRawFrame(rawFrame, now); err != nil {
						return err
					}
					if err := reportStats(time.Now()); err != nil {
						return err
					}
					continue
				}
			}
			frame, err := h.source.Capture(ctx)
			lastCaptureMs = float64(time.Since(captureStarted).Microseconds()) / 1000
			if err != nil {
				return err
			}
			captureFormat = "rgba"
			if err := sendRGBA(frame, now); err != nil {
				return err
			}
			if err := reportStats(time.Now()); err != nil {
				return err
			}
		}
	}
}
