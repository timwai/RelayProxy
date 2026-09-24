//go:build windows && amd64

package codec

import (
	"context"
	"encoding/binary"
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
	oneVPLIOPatternOutVideoMemory  = 0x10
	oneVPLIOPatternOutSystemMemory = 0x20
	oneVPLMapRead                  = 0x1

	oneVPLHandleD3D11Device            = 3
	oneVPLResourceDX11Texture          = 5
	oneVPLFrameInterfaceAddRef         = 16
	oneVPLFrameInterfaceGetNativeHandle = 56
	oneVPLFrameInterfaceGetDeviceHandle = 64
	oneVPLFrameInterfaceSync            = 72

	oneVPLBitstreamCodecID   = 20
	oneVPLBitstreamTimestamp = 32

	oneVPLWarnVideoParamChanged     = 3
	oneVPLErrMoreSurface            = -11
	oneVPLErrIncompatibleVideoParam = -14
	oneVPLErrReallocSurface         = -22

	oneVPLDecoderPendingLimit = 32 << 20
)

type oneVPLH265DecoderAPI struct {
	decodeHeader uintptr
	query        uintptr
	init         uintptr
	decode       uintptr
	closeDecoder uintptr
	setHandle    uintptr
}

func loadOneVPLH265DecoderAPI(module windows.Handle) (oneVPLH265DecoderAPI, error) {
	resolve := func(name string) (uintptr, error) {
		proc, err := windows.GetProcAddress(module, name)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", name, err)
		}
		return proc, nil
	}
	var api oneVPLH265DecoderAPI
	var err error
	if api.decodeHeader, err = resolve("MFXVideoDECODE_DecodeHeader"); err != nil {
		return oneVPLH265DecoderAPI{}, err
	}
	if api.query, err = resolve("MFXVideoDECODE_Query"); err != nil {
		return oneVPLH265DecoderAPI{}, err
	}
	if api.init, err = resolve("MFXVideoDECODE_Init"); err != nil {
		return oneVPLH265DecoderAPI{}, err
	}
	if api.decode, err = resolve("MFXVideoDECODE_DecodeFrameAsync"); err != nil {
		return oneVPLH265DecoderAPI{}, err
	}
	if api.closeDecoder, err = resolve("MFXVideoDECODE_Close"); err != nil {
		return oneVPLH265DecoderAPI{}, err
	}
	if api.setHandle, err = resolve("MFXVideoCORE_SetHandle"); err != nil {
		return oneVPLH265DecoderAPI{}, err
	}
	return api, nil
}

func createOneVPLH265DecoderSession(
	ctx context.Context,
	api *oneVPLAPI,
) (loader, session uintptr, err error) {
	loader, err = api.newLoader()
	if err != nil {
		return 0, 0, err
	}
	fail := func(cause error) (uintptr, uintptr, error) {
		api.unload(loader)
		return 0, 0, cause
	}
	filters := oneVPLDirectionFilters(oneVPLPropHEVCDecoder, oneVPLPropHEVCDecoderColor)
	filters = append(filters, oneVPLFilter{name: oneVPLPropAPIVersion, value: oneVPLAPIVersion22})
	for _, filter := range filters {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		config, configErr := api.createConfig(loader)
		if configErr != nil {
			return fail(configErr)
		}
		if configErr = api.setU32(config, filter.name, filter.value); configErr != nil {
			return fail(configErr)
		}
	}
	session, status := api.createSession(loader)
	if status != 0 || session == 0 {
		if session != 0 {
			api.closeSession(session)
		}
		return fail(fmt.Errorf("%w: oneVPL HEVC 4:4:4 decoder session status=%d", ErrDecoderUnavailable, status))
	}
	return loader, session, nil
}

func oneVPLHEVC444DecoderDesiredParam(cfg VideoConfig) (oneVPLVideoParam, VideoConfig, error) {
	return oneVPLHEVC444DecoderDesiredParamForIO(cfg, oneVPLIOPatternOutSystemMemory)
}

