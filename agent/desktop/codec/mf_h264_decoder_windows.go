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
	Hardware bool
	Async    bool
	Config   VideoConfig
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

func configureH264Decoder(transform unsafe.Pointer, cfg VideoConfig) (bool, error) {
	attributes, attrErr := transformAttributes(transform)
	if attrErr == nil {
		defer releaseIUnknown(attributes)
		async, getErr := attributeGetUINT32(attributes, &mfTransformAsync)
		isAsync := getErr == nil && async != 0
		if isAsync {
			if err := attributeSetUINT32(attributes, &mfTransformAsyncUnlock, 1); err != nil {
				return false, fmt.Errorf("unlock async decoder MFT: %w", err)
			}
		}
		if !cfg.DisableLowLatency {
			_ = attributeSetUINT32(attributes, &mfLowLatency, 1)
		}
	}

	inputType, err := createVideoMediaType(&mfVideoFormatH264, cfg, true)
	if err != nil {
		return false, err
	}
	defer releaseIUnknown(inputType)
	if err := setTransformType(transform, imfTransformSetInputType, inputType); err != nil {
		return false, err
	}
	if err := applyDecoderOutputType(transform, cfg); err != nil {
		return false, err
	}
	if err := processTransformMessage(transform, mftMessageNotifyBeginStreaming); err != nil {
		return false, err
	}
	if err := processTransformMessage(transform, mftMessageNotifyStartOfStream); err != nil {
		return false, err
	}
	return false, nil
}

func openConfiguredH264Decoder(ctx context.Context, cfg VideoConfig, preferHardware bool) (unsafe.Pointer, MFH264DecoderInfo, error) {
	h264 := mftRegisterTypeInfo{MajorType: mfMediaTypeVideo, Subtype: mfVideoFormatH264}
	rawNV12 := mftRegisterTypeInfo{MajorType: mfMediaTypeVideo, Subtype: mfVideoFormatNV12}
	var failures []error

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
			transform, err := activateObject(activation, &iidIMFTransform)
			releaseIUnknown(activation)
			activations[index] = nil
			if err != nil {
				failures = append(failures, err)
				continue
			}
			async, err := configureH264Decoder(transform, cfg)
			if err == nil {
				releaseMFTActivations(activations[index+1:])
				return transform, MFH264DecoderInfo{Hardware: group.hardware, Async: async, Config: cfg}, nil
			}
			releaseIUnknown(transform)
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
	cfg, err := NormalizeVideoConfig(cfg)
	if err != nil {
		return nil, err
	}
	session := &MFH264Decoder{
		commands: make(chan mfDecodeCommand),
		done:     make(chan struct{}),
	}
	initCh := make(chan mfDecoderInit, 1)
	go session.run(cfg, preferHardware, initCh)

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

func (d *MFH264Decoder) run(cfg VideoConfig, preferHardware bool, initCh chan<- mfDecoderInit) {
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

	transform, info, err := openConfiguredH264Decoder(context.Background(), cfg, preferHardware)
	if err != nil {
		initCh <- mfDecoderInit{err: err}
		return
	}
	defer releaseIUnknown(transform)

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
		asyncState = newMFAsyncDecodeState(transform, info)
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
					frames, err := processSyncH264Decode(transform, info, *command.input)
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

	needInput int
	pending   []mfDecodeCommand
	inFlight  []mfDecodeCommand
	output    []DecodedFrame
	fatal     error
}

func newMFAsyncDecodeState(transform unsafe.Pointer, info MFH264DecoderInfo) *mfAsyncDecodeState {
	return &mfAsyncDecodeState{transform: transform, info: info}
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
		if len(s.inFlight) > 0 {
			s.finishOldest()
		}
		s.needInput++
		s.submitAvailable()

	case meTransformHaveOutput:
		fallback := time.Duration(0)
		if len(s.inFlight) > 0 && s.inFlight[0].input != nil {
			fallback = s.inFlight[0].input.timestamp
		}
		frame, hr, err := processDecoderOutputOnce(s.transform, s.info, fallback)
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

func processDecoderOutputOnce(transform unsafe.Pointer, info MFH264DecoderInfo, fallbackTimestamp time.Duration) (*DecodedFrame, uintptr, error) {
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
	data, err := sampleBytes(out.Sample)
	timestamp := sampleTimestamp(out.Sample, fallbackTimestamp)
	releaseIUnknown(out.Sample)
	if err != nil {
		return nil, hr, err
	}
	required := info.Config.Width * info.Config.Height * 3 / 2
	if len(data) < required {
		return nil, hr, fmt.Errorf("%w: decoder NV12 sample is %d bytes, need %d", ErrInvalidFrame, len(data), required)
	}
	frame := &DecodedFrame{
		Format:    PixelFormatNV12,
		Pix:       append([]byte(nil), data[:required]...),
		Width:     info.Config.Width,
		Height:    info.Config.Height,
		Stride:    info.Config.Width,
		Timestamp: timestamp,
		Hardware:  info.Hardware,
	}
	return frame, hr, nil
}

func drainSyncH264DecoderOutput(transform unsafe.Pointer, info MFH264DecoderInfo, fallbackTimestamp time.Duration) ([]DecodedFrame, error) {
	var frames []DecodedFrame
	for {
		frame, hr, err := processDecoderOutputOnce(transform, info, fallbackTimestamp)
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

func processSyncH264Decode(transform unsafe.Pointer, info MFH264DecoderInfo, input mfDecodeInput) ([]DecodedFrame, error) {
	sample, err := createInputSample(input.data, input.timestamp, input.duration)
	if err != nil {
		return nil, err
	}
	defer releaseIUnknown(sample)

	var frames []DecodedFrame
	hr := processTransformInputSample(transform, sample)
	if uint32(hr) == mfENotAccepting {
		pending, err := drainSyncH264DecoderOutput(transform, info, input.timestamp)
		if err != nil {
			return nil, err
		}
		frames = append(frames, pending...)
		hr = processTransformInputSample(transform, sample)
	}
	if hresultFailed(hr) {
		return nil, hresultError("H.264 decoder IMFTransform.ProcessInput", hr)
	}
	decoded, err := drainSyncH264DecoderOutput(transform, info, input.timestamp)
	if err != nil {
		return nil, err
	}
	return append(frames, decoded...), nil
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
