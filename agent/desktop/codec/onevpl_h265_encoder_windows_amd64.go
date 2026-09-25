//go:build windows && amd64

package codec

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	oneVPLVideoParamSize = 208
	oneVPLBitstreamSize  = 72

	oneVPLVideoParamAsyncDepth    = 14
	oneVPLVideoParamBRCMultiplier = 46
	oneVPLVideoParamFrameInfo     = 48
	oneVPLVideoParamCodecID       = 116
	oneVPLVideoParamCodecProfile  = 120
	oneVPLVideoParamTargetUsage   = 126
	oneVPLVideoParamGopPicSize    = 128
	oneVPLVideoParamGopRefDist    = 130
	oneVPLVideoParamIDRInterval   = 134
	oneVPLVideoParamRateControl   = 136
	oneVPLVideoParamTargetKbps    = 142
	oneVPLVideoParamMaxKbps       = 144
	oneVPLVideoParamIOPattern     = 186

	oneVPLFrameInfoBitDepthLuma   = oneVPLVideoParamFrameInfo + 18
	oneVPLFrameInfoBitDepthChroma = oneVPLVideoParamFrameInfo + 20
	oneVPLFrameInfoFourCC         = oneVPLVideoParamFrameInfo + 32
	oneVPLFrameInfoWidth          = oneVPLVideoParamFrameInfo + 36
	oneVPLFrameInfoHeight         = oneVPLVideoParamFrameInfo + 38
	oneVPLFrameInfoCropW          = oneVPLVideoParamFrameInfo + 44
	oneVPLFrameInfoCropH          = oneVPLVideoParamFrameInfo + 46
	oneVPLFrameInfoFrameRateN     = oneVPLVideoParamFrameInfo + 48
	oneVPLFrameInfoFrameRateD     = oneVPLVideoParamFrameInfo + 52
	oneVPLFrameInfoPicStruct      = oneVPLVideoParamFrameInfo + 62
	oneVPLFrameInfoChroma         = oneVPLVideoParamFrameInfo + 64

	oneVPLSurfaceFrameInterface = 0
	oneVPLSurfaceInfo           = 16
	oneVPLSurfaceData           = 88
	oneVPLSurfaceInfoFourCC     = oneVPLSurfaceInfo + 32
	oneVPLSurfacePitchHigh      = oneVPLSurfaceData + 30
	oneVPLSurfaceTimestamp      = oneVPLSurfaceData + 32
	oneVPLSurfacePitchLow       = oneVPLSurfaceData + 46
	oneVPLSurfaceY              = oneVPLSurfaceData + 48
	oneVPLSurfaceU              = oneVPLSurfaceData + 56
	oneVPLSurfaceV              = oneVPLSurfaceData + 64
	oneVPLSurfaceA              = oneVPLSurfaceData + 72

	oneVPLFrameInterfaceRelease = 24
	oneVPLFrameInterfaceMap     = 40
	oneVPLFrameInterfaceUnmap   = 48

	oneVPLBitstreamData       = 40
	oneVPLBitstreamDataOffset = 48
	oneVPLBitstreamDataLength = 52
	oneVPLBitstreamMaxLength  = 56
	oneVPLBitstreamFrameType  = 62

	oneVPLMapWrite                = 0x2
	oneVPLIOPatternInVideoMemory  = 0x01
	oneVPLIOPatternInSystemMemory = 0x02
	oneVPLChromaYUV444            = 3
	oneVPLPicStructProgressive    = 1
	oneVPLHEVCProfileRExt         = 4
	oneVPLTargetUsageBalanced     = 4
	oneVPLRateControlVBR          = 2

	oneVPLFrameTypeI   = 0x0001
	oneVPLFrameTypeRef = 0x0040
	oneVPLFrameTypeIDR = 0x0080

	oneVPLWarnInExecution            = 1
	oneVPLWarnDeviceBusy             = 2
	oneVPLWarnIncompatibleVideoParam = 5
	oneVPLErrNotEnoughBuffer         = -5
	oneVPLErrMoreData                = -10

	oneVPLAPIVersion22   = uint32(2<<16 | 2)
	oneVPLPropAPIVersion = "mfxImplDescription.ApiVersion.Version"
)

type oneVPLVideoParam [oneVPLVideoParamSize]byte
type oneVPLBitstream [oneVPLBitstreamSize]byte

func (p *oneVPLVideoParam) putU16(offset int, value uint16) {
	binary.LittleEndian.PutUint16(p[offset:offset+2], value)
}

func (p *oneVPLVideoParam) putU32(offset int, value uint32) {
	binary.LittleEndian.PutUint32(p[offset:offset+4], value)
}

