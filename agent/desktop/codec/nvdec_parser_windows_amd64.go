//go:build windows && amd64

package codec

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

const (
	nvdecParserMaxDecodeSurfaces uint32 = 1
	nvdecParserClockRate         uint32 = 10_000_000

	nvdecPacketEndOfStream  uint32 = 0x01
	nvdecPacketTimestamp    uint32 = 0x02
	nvdecPacketEndOfPicture uint32 = 0x08

	nvdecSurfaceFormatYUV444 int32  = 2
	nvdecDeinterlaceWeave    int32  = 0
	nvdecCreatePreferCUVID   uint32 = 0x04

	nvdecOutputSurfaces uint32 = 2
)

type nvdecRect32 struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

type nvdecRect16 struct {
	Left   int16
	Top    int16
	Right  int16
	Bottom int16
}

type nvdecVideoFormat struct {
	Codec                  int32
	FrameRateNumerator     uint32
	FrameRateDenominator   uint32
	ProgressiveSequence    uint8
	BitDepthLumaMinus8     uint8
	BitDepthChromaMinus8   uint8
	MinNumDecodeSurfaces   uint8
	CodedWidth             uint32
	CodedHeight            uint32
	DisplayArea            nvdecRect32
	ChromaFormat           int32
	Bitrate                uint32
	DisplayAspectRatioX    int32
	DisplayAspectRatioY    int32
	VideoSignalDescription [4]byte
	SequenceHeaderDataLen  uint32
}

type nvdecParserParams struct {
	CodecType            int32
	MaxNumDecodeSurfaces uint32
	ClockRate            uint32
	ErrorThreshold       uint32
	MaxDisplayDelay      uint32
	Flags                uint32
	Reserved1            [4]uint32
	UserData             uintptr
	SequenceCallback     uintptr
	DecodeCallback       uintptr
	DisplayCallback      uintptr
	OperatingPointCB     uintptr
	SEICallback          uintptr
	Reserved2            [5]uintptr
	ExtVideoInfo         uintptr
}

type nvdecSourceDataPacket struct {
	Flags       uint32
	PayloadSize uint32
	Payload     uintptr
	Timestamp   int64
}

type nvdecDecodeCreateInfo struct {
	Width               uint32
	Height              uint32
	NumDecodeSurfaces   uint32
	CodecType           int32
	ChromaFormat        int32
	CreationFlags       uint32
	BitDepthMinus8      uint32
	IntraDecodeOnly     uint32
	MaxWidth            uint32
	MaxHeight           uint32
	Reserved1           uint32
	DisplayArea         nvdecRect16
	OutputFormat        int32
	DeinterlaceMode     int32
	TargetWidth         uint32
	TargetHeight        uint32
	NumOutputSurfaces   uint32
	VideoContextLock    uintptr
	TargetRect          nvdecRect16
	EnableHistogram     uint32
	EnableDecodeFeature uint32
	Reserved2           [3]uint32
}

type nvdecHEVC444Parser struct {
	callMu sync.Mutex
	mu     sync.Mutex

	session *nvdecD3D11Session
	cfg     VideoConfig

	handle  uintptr
	parser  uintptr
	decoder uintptr

	sequenceSeen bool
	decodeCalls  uint64
	displayCalls uint64
	callbackErr  error

	surfaceWidth  int
	surfaceHeight int
	displayQueue  []nvdecParserDisplayInfo
	mappedFrames  map[uint64]*nvdecMappedFrame
	closed        bool
}

var (
	nvdecParserHandleCounter atomic.Uint64
	nvdecParserHandleMap     sync.Map

	nvdecSequenceCallbackProc = syscall.NewCallback(nvdecSequenceCallback)
	nvdecDecodeCallbackProc   = syscall.NewCallback(nvdecDecodeCallback)
	nvdecDisplayCallbackProc  = syscall.NewCallback(nvdecDisplayCallback)
)

func nextNVDECParserHandle() uintptr {
	for {
		id := nvdecParserHandleCounter.Add(1)
		if id != 0 {
			return uintptr(id)
		}
	}
}

func lookupNVDECParser(handle uintptr) *nvdecHEVC444Parser {
	value, ok := nvdecParserHandleMap.Load(handle)
	if !ok {
		return nil
	}
	parser, _ := value.(*nvdecHEVC444Parser)
	return parser
}

