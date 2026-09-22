//go:build windows

package codec

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"
	"unsafe"
)

const mfETransformStreamChange = 0xc00d6d61

type MFH264Decoder struct {
	info      MFH264DecoderInfo
	commands  chan mfDecodeCommand
	done      chan struct{}
	closeOnce sync.Once
}

type MFH264DecoderInfo struct {
	Hardware   bool
	Async      bool
	D3D11Aware bool
	ZeroCopy   bool
	Config     VideoConfig
}

type mfDecodeInput struct {
	data      []byte
	timestamp time.Duration
	duration  time.Duration
}

type mfDecodeCommand struct {
	close bool
	flush bool
	input *mfDecodeInput
	reply chan mfDecodeResult
}

type mfDecodeResult struct {
	frames []DecodedFrame
	err    error
}

type mfDecoderInit struct {
	info MFH264DecoderInfo
	err  error
}

func applyDecoderOutputType(transform unsafe.Pointer, cfg VideoConfig) error {
	outputType, err := createVideoMediaType(&mfVideoFormatNV12, cfg, false)
	if err != nil {
		return err
	}
	defer releaseIUnknown(outputType)
	return setTransformType(transform, imfTransformSetOutputType, outputType)
}

func configureH264Decoder(transform unsafe.Pointer, cfg VideoConfig, graphics *mfDecoderD3D11) (bool, bool, error) {
	d3d11Aware := false
	if graphics != nil {
		aware, err := graphics.Attach(transform)
		if err != nil {
			return false, false, fmt.Errorf("attach D3D11 decoder manager: %w", err)
		}
		d3d11Aware = aware
	}
	isAsync := false
	attributes, attrErr := transformAttributes(transform)
	if attrErr == nil {
		defer releaseIUnknown(attributes)
		async, getErr := attributeGetUINT32(attributes, &mfTransformAsync)
		isAsync = getErr == nil && async != 0
		if isAsync {
			if err := attributeSetUINT32(attributes, &mfTransformAsyncUnlock, 1); err != nil {
				return false, d3d11Aware, fmt.Errorf("unlock async decoder MFT: %w", err)
			}
		}
		if !cfg.DisableLowLatency {
			_ = attributeSetUINT32(attributes, &mfLowLatency, 1)
		}
	}

	inputType, err := createVideoMediaType(&mfVideoFormatH264, cfg, true)
	if err != nil {
		return false, d3d11Aware, err
	}
	defer releaseIUnknown(inputType)
	if err := setTransformType(transform, imfTransformSetInputType, inputType); err != nil {
		return false, d3d11Aware, err
	}
	if err := applyDecoderOutputType(transform, cfg); err != nil {
		return false, d3d11Aware, err
	}
	if d3d11Aware {
		streamInfo, err := getOutputStreamInfo(transform)
		if err != nil {
			return false, d3d11Aware, err
		}
		if streamInfo.Flags&(mftOutputStreamProvidesSamples|mftOutputStreamCanProvideSamples) == 0 {
			return false, d3d11Aware, errors.New("D3D11 decoder requires caller-provided GPU output samples")
		}
	}
	if err := processTransformMessage(transform, mftMessageNotifyBeginStreaming); err != nil {
		return false, d3d11Aware, err
	}
	if err := processTransformMessage(transform, mftMessageNotifyStartOfStream); err != nil {
		return false, d3d11Aware, err
	}
	return isAsync, d3d11Aware, nil
}

