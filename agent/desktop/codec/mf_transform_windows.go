//go:build windows

package codec

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	imfAttributesGetUINT32   = 7
	imfAttributesGetBlobSize = 14
	imfAttributesGetBlob     = 15
	imfAttributesSetUINT32   = 21
	imfAttributesSetUINT64   = 22
	imfAttributesSetGUID     = 24

	imfActivateActivateObject = 33

	imfTransformGetOutputStreamInfo = 7
	imfTransformGetAttributes       = 8
	imfTransformSetInputType        = 15
	imfTransformSetOutputType       = 16
	imfTransformProcessMessage      = 23
	imfTransformProcessInput        = 24
	imfTransformProcessOutput       = 25

	mftMessageCommandFlush         = 0x00000000
	mftMessageSetD3DManager        = 0x00000002
	mftMessageNotifyBeginStreaming = 0x10000000
	mftMessageNotifyEndStreaming   = 0x10000001
	mftMessageNotifyEndOfStream    = 0x10000002
	mftMessageNotifyStartOfStream  = 0x10000003

	mfVideoInterlaceProgressive = 2

	mftOutputStreamProvidesSamples   = 0x100
	mftOutputStreamCanProvideSamples = 0x200
)

var (
	iidIMFTransform = windows.GUID{
		Data1: 0xbf94c121, Data2: 0x5b05, Data3: 0x4e6f,
		Data4: [8]byte{0x80, 0x00, 0xba, 0x59, 0x89, 0x61, 0x41, 0x4d},
	}
	mfMTMajorType = windows.GUID{
		Data1: 0x48eba18e, Data2: 0xf8c9, Data3: 0x4687,
		Data4: [8]byte{0xbf, 0x11, 0x0a, 0x74, 0xc9, 0xf9, 0x6a, 0x8f},
	}
	mfMTSubtype = windows.GUID{
		Data1: 0xf7e34c9a, Data2: 0x42e8, Data3: 0x4714,
		Data4: [8]byte{0xb7, 0x4b, 0xcb, 0x29, 0xd7, 0x2c, 0x35, 0xe5},
	}
	mfMTFrameSize = windows.GUID{
		Data1: 0x1652c33d, Data2: 0xd6b2, Data3: 0x4012,
		Data4: [8]byte{0xb8, 0x34, 0x72, 0x03, 0x08, 0x49, 0xa3, 0x7d},
	}
	mfMTFrameRate = windows.GUID{
		Data1: 0xc459a2e8, Data2: 0x3d2c, Data3: 0x4e44,
		Data4: [8]byte{0xb1, 0x32, 0xfe, 0xe5, 0x15, 0x6c, 0x7b, 0xb0},
	}
	mfMTPixelAspectRatio = windows.GUID{
		Data1: 0xc6376a1e, Data2: 0x8d0a, Data3: 0x4027,
		Data4: [8]byte{0xbe, 0x45, 0x6d, 0x9a, 0x0a, 0xd3, 0x9b, 0xb6},
	}
	mfMTInterlaceMode = windows.GUID{
		Data1: 0xe2724bb8, Data2: 0xe676, Data3: 0x4806,
		Data4: [8]byte{0xb4, 0xb2, 0xa8, 0xd6, 0xef, 0xb4, 0x4c, 0xcd},
	}
	mfMTAvgBitrate = windows.GUID{
		Data1: 0x20332624, Data2: 0xfb0d, Data3: 0x4d9e,
		Data4: [8]byte{0xbd, 0x0d, 0xcb, 0xf6, 0x78, 0x6c, 0x10, 0x2e},
	}
	mfMTMPEGSequenceHeader = windows.GUID{
		Data1: 0x3c036de7, Data2: 0x3ad0, Data3: 0x4c9e,
		Data4: [8]byte{0x92, 0x16, 0xee, 0x6d, 0x6a, 0xc2, 0x1c, 0xb3},
	}
	mfMTFixedSizeSamples = windows.GUID{
		Data1: 0xb8ebefaf, Data2: 0xb718, Data3: 0x4e04,
		Data4: [8]byte{0xb0, 0xa9, 0x11, 0x67, 0x75, 0xe3, 0x32, 0x1b},
	}
	mfMTSampleSize = windows.GUID{
		Data1: 0xdad3ab78, Data2: 0x1990, Data3: 0x408b,
		Data4: [8]byte{0xbc, 0xe2, 0xeb, 0xa6, 0x73, 0xda, 0xcc, 0x10},
	}
	mfTransformAsync = windows.GUID{
		Data1: 0xf81a699a, Data2: 0x649a, Data3: 0x497d,
		Data4: [8]byte{0x8c, 0x73, 0x29, 0xf8, 0xfe, 0xd6, 0xad, 0x7a},
	}
	mfTransformAsyncUnlock = windows.GUID{
		Data1: 0xe5666d6b, Data2: 0x3422, Data3: 0x4eb6,
		Data4: [8]byte{0xa4, 0x21, 0xda, 0x7d, 0xb1, 0xf8, 0xe2, 0x07},
	}
	mfLowLatency = windows.GUID{
		Data1: 0x9c27891a, Data2: 0xed7a, Data3: 0x40e1,
		Data4: [8]byte{0x88, 0xe8, 0xb2, 0x27, 0x27, 0xa0, 0x24, 0xee},
	}
)