func (p *oneVPLVideoParam) u16(offset int) uint16 {
	return binary.LittleEndian.Uint16(p[offset : offset+2])
}

func (p *oneVPLVideoParam) u32(offset int) uint32 {
	return binary.LittleEndian.Uint32(p[offset : offset+4])
}

func alignOneVPLDimension(value int) int {
	return (value + 15) &^ 15
}

func oneVPLBitrateFields(targetBitrate int) (multiplier, targetKbps uint16) {
	kbps := (targetBitrate + 999) / 1000
	if kbps < 1 {
		kbps = 1
	}
	mult := (kbps + 65534) / 65535
	if mult < 1 {
		mult = 1
	}
	if mult > 65535 {
		mult = 65535
	}
	value := (kbps + mult - 1) / mult
	if value > 65535 {
		value = 65535
	}
	return uint16(mult), uint16(value)
}

func oneVPLHEVC444VideoParam(cfg VideoConfig) (oneVPLVideoParam, VideoConfig, error) {
	return oneVPLHEVC444VideoParamForIO(cfg, oneVPLIOPatternInSystemMemory)
}

func oneVPLHEVC444VideoParamForIO(
	cfg VideoConfig,
	ioPattern uint16,
) (oneVPLVideoParam, VideoConfig, error) {
	if ioPattern != oneVPLIOPatternInSystemMemory && ioPattern != oneVPLIOPatternInVideoMemory {
		return oneVPLVideoParam{}, VideoConfig{}, fmt.Errorf("%w: invalid oneVPL encoder IOPattern 0x%x", ErrInvalidVideoConfig, ioPattern)
	}
	cfg, err := NormalizeVideoConfig(cfg)
	if err != nil {
		return oneVPLVideoParam{}, VideoConfig{}, err
	}
	if cfg.Chroma != Chroma444 || cfg.BitDepth != 8 {
		return oneVPLVideoParam{}, VideoConfig{}, fmt.Errorf(
			"%w: oneVPL HEVC encoder requires 8-bit 4:4:4 video", ErrInvalidVideoConfig,
		)
	}
	alignedWidth := alignOneVPLDimension(cfg.Width)
	alignedHeight := alignOneVPLDimension(cfg.Height)
	if alignedWidth > 65535 || alignedHeight > 65535 {
		return oneVPLVideoParam{}, VideoConfig{}, fmt.Errorf("%w: oneVPL dimensions overflow", ErrInvalidVideoConfig)
	}

	multiplier, targetKbps := oneVPLBitrateFields(cfg.TargetBitrate)
	var param oneVPLVideoParam
	param.putU16(oneVPLVideoParamAsyncDepth, 1)
	param.putU16(oneVPLVideoParamBRCMultiplier, multiplier)
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
	param.putU16(oneVPLVideoParamTargetUsage, oneVPLTargetUsageBalanced)
	gop := cfg.FPS * 2
	if gop < 1 {
		gop = 1
	}
	if gop > 65535 {
		gop = 65535
	}
	param.putU16(oneVPLVideoParamGopPicSize, uint16(gop))
	param.putU16(oneVPLVideoParamGopRefDist, 1)
	param.putU16(oneVPLVideoParamIDRInterval, 0)
	param.putU16(oneVPLVideoParamRateControl, oneVPLRateControlVBR)
	param.putU16(oneVPLVideoParamTargetKbps, targetKbps)
	param.putU16(oneVPLVideoParamMaxKbps, targetKbps)
	param.putU16(oneVPLVideoParamIOPattern, ioPattern)
	return param, cfg, nil
}

func oneVPLHEVC444ParamPreserved(param *oneVPLVideoParam) bool {
	return oneVPLHEVC444ParamPreservedForIO(param, oneVPLIOPatternInSystemMemory)
}

func oneVPLHEVC444ParamPreservedForIO(param *oneVPLVideoParam, ioPattern uint16) bool {
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

func oneVPLStatusOK(status int32) bool {
	return status == 0 || status == oneVPLWarnIncompatibleVideoParam
}

type oneVPLH265API struct {
	query        uintptr
	init         uintptr
	reset        uintptr
	encode       uintptr
	closeEncoder uintptr
	getSurface   uintptr
	sync         uintptr
	setHandle    uintptr
}

func loadOneVPLH265API(module windows.Handle) (oneVPLH265API, error) {
	resolve := func(name string) (uintptr, error) {
		proc, err := windows.GetProcAddress(module, name)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", name, err)
		}
		return proc, nil
	}
	var api oneVPLH265API
	var err error
	if api.query, err = resolve("MFXVideoENCODE_Query"); err != nil {
		return oneVPLH265API{}, err
	}
	if api.init, err = resolve("MFXVideoENCODE_Init"); err != nil {
		return oneVPLH265API{}, err
	}
	if api.reset, err = resolve("MFXVideoENCODE_Reset"); err != nil {
		return oneVPLH265API{}, err
	}
	if api.encode, err = resolve("MFXVideoENCODE_EncodeFrameAsync"); err != nil {
		return oneVPLH265API{}, err
	}
	if api.closeEncoder, err = resolve("MFXVideoENCODE_Close"); err != nil {
		return oneVPLH265API{}, err
	}
	if api.getSurface, err = resolve("MFXMemory_GetSurfaceForEncode"); err != nil {
		return oneVPLH265API{}, err
	}
	if api.sync, err = resolve("MFXVideoCORE_SyncOperation"); err != nil {
		return oneVPLH265API{}, err
	}
	if api.setHandle, err = resolve("MFXVideoCORE_SetHandle"); err != nil {
		return oneVPLH265API{}, err
	}
	return api, nil
}