func oneVPLHEVC444DecoderDesiredParamForIO(
	cfg VideoConfig,
	ioPattern uint16,
) (oneVPLVideoParam, VideoConfig, error) {
	if ioPattern != oneVPLIOPatternOutSystemMemory && ioPattern != oneVPLIOPatternOutVideoMemory {
		return oneVPLVideoParam{}, VideoConfig{}, fmt.Errorf("%w: invalid oneVPL decoder IOPattern 0x%x", ErrInvalidVideoConfig, ioPattern)
	}
	cfg, err := NormalizeVideoConfig(cfg)
	if err != nil {
		return oneVPLVideoParam{}, VideoConfig{}, err
	}
	if cfg.Chroma != Chroma444 || cfg.BitDepth != 8 {
		return oneVPLVideoParam{}, VideoConfig{}, fmt.Errorf(
			"%w: oneVPL HEVC decoder requires 8-bit 4:4:4 video", ErrInvalidVideoConfig,
		)
	}
	alignedWidth := alignOneVPLDimension(cfg.Width)
	alignedHeight := alignOneVPLDimension(cfg.Height)
	if alignedWidth > 65535 || alignedHeight > 65535 {
		return oneVPLVideoParam{}, VideoConfig{}, fmt.Errorf("%w: oneVPL dimensions overflow", ErrInvalidVideoConfig)
	}

	var param oneVPLVideoParam
	param.putU16(oneVPLVideoParamAsyncDepth, 1)
	param.putU16(oneVPLFrameInfoBitDepthLuma, 8)
	param.putU16(oneVPLFrameInfoBitDepthChroma, 8)
	param.putU32(oneVPLFrameInfoFourCC, oneVPLFourCCAYUV)
	param.putU16(oneVPLFrameInfoWidth, uint16(alignedWidth))
	param.putU16(oneVPLFrameInfoHeight, uint16(alignedHeight))
	param.putU16(oneVPLFrameInfoCropW, uint16(cfg.Width))
	param.putU16(oneVPLFrameInfoCropH, uint16(cfg.Height))
	param.putU32(oneVPLFrameInfoFrameRateN, uint32(cfg.FPS))
	param.putU32(oneVPLFrameInfoFrameRateD, 1)
	param.putU16(oneVPLFrameInfoPicStruct, oneVPLPicStructProgressive)
	param.putU16(oneVPLFrameInfoChroma, oneVPLChromaYUV444)
	param.putU32(oneVPLVideoParamCodecID, oneVPLCodecHEVC)
	param.putU16(oneVPLVideoParamCodecProfile, oneVPLHEVCProfileRExt)
	param.putU16(oneVPLVideoParamIOPattern, ioPattern)
	return param, cfg, nil
}

func oneVPLHEVC444DecoderParamPreserved(param *oneVPLVideoParam) bool {
	return oneVPLHEVC444DecoderParamPreservedForIO(param, oneVPLIOPatternOutSystemMemory)
}

func oneVPLHEVC444DecoderParamPreservedForIO(param *oneVPLVideoParam, ioPattern uint16) bool {
	if param == nil {
		return false
	}
	return param.u32(oneVPLVideoParamCodecID) == oneVPLCodecHEVC &&
		param.u16(oneVPLVideoParamCodecProfile) == oneVPLHEVCProfileRExt &&
		param.u32(oneVPLFrameInfoFourCC) == oneVPLFourCCAYUV &&
		param.u16(oneVPLFrameInfoBitDepthLuma) == 8 &&
		param.u16(oneVPLFrameInfoBitDepthChroma) == 8 &&
		param.u16(oneVPLFrameInfoChroma) == oneVPLChromaYUV444 &&
		param.u16(oneVPLVideoParamIOPattern) == ioPattern
}

func applyOneVPLHEVC444DecoderOutput(header oneVPLVideoParam, cfg VideoConfig) (oneVPLVideoParam, error) {
	return applyOneVPLHEVC444DecoderOutputForIO(header, cfg, oneVPLIOPatternOutSystemMemory)
}

