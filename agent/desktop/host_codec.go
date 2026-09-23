package desktop

import (
	"context"
	"errors"
	"fmt"
	"image"
	"log"
	"time"

	desktopcodec "relayproxy/agent/desktop/codec"
	desktopmedia "relayproxy/internal/desktop"
	"relayproxy/internal/protocol"
)

type h264RuntimeError struct {
	Generation uint32
	Err        error
}

func (e *h264RuntimeError) Error() string {
	if e == nil || e.Err == nil {
		return "H.264 runtime failure"
	}
	return e.Err.Error()
}

func (e *h264RuntimeError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func nextDesktopMediaGeneration(current uint32) (uint32, error) {
	if current == ^uint32(0) {
		return 0, errors.New("Relay Desktop media generation exhausted")
	}
	if current == 0 {
		return 1, nil
	}
	return current + 1, nil
}

type h264GenerationEncoder interface {
	desktopcodec.Encoder
	SequenceHeader() []byte
}

type h264GenerationEncoderOpener func(
	context.Context,
	desktopcodec.VideoConfig,
	bool,
) (h264GenerationEncoder, error)

func openMFH264GenerationEncoder(
	ctx context.Context,
	cfg desktopcodec.VideoConfig,
	preferHardware bool,
) (h264GenerationEncoder, error) {
	return desktopcodec.OpenMFH264Encoder(ctx, cfg, preferHardware)
}

func openH264GenerationEncoder(
	ctx context.Context,
	cfg desktopcodec.VideoConfig,
	opener h264GenerationEncoderOpener,
) (h264GenerationEncoder, desktopcodec.VideoConfig, []byte, error) {
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
	// A fresh encoder normally starts with an IDR. Ask explicitly as well so
	// every generation can be decoded independently. Unsupported control is
	// tolerated; other control failures indicate that this encoder instance
	// is not healthy enough to advertise as a new generation.
	if err := encoder.ForceIDR(ctx); err != nil && !errors.Is(err, desktopcodec.ErrEncoderControlUnsupported) {
		_ = encoder.Close()
		return nil, desktopcodec.VideoConfig{}, nil, err
	}
	return encoder, normalized, encoder.SequenceHeader(), nil
}

func openNextH264CPUGeneration(
	ctx context.Context,
	currentGeneration uint32,
	cfg desktopcodec.VideoConfig,
	opener h264GenerationEncoderOpener,
) (
	h264GenerationEncoder,
	desktopcodec.VideoConfig,
	[]byte,
	uint32,
	error,
) {
	nextGeneration, err := nextDesktopMediaGeneration(currentGeneration)
	if err != nil {
		return nil, desktopcodec.VideoConfig{}, nil, 0, err
	}
	encoder, normalized, sequenceHeader, err := openH264GenerationEncoder(ctx, cfg, opener)
	if err != nil {
		return nil, desktopcodec.VideoConfig{}, nil, 0, err
	}
	return encoder, normalized, sequenceHeader, nextGeneration, nil
}

func openH264D3D11Generation(
	ctx context.Context,
	cfg desktopcodec.VideoConfig,
	frame *D3D11CaptureFrame,
) (
	h264GenerationEncoder,
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
	) (h264GenerationEncoder, error) {
		return desktopcodec.OpenMFH264EncoderWithD3D11(ctx, cfg, preferHardware, frame.Device)
	}
	encoder, normalized, sequenceHeader, err := openH264GenerationEncoder(ctx, normalized, opener)
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

func h264DesktopVideoConfig(
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
		Codec:         "h264",
		CodecString:   desktopcodec.H264CodecString(sequenceHeader),
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
	captureBackend string,
	idrRequests <-chan struct{},
	bitrateUpdates <-chan int,
	fpsUpdates <-chan int,
	resolutionUpdates <-chan desktopResolutionTarget,
) error {
	preference := desktopcodec.NormalizeCodecPreference(options.Codec)
	jpegGeneration := uint32(1)
	if preference == "h264" && h.canEncodeH264() {
		if err := h.streamH264Frames(ctx, conn, cfg, captureBackend, idrRequests, bitrateUpdates, fpsUpdates, resolutionUpdates); err == nil || errors.Is(err, context.Canceled) {
			return err
		} else {
			var runtimeErr *h264RuntimeError
			if errors.As(err, &runtimeErr) {
				nextGeneration, generationErr := nextDesktopMediaGeneration(runtimeErr.Generation)
				if generationErr != nil {
					return generationErr
				}
				jpegGeneration = nextGeneration
				log.Printf("[Desktop] H.264 runtime failed at generation=%d, falling back to JPEG generation=%d: %v",
					runtimeErr.Generation, jpegGeneration, runtimeErr.Err)
			} else {
				log.Printf("[Desktop] H.264 session unavailable, falling back to JPEG: %v", err)
			}
		}
	}
	if err := sendVideoConfig(ctx, conn, protocol.DesktopVideoConfig{
		Generation:    jpegGeneration,
		Codec:         "jpeg",
		Width:         cfg.MaxWidth,
		Height:        cfg.MaxHeight,
		MaxWidth:      cfg.MaxWidth,
		MaxHeight:     cfg.MaxHeight,
		FPS:           cfg.MaxFPS,
		TargetBitrate: cfg.MaxBitrate,
		DisplayID:     cfg.DisplayID,
	}); err != nil {
		return err
	}
	return h.streamFrames(ctx, conn, cfg, captureBackend, jpegGeneration, fpsUpdates)
}

func fitEvenDimensions(width, height, maxWidth, maxHeight int) (int, int, error) {
	if width <= 0 || height <= 0 || maxWidth <= 0 || maxHeight <= 0 {
		return 0, 0, errors.New("invalid H.264 source or target dimensions")
	}
	dw, dh := width, height
	if width > maxWidth || height > maxHeight {
		dw, dh = maxWidth, height*maxWidth/width
		if dh > maxHeight {
			dh = maxHeight
			dw = width * maxHeight / height
		}
	}
	dw &^= 1
	dh &^= 1
	if dw < 2 || dh < 2 {
		return 0, 0, errors.New("H.264 fitted dimensions are too small")
	}
	return dw, dh, nil
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

func rawFrameFitsH264(frame desktopcodec.RawFrame, maxWidth, maxHeight int) bool {
	if frame.Validate() != nil {
		return false
	}
	if maxWidth > 0 && frame.Width > maxWidth {
		return false
	}
	if maxHeight > 0 && frame.Height > maxHeight {
		return false
	}
	return true
}

func h264ResolutionConfig(
	src *image.RGBA,
	target desktopResolutionTarget,
	current desktopcodec.VideoConfig,
) (*image.RGBA, desktopcodec.VideoConfig, error) {
	if src == nil {
		return nil, current, errors.New("H.264 resolution update requires a capture frame")
	}
	frame := fitRGBAEven(src, target.MaxWidth, target.MaxHeight)
	if frame == nil {
		return nil, current, errors.New("H.264 resolution update produced an empty frame")
	}
	next := current
	next.Width = frame.Bounds().Dx()
	next.Height = frame.Bounds().Dy()
	normalized, err := desktopcodec.NormalizeVideoConfig(next)
	if err != nil {
		return nil, current, err
	}
	return frame, normalized, nil
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
			retErr = &h264RuntimeError{Generation: advertisedGeneration, Err: retErr}
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
		if available && rawFrameFitsH264(candidate, cfg.MaxWidth, cfg.MaxHeight) {
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
			return errors.New("H.264 capture requires an even-sized frame")
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
		encoder        h264GenerationEncoder
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
			log.Printf("[Desktop] D3D11 capture probe failed, keeping CPU H.264 path: %v", captureErr)
		} else if available && candidate != nil {
			encoder, d3dEncoder, d3dConverter, normalizedCfg, sequenceHeader, err =
				openH264D3D11Generation(ctx, videoCfg, candidate)
			if err == nil {
				d3dSource = source
				firstD3D = candidate
				gpuEnabled = true
				gpuInputWidth = candidate.Width
				gpuInputHeight = candidate.Height
				log.Printf("[Desktop] H.264 zero-copy path enabled capture=%dx%d encode=%dx%d",
					candidate.Width, candidate.Height, normalizedCfg.Width, normalizedCfg.Height)
			} else {
				candidate.Close()
				log.Printf("[Desktop] D3D11 H.264 initialization failed, keeping CPU path: %v", err)
			}
		}
	}
	if !gpuEnabled {
		encoder, normalizedCfg, sequenceHeader, err = openH264GenerationEncoder(
			ctx, videoCfg, openMFH264GenerationEncoder,
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
	if err := sendVideoConfig(ctx, conn, h264DesktopVideoConfig(
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
			return errors.New("desktop capture dimensions changed during H.264 session")
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
				data = desktopcodec.H264WithSequenceHeader(data, sequenceHeader)
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
				data = desktopcodec.H264WithSequenceHeader(data, sequenceHeader)
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
			return errors.New("desktop capture dimensions changed during H.264 session")
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
			openNextH264CPUGeneration(ctx, generation, videoCfg, openMFH264GenerationEncoder)
		if openErr != nil {
			return errors.Join(cause, fmt.Errorf("CPU H.264 runtime fallback unavailable: %w", openErr))
		}
		nextProtocolConfig := h264DesktopVideoConfig(
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
				log.Printf("[Desktop] close failed D3D11 H.264 encoder during CPU migration: %v", err)
			}
		}
		if oldConverter != nil {
			_ = oldConverter.Close()
		}
		log.Printf(
			"[Desktop] H.264 GPU runtime failure migrated to CPU generation=%d encode=%dx%d bitrate=%d cause=%v",
			generation, videoCfg.Width, videoCfg.Height, videoCfg.TargetBitrate, cause,
		)
		return nil
	}

	if gpuEnabled {
		if err := sendD3D11Frame(firstD3D, started); err != nil {
			return err
		}
		firstD3D.Close()
		firstD3D = nil
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
				log.Printf("[Desktop] H.264 IDR request failed: %v", err)
			} else {
				lastIDR = time.Now()
			}

		case targetBitrate := <-bitrateUpdates:
			nextConfig, err := reconfigureH264Bitrate(ctx, encoder, videoCfg, targetBitrate, sessionMaxBitrate)
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
			applyCaptureFPS(h.source, targetFPS)
			log.Printf("[Desktop] H.264 capture fps updated=%d", targetFPS)

		case target := <-resolutionUpdates:
			now := time.Now()
			if gpuEnabled && d3dSource != nil {
				captureStarted := now
				frame, available, captureErr := d3dSource.CaptureD3D11(ctx)
				lastCaptureMs = float64(time.Since(captureStarted).Microseconds()) / 1000
				if captureErr != nil {
					log.Printf("[Desktop] H.264 GPU resolution capture failed target=%dx%d: %v",
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
					log.Printf("[Desktop] H.264 GPU resolution update rejected target=%dx%d: %v",
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
					openH264D3D11Generation(ctx, nextConfig, frame)
				if openErr != nil {
					frame.Close()
					log.Printf("[Desktop] H.264 D3D11 generation rebuild failed target=%dx%d: %v",
						nextWidth, nextHeight, openErr)
					continue
				}
				nextProtocolConfig := h264DesktopVideoConfig(
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
						log.Printf("[Desktop] close previous D3D11 H.264 generation failed: %v", err)
					}
				}
				if oldConverter != nil {
					_ = oldConverter.Close()
				}
				log.Printf("[Desktop] H.264 D3D11 generation switched=%d capture=%dx%d encode=%dx%d bitrate=%d",
					generation, frame.Width, frame.Height, videoCfg.Width, videoCfg.Height, videoCfg.TargetBitrate)
				err := sendD3D11Frame(frame, now)
				frame.Close()
				if err != nil {
					return err
				}
				continue
			}
			captureStarted := now
			rawFrame, err := h.source.Capture(ctx)
			lastCaptureMs = float64(time.Since(captureStarted).Microseconds()) / 1000
			if err != nil {
				return err
			}
			nextFrame, nextConfig, err := h264ResolutionConfig(rawFrame, target, videoCfg)
			if err != nil {
				log.Printf("[Desktop] H.264 resolution update rejected target=%dx%d: %v",
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
			nextEncoder, nextConfig, nextSequenceHeader, err := openH264GenerationEncoder(
				ctx, nextConfig, openMFH264GenerationEncoder,
			)
			if err != nil {
				log.Printf("[Desktop] H.264 encoder rebuild failed target=%dx%d: %v",
					nextConfig.Width, nextConfig.Height, err)
				continue
			}
			nextProtocolConfig := h264DesktopVideoConfig(
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
					log.Printf("[Desktop] close previous H.264 generation failed: %v", err)
				}
			}
			log.Printf("[Desktop] H.264 generation switched=%d size=%dx%d bitrate=%d",
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
					return captureErr
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
						return convertErr
					}
					oldConverter := d3dConverter
					d3dConverter = nextConverter
					gpuInputWidth = frame.Width
					gpuInputHeight = frame.Height
					if oldConverter != nil {
						_ = oldConverter.Close()
					}
					log.Printf("[Desktop] H.264 D3D11 capture geometry updated=%dx%d encode=%dx%d",
						gpuInputWidth, gpuInputHeight, videoCfg.Width, videoCfg.Height)
				}
				err := sendD3D11Frame(frame, now)
				frame.Close()
				if err != nil {
					return err
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
					rawFrameFitsH264(rawFrame, videoCfg.Width, videoCfg.Height) {
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