func openConfiguredH264Decoder(ctx context.Context, cfg VideoConfig, preferHardware bool, graphics *mfDecoderD3D11) (unsafe.Pointer, MFH264DecoderInfo, error) {
	h264 := mftRegisterTypeInfo{MajorType: mfMediaTypeVideo, Subtype: mfVideoFormatH264}
	rawNV12 := mftRegisterTypeInfo{MajorType: mfMediaTypeVideo, Subtype: mfVideoFormatNV12}
	var failures []error

	tryActivation := func(activation unsafe.Pointer, hardware bool, candidateGraphics *mfDecoderD3D11) (unsafe.Pointer, MFH264DecoderInfo, error) {
		transform, err := activateObject(activation, &iidIMFTransform)
		if err != nil {
			return nil, MFH264DecoderInfo{}, err
		}
		async, d3d11Aware, err := configureH264Decoder(transform, cfg, candidateGraphics)
		if err != nil {
			releaseIUnknown(transform)
			return nil, MFH264DecoderInfo{}, err
		}
		return transform, MFH264DecoderInfo{
			Hardware: hardware,
			Async: async,
			D3D11Aware: d3d11Aware,
			ZeroCopy: d3d11Aware && candidateGraphics != nil && candidateGraphics.shared,
			Config: cfg,
		}, nil
	}

	for _, group := range activationGroups(preferHardware) {
		if err := ctx.Err(); err != nil {
			return nil, MFH264DecoderInfo{}, err
		}
		activations, err := enumerateMFTActivations(mftCategoryVideoDecoder, group.flags, &h264, &rawNV12)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		for index, activation := range activations {
			if activation == nil {
				continue
			}

			if group.hardware && graphics != nil {
				transform, info, err := tryActivation(activation, true, graphics)
				if err == nil {
					releaseIUnknown(activation)
					activations[index] = nil
					releaseMFTActivations(activations[index+1:])
					return transform, info, nil
				}
				failures = append(failures, fmt.Errorf("D3D11 hardware decoder: %w", err))
			}

			transform, info, err := tryActivation(activation, group.hardware, nil)
			releaseIUnknown(activation)
			activations[index] = nil
			if err == nil {
				releaseMFTActivations(activations[index+1:])
				return transform, info, nil
			}
			failures = append(failures, err)
		}
		releaseMFTActivations(activations)
	}
	if len(failures) == 0 {
		return nil, MFH264DecoderInfo{}, ErrDecoderUnavailable
	}
	return nil, MFH264DecoderInfo{}, fmt.Errorf("%w: %v", ErrDecoderUnavailable, errors.Join(failures...))
}

func OpenMFH264Decoder(ctx context.Context, cfg VideoConfig, preferHardware bool) (Decoder, error) {
	return openMFH264Decoder(ctx, cfg, preferHardware, 0)
}

func OpenMFH264DecoderWithD3D11(ctx context.Context, cfg VideoConfig, preferHardware bool, device uintptr) (Decoder, error) {
	return openMFH264Decoder(ctx, cfg, preferHardware, device)
}

func openMFH264Decoder(ctx context.Context, cfg VideoConfig, preferHardware bool, device uintptr) (Decoder, error) {
	cfg, err := NormalizeVideoConfig(cfg)
	if err != nil {
		return nil, err
	}
	session := &MFH264Decoder{
		commands: make(chan mfDecodeCommand),
		done:     make(chan struct{}),
	}
	initCh := make(chan mfDecoderInit, 1)
	go session.run(cfg, preferHardware, device, initCh)

	select {
	case <-ctx.Done():
		session.closeOnce.Do(func() { close(session.commands) })
		<-session.done
		return nil, ctx.Err()
	case initialized := <-initCh:
		if initialized.err != nil {
			<-session.done
			return nil, initialized.err
		}
		session.info = initialized.info
		return session, nil
	}
}