func createOneVPLH265Session(
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
	filters := oneVPLDirectionFilters(oneVPLPropHEVCEncoder, oneVPLPropHEVCEncoderColor)
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
		return fail(fmt.Errorf("%w: oneVPL HEVC 4:4:4 session status=%d", ErrEncoderUnavailable, status))
	}
	return loader, session, nil
}

type oneVPLH265Encoder struct {
	mu sync.Mutex

	base    *oneVPLAPI
	api     oneVPLH265API
	loader  uintptr
	session uintptr
	cfg     VideoConfig
	param   oneVPLVideoParam

	bitstream     oneVPLBitstream
	bitstreamData []byte
	i444Scratch   []byte
	forceIDR      bool
	sequence      []byte
	stats         EncoderStats
	closed        bool
	ioPattern     uint16
	d3d11Device   uintptr
	d3d11Context  unsafe.Pointer
}

func OpenOneVPLH265Encoder(ctx context.Context, cfg VideoConfig) (SequenceHeaderEncoder, error) {
	return openOneVPLH265Encoder(ctx, cfg, 0)
}

func OpenOneVPLH265EncoderWithD3D11(
	ctx context.Context,
	cfg VideoConfig,
	device uintptr,
) (SequenceHeaderEncoder, error) {
	if device == 0 {
		return nil, fmt.Errorf("%w: D3D11 device is nil", ErrEncoderUnavailable)
	}
	return openOneVPLH265Encoder(ctx, cfg, device)
}

func openOneVPLH265Encoder(
	ctx context.Context,
	cfg VideoConfig,
	device uintptr,
) (SequenceHeaderEncoder, error) {
	ioPattern := uint16(oneVPLIOPatternInSystemMemory)
	if device != 0 {
		ioPattern = oneVPLIOPatternInVideoMemory
	}
	param, cfg, err := oneVPLHEVC444VideoParamForIO(cfg, ioPattern)
	if err != nil {
		return nil, err
	}
	base, err := loadOneVPLAPI()
	if err != nil {
		return nil, fmt.Errorf("%w: load oneVPL dispatcher: %v", ErrEncoderUnavailable, err)
	}
	cleanupBase := true
	defer func() {
		if cleanupBase {
			base.Close()
		}
	}()

	encAPI, err := loadOneVPLH265API(base.module)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve oneVPL encoder API: %v", ErrEncoderUnavailable, err)
	}
	loader, session, err := createOneVPLH265Session(ctx, base)
	if err != nil {
		return nil, err
	}
	var d3d11Context unsafe.Pointer
	cleanupSession := true
	defer func() {
		if cleanupSession {
			base.closeSession(session)
			base.unload(loader)
			releaseIUnknown(d3d11Context)
		}
	}()

	if device != 0 {
		status, _, _ := syscall.SyscallN(
			encAPI.setHandle,
			session,
			oneVPLHandleD3D11Device,
			device,
		)
		if got := oneVPLStatus(status); got != 0 {
			return nil, fmt.Errorf("%w: MFXVideoCORE_SetHandle(D3D11) returned %d", ErrEncoderUnavailable, got)
		}
		comCall(
			unsafe.Pointer(device),
			40, // ID3D11Device::GetImmediateContext
			uintptr(unsafe.Pointer(&d3d11Context)),
		)
		if d3d11Context == nil {
			return nil, fmt.Errorf("%w: D3D11 device returned nil immediate context", ErrEncoderUnavailable)
		}
	}

	queryParam := param
	status, _, _ := syscall.SyscallN(
		encAPI.query,
		session,
		uintptr(unsafe.Pointer(&queryParam[0])),
		uintptr(unsafe.Pointer(&queryParam[0])),
	)
	queryStatus := oneVPLStatus(status)
	if !oneVPLStatusOK(queryStatus) || !oneVPLHEVC444ParamPreservedForIO(&queryParam, ioPattern) {
		return nil, fmt.Errorf(
			"%w: oneVPL HEVC 4:4:4 query status=%d preserved=%t",
			ErrEncoderUnavailable, queryStatus, oneVPLHEVC444ParamPreservedForIO(&queryParam, ioPattern),
		)
	}

	status, _, _ = syscall.SyscallN(
		encAPI.init,
		session,
		uintptr(unsafe.Pointer(&queryParam[0])),
	)
	initStatus := oneVPLStatus(status)
	if initStatus < 0 {
		return nil, fmt.Errorf("%w: MFXVideoENCODE_Init returned %d", ErrEncoderUnavailable, initStatus)
	}

	bufferSize := cfg.Width*cfg.Height*4 + 1<<20
	if bufferSize < 4<<20 {
		bufferSize = 4 << 20
	}
	backend := "onevpl-hevc444"
	if device != 0 {
		backend = "onevpl-hevc444-d3d11-zero-copy"
	}
	encoder := &oneVPLH265Encoder{
		base:          base,
		api:           encAPI,
		loader:        loader,
		session:       session,
		cfg:           cfg,
		param:         queryParam,
		bitstreamData: make([]byte, bufferSize),
		forceIDR:      true,
		ioPattern:     ioPattern,
		d3d11Device:   device,
		d3d11Context:  d3d11Context,
		stats: EncoderStats{
			Hardware: true,
			Backend:  backend,
		},
	}
	encoder.resetBitstreamLocked()
	cleanupBase = false
	cleanupSession = false
	return encoder, nil
}