type MFH264TransformInfo struct {
	Hardware       bool
	Async          bool
	Config         VideoConfig
	SequenceHeader []byte
}

type MFH264Transform struct {
	info      MFH264TransformInfo
	commands  chan mfTransformCommand
	done      chan struct{}
	closeOnce sync.Once
}

type mfTransformCommand struct {
	close    bool
	input    *mfEncodeInput
	forceIDR bool
	bitrate  *int
	reply    chan mfTransformResult
}

type mfEncodeInput struct {
	data      []byte
	timestamp time.Duration
	duration  time.Duration
}

type mfTransformResult struct {
	packets []EncodedPacket
	err     error
}

type mftOutputStreamInfo struct {
	Flags     uint32
	Size      uint32
	Alignment uint32
}

type mftOutputDataBuffer struct {
	StreamID uint32
	Sample   unsafe.Pointer
	Status   uint32
	Events   unsafe.Pointer
}

type mfTransformInit struct {
	info MFH264TransformInfo
	err  error
}

func comMethod(object unsafe.Pointer, index int) uintptr {
	vtable := *(*unsafe.Pointer)(object)
	return (*[64]uintptr)(vtable)[index]
}

func comCall(object unsafe.Pointer, index int, args ...uintptr) uintptr {
	params := make([]uintptr, 0, len(args)+1)
	params = append(params, uintptr(object))
	params = append(params, args...)
	result, _, _ := syscall.SyscallN(comMethod(object, index), params...)
	return result
}

func attributeSetGUID(attributes unsafe.Pointer, key, value *windows.GUID) error {
	hr := comCall(attributes, imfAttributesSetGUID, uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(value)))
	if hresultFailed(hr) {
		return hresultError("IMFAttributes.SetGUID", hr)
	}
	return nil
}

func attributeSetUINT32(attributes unsafe.Pointer, key *windows.GUID, value uint32) error {
	hr := comCall(attributes, imfAttributesSetUINT32, uintptr(unsafe.Pointer(key)), uintptr(value))
	if hresultFailed(hr) {
		return hresultError("IMFAttributes.SetUINT32", hr)
	}
	return nil
}

func attributeSetUINT64(attributes unsafe.Pointer, key *windows.GUID, value uint64) error {
	hr := comCall(attributes, imfAttributesSetUINT64, uintptr(unsafe.Pointer(key)), uintptr(value))
	if hresultFailed(hr) {
		return hresultError("IMFAttributes.SetUINT64", hr)
	}
	return nil
}