func (d *MFH264Decoder) run(cfg VideoConfig, preferHardware bool, device uintptr, initCh chan<- mfDecoderInit) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(d.done)

	uninitCOM, err := initializeCOM()
	if err != nil {
		initCh <- mfDecoderInit{err: err}
		return
	}
	defer uninitCOM()

	shutdownMF, err := startupMediaFoundation()
	if err != nil {
		initCh <- mfDecoderInit{err: err}
		return
	}
	defer shutdownMF()

	graphics, graphicsErr := createMFDecoderD3D11FromDevice(device)
	if graphicsErr != nil {
		graphics = nil
	}
	transform, info, err := openConfiguredH264Decoder(context.Background(), cfg, preferHardware, graphics)
	if err != nil {
		if graphics != nil {
			graphics.Close()
		}
		initCh <- mfDecoderInit{err: err}
		return
	}
	defer releaseIUnknown(transform)
	if !info.D3D11Aware && graphics != nil {
		graphics.Close()
		graphics = nil
	}
	if graphics != nil {
		defer graphics.Close()
	}

	var eventPump *mfEventPump
	var eventCh <-chan mfAsyncEvent
	var asyncState *mfAsyncDecodeState
	if info.Async {
		eventPump, err = startMFEventPump(transform)
		if err != nil {
			initCh <- mfDecoderInit{err: fmt.Errorf("start decoder Media Foundation async event pump: %w", err)}
			return
		}
		defer eventPump.Close()
		eventCh = eventPump.events
		asyncState = newMFAsyncDecodeState(transform, info, graphics)
	}
	initCh <- mfDecoderInit{info: info}

	for {
		select {
		case command, ok := <-d.commands:
			if !ok {
				if asyncState != nil {
					asyncState.fail(ErrDecoderUnavailable)
				}
				return
			}
			switch {
			case command.close:
				if asyncState != nil {
					asyncState.fail(ErrDecoderUnavailable)
				}
				if eventPump != nil {
					if err := eventPump.Close(); err != nil {
						command.reply <- mfDecodeResult{err: err}
						return
					}
					eventPump = nil
					eventCh = nil
				}
				_ = processTransformMessage(transform, mftMessageNotifyEndOfStream)
				_ = processTransformMessage(transform, mftMessageNotifyEndStreaming)
				_ = processTransformMessage(transform, mftMessageCommandFlush)
				command.reply <- mfDecodeResult{}
				return

			case command.flush:
				if asyncState != nil {
					asyncState.reset(errors.New("decoder flushed"))
				}
				err := processTransformMessage(transform, mftMessageCommandFlush)
				if err == nil {
					err = processTransformMessage(transform, mftMessageNotifyStartOfStream)
				}
				command.reply <- mfDecodeResult{err: err}

			case command.input != nil:
				if asyncState != nil {
					asyncState.enqueue(command)
				} else {
					frames, err := processSyncH264Decode(transform, info, graphics, *command.input)
					command.reply <- mfDecodeResult{frames: frames, err: err}
				}

			default:
				command.reply <- mfDecodeResult{err: errors.New("unsupported Media Foundation decoder command")}
			}

		case event, ok := <-eventCh:
			if !ok {
				if asyncState != nil && asyncState.fatal == nil {
					asyncState.fail(errors.New("decoder Media Foundation async event pump stopped"))
				}
				eventCh = nil
				continue
			}
			asyncState.handle(event)
		}
	}
}

type mfAsyncDecodeState struct {
	transform unsafe.Pointer
	info      MFH264DecoderInfo
	graphics  *mfDecoderD3D11

	needInput int
	pending   []mfDecodeCommand
	inFlight  []mfDecodeCommand
	output    []DecodedFrame
	fatal     error
}

func newMFAsyncDecodeState(transform unsafe.Pointer, info MFH264DecoderInfo, graphics *mfDecoderD3D11) *mfAsyncDecodeState {
	return &mfAsyncDecodeState{transform: transform, info: info, graphics: graphics}
}

func (s *mfAsyncDecodeState) reply(command mfDecodeCommand, frames []DecodedFrame, err error) {
	command.reply <- mfDecodeResult{frames: frames, err: err}
}

func (s *mfAsyncDecodeState) reset(err error) {
	if err == nil {
		err = ErrDecoderUnavailable
	}
	for _, command := range s.pending {
		s.reply(command, nil, err)
	}
	for _, command := range s.inFlight {
		s.reply(command, nil, err)
	}
	s.pending = nil
	s.inFlight = nil
	closeDecodedFrames(s.output)
	s.output = nil
	s.needInput = 0
}

func (s *mfAsyncDecodeState) fail(err error) {
	if err == nil {
		err = ErrDecoderUnavailable
	}
	if s.fatal == nil {
		s.fatal = err
	}
	s.reset(s.fatal)
}

func (s *mfAsyncDecodeState) enqueue(command mfDecodeCommand) {
	if s.fatal != nil {
		s.reply(command, nil, s.fatal)
		return
	}
	s.pending = append(s.pending, command)
	s.submitAvailable()
}

func (s *mfAsyncDecodeState) submitAvailable() {
	for s.fatal == nil && s.needInput > 0 && len(s.pending) > 0 {
		command := s.pending[0]
		s.pending = s.pending[1:]
		sample, err := createInputSample(command.input.data, command.input.timestamp, command.input.duration)
		if err != nil {
			s.reply(command, nil, err)
			s.fail(err)
			return
		}
		hr := processTransformInputSample(s.transform, sample)
		releaseIUnknown(sample)
		if hresultFailed(hr) {
			err := hresultError("H.264 decoder IMFTransform.ProcessInput async", hr)
			s.reply(command, nil, err)
			s.fail(err)
			return
		}
		s.needInput--
		s.inFlight = append(s.inFlight, command)
	}
}