func (e *oneVPLH265Encoder) resetBitstreamLocked() {
	for i := range e.bitstream {
		e.bitstream[i] = 0
	}
	if len(e.bitstreamData) == 0 {
		return
	}
	binary.LittleEndian.PutUint64(
		e.bitstream[oneVPLBitstreamData:oneVPLBitstreamData+8],
		uint64(uintptr(unsafe.Pointer(&e.bitstreamData[0]))),
	)
	binary.LittleEndian.PutUint32(
		e.bitstream[oneVPLBitstreamMaxLength:oneVPLBitstreamMaxLength+4],
		uint32(len(e.bitstreamData)),
	)
}

func (e *oneVPLH265Encoder) rawFrameI444(frame RawFrame) ([]byte, int, error) {
	if err := frame.Validate(); err != nil {
		return nil, 0, err
	}
	if frame.Width != e.cfg.Width || frame.Height != e.cfg.Height {
		return nil, 0, ErrInvalidFrame
	}
	switch frame.Format {
	case PixelFormatI444:
		return frame.Pix, frame.Stride, nil
	case PixelFormatRGBA:
		src := &image.RGBA{
			Pix:    frame.Pix,
			Stride: frame.Stride,
			Rect:   image.Rect(0, 0, frame.Width, frame.Height),
		}
		converted, err := RGBAtoI444(src, e.i444Scratch)
		if err != nil {
			return nil, 0, err
		}
		e.i444Scratch = converted
		return e.i444Scratch, frame.Width, nil
	case PixelFormatBGRA:
		converted, err := BGRAtoI444(frame.Pix, frame.Width, frame.Height, frame.Stride, e.i444Scratch)
		if err != nil {
			return nil, 0, err
		}
		e.i444Scratch = converted
		return e.i444Scratch, frame.Width, nil
	default:
		return nil, 0, fmt.Errorf("%w: oneVPL HEVC 4:4:4 cannot use %s input", ErrInvalidFrame, frame.Format)
	}
}

func oneVPLSurfaceInterfaceMethod(surface uintptr, offset uintptr) (uintptr, error) {
	if surface == 0 {
		return 0, errors.New("oneVPL surface is nil")
	}
	iface := *(*uintptr)(unsafe.Pointer(surface + oneVPLSurfaceFrameInterface))
	if iface == 0 {
		return 0, errors.New("oneVPL surface interface is nil")
	}
	method := *(*uintptr)(unsafe.Pointer(iface + offset))
	if method == 0 {
		return 0, errors.New("oneVPL surface interface method is nil")
	}
	return method, nil
}

func releaseOneVPLSurface(surface uintptr) {
	method, err := oneVPLSurfaceInterfaceMethod(surface, oneVPLFrameInterfaceRelease)
	if err == nil {
		syscall.SyscallN(method, surface)
	}
}