func applyOneVPLHEVC444DecoderOutputForIO(
	header oneVPLVideoParam,
	cfg VideoConfig,
	ioPattern uint16,
) (oneVPLVideoParam, error) {
	desired, normalized, err := oneVPLHEVC444DecoderDesiredParamForIO(cfg, ioPattern)
	if err != nil {
		return oneVPLVideoParam{}, err
	}
	if profile := header.u16(oneVPLVideoParamCodecProfile); profile != 0 && profile != oneVPLHEVCProfileRExt {
		return oneVPLVideoParam{}, fmt.Errorf("%w: HEVC profile=%d is not RExt", ErrDecoderUnavailable, profile)
	}
	if chroma := header.u16(oneVPLFrameInfoChroma); chroma != 0 && chroma != oneVPLChromaYUV444 {
		return oneVPLVideoParam{}, fmt.Errorf("%w: HEVC chroma=%d is not 4:4:4", ErrDecoderUnavailable, chroma)
	}
	if bitDepth := header.u16(oneVPLFrameInfoBitDepthLuma); bitDepth != 0 && bitDepth != 8 {
		return oneVPLVideoParam{}, fmt.Errorf("%w: HEVC luma bit depth=%d is not 8", ErrDecoderUnavailable, bitDepth)
	}
	if bitDepth := header.u16(oneVPLFrameInfoBitDepthChroma); bitDepth != 0 && bitDepth != 8 {
		return oneVPLVideoParam{}, fmt.Errorf("%w: HEVC chroma bit depth=%d is not 8", ErrDecoderUnavailable, bitDepth)
	}
	if width := header.u16(oneVPLFrameInfoCropW); width != 0 && int(width) != normalized.Width {
		return oneVPLVideoParam{}, fmt.Errorf("%w: HEVC width=%d want=%d", ErrDecoderUnavailable, width, normalized.Width)
	}
	if height := header.u16(oneVPLFrameInfoCropH); height != 0 && int(height) != normalized.Height {
		return oneVPLVideoParam{}, fmt.Errorf("%w: HEVC height=%d want=%d", ErrDecoderUnavailable, height, normalized.Height)
	}

	param := header
	param.putU16(oneVPLVideoParamAsyncDepth, desired.u16(oneVPLVideoParamAsyncDepth))
	param.putU16(oneVPLFrameInfoBitDepthLuma, 8)
	param.putU16(oneVPLFrameInfoBitDepthChroma, 8)
	param.putU32(oneVPLFrameInfoFourCC, oneVPLFourCCAYUV)
	param.putU16(oneVPLFrameInfoWidth, desired.u16(oneVPLFrameInfoWidth))
	param.putU16(oneVPLFrameInfoHeight, desired.u16(oneVPLFrameInfoHeight))
	param.putU16(oneVPLFrameInfoCropW, uint16(normalized.Width))
	param.putU16(oneVPLFrameInfoCropH, uint16(normalized.Height))
	if param.u32(oneVPLFrameInfoFrameRateN) == 0 || param.u32(oneVPLFrameInfoFrameRateD) == 0 {
		param.putU32(oneVPLFrameInfoFrameRateN, uint32(normalized.FPS))
		param.putU32(oneVPLFrameInfoFrameRateD, 1)
	}
	param.putU16(oneVPLFrameInfoPicStruct, oneVPLPicStructProgressive)
	param.putU16(oneVPLFrameInfoChroma, oneVPLChromaYUV444)
	param.putU32(oneVPLVideoParamCodecID, oneVPLCodecHEVC)
	param.putU16(oneVPLVideoParamCodecProfile, oneVPLHEVCProfileRExt)
	param.putU16(oneVPLVideoParamIOPattern, ioPattern)
	return param, nil
}

type oneVPLH265Decoder struct {
	mu sync.Mutex

	base    *oneVPLAPI
	api     oneVPLH265DecoderAPI
	loader  uintptr
	session uintptr
	cfg     VideoConfig
	param   oneVPLVideoParam

	initialized bool
	closed      bool
	pending     []byte
	inputBuffer []byte
	i444Scratch []byte
	ioPattern   uint16
	d3d11Device uintptr
}

func OpenOneVPLH265Decoder(ctx context.Context, cfg VideoConfig) (Decoder, error) {
	return openOneVPLH265Decoder(ctx, cfg, 0)
}