func attributeGetUINT32(attributes unsafe.Pointer, key *windows.GUID) (uint32, error) {
	var value uint32
	hr := comCall(attributes, imfAttributesGetUINT32, uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(&value)))
	if hresultFailed(hr) {
		return 0, hresultError("IMFAttributes.GetUINT32", hr)
	}
	return value, nil
}

func attributeGetBlob(attributes unsafe.Pointer, key *windows.GUID) ([]byte, error) {
	var size uint32
	hr := comCall(attributes, imfAttributesGetBlobSize, uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(&size)))
	if hresultFailed(hr) {
		return nil, hresultError("IMFAttributes.GetBlobSize", hr)
	}
	if size == 0 {
		return nil, nil
	}
	data := make([]byte, size)
	var written uint32
	hr = comCall(
		attributes,
		imfAttributesGetBlob,
		uintptr(unsafe.Pointer(key)),
		uintptr(unsafe.Pointer(&data[0])),
		uintptr(size),
		uintptr(unsafe.Pointer(&written)),
	)
	if hresultFailed(hr) {
		return nil, hresultError("IMFAttributes.GetBlob", hr)
	}
	if written > size {
		return nil, errors.New("IMFAttributes.GetBlob returned an invalid size")
	}
	return data[:written], nil
}

func packPair(high, low uint32) uint64 {
	return uint64(high)<<32 | uint64(low)
}

func createVideoMediaType(subtype *windows.GUID, cfg VideoConfig, compressed bool) (unsafe.Pointer, error) {
	var mediaType unsafe.Pointer
	hr, _, _ := procMFCreateMediaType.Call(uintptr(unsafe.Pointer(&mediaType)))
	if hresultFailed(hr) {
		return nil, hresultError("MFCreateMediaType", hr)
	}
	fail := func(err error) (unsafe.Pointer, error) {
		releaseIUnknown(mediaType)
		return nil, err
	}
	if err := attributeSetGUID(mediaType, &mfMTMajorType, &mfMediaTypeVideo); err != nil {
		return fail(err)
	}
	if err := attributeSetGUID(mediaType, &mfMTSubtype, subtype); err != nil {
		return fail(err)
	}
	if err := attributeSetUINT64(mediaType, &mfMTFrameSize, packPair(uint32(cfg.Width), uint32(cfg.Height))); err != nil {
		return fail(err)
	}
	if err := attributeSetUINT64(mediaType, &mfMTFrameRate, packPair(uint32(cfg.FPS), 1)); err != nil {
		return fail(err)
	}
	if err := attributeSetUINT64(mediaType, &mfMTPixelAspectRatio, packPair(1, 1)); err != nil {
		return fail(err)
	}
	if err := attributeSetUINT32(mediaType, &mfMTInterlaceMode, mfVideoInterlaceProgressive); err != nil {
		return fail(err)
	}
	if compressed {
		if err := attributeSetUINT32(mediaType, &mfMTAvgBitrate, uint32(cfg.TargetBitrate)); err != nil {
			return fail(err)
		}
	} else {
		if err := attributeSetUINT32(mediaType, &mfMTFixedSizeSamples, 1); err != nil {
			return fail(err)
		}
		if err := attributeSetUINT32(mediaType, &mfMTSampleSize, uint32(cfg.Width*cfg.Height*3/2)); err != nil {
			return fail(err)
		}
	}
	return mediaType, nil
}

func activateObject(activation unsafe.Pointer, iid *windows.GUID) (unsafe.Pointer, error) {
	var object unsafe.Pointer
	hr := comCall(activation, imfActivateActivateObject, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&object)))
	if hresultFailed(hr) {
		return nil, hresultError("IMFActivate.ActivateObject", hr)
	}
	if object == nil {
		return nil, errors.New("IMFActivate.ActivateObject returned a nil transform")
	}
	return object, nil
}