func nvdecSequenceCallback(userData uintptr, formatPtr uintptr) uintptr {
	parser := lookupNVDECParser(userData)
	if parser == nil {
		return 0
	}
	surfaces, err := parser.handleSequence(formatPtr)
	if err != nil {
		parser.setCallbackError(err)
		return 0
	}
	if surfaces < 1 {
		surfaces = 1
	}
	return uintptr(surfaces)
}

func nvdecDecodeCallback(userData uintptr, pictureParams uintptr) uintptr {
	parser := lookupNVDECParser(userData)
	if parser == nil {
		return 0
	}
	if err := parser.handleDecode(pictureParams); err != nil {
		parser.setCallbackError(err)
		return 0
	}
	return 1
}

func nvdecDisplayCallback(userData uintptr, displayInfo uintptr) uintptr {
	parser := lookupNVDECParser(userData)
	if parser == nil {
		return 0
	}
	if err := parser.handleDisplay(displayInfo); err != nil {
		parser.setCallbackError(err)
		return 0
	}
	return 1
}

func (p *nvdecHEVC444Parser) setCallbackError(err error) {
	if p == nil || err == nil {
		return
	}
	p.mu.Lock()
	if p.callbackErr == nil {
		p.callbackErr = err
	}
	p.mu.Unlock()
}

func validateNVDECHEVC444Format(
	format *nvdecVideoFormat,
	cfg VideoConfig,
) (uint32, int, int, error) {
	if format == nil {
		return 0, 0, 0, fmt.Errorf("%w: NVDEC sequence format is nil", ErrDecoderUnavailable)
	}
	normalized, err := NormalizeVideoConfig(cfg)
	if err != nil {
		return 0, 0, 0, err
	}
	if format.Codec != nvVideoCodecHEVC {
		return 0, 0, 0, fmt.Errorf("%w: NVDEC codec=%d want HEVC=%d", ErrDecoderUnavailable, format.Codec, nvVideoCodecHEVC)
	}
	if format.ChromaFormat != nvVideoChroma444 {
		return 0, 0, 0, fmt.Errorf("%w: NVDEC chroma=%d want 4:4:4=%d", ErrDecoderUnavailable, format.ChromaFormat, nvVideoChroma444)
	}
	if format.BitDepthLumaMinus8 != 0 || format.BitDepthChromaMinus8 != 0 {
		return 0, 0, 0, fmt.Errorf(
			"%w: NVDEC bit depth luma=%d chroma=%d want 8-bit",
			ErrDecoderUnavailable,
			int(format.BitDepthLumaMinus8)+8,
			int(format.BitDepthChromaMinus8)+8,
		)
	}
	if format.ProgressiveSequence == 0 {
		return 0, 0, 0, fmt.Errorf("%w: NVDEC interlaced HEVC 4:4:4 is not supported", ErrDecoderUnavailable)
	}
	if format.CodedWidth == 0 || format.CodedHeight == 0 {
		return 0, 0, 0, fmt.Errorf("%w: NVDEC coded dimensions are empty", ErrDecoderUnavailable)
	}
	displayWidth := int(format.DisplayArea.Right - format.DisplayArea.Left)
	displayHeight := int(format.DisplayArea.Bottom - format.DisplayArea.Top)
	if displayWidth <= 0 {
		displayWidth = int(format.CodedWidth)
	}
	if displayHeight <= 0 {
		displayHeight = int(format.CodedHeight)
	}
	if displayWidth != normalized.Width || displayHeight != normalized.Height {
		return 0, 0, 0, fmt.Errorf(
			"%w: NVDEC sequence display=%dx%d expected=%dx%d",
			ErrDecoderUnavailable,
			displayWidth,
			displayHeight,
			normalized.Width,
			normalized.Height,
		)
	}
	surfaces := uint32(format.MinNumDecodeSurfaces)
	if surfaces == 0 {
		surfaces = 1
	}
	return surfaces, displayWidth, displayHeight, nil
}

func int16NVDEC(value int32, label string) (int16, error) {
	if value < -32768 || value > 32767 {
		return 0, fmt.Errorf("%w: NVDEC %s=%d overflows int16", ErrDecoderUnavailable, label, value)
	}
	return int16(value), nil
}