func OpenOneVPLH265DecoderWithD3D11(
	ctx context.Context,
	cfg VideoConfig,
	device uintptr,
) (Decoder, error) {
	if device == 0 {
		return nil, fmt.Errorf("%w: D3D11 device is nil", ErrDecoderUnavailable)
	}
	return openOneVPLH265Decoder(ctx, cfg, device)
}

func openOneVPLH265Decoder(
	ctx context.Context,
	cfg VideoConfig,
	device uintptr,
) (Decoder, error) {
	ioPattern := uint16(oneVPLIOPatternOutSystemMemory)
	if device != 0 {
		ioPattern = oneVPLIOPatternOutVideoMemory
	}
	_, cfg, err := oneVPLHEVC444DecoderDesiredParamForIO(cfg, ioPattern)
	if err != nil {
		return nil, err
	}
	base, err := loadOneVPLAPI()
	if err != nil {
		return nil, fmt.Errorf("%w: load oneVPL dispatcher: %v", ErrDecoderUnavailable, err)
	}
	cleanupBase := true
	defer func() {
		if cleanupBase {
			base.Close()
		}
	}()

	decoderAPI, err := loadOneVPLH265DecoderAPI(base.module)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve oneVPL decoder API: %v", ErrDecoderUnavailable, err)
	}
	loader, session, err := createOneVPLH265DecoderSession(ctx, base)
	if err != nil {
		return nil, err
	}
	cleanupSession := true
	defer func() {
		if cleanupSession {
			base.closeSession(session)
			base.unload(loader)
		}
	}()

	if device != 0 {
		status, _, _ := syscall.SyscallN(
			decoderAPI.setHandle,
			session,
			oneVPLHandleD3D11Device,
			device,
		)
		if got := oneVPLStatus(status); got != 0 {
			return nil, fmt.Errorf("%w: MFXVideoCORE_SetHandle(D3D11) returned %d", ErrDecoderUnavailable, got)
		}
	}

	decoder := &oneVPLH265Decoder{
		base:    base,
		api:     decoderAPI,
		loader:  loader,
		session:      session,
		cfg:          cfg,
		ioPattern:    ioPattern,
		d3d11Device:  device,
	}
	cleanupBase = false
	cleanupSession = false
	return decoder, nil
}

func (d *oneVPLH265Decoder) alignedInputLocked(src []byte) []byte {
	required := len(src) + 31
	if cap(d.inputBuffer) < required {
		d.inputBuffer = make([]byte, required)
	} else {
		d.inputBuffer = d.inputBuffer[:required]
	}
	if len(src) == 0 {
		return d.inputBuffer[:0]
	}
	base := uintptr(unsafe.Pointer(&d.inputBuffer[0]))
	offset := int((32 - base%32) % 32)
	aligned := d.inputBuffer[offset : offset+len(src)]
	copy(aligned, src)
	return aligned
}

func oneVPLHEVCInputBitstream(data []byte, timestamp time.Duration) oneVPLBitstream {
	var bitstream oneVPLBitstream
	binary.LittleEndian.PutUint32(
		bitstream[oneVPLBitstreamCodecID:oneVPLBitstreamCodecID+4],
		oneVPLCodecHEVC,
	)
	stamp := uint64(timestamp.Nanoseconds()) * 90_000 / uint64(time.Second)
	binary.LittleEndian.PutUint64(
		bitstream[oneVPLBitstreamTimestamp:oneVPLBitstreamTimestamp+8],
		stamp,
	)
	if len(data) > 0 {
		binary.LittleEndian.PutUint64(
			bitstream[oneVPLBitstreamData:oneVPLBitstreamData+8],
			uint64(uintptr(unsafe.Pointer(&data[0]))),
		)
		binary.LittleEndian.PutUint32(
			bitstream[oneVPLBitstreamDataLength:oneVPLBitstreamDataLength+4],
			uint32(len(data)),
		)
		binary.LittleEndian.PutUint32(
			bitstream[oneVPLBitstreamMaxLength:oneVPLBitstreamMaxLength+4],
			uint32(len(data)),
		)
	}
	return bitstream
}