func transformAttributes(transform unsafe.Pointer) (unsafe.Pointer, error) {
	var attributes unsafe.Pointer
	hr := comCall(transform, imfTransformGetAttributes, uintptr(unsafe.Pointer(&attributes)))
	if hresultFailed(hr) {
		return nil, hresultError("IMFTransform.GetAttributes", hr)
	}
	if attributes == nil {
		return nil, errors.New("IMFTransform.GetAttributes returned nil")
	}
	return attributes, nil
}

func setTransformType(transform unsafe.Pointer, method int, mediaType unsafe.Pointer) error {
	hr := comCall(transform, method, 0, uintptr(mediaType), 0)
	if hresultFailed(hr) {
		if method == imfTransformSetOutputType {
			return hresultError("IMFTransform.SetOutputType", hr)
		}
		return hresultError("IMFTransform.SetInputType", hr)
	}
	return nil
}

func processTransformMessageParam(transform unsafe.Pointer, message uint32, param uintptr) error {
	hr := comCall(transform, imfTransformProcessMessage, uintptr(message), param)
	if hresultFailed(hr) {
		return hresultError("IMFTransform.ProcessMessage", hr)
	}
	return nil
}

func processTransformMessage(transform unsafe.Pointer, message uint32) error {
	return processTransformMessageParam(transform, message, 0)
}

func configureH264Transform(transform unsafe.Pointer, cfg VideoConfig) (bool, []byte, error) {
	attributes, err := transformAttributes(transform)
	if err == nil {
		defer releaseIUnknown(attributes)
		async, getErr := attributeGetUINT32(attributes, &mfTransformAsync)
		isAsync := getErr == nil && async != 0
		if isAsync {
			if err := attributeSetUINT32(attributes, &mfTransformAsyncUnlock, 1); err != nil {
				return false, nil, fmt.Errorf("unlock async MFT: %w", err)
			}
		}
		if !cfg.DisableLowLatency {
			_ = attributeSetUINT32(attributes, &mfLowLatency, 1)
		}

		outputType, err := createVideoMediaType(&mfVideoFormatH264, cfg, true)
		if err != nil {
			return false, nil, err
		}
		defer releaseIUnknown(outputType)
		// The Microsoft H.264 encoder requires its output media type first.
		if err := setTransformType(transform, imfTransformSetOutputType, outputType); err != nil {
			return false, nil, err
		}
		sequenceHeader, _ := attributeGetBlob(outputType, &mfMTMPEGSequenceHeader)

		inputType, err := createVideoMediaType(&mfVideoFormatNV12, cfg, false)
		if err != nil {
			return false, nil, err
		}
		defer releaseIUnknown(inputType)
		if err := setTransformType(transform, imfTransformSetInputType, inputType); err != nil {
			return false, nil, err
		}
		if err := processTransformMessage(transform, mftMessageNotifyBeginStreaming); err != nil {
			return false, nil, err
		}
		if err := processTransformMessage(transform, mftMessageNotifyStartOfStream); err != nil {
			return false, nil, err
		}
		return isAsync, sequenceHeader, nil
	}

	// Some older transforms do not expose a transform attribute store. Media
	// type negotiation still works, but async/low-latency hints cannot be set.
	outputType, err := createVideoMediaType(&mfVideoFormatH264, cfg, true)
	if err != nil {
		return false, nil, err
	}
	defer releaseIUnknown(outputType)
	if err := setTransformType(transform, imfTransformSetOutputType, outputType); err != nil {
		return false, nil, err
	}
	sequenceHeader, _ := attributeGetBlob(outputType, &mfMTMPEGSequenceHeader)
	inputType, err := createVideoMediaType(&mfVideoFormatNV12, cfg, false)
	if err != nil {
		return false, nil, err
	}
	defer releaseIUnknown(inputType)
	if err := setTransformType(transform, imfTransformSetInputType, inputType); err != nil {
		return false, nil, err
	}
	if err := processTransformMessage(transform, mftMessageNotifyBeginStreaming); err != nil {
		return false, nil, err
	}
	if err := processTransformMessage(transform, mftMessageNotifyStartOfStream); err != nil {
		return false, nil, err
	}
	return false, sequenceHeader, nil
}