func buildNVDECDecodeCreateInfo(
	format *nvdecVideoFormat,
	cfg VideoConfig,
	videoCtxLock uintptr,
) (nvdecDecodeCreateInfo, uint32, error) {
	surfaces, displayWidth, displayHeight, err := validateNVDECHEVC444Format(format, cfg)
	if err != nil {
		return nvdecDecodeCreateInfo{}, 0, err
	}
	left, err := int16NVDEC(format.DisplayArea.Left, "display left")
	if err != nil {
		return nvdecDecodeCreateInfo{}, 0, err
	}
	top, err := int16NVDEC(format.DisplayArea.Top, "display top")
	if err != nil {
		return nvdecDecodeCreateInfo{}, 0, err
	}
	right, err := int16NVDEC(format.DisplayArea.Right, "display right")
	if err != nil {
		return nvdecDecodeCreateInfo{}, 0, err
	}
	bottom, err := int16NVDEC(format.DisplayArea.Bottom, "display bottom")
	if err != nil {
		return nvdecDecodeCreateInfo{}, 0, err
	}
	if right <= left || bottom <= top {
		left = 0
		top = 0
		right = int16(displayWidth)
		bottom = int16(displayHeight)
	}

	info := nvdecDecodeCreateInfo{
		Width:             format.CodedWidth,
		Height:            format.CodedHeight,
		NumDecodeSurfaces: surfaces,
		CodecType:         nvVideoCodecHEVC,
		ChromaFormat:      nvVideoChroma444,
		CreationFlags:     nvdecCreatePreferCUVID,
		BitDepthMinus8:    0,
		MaxWidth:          format.CodedWidth,
		MaxHeight:         format.CodedHeight,
		DisplayArea: nvdecRect16{
			Left: left, Top: top, Right: right, Bottom: bottom,
		},
		OutputFormat:      nvdecSurfaceFormatYUV444,
		DeinterlaceMode:   nvdecDeinterlaceWeave,
		TargetWidth:       uint32(displayWidth),
		TargetHeight:      uint32(displayHeight),
		NumOutputSurfaces: nvdecOutputSurfaces,
		VideoContextLock:  videoCtxLock,
	}
	return info, surfaces, nil
}

func openNVDECHEVC444Parser(
	ctx context.Context,
	cfg VideoConfig,
	device uintptr,
) (*nvdecHEVC444Parser, error) {
	normalized, err := NormalizeVideoConfig(cfg)
	if err != nil {
		return nil, err
	}
	if normalized.Chroma != Chroma444 || normalized.BitDepth != 8 {
		return nil, fmt.Errorf("%w: NVDEC HEVC requires 8-bit 4:4:4 video", ErrInvalidVideoConfig)
	}
	session, err := openNVDECHEVC444D3D11Session(ctx, device)
	if err != nil {
		return nil, err
	}
	cleanupSession := true
	defer func() {
		if cleanupSession {
			_ = session.Close()
		}
	}()

	api, err := session.API()
	if err != nil {
		return nil, err
	}
	parser := &nvdecHEVC444Parser{
		session:      session,
		cfg:          normalized,
		handle:       nextNVDECParserHandle(),
		mappedFrames: make(map[uint64]*nvdecMappedFrame),
	}
	nvdecParserHandleMap.Store(parser.handle, parser)
	cleanupHandle := true
	defer func() {
		if cleanupHandle {
			nvdecParserHandleMap.Delete(parser.handle)
		}
	}()

	params := nvdecParserParams{
		CodecType:            nvVideoCodecHEVC,
		MaxNumDecodeSurfaces: nvdecParserMaxDecodeSurfaces,
		ClockRate:            nvdecParserClockRate,
		MaxDisplayDelay:      0,
		UserData:             parser.handle,
		SequenceCallback:     nvdecSequenceCallbackProc,
		DecodeCallback:       nvdecDecodeCallbackProc,
		DisplayCallback:      nvdecDisplayCallbackProc,
	}
	status := cudaDriverCall(
		api.CuvidCreateVideoParser,
		uintptr(unsafe.Pointer(&parser.parser)),
		uintptr(unsafe.Pointer(&params)),
	)
	runtime.KeepAlive(&params)
	runtime.KeepAlive(parser)
	if status != 0 || parser.parser == 0 {
		return nil, fmt.Errorf("%w: cuvidCreateVideoParser returned %d", ErrDecoderUnavailable, status)
	}

	cleanupHandle = false
	cleanupSession = false
	return parser, nil
}