func mapOneVPLSurface(surface uintptr) error {
	method, err := oneVPLSurfaceInterfaceMethod(surface, oneVPLFrameInterfaceMap)
	if err != nil {
		return err
	}
	status, _, _ := syscall.SyscallN(method, surface, oneVPLMapWrite)
	if got := oneVPLStatus(status); got != 0 {
		return fmt.Errorf("oneVPL surface Map returned %d", got)
	}
	return nil
}

func unmapOneVPLSurface(surface uintptr) error {
	method, err := oneVPLSurfaceInterfaceMethod(surface, oneVPLFrameInterfaceUnmap)
	if err != nil {
		return err
	}
	status, _, _ := syscall.SyscallN(method, surface)
	if got := oneVPLStatus(status); got != 0 {
		return fmt.Errorf("oneVPL surface Unmap returned %d", got)
	}
	return nil
}

func fillOneVPLAYUVSurface(
	surface uintptr,
	i444 []byte,
	i444Stride int,
	width int,
	height int,
	timestamp time.Duration,
) error {
	if got := *(*uint32)(unsafe.Pointer(surface + oneVPLSurfaceInfoFourCC)); got != oneVPLFourCCAYUV {
		return fmt.Errorf("oneVPL encode surface FourCC=0x%08x, want AYUV", got)
	}
	pitch := int(*(*uint16)(unsafe.Pointer(surface + oneVPLSurfacePitchLow))) |
		int(*(*uint16)(unsafe.Pointer(surface + oneVPLSurfacePitchHigh)))<<16
	if pitch < width*4 {
		return fmt.Errorf("oneVPL AYUV pitch=%d is smaller than visible row=%d", pitch, width*4)
	}
	yPtr := *(*uintptr)(unsafe.Pointer(surface + oneVPLSurfaceY))
	uPtr := *(*uintptr)(unsafe.Pointer(surface + oneVPLSurfaceU))
	vPtr := *(*uintptr)(unsafe.Pointer(surface + oneVPLSurfaceV))
	aPtr := *(*uintptr)(unsafe.Pointer(surface + oneVPLSurfaceA))
	if yPtr == 0 || uPtr == 0 || vPtr == 0 || aPtr == 0 {
		return errors.New("oneVPL AYUV surface has incomplete channel pointers")
	}

	planeBytes := i444Stride * height
	if i444Stride < width || len(i444) < planeBytes*3 {
		return ErrInvalidFrame
	}
	yPlane := i444[:planeBytes]
	uPlane := i444[planeBytes : planeBytes*2]
	vPlane := i444[planeBytes*2 : planeBytes*3]
	for row := 0; row < height; row++ {
		yRow := unsafe.Slice((*byte)(unsafe.Pointer(yPtr+uintptr(row*pitch))), pitch)
		uRow := unsafe.Slice((*byte)(unsafe.Pointer(uPtr+uintptr(row*pitch))), pitch)
		vRow := unsafe.Slice((*byte)(unsafe.Pointer(vPtr+uintptr(row*pitch))), pitch)
		aRow := unsafe.Slice((*byte)(unsafe.Pointer(aPtr+uintptr(row*pitch))), pitch)
		src := row * i444Stride
		for x := 0; x < width; x++ {
			offset := x * 4
			yRow[offset] = yPlane[src+x]
			uRow[offset] = uPlane[src+x]
			vRow[offset] = vPlane[src+x]
			aRow[offset] = 0xff
		}
	}
	stamp := uint64(timestamp.Nanoseconds()) * 90_000 / uint64(time.Second)
	*(*uint64)(unsafe.Pointer(surface + oneVPLSurfaceTimestamp)) = stamp
	return nil
}