func (s *mfAsyncDecodeState) finishOldest() {
	if len(s.inFlight) == 0 {
		return
	}
	command := s.inFlight[0]
	s.inFlight = s.inFlight[1:]
	frames := append([]DecodedFrame(nil), s.output...)
	s.output = nil
	s.reply(command, frames, nil)
}

func (s *mfAsyncDecodeState) handle(event mfAsyncEvent) {
	if s.fatal != nil {
		return
	}
	if event.err != nil {
		s.fail(event.err)
		return
	}
	switch event.kind {
	case meTransformNeedInput:
		s.needInput++
		s.submitAvailable()

	case meTransformHaveOutput:
		fallback := time.Duration(0)
		if len(s.inFlight) > 0 && s.inFlight[0].input != nil {
			fallback = s.inFlight[0].input.timestamp
		}
		frame, hr, err := processDecoderOutputOnce(s.transform, s.info, s.graphics, fallback)
		if err != nil {
			s.fail(err)
			return
		}
		switch uint32(hr) {
		case mfETransformNeedMoreInput:
			return
		case mfETransformStreamChange:
			if err := applyDecoderOutputType(s.transform, s.info.Config); err != nil {
				s.fail(err)
			}
			return
		}
		if hresultFailed(hr) {
			s.fail(hresultError("H.264 decoder IMFTransform.ProcessOutput async", hr))
			return
		}
		if frame != nil {
			s.output = append(s.output, *frame)
			s.finishOldest()
		}
	}
}

func processDecoderOutputOnce(transform unsafe.Pointer, info MFH264DecoderInfo, graphics *mfDecoderD3D11, fallbackTimestamp time.Duration) (*DecodedFrame, uintptr, error) {
	streamInfo, err := getOutputStreamInfo(transform)
	if err != nil {
		return nil, 0, err
	}
	var outputSample unsafe.Pointer
	if streamInfo.Flags&(mftOutputStreamProvidesSamples|mftOutputStreamCanProvideSamples) == 0 {
		size := streamInfo.Size
		minimum := uint32(info.Config.Width * info.Config.Height * 3 / 2)
		if size < minimum {
			size = minimum
		}
		outputSample, err = createOutputSample(size, streamInfo.Alignment)
		if err != nil {
			return nil, 0, err
		}
	}

	out := mftOutputDataBuffer{StreamID: 0, Sample: outputSample}
	var status uint32
	hr := comCall(
		transform,
		imfTransformProcessOutput,
		0,
		1,
		uintptr(unsafe.Pointer(&out)),
		uintptr(unsafe.Pointer(&status)),
	)
	if out.Events != nil {
		releaseIUnknown(out.Events)
	}
	if hresultFailed(hr) {
		if out.Sample != nil {
			releaseIUnknown(out.Sample)
		}
		return nil, hr, nil
	}
	if out.Sample == nil {
		return nil, hr, errors.New("H.264 decoder ProcessOutput succeeded without a sample")
	}
	timestamp := sampleTimestamp(out.Sample, fallbackTimestamp)

	if graphics != nil && info.ZeroCopy {
		if surface, surfaceErr := decoderSampleSurface(out.Sample, graphics, info.Config.Width, info.Config.Height); surfaceErr == nil {
			releaseIUnknown(out.Sample)
			return &DecodedFrame{
				Format:    PixelFormatNV12,
				Width:     info.Config.Width,
				Height:    info.Config.Height,
				Stride:    info.Config.Width,
				Timestamp: timestamp,
				Hardware:  info.Hardware,
				D3D11:     surface,
			}, hr, nil
		}
	}

	data, err := decoderSampleBytes(out.Sample, info, graphics)
	releaseIUnknown(out.Sample)
	if err != nil {
		return nil, hr, err
	}
	required := info.Config.Width * info.Config.Height * 3 / 2
	if len(data) < required {
		return nil, hr, fmt.Errorf("%w: decoder NV12 sample is %d bytes, need %d", ErrInvalidFrame, len(data), required)
	}
	return &DecodedFrame{
		Format:    PixelFormatNV12,
		Pix:       append([]byte(nil), data[:required]...),
		Width:     info.Config.Width,
		Height:    info.Config.Height,
		Stride:    info.Config.Width,
		Timestamp: timestamp,
		Hardware:  info.Hardware,
	}, hr, nil
}