func oneVPLBitstreamRemaining(bitstream *oneVPLBitstream, data []byte) ([]byte, error) {
	if bitstream == nil {
		return nil, nil
	}
	offset := int(binary.LittleEndian.Uint32(bitstream[oneVPLBitstreamDataOffset : oneVPLBitstreamDataOffset+4]))
	length := int(binary.LittleEndian.Uint32(bitstream[oneVPLBitstreamDataLength : oneVPLBitstreamDataLength+4]))
	if length == 0 {
		return nil, nil
	}
	if offset < 0 || length < 0 || offset+length > len(data) {
		return nil, fmt.Errorf("oneVPL decoder returned invalid bitstream range offset=%d length=%d buffer=%d", offset, length, len(data))
	}
	return data[offset : offset+length], nil
}

func (d *oneVPLH265Decoder) initializeLocked(ctx context.Context, bitstream *oneVPLBitstream) error {
	if d.initialized {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var header oneVPLVideoParam
	header.putU32(oneVPLVideoParamCodecID, oneVPLCodecHEVC)
	header.putU16(oneVPLVideoParamIOPattern, d.ioPattern)
	status, _, _ := syscall.SyscallN(
		d.api.decodeHeader,
		d.session,
		uintptr(unsafe.Pointer(&bitstream[0])),
		uintptr(unsafe.Pointer(&header[0])),
	)
	got := oneVPLStatus(status)
	if got == oneVPLErrMoreData {
		return nil
	}
	if got != 0 {
		return fmt.Errorf("%w: MFXVideoDECODE_DecodeHeader returned %d", ErrDecoderUnavailable, got)
	}

	param, err := applyOneVPLHEVC444DecoderOutputForIO(header, d.cfg, d.ioPattern)
	if err != nil {
		return err
	}
	queryParam := param
	status, _, _ = syscall.SyscallN(
		d.api.query,
		d.session,
		uintptr(unsafe.Pointer(&queryParam[0])),
		uintptr(unsafe.Pointer(&queryParam[0])),
	)
	queryStatus := oneVPLStatus(status)
	if !oneVPLStatusOK(queryStatus) || !oneVPLHEVC444DecoderParamPreservedForIO(&queryParam, d.ioPattern) {
		return fmt.Errorf(
			"%w: oneVPL HEVC 4:4:4 decoder query status=%d preserved=%t",
			ErrDecoderUnavailable, queryStatus, oneVPLHEVC444DecoderParamPreservedForIO(&queryParam, d.ioPattern),
		)
	}
	status, _, _ = syscall.SyscallN(
		d.api.init,
		d.session,
		uintptr(unsafe.Pointer(&queryParam[0])),
	)
	initStatus := oneVPLStatus(status)
	if !oneVPLStatusOK(initStatus) {
		return fmt.Errorf("%w: MFXVideoDECODE_Init returned %d", ErrDecoderUnavailable, initStatus)
	}
	d.param = queryParam
	d.initialized = true
	return nil
}

func synchronizeOneVPLSurface(ctx context.Context, surface uintptr) error {
	method, err := oneVPLSurfaceInterfaceMethod(surface, oneVPLFrameInterfaceSync)
	if err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		status, _, _ := syscall.SyscallN(method, surface, 50)
		got := oneVPLStatus(status)
		if got == oneVPLWarnInExecution {
			continue
		}
		if got != 0 {
			return fmt.Errorf("oneVPL surface Synchronize returned %d", got)
		}
		return nil
	}
}

func mapOneVPLSurfaceRead(surface uintptr) error {
	method, err := oneVPLSurfaceInterfaceMethod(surface, oneVPLFrameInterfaceMap)
	if err != nil {
		return err
	}
	status, _, _ := syscall.SyscallN(method, surface, oneVPLMapRead)
	if got := oneVPLStatus(status); got != 0 {
		return fmt.Errorf("oneVPL surface Map(read) returned %d", got)
	}
	return nil
}