func copyOneVPLAYUVD3D11Surface(
	context unsafe.Pointer,
	surface uintptr,
	frame D3D11EncodeFrame,
) error {
	if context == nil {
		return fmt.Errorf("%w: oneVPL D3D11 context is nil", ErrEncoderUnavailable)
	}
	if err := frame.Validate(); err != nil {
		return err
	}
	if frame.PixelFormat() != PixelFormatAYUV {
		return fmt.Errorf("%w: oneVPL D3D11 HEVC 4:4:4 requires AYUV input", ErrInvalidFrame)
	}
	targetResource, targetDevice, err := oneVPLD3D11SurfaceHandles(surface)
	if err != nil {
		return err
	}
	if frame.Device != 0 && targetDevice != frame.Device {
		return fmt.Errorf(
			"%w: source D3D11 device %#x does not match oneVPL device %#x",
			ErrInvalidFrame, frame.Device, targetDevice,
		)
	}

	source := unsafe.Pointer(frame.Resource)
	target := unsafe.Pointer(targetResource)
	var sourceDesc, targetDesc mfD3D11Texture2DDesc
	comCall(source, 10, uintptr(unsafe.Pointer(&sourceDesc))) // ID3D11Texture2D::GetDesc
	comCall(target, 10, uintptr(unsafe.Pointer(&targetDesc)))
	if sourceDesc.MipLevels == 0 || targetDesc.MipLevels == 0 {
		return fmt.Errorf("%w: invalid D3D11 texture metadata", ErrInvalidFrame)
	}
	if sourceDesc.Format != dxgiFormatAYUV || targetDesc.Format != dxgiFormatAYUV {
		return fmt.Errorf(
			"%w: AYUV copy requires DXGI format %d, source=%d target=%d",
			ErrInvalidFrame, dxgiFormatAYUV, sourceDesc.Format, targetDesc.Format,
		)
	}
	if int(sourceDesc.Width) < frame.Width || int(sourceDesc.Height) < frame.Height {
		return fmt.Errorf(
			"%w: AYUV source texture=%dx%d smaller than frame=%dx%d",
			ErrInvalidFrame, sourceDesc.Width, sourceDesc.Height, frame.Width, frame.Height,
		)
	}
	if int(targetDesc.Width) < frame.Width || int(targetDesc.Height) < frame.Height {
		return fmt.Errorf(
			"%w: oneVPL AYUV target=%dx%d smaller than frame=%dx%d",
			ErrInvalidFrame, targetDesc.Width, targetDesc.Height, frame.Width, frame.Height,
		)
	}
	sourceSubresources := uint64(sourceDesc.MipLevels) * uint64(sourceDesc.ArraySize)
	if uint64(frame.Subresource) >= sourceSubresources {
		return fmt.Errorf(
			"%w: D3D11 source subresource=%d exceeds %d",
			ErrInvalidFrame, frame.Subresource, sourceSubresources,
		)
	}

	comCall(
		context,
		46, // ID3D11DeviceContext::CopySubresourceRegion
		uintptr(target),
		0,
		0,
		0,
		0,
		uintptr(source),
		uintptr(frame.Subresource),
		0,
	)
	stamp := uint64(frame.Timestamp.Nanoseconds()) * 90_000 / uint64(time.Second)
	*(*uint64)(unsafe.Pointer(surface + oneVPLSurfaceTimestamp)) = stamp
	return nil
}

func oneVPLH265IsKeyFrame(data []byte, frameType uint16) bool {
	if frameType&(oneVPLFrameTypeI|oneVPLFrameTypeIDR) != 0 {
		return true
	}
	for _, nal := range annexBNALUnits(data) {
		typeID := H265NALUnitType(nal)
		if typeID >= 16 && typeID <= 21 {
			return true
		}
	}
	return false
}

func oneVPLH265ParameterSets(data []byte) []byte {
	var out []byte
	for _, nal := range annexBNALUnits(data) {
		switch H265NALUnitType(nal) {
		case 32, 33, 34:
			out = append(out, 0, 0, 0, 1)
			out = append(out, nal...)
		}
	}
	if H265HasParameterSets(out) {
		return out
	}
	return nil
}

func (e *oneVPLH265Encoder) syncOutputLocked(ctx context.Context, syncPoint uintptr, timestamp time.Duration) ([]EncodedPacket, error) {
	if syncPoint == 0 {
		return nil, nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		status, _, _ := syscall.SyscallN(e.api.sync, e.session, syncPoint, 50)
		got := oneVPLStatus(status)
		if got == oneVPLWarnInExecution {
			continue
		}
		if got != 0 {
			return nil, fmt.Errorf("MFXVideoCORE_SyncOperation returned %d", got)
		}
		break
	}
	offset := int(binary.LittleEndian.Uint32(e.bitstream[oneVPLBitstreamDataOffset : oneVPLBitstreamDataOffset+4]))
	length := int(binary.LittleEndian.Uint32(e.bitstream[oneVPLBitstreamDataLength : oneVPLBitstreamDataLength+4]))
	if offset < 0 || length <= 0 || offset+length > len(e.bitstreamData) {
		return nil, fmt.Errorf("oneVPL returned invalid bitstream range offset=%d length=%d capacity=%d", offset, length, len(e.bitstreamData))
	}
	data := append([]byte(nil), e.bitstreamData[offset:offset+length]...)
	frameType := binary.LittleEndian.Uint16(e.bitstream[oneVPLBitstreamFrameType : oneVPLBitstreamFrameType+2])
	keyFrame := oneVPLH265IsKeyFrame(data, frameType)
	if sequence := oneVPLH265ParameterSets(data); len(sequence) > 0 {
		e.sequence = sequence
	}
	e.resetBitstreamLocked()
	return []EncodedPacket{{
		Codec:     "h265",
		Data:      data,
		Timestamp: timestamp,
		KeyFrame:  keyFrame,
	}}, nil
}