func activationGroups(preferHardware bool) []struct {
	hardware bool
	flags    uint32
} {
	hardware := struct {
		hardware bool
		flags    uint32
	}{true, uint32(mftEnumHardware | mftEnumSortAndFilter)}
	software := struct {
		hardware bool
		flags    uint32
	}{false, uint32(mftEnumSync | mftEnumAsync | mftEnumLocal | mftEnumSortAndFilter)}
	if preferHardware {
		return []struct {
			hardware bool
			flags    uint32
		}{hardware, software}
	}
	return []struct {
		hardware bool
		flags    uint32
	}{software, hardware}
}

func openConfiguredH264Transform(ctx context.Context, cfg VideoConfig, preferHardware, allowAsync bool) (unsafe.Pointer, MFH264TransformInfo, error) {
	rawNV12 := mftRegisterTypeInfo{MajorType: mfMediaTypeVideo, Subtype: mfVideoFormatNV12}
	h264 := mftRegisterTypeInfo{MajorType: mfMediaTypeVideo, Subtype: mfVideoFormatH264}
	var failures []error

	for _, group := range activationGroups(preferHardware) {
		if err := ctx.Err(); err != nil {
			return nil, MFH264TransformInfo{}, err
		}
		activations, err := enumerateMFTActivations(mftCategoryVideoEncoder, group.flags, &rawNV12, &h264)
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
			async, sequenceHeader, err := configureH264Transform(transform, cfg)
			if err == nil && (!async || allowAsync) {
				releaseMFTActivations(activations[index+1:])
				return transform, MFH264TransformInfo{
					Hardware: group.hardware, Async: async, Config: cfg,
					SequenceHeader: append([]byte(nil), sequenceHeader...),
				}, nil
			}
			releaseIUnknown(transform)
			if err != nil {
				failures = append(failures, err)
			} else {
				failures = append(failures, errors.New("asynchronous MFT requires event-driven processing"))
			}
		}
		releaseMFTActivations(activations)
	}
	if len(failures) == 0 {
		return nil, MFH264TransformInfo{}, ErrEncoderUnavailable
	}
	return nil, MFH264TransformInfo{}, fmt.Errorf("%w: %v", ErrEncoderUnavailable, errors.Join(failures...))
}

func OpenMFH264Transform(ctx context.Context, cfg VideoConfig, preferHardware bool) (*MFH264Transform, error) {
	return openMFH264Transform(ctx, cfg, preferHardware, true)
}