func (p *nvdecHEVC444Parser) handleSequence(formatPtr uintptr) (uint32, error) {
	if p == nil || formatPtr == 0 {
		return 0, fmt.Errorf("%w: NVDEC sequence callback received nil format", ErrDecoderUnavailable)
	}
	format := *(*nvdecVideoFormat)(unsafe.Pointer(formatPtr))
	runtime.KeepAlive(formatPtr)

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.session == nil {
		return 0, ErrDecoderUnavailable
	}
	info, surfaces, err := buildNVDECDecodeCreateInfo(&format, p.cfg, p.session.VideoContextLock())
	if err != nil {
		return 0, err
	}
	if p.decoder != 0 {
		p.sequenceSeen = true
		p.surfaceWidth = int(info.TargetWidth)
		p.surfaceHeight = int(info.TargetHeight)
		return surfaces, nil
	}

	api, err := p.session.API()
	if err != nil {
		return 0, err
	}
	caps := nvcuvidDecodeCaps{
		CodecType:      nvVideoCodecHEVC,
		ChromaFormat:   nvVideoChroma444,
		BitDepthMinus8: 0,
	}
	lock := p.session.VideoContextLock()
	if lock == 0 {
		return 0, ErrDecoderUnavailable
	}
	if status := cudaDriverCall(api.CuvidCtxLock, lock, 0); status != 0 {
		return 0, fmt.Errorf("%w: cuvidCtxLock returned %d", ErrDecoderUnavailable, status)
	}
	capsStatus := cudaDriverCall(
		api.CuvidGetDecoderCaps,
		uintptr(unsafe.Pointer(&caps)),
	)
	unlockStatus := cudaDriverCall(api.CuvidCtxUnlock, lock, 0)
	runtime.KeepAlive(&caps)
	if capsStatus != 0 {
		return 0, fmt.Errorf("%w: cuvidGetDecoderCaps returned %d", ErrDecoderUnavailable, capsStatus)
	}
	if unlockStatus != 0 {
		return 0, fmt.Errorf("%w: cuvidCtxUnlock returned %d", ErrDecoderUnavailable, unlockStatus)
	}
	if caps.IsSupported == 0 {
		return 0, fmt.Errorf("%w: NVDEC device does not support HEVC 8-bit 4:4:4", ErrDecoderUnavailable)
	}
	if caps.MaxWidth != 0 && info.Width > caps.MaxWidth {
		return 0, fmt.Errorf("%w: NVDEC coded width=%d exceeds max=%d", ErrDecoderUnavailable, info.Width, caps.MaxWidth)
	}
	if caps.MaxHeight != 0 && info.Height > caps.MaxHeight {
		return 0, fmt.Errorf("%w: NVDEC coded height=%d exceeds max=%d", ErrDecoderUnavailable, info.Height, caps.MaxHeight)
	}

	if status := cudaDriverCall(api.CuvidCtxLock, lock, 0); status != 0 {
		return 0, fmt.Errorf("%w: cuvidCtxLock(create decoder) returned %d", ErrDecoderUnavailable, status)
	}
	status := cudaDriverCall(
		api.CuvidCreateDecoder,
		uintptr(unsafe.Pointer(&p.decoder)),
		uintptr(unsafe.Pointer(&info)),
	)
	createUnlockStatus := cudaDriverCall(api.CuvidCtxUnlock, lock, 0)
	runtime.KeepAlive(&info)
	runtime.KeepAlive(p)
	if status != 0 || p.decoder == 0 {
		return 0, fmt.Errorf("%w: cuvidCreateDecoder returned %d", ErrDecoderUnavailable, status)
	}
	if createUnlockStatus != 0 {
		return 0, fmt.Errorf("%w: cuvidCtxUnlock(create decoder) returned %d", ErrDecoderUnavailable, createUnlockStatus)
	}
	p.sequenceSeen = true
	p.surfaceWidth = int(info.TargetWidth)
	p.surfaceHeight = int(info.TargetHeight)
	return surfaces, nil
}