func (e *oneVPLH265Encoder) Encode(ctx context.Context, frame RawFrame) ([]EncodedPacket, error) {
	if e == nil {
		return nil, ErrEncoderUnavailable
	}
	start := time.Now()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || e.session == 0 {
		return nil, ErrEncoderUnavailable
	}
	if e.ioPattern != oneVPLIOPatternInSystemMemory {
		return nil, fmt.Errorf("%w: raw frames are disabled for the oneVPL D3D11 encoder", ErrEncoderUnavailable)
	}
	i444, stride, err := e.rawFrameI444(frame)
	if err != nil {
		return nil, err
	}

	for attempt := 0; attempt < 4; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var surface uintptr
		status, _, _ := syscall.SyscallN(
			e.api.getSurface,
			e.session,
			uintptr(unsafe.Pointer(&surface)),
		)
		if got := oneVPLStatus(status); got != 0 || surface == 0 {
			return nil, fmt.Errorf("MFXMemory_GetSurfaceForEncode returned %d", got)
		}
		mapped := false
		if err := mapOneVPLSurface(surface); err != nil {
			releaseOneVPLSurface(surface)
			return nil, err
		}
		mapped = true
		if err := fillOneVPLAYUVSurface(surface, i444, stride, frame.Width, frame.Height, frame.Timestamp); err != nil {
			if mapped {
				_ = unmapOneVPLSurface(surface)
			}
			releaseOneVPLSurface(surface)
			return nil, err
		}
		if err := unmapOneVPLSurface(surface); err != nil {
			releaseOneVPLSurface(surface)
			return nil, err
		}
		mapped = false

		var ctrl [56]byte
		ctrlPtr := uintptr(0)
		if e.forceIDR {
			binary.LittleEndian.PutUint16(ctrl[32:34], oneVPLFrameTypeI|oneVPLFrameTypeRef|oneVPLFrameTypeIDR)
			ctrlPtr = uintptr(unsafe.Pointer(&ctrl[0]))
		}
		var syncPoint uintptr
		status, _, _ = syscall.SyscallN(
			e.api.encode,
			e.session,
			ctrlPtr,
			surface,
			uintptr(unsafe.Pointer(&e.bitstream[0])),
			uintptr(unsafe.Pointer(&syncPoint)),
		)
		releaseOneVPLSurface(surface)
		runtime.KeepAlive(i444)
		runtime.KeepAlive(ctrl)
		runtime.KeepAlive(e.bitstreamData)
		got := oneVPLStatus(status)
		switch got {
		case 0:
			e.forceIDR = false
			packets, syncErr := e.syncOutputLocked(ctx, syncPoint, frame.Timestamp)
			if syncErr != nil {
				return nil, syncErr
			}
			for _, packet := range packets {
				e.stats.Bytes += uint64(len(packet.Data))
			}
			e.stats.Frames++
			e.stats.LastEncodeTime = time.Since(start)
			return packets, nil
		case oneVPLErrMoreData:
			e.forceIDR = false
			e.stats.Frames++
			e.stats.LastEncodeTime = time.Since(start)
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
		case oneVPLErrNotEnoughBuffer:
			return nil, fmt.Errorf("oneVPL HEVC output exceeded %d-byte bitstream buffer", len(e.bitstreamData))
		default:
			return nil, fmt.Errorf("MFXVideoENCODE_EncodeFrameAsync returned %d", got)
		}
	}
	return nil, errors.New("oneVPL HEVC encoder remained busy")
}