func openMFH264Transform(ctx context.Context, cfg VideoConfig, preferHardware, allowAsync bool) (*MFH264Transform, error) {
	cfg, err := NormalizeVideoConfig(cfg)
	if err != nil {
		return nil, err
	}
	initCh := make(chan mfTransformInit, 1)
	session := &MFH264Transform{
		commands: make(chan mfTransformCommand),
		done:     make(chan struct{}),
	}
	go session.run(cfg, preferHardware, allowAsync, initCh)

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

func (s *MFH264Transform) run(cfg VideoConfig, preferHardware, allowAsync bool, initCh chan<- mfTransformInit) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(s.done)

	uninitCOM, err := initializeCOM()
	if err != nil {
		initCh <- mfTransformInit{err: err}
		return
	}
	defer uninitCOM()

	shutdownMF, err := startupMediaFoundation()
	if err != nil {
		initCh <- mfTransformInit{err: err}
		return
	}
	defer shutdownMF()

	transform, info, err := openConfiguredH264Transform(context.Background(), cfg, preferHardware, allowAsync)
	if err != nil {
		initCh <- mfTransformInit{err: err}
		return
	}
	defer releaseIUnknown(transform)

	var eventPump *mfEventPump
	var eventCh <-chan mfAsyncEvent
	var asyncState *mfAsyncState
	if info.Async {
		eventPump, err = startMFEventPump(transform)
		if err != nil {
			initCh <- mfTransformInit{err: fmt.Errorf("start Media Foundation async event pump: %w", err)}
			return
		}
		defer eventPump.Close()
		eventCh = eventPump.events
		asyncState = newMFAsyncState(transform, info.Config)
	}
	initCh <- mfTransformInit{info: info}

	for {
		select {
		case command, ok := <-s.commands:
			if !ok {
				if asyncState != nil {
					asyncState.fail(ErrEncoderUnavailable)
				}
				return
			}
			if command.close {
				if asyncState != nil {
					asyncState.fail(ErrEncoderUnavailable)
				}
				if eventPump != nil {
					if err := eventPump.Close(); err != nil {
						command.reply <- mfTransformResult{err: err}
						return
					}
					eventPump = nil
					eventCh = nil
				}
				_ = processTransformMessage(transform, mftMessageNotifyEndOfStream)
				_ = processTransformMessage(transform, mftMessageNotifyEndStreaming)
				_ = processTransformMessage(transform, mftMessageCommandFlush)
				command.reply <- mfTransformResult{}
				return
			}
			if command.forceIDR {
				command.reply <- mfTransformResult{err: forceH264IDR(transform)}
				continue
			}
			if command.bitrate != nil {
				command.reply <- mfTransformResult{err: setH264MeanBitrate(transform, *command.bitrate)}
				continue
			}
			if command.input != nil {
				if asyncState != nil {
					asyncState.enqueue(command)
				} else {
					packets, err := processSyncH264Frame(transform, info.Config, *command.input)
					command.reply <- mfTransformResult{packets: packets, err: err}
				}
				continue
			}
			command.reply <- mfTransformResult{err: errors.New("unsupported Media Foundation transform command")}

		case event, ok := <-eventCh:
			if !ok {
				if asyncState != nil && asyncState.fatal == nil {
					asyncState.fail(errors.New("Media Foundation async event pump stopped"))
				}
				eventCh = nil
				continue
			}
			asyncState.handle(event)
		}
	}
}

func getOutputStreamInfo(transform unsafe.Pointer) (mftOutputStreamInfo, error) {
	var info mftOutputStreamInfo
	hr := comCall(transform, imfTransformGetOutputStreamInfo, 0, uintptr(unsafe.Pointer(&info)))
	if hresultFailed(hr) {
		return mftOutputStreamInfo{}, hresultError("IMFTransform.GetOutputStreamInfo", hr)
	}
	return info, nil
}

func processTransformInputSample(transform, sample unsafe.Pointer) uintptr {
	return comCall(transform, imfTransformProcessInput, 0, uintptr(sample), 0)
}

func processTransformOutputOnce(transform unsafe.Pointer, fallbackTimestamp time.Duration) (*EncodedPacket, uintptr, error) {
	info, err := getOutputStreamInfo(transform)
	if err != nil {
		return nil, 0, err
	}

	var outputSample unsafe.Pointer
	if info.Flags&(mftOutputStreamProvidesSamples|mftOutputStreamCanProvideSamples) == 0 {
		if info.Size == 0 {
			return nil, 0, errors.New("MFT requires a caller output sample but reported zero buffer size")
		}
		outputSample, err = createOutputSample(info.Size, info.Alignment)
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
		return nil, hr, errors.New("MFT ProcessOutput succeeded without a sample")
	}
	data, err := sampleBytes(out.Sample)
	packet := &EncodedPacket{
		Codec:     "h264",
		Data:      data,
		Timestamp: sampleTimestamp(out.Sample, fallbackTimestamp),
		KeyFrame:  sampleIsCleanPoint(out.Sample),
	}
	releaseIUnknown(out.Sample)
	if err != nil {
		return nil, hr, err
	}
	if len(packet.Data) == 0 {
		return nil, hr, nil
	}
	return packet, hr, nil
}