func readOneVPLAYUVSurface(
	surface uintptr,
	width int,
	height int,
	dst []byte,
) ([]byte, error) {
	if got := *(*uint32)(unsafe.Pointer(surface + oneVPLSurfaceInfoFourCC)); got != oneVPLFourCCAYUV {
		return nil, fmt.Errorf("oneVPL decode surface FourCC=0x%08x, want AYUV", got)
	}
	pitch := int(*(*uint16)(unsafe.Pointer(surface + oneVPLSurfacePitchLow))) |
		int(*(*uint16)(unsafe.Pointer(surface + oneVPLSurfacePitchHigh)))<<16
	if pitch < width*4 {
		return nil, fmt.Errorf("oneVPL AYUV pitch=%d is smaller than visible row=%d", pitch, width*4)
	}
	yPtr := *(*uintptr)(unsafe.Pointer(surface + oneVPLSurfaceY))
	uPtr := *(*uintptr)(unsafe.Pointer(surface + oneVPLSurfaceU))
	vPtr := *(*uintptr)(unsafe.Pointer(surface + oneVPLSurfaceV))
	if yPtr == 0 || uPtr == 0 || vPtr == 0 {
		return nil, errors.New("oneVPL AYUV decode surface has incomplete channel pointers")
	}

	planeBytes := width * height
	required := planeBytes * 3
	if cap(dst) < required {
		dst = make([]byte, required)
	} else {
		dst = dst[:required]
	}
	yPlane := dst[:planeBytes]
	uPlane := dst[planeBytes : planeBytes*2]
	vPlane := dst[planeBytes*2:]
	for row := 0; row < height; row++ {
		yRow := unsafe.Slice((*byte)(unsafe.Pointer(yPtr+uintptr(row*pitch))), pitch)
		uRow := unsafe.Slice((*byte)(unsafe.Pointer(uPtr+uintptr(row*pitch))), pitch)
		vRow := unsafe.Slice((*byte)(unsafe.Pointer(vPtr+uintptr(row*pitch))), pitch)
		dstRow := row * width
		for x := 0; x < width; x++ {
			offset := x * 4
			index := dstRow + x
			yPlane[index] = yRow[offset]
			uPlane[index] = uRow[offset]
			vPlane[index] = vRow[offset]
		}
	}
	return dst, nil
}

func (d *oneVPLH265Decoder) outputSurfaceLocked(
	ctx context.Context,
	surface uintptr,
	timestamp time.Duration,
) ([]DecodedFrame, error) {
	if surface == 0 {
		return nil, errors.New("oneVPL decoder returned a nil output surface")
	}
	defer releaseOneVPLSurface(surface)
	if err := synchronizeOneVPLSurface(ctx, surface); err != nil {
		return nil, err
	}
	if err := mapOneVPLSurfaceRead(surface); err != nil {
		return nil, err
	}
	i444, err := readOneVPLAYUVSurface(surface, d.cfg.Width, d.cfg.Height, d.i444Scratch)
	unmapErr := unmapOneVPLSurface(surface)
	if err != nil {
		return nil, err
	}
	if unmapErr != nil {
		return nil, unmapErr
	}
	d.i444Scratch = i444
	frameData := append([]byte(nil), i444...)
	return []DecodedFrame{{
		Format:    PixelFormatI444,
		Pix:       frameData,
		Width:     d.cfg.Width,
		Height:    d.cfg.Height,
		Stride:    d.cfg.Width,
		Timestamp: timestamp,
		Hardware:  true,
	}}, nil
}