func (p *nvdecHEVC444Parser) handleDecode(pictureParams uintptr) error {
	if p == nil || pictureParams == 0 {
		return fmt.Errorf("%w: NVDEC decode callback received nil picture params", ErrDecoderUnavailable)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.session == nil || p.decoder == 0 {
		return ErrDecoderUnavailable
	}
	api, err := p.session.API()
	if err != nil {
		return err
	}
	status := cudaDriverCall(api.CuvidDecodePicture, p.decoder, pictureParams)
	runtime.KeepAlive(pictureParams)
	if status != 0 {
		return fmt.Errorf("%w: cuvidDecodePicture returned %d", ErrDecoderUnavailable, status)
	}
	p.decodeCalls++
	return nil
}

func (p *nvdecHEVC444Parser) Parse(
	ctx context.Context,
	data []byte,
	timestamp time.Duration,
) error {
	if p == nil {
		return ErrDecoderUnavailable
	}
	if len(data) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	p.callMu.Lock()
	defer p.callMu.Unlock()

	p.mu.Lock()
	if p.closed || p.parser == 0 || p.session == nil {
		p.mu.Unlock()
		return ErrDecoderUnavailable
	}
	parserHandle := p.parser
	p.callbackErr = nil
	session := p.session
	p.mu.Unlock()

	api, err := session.API()
	if err != nil {
		return err
	}
	packet := nvdecSourceDataPacket{
		Flags:       nvdecPacketTimestamp | nvdecPacketEndOfPicture,
		PayloadSize: uint32(len(data)),
		Payload:     uintptr(unsafe.Pointer(&data[0])),
		Timestamp:   timestamp.Nanoseconds() * int64(nvdecParserClockRate) / int64(time.Second),
	}
	status := cudaDriverCall(
		api.CuvidParseVideoData,
		parserHandle,
		uintptr(unsafe.Pointer(&packet)),
	)
	runtime.KeepAlive(packet)
	runtime.KeepAlive(data)
	if status != 0 {
		return fmt.Errorf("%w: cuvidParseVideoData returned %d", ErrDecoderUnavailable, status)
	}
	p.mu.Lock()
	callbackErr := p.callbackErr
	p.mu.Unlock()
	return callbackErr
}

func (p *nvdecHEVC444Parser) EndOfStream(ctx context.Context) error {
	if p == nil {
		return ErrDecoderUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	p.callMu.Lock()
	defer p.callMu.Unlock()

	p.mu.Lock()
	if p.closed || p.parser == 0 || p.session == nil {
		p.mu.Unlock()
		return ErrDecoderUnavailable
	}
	parserHandle := p.parser
	p.callbackErr = nil
	session := p.session
	p.mu.Unlock()

	api, err := session.API()
	if err != nil {
		return err
	}
	packet := nvdecSourceDataPacket{Flags: nvdecPacketEndOfStream}
	status := cudaDriverCall(
		api.CuvidParseVideoData,
		parserHandle,
		uintptr(unsafe.Pointer(&packet)),
	)
	runtime.KeepAlive(&packet)
	if status != 0 {
		return fmt.Errorf("%w: cuvidParseVideoData(EOS) returned %d", ErrDecoderUnavailable, status)
	}
	p.mu.Lock()
	callbackErr := p.callbackErr
	p.mu.Unlock()
	return callbackErr
}

func (p *nvdecHEVC444Parser) DecoderCreated() bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.closed && p.sequenceSeen && p.decoder != 0
}

func (p *nvdecHEVC444Parser) Close() error {
	if p == nil {
		return nil
	}
	p.callMu.Lock()
	defer p.callMu.Unlock()

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	parserHandle := p.parser
	decoder := p.decoder
	session := p.session
	handle := p.handle
	mapped := make([]*nvdecMappedFrame, 0, len(p.mappedFrames))
	for _, frame := range p.mappedFrames {
		mapped = append(mapped, frame)
	}
	p.parser = 0
	p.decoder = 0
	p.session = nil
	p.handle = 0
	p.displayQueue = nil
	p.mappedFrames = nil
	p.surfaceWidth = 0
	p.surfaceHeight = 0
	p.mu.Unlock()

	nvdecParserHandleMap.Delete(handle)

	var closeErr error
	if session != nil {
		api, apiErr := session.API()
		if apiErr == nil {
			for _, frame := range mapped {
				devicePtr := frame.detach()
				if devicePtr != 0 && decoder != 0 {
					if status := cudaDriverCall(api.CuvidUnmapVideoFrame64, decoder, uintptr(devicePtr)); status != 0 && closeErr == nil {
						closeErr = fmt.Errorf("cuvidUnmapVideoFrame64 returned %d", status)
					}
				}
			}
			if parserHandle != 0 {
				if status := cudaDriverCall(api.CuvidDestroyVideoParser, parserHandle); status != 0 && closeErr == nil {
					closeErr = fmt.Errorf("cuvidDestroyVideoParser returned %d", status)
				}
			}
			if decoder != 0 {
				if status := cudaDriverCall(api.CuvidDestroyDecoder, decoder); status != 0 && closeErr == nil {
					closeErr = fmt.Errorf("cuvidDestroyDecoder returned %d", status)
				}
			}
		} else if parserHandle != 0 || decoder != 0 || len(mapped) > 0 {
			closeErr = apiErr
		}
		if err := session.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
	}
	for _, frame := range mapped {
		_ = frame.detach()
	}
	return closeErr
}