func drainSyncH264Output(transform unsafe.Pointer, fallbackTimestamp time.Duration) ([]EncodedPacket, error) {
	var packets []EncodedPacket
	for {
		packet, hr, err := processTransformOutputOnce(transform, fallbackTimestamp)
		if err != nil {
			return nil, err
		}
		if uint32(hr) == mfETransformNeedMoreInput {
			return packets, nil
		}
		if hresultFailed(hr) {
			return nil, hresultError("IMFTransform.ProcessOutput", hr)
		}
		if packet != nil {
			packets = append(packets, *packet)
		}
	}
}

func processSyncH264Frame(transform unsafe.Pointer, cfg VideoConfig, input mfEncodeInput) ([]EncodedPacket, error) {
	sample, err := createInputSample(input.data, input.timestamp, input.duration)
	if err != nil {
		return nil, err
	}
	defer releaseIUnknown(sample)

	var packets []EncodedPacket
	hr := processTransformInputSample(transform, sample)
	if uint32(hr) == mfENotAccepting {
		pending, err := drainSyncH264Output(transform, input.timestamp)
		if err != nil {
			return nil, err
		}
		packets = append(packets, pending...)
		hr = processTransformInputSample(transform, sample)
	}
	if hresultFailed(hr) {
		return nil, hresultError("IMFTransform.ProcessInput", hr)
	}
	encoded, err := drainSyncH264Output(transform, input.timestamp)
	if err != nil {
		return nil, err
	}
	return append(packets, encoded...), nil
}

func (s *MFH264Transform) EncodeNV12(ctx context.Context, data []byte, timestamp time.Duration) ([]EncodedPacket, error) {
	if s == nil {
		return nil, ErrEncoderUnavailable
	}
	if len(data) != s.info.Config.Width*s.info.Config.Height*3/2 {
		return nil, fmt.Errorf("%w: NV12 sample size does not match configured frame", ErrInvalidFrame)
	}
	duration := time.Second / time.Duration(s.info.Config.FPS)
	reply := make(chan mfTransformResult, 1)
	command := mfTransformCommand{
		input: &mfEncodeInput{
			data:      append([]byte(nil), data...),
			timestamp: timestamp,
			duration:  duration,
		},
		reply: reply,
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.done:
		return nil, ErrEncoderUnavailable
	case s.commands <- command:
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.done:
		return nil, ErrEncoderUnavailable
	case result := <-reply:
		return result.packets, result.err
	}
}

func (s *MFH264Transform) runControl(ctx context.Context, command mfTransformCommand) error {
	if s == nil {
		return ErrEncoderUnavailable
	}
	if command.reply == nil {
		command.reply = make(chan mfTransformResult, 1)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return ErrEncoderUnavailable
	case s.commands <- command:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return ErrEncoderUnavailable
	case result := <-command.reply:
		return result.err
	}
}

func (s *MFH264Transform) ForceIDR(ctx context.Context) error {
	return s.runControl(ctx, mfTransformCommand{forceIDR: true})
}

func (s *MFH264Transform) SetBitrate(ctx context.Context, bitrate int) error {
	if bitrate < 250_000 || bitrate > 100_000_000 {
		return fmt.Errorf("%w: bitrate must be between 250 kbps and 100 Mbps", ErrInvalidVideoConfig)
	}
	value := bitrate
	return s.runControl(ctx, mfTransformCommand{bitrate: &value})
}

func (s *MFH264Transform) Info() MFH264TransformInfo {
	if s == nil {
		return MFH264TransformInfo{}
	}
	return s.info
}

func (s *MFH264Transform) Close() error {
	if s == nil {
		return nil
	}
	var closeErr error
	s.closeOnce.Do(func() {
		reply := make(chan mfTransformResult, 1)
		select {
		case s.commands <- mfTransformCommand{close: true, reply: reply}:
			closeErr = (<-reply).err
			close(s.commands)
		case <-s.done:
		}
	})
	<-s.done
	return closeErr
}