func (d *oneVPLH265Decoder) decodeBitstreamLocked(
	ctx context.Context,
	bitstream *oneVPLBitstream,
	input []byte,
	timestamp time.Duration,
) ([]DecodedFrame, error) {
	for attempt := 0; attempt < 4; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var surface uintptr
		var syncPoint uintptr
		status, _, _ := syscall.SyscallN(
			d.api.decode,
			d.session,
			uintptr(unsafe.Pointer(&bitstream[0])),
			0,
			uintptr(unsafe.Pointer(&surface)),
			uintptr(unsafe.Pointer(&syncPoint)),
		)
		runtime.KeepAlive(input)
		got := oneVPLStatus(status)
		switch got {
		case 0:
			return d.outputSurfaceLocked(ctx, surface, timestamp)
		case oneVPLWarnVideoParamChanged:
			if surface != 0 {
				return d.outputSurfaceLocked(ctx, surface, timestamp)
			}
			return nil, fmt.Errorf("%w: oneVPL decoder changed video parameters inside one generation", ErrDecoderUnavailable)
		case oneVPLErrMoreData:
			return nil, nil
		case oneVPLWarnDeviceBusy:
			timer := time.NewTimer(2 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
			continue
		case oneVPLErrMoreSurface:
			return nil, fmt.Errorf("%w: oneVPL decoder requested an additional output surface", ErrDecoderUnavailable)
		case oneVPLErrIncompatibleVideoParam, oneVPLErrReallocSurface:
			return nil, fmt.Errorf("%w: oneVPL decoder requires generation rebuild status=%d", ErrDecoderUnavailable, got)
		default:
			return nil, fmt.Errorf("MFXVideoDECODE_DecodeFrameAsync returned %d", got)
		}
	}
	return nil, errors.New("oneVPL HEVC decoder remained busy")
}

func (d *oneVPLH265Decoder) Decode(
	ctx context.Context,
	data []byte,
	timestamp time.Duration,
) ([]DecodedFrame, error) {
	if d == nil {
		return nil, ErrDecoderUnavailable
	}
	if len(data) == 0 {
		return nil, nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || d.session == 0 {
		return nil, ErrDecoderUnavailable
	}

	if len(d.pending)+len(data) > oneVPLDecoderPendingLimit {
		d.pending = nil
		return nil, fmt.Errorf("%w: oneVPL decoder pending input exceeds %d bytes", ErrDecoderUnavailable, oneVPLDecoderPendingLimit)
	}
	if len(d.pending) > 0 {
		d.pending = append(d.pending, data...)
		data = d.pending
	}
	input := d.alignedInputLocked(data)
	bitstream := oneVPLHEVCInputBitstream(input, timestamp)

	if !d.initialized {
		if err := d.initializeLocked(ctx, &bitstream); err != nil {
			return nil, err
		}
		if !d.initialized {
			d.pending = append(d.pending[:0], data...)
			return nil, nil
		}
	}

	frames, err := d.decodeBitstreamLocked(ctx, &bitstream, input, timestamp)
	if err != nil {
		d.pending = nil
		return nil, err
	}
	remaining, remainingErr := oneVPLBitstreamRemaining(&bitstream, input)
	if remainingErr != nil {
		d.pending = nil
		return nil, remainingErr
	}
	if len(remaining) > 0 {
		d.pending = append(d.pending[:0], remaining...)
	} else {
		d.pending = d.pending[:0]
	}
	return frames, nil
}

func (d *oneVPLH265Decoder) closeComponentLocked() error {
	if !d.initialized || d.session == 0 || d.api.closeDecoder == 0 {
		d.initialized = false
		d.param = oneVPLVideoParam{}
		return nil
	}
	status, _, _ := syscall.SyscallN(d.api.closeDecoder, d.session)
	d.initialized = false
	d.param = oneVPLVideoParam{}
	if got := oneVPLStatus(status); got < 0 {
		return fmt.Errorf("MFXVideoDECODE_Close returned %d", got)
	}
	return nil
}

func (d *oneVPLH265Decoder) Flush(ctx context.Context) error {
	if d == nil {
		return ErrDecoderUnavailable
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || d.session == 0 {
		return ErrDecoderUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	d.pending = d.pending[:0]
	return d.closeComponentLocked()
}

func (d *oneVPLH265Decoder) Hardware() bool {
	return d != nil
}

func (d *oneVPLH265Decoder) Backend() string {
	if d == nil {
		return ""
	}
	return "onevpl-hevc444"
}

func (d *oneVPLH265Decoder) Close() error {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	d.closed = true
	closeErr := d.closeComponentLocked()
	base := d.base
	loader := d.loader
	session := d.session
	d.base = nil
	d.loader = 0
	d.session = 0
	d.pending = nil
	d.inputBuffer = nil
	d.i444Scratch = nil
	d.mu.Unlock()

	if base != nil {
		if session != 0 {
			base.closeSession(session)
		}
		if loader != 0 {
			base.unload(loader)
		}
		base.Close()
	}
	return closeErr
}