func (e *oneVPLH265Encoder) EncodeD3D11(
	ctx context.Context,
	frame D3D11EncodeFrame,
) ([]EncodedPacket, error) {
	if e == nil {
		return nil, ErrEncoderUnavailable
	}
	start := time.Now()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || e.session == 0 || e.ioPattern != oneVPLIOPatternInVideoMemory ||
		e.d3d11Device == 0 || e.d3d11Context == nil {
		return nil, ErrEncoderUnavailable
	}
	if err := frame.Validate(); err != nil {
		return nil, err
	}
	if frame.Width != e.cfg.Width || frame.Height != e.cfg.Height {
		return nil, ErrInvalidFrame
	}
	if frame.PixelFormat() != PixelFormatAYUV {
		return nil, fmt.Errorf("%w: oneVPL D3D11 HEVC 4:4:4 requires AYUV input", ErrInvalidFrame)
	}
	if frame.Device != 0 && frame.Device != e.d3d11Device {
		return nil, fmt.Errorf(
			"%w: D3D11 frame device %#x does not match encoder device %#x",
			ErrInvalidFrame, frame.Device, e.d3d11Device,
		)
	}

	for attempt := 0; attempt < 4; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var surface uintptr
		status, _, _ := syscall.SyscallN(
			e.api.getSurface,
			e.session,
			uintptr(unsafe.Pointer(&surface)),
		)
		if got := oneVPLStatus(status); got != 0 || surface == 0 {
			return nil, fmt.Errorf("MFXMemory_GetSurfaceForEncode returned %d", got)
		}
		if err := copyOneVPLAYUVD3D11Surface(e.d3d11Context, surface, frame); err != nil {
			releaseOneVPLSurface(surface)
			return nil, err
		}

		var ctrl [56]byte
		ctrlPtr := uintptr(0)
		if e.forceIDR {
			binary.LittleEndian.PutUint16(ctrl[32:34], oneVPLFrameTypeI|oneVPLFrameTypeRef|oneVPLFrameTypeIDR)
			ctrlPtr = uintptr(unsafe.Pointer(&ctrl[0]))
		}
		var syncPoint uintptr
		status, _, _ = syscall.SyscallN(
			e.api.encode,
			e.session,
			ctrlPtr,
			surface,
			uintptr(unsafe.Pointer(&e.bitstream[0])),
			uintptr(unsafe.Pointer(&syncPoint)),
		)
		releaseOneVPLSurface(surface)
		runtime.KeepAlive(frame)
		runtime.KeepAlive(ctrl)
		runtime.KeepAlive(e.bitstreamData)
		got := oneVPLStatus(status)
		switch got {
		case 0:
			e.forceIDR = false
			packets, syncErr := e.syncOutputLocked(ctx, syncPoint, frame.Timestamp)
			if syncErr != nil {
				return nil, syncErr
			}
			for _, packet := range packets {
				e.stats.Bytes += uint64(len(packet.Data))
			}
			e.stats.Frames++
			e.stats.LastEncodeTime = time.Since(start)
			return packets, nil
		case oneVPLErrMoreData:
			e.forceIDR = false
			e.stats.Frames++
			e.stats.LastEncodeTime = time.Since(start)
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
		case oneVPLErrNotEnoughBuffer:
			return nil, fmt.Errorf("oneVPL HEVC output exceeded %d-byte bitstream buffer", len(e.bitstreamData))
		default:
			return nil, fmt.Errorf("MFXVideoENCODE_EncodeFrameAsync returned %d", got)
		}
	}
	return nil, errors.New("oneVPL HEVC D3D11 encoder remained busy")
}

func (e *oneVPLH265Encoder) ForceIDR(context.Context) error {
	if e == nil {
		return ErrEncoderUnavailable
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return ErrEncoderUnavailable
	}
	e.forceIDR = true
	return nil
}

func (e *oneVPLH265Encoder) Reconfigure(ctx context.Context, cfg VideoConfig) error {
	if e == nil {
		return ErrEncoderUnavailable
	}
	param, cfg, err := oneVPLHEVC444VideoParamForIO(cfg, e.ioPattern)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || e.session == 0 {
		return ErrEncoderUnavailable
	}
	if !bitrateOnlyReconfigure(e.cfg, cfg) {
		return ErrEncoderRebuildRequired
	}
	if cfg.TargetBitrate == e.cfg.TargetBitrate {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	status, _, _ := syscall.SyscallN(
		e.api.reset,
		e.session,
		uintptr(unsafe.Pointer(&param[0])),
	)
	got := oneVPLStatus(status)
	if got < 0 {
		return fmt.Errorf("MFXVideoENCODE_Reset returned %d", got)
	}
	e.cfg = cfg
	e.param = param
	e.forceIDR = true
	return nil
}

func (e *oneVPLH265Encoder) SequenceHeader() []byte {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]byte(nil), e.sequence...)
}

func (e *oneVPLH265Encoder) Stats() EncoderStats {
	if e == nil {
		return EncoderStats{}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.stats
}

func (e *oneVPLH265Encoder) Close() error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	base := e.base
	loader := e.loader
	session := e.session
	closeEncoder := e.api.closeEncoder
	d3d11Context := e.d3d11Context
	e.base = nil
	e.loader = 0
	e.session = 0
	e.d3d11Context = nil
	e.d3d11Device = 0
	e.mu.Unlock()

	var closeErr error
	if session != 0 && closeEncoder != 0 {
		status, _, _ := syscall.SyscallN(closeEncoder, session)
		if got := oneVPLStatus(status); got < 0 {
			closeErr = fmt.Errorf("MFXVideoENCODE_Close returned %d", got)
		}
	}
	if base != nil {
		if session != 0 {
			base.closeSession(session)
		}
		if loader != 0 {
			base.unload(loader)
		}
		base.Close()
	}
	releaseIUnknown(d3d11Context)
	return closeErr
}