func drainSyncH264DecoderOutput(transform unsafe.Pointer, info MFH264DecoderInfo, graphics *mfDecoderD3D11, fallbackTimestamp time.Duration) ([]DecodedFrame, error) {
	var frames []DecodedFrame
	for {
		frame, hr, err := processDecoderOutputOnce(transform, info, graphics, fallbackTimestamp)
		if err != nil {
			return nil, err
		}
		switch uint32(hr) {
		case mfETransformNeedMoreInput:
			return frames, nil
		case mfETransformStreamChange:
			if err := applyDecoderOutputType(transform, info.Config); err != nil {
				return nil, err
			}
			continue
		}
		if hresultFailed(hr) {
			return nil, hresultError("H.264 decoder IMFTransform.ProcessOutput", hr)
		}
		if frame != nil {
			frames = append(frames, *frame)
		}
	}
}

func processSyncH264Decode(transform unsafe.Pointer, info MFH264DecoderInfo, graphics *mfDecoderD3D11, input mfDecodeInput) ([]DecodedFrame, error) {
	sample, err := createInputSample(input.data, input.timestamp, input.duration)
	if err != nil {
		return nil, err
	}
	defer releaseIUnknown(sample)

	var frames []DecodedFrame
	hr := processTransformInputSample(transform, sample)
	if uint32(hr) == mfENotAccepting {
		pending, err := drainSyncH264DecoderOutput(transform, info, graphics, input.timestamp)
		if err != nil {
			return nil, err
		}
		frames = append(frames, pending...)
		hr = processTransformInputSample(transform, sample)
	}
	if hresultFailed(hr) {
		return nil, hresultError("H.264 decoder IMFTransform.ProcessInput", hr)
	}
	decoded, err := drainSyncH264DecoderOutput(transform, info, graphics, input.timestamp)
	if err != nil {
		return nil, err
	}
	return append(frames, decoded...), nil
}

func closeDecodedFrames(frames []DecodedFrame) {
	for i := range frames {
		frames[i].Close()
	}
}

func (d *MFH264Decoder) runCommand(ctx context.Context, command mfDecodeCommand) (mfDecodeResult, error) {
	if d == nil {
		return mfDecodeResult{}, ErrDecoderUnavailable
	}
	if command.reply == nil {
		command.reply = make(chan mfDecodeResult, 1)
	}
	select {
	case <-ctx.Done():
		return mfDecodeResult{}, ctx.Err()
	case <-d.done:
		return mfDecodeResult{}, ErrDecoderUnavailable
	case d.commands <- command:
	}
	select {
	case <-ctx.Done():
		go func() {
			select {
			case result := <-command.reply:
				closeDecodedFrames(result.frames)
			case <-d.done:
			}
		}()
		return mfDecodeResult{}, ctx.Err()
	case <-d.done:
		return mfDecodeResult{}, ErrDecoderUnavailable
	case result := <-command.reply:
		return result, result.err
	}
}

func (d *MFH264Decoder) Decode(ctx context.Context, data []byte, timestamp time.Duration) ([]DecodedFrame, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("%w: empty H.264 access unit", ErrInvalidFrame)
	}
	duration := time.Second / time.Duration(d.info.Config.FPS)
	result, err := d.runCommand(ctx, mfDecodeCommand{
		input: &mfDecodeInput{
			data:      append([]byte(nil), data...),
			timestamp: timestamp,
			duration:  duration,
		},
	})
	if err != nil {
		return nil, err
	}
	return result.frames, nil
}

func (d *MFH264Decoder) Flush(ctx context.Context) error {
	_, err := d.runCommand(ctx, mfDecodeCommand{flush: true})
	return err
}

func (d *MFH264Decoder) Hardware() bool {
	return d != nil && d.info.Hardware
}

func (d *MFH264Decoder) Backend() string {
	if d == nil {
		return ""
	}
	switch {
	case d.info.Hardware && d.info.ZeroCopy:
		return "media-foundation-d3d11-zero-copy"
	case d.info.Hardware && d.info.D3D11Aware:
		return "media-foundation-d3d11"
	case d.info.Hardware:
		return "media-foundation-hardware"
	default:
		return "media-foundation-software"
	}
}

func (d *MFH264Decoder) Close() error {
	if d == nil {
		return nil
	}
	var closeErr error
	d.closeOnce.Do(func() {
		reply := make(chan mfDecodeResult, 1)
		select {
		case d.commands <- mfDecodeCommand{close: true, reply: reply}:
			closeErr = (<-reply).err
			close(d.commands)
		case <-d.done:
		}
	})
	<-d.done
	return closeErr
}
