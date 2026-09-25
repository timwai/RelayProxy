//go:build windows

package codec

import (
	"errors"
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	dxgiFormatB8G8R8A8UNorm = 87
	dxgiFormatAYUV          = 100

	d3d11UsageDefault       = 0
	d3d11BindRenderTarget   = 0x20
	d3d11VPIVTexture2D      = 1
	d3d11VPOVTexture2D      = 1
	d3d11VPFormatInput      = 0x1
	d3d11VPFormatOutput     = 0x2
	d3d11VPUsageSpeed       = 1
	d3d11VPFrameProgressive = 0

	id3d11VideoDeviceCreateProcessor  = 4
	id3d11VideoDeviceCreateInputView  = 8
	id3d11VideoDeviceCreateOutputView = 9
	id3d11VideoDeviceCreateEnumerator = 10

	id3d11VPEnumeratorCheckFormat = 8

	id3d11VideoContextSetStreamFrameFormat = 27
	id3d11VideoContextSetStreamSourceRect  = 30
	id3d11VideoContextSetStreamDestRect    = 31
	id3d11VideoContextBlt                  = 53
)

var (
	iidID3D11VideoDevice = windows.GUID{
		Data1: 0x10ec4d5b, Data2: 0x975a, Data3: 0x4689,
		Data4: [8]byte{0xb9, 0xe4, 0xd0, 0xaa, 0xc3, 0x0f, 0xe3, 0x33},
	}
	iidID3D11VideoContext = windows.GUID{
		Data1: 0x61f21c45, Data2: 0x3c0e, Data3: 0x4a74,
		Data4: [8]byte{0x9c, 0xea, 0x67, 0x10, 0x0d, 0x9a, 0xd5, 0xe4},
	}
)

type d3d11VPRational struct {
	Numerator   uint32
	Denominator uint32
}

type d3d11VPContentDesc struct {
	InputFrameFormat uint32
	InputFrameRate   d3d11VPRational
	InputWidth       uint32
	InputHeight      uint32
	OutputFrameRate  d3d11VPRational
	OutputWidth      uint32
	OutputHeight     uint32
	Usage            uint32
}

type d3d11VPTex2DInput struct {
	MipSlice   uint32
	ArraySlice uint32
}

type d3d11VPInputViewDesc struct {
	FourCC        uint32
	ViewDimension uint32
	Texture2D     d3d11VPTex2DInput
}

type d3d11VPOutputViewDesc struct {
	ViewDimension uint32
	MipSlice      uint32
	FirstArray    uint32
	ArraySize     uint32
}

type d3d11VPRect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

type d3d11VPStream struct {
	Enable            int32
	OutputIndex       uint32
	InputFrameOrField uint32
	PastFrames        uint32
	FutureFrames      uint32

	PastSurfaces        unsafe.Pointer
	InputSurface        unsafe.Pointer
	FutureSurfaces      unsafe.Pointer
	PastSurfacesRight   unsafe.Pointer
	InputSurfaceRight   unsafe.Pointer
	FutureSurfacesRight unsafe.Pointer
}

// D3D11NV12Converter owns a reusable NV12 output texture on an external D3D11
// device. Convert returns a borrowed output resource: callers must synchronously
// submit it to the encoder before calling Convert again or closing the converter.
type D3D11NV12Converter struct {
	cfg          D3D11ConvertConfig
	outputFormat PixelFormat
	outputDXGI   uint32

	device        unsafe.Pointer
	context       unsafe.Pointer
	videoDevice   unsafe.Pointer
	videoContext  unsafe.Pointer
	enumerator    unsafe.Pointer
	processor     unsafe.Pointer
	outputTexture unsafe.Pointer
	outputView    unsafe.Pointer
	outputFrame   uint32
}

func OpenD3D11NV12Converter(deviceHandle uintptr, cfg D3D11ConvertConfig) (*D3D11NV12Converter, error) {
	return openD3D11VideoConverter(deviceHandle, cfg, PixelFormatNV12)
}

func OpenD3D11AYUVConverter(deviceHandle uintptr, cfg D3D11ConvertConfig) (*D3D11NV12Converter, error) {
	return openD3D11VideoConverter(deviceHandle, cfg, PixelFormatAYUV)
}

func d3d11OutputDXGI(format PixelFormat) (uint32, bool) {
	switch format {
	case PixelFormatNV12:
		return dxgiFormatNV12, true
	case PixelFormatAYUV:
		return dxgiFormatAYUV, true
	default:
		return 0, false
	}
}

func openD3D11VideoConverter(
	deviceHandle uintptr,
	cfg D3D11ConvertConfig,
	outputFormat PixelFormat,
) (*D3D11NV12Converter, error) {
	if deviceHandle == 0 {
		return nil, fmt.Errorf("%w: D3D11 converter device is nil", ErrEncoderUnavailable)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	outputDXGI, ok := d3d11OutputDXGI(outputFormat)
	if !ok {
		return nil, fmt.Errorf("%w: unsupported D3D11 converter output %q", ErrEncoderUnavailable, outputFormat)
	}

	device := unsafe.Pointer(deviceHandle)
	comCall(device, 1) // IUnknown::AddRef; converter owns this reference.

	var context unsafe.Pointer
	comCall(
		device,
		40, // ID3D11Device::GetImmediateContext
		uintptr(unsafe.Pointer(&context)),
	)
	if context == nil {
		releaseIUnknown(device)
		return nil, errors.New("D3D11 converter device returned nil immediate context")
	}

	converter := &D3D11NV12Converter{
		cfg:          cfg,
		outputFormat: outputFormat,
		outputDXGI:   outputDXGI,
		device:       device,
		context:      context,
	}
	fail := func(err error) (*D3D11NV12Converter, error) {
		converter.Close()
		return nil, err
	}

	if multithread, err := comQueryInterface(device, &iidID3D10Multithread); err == nil {
		protected := comCall(multithread, id3d10MultithreadSetMultithreadProtected, 1)
		releaseIUnknown(multithread)
		if protected == 0 {
			return fail(errors.New("D3D11 converter failed to enable multithread protection"))
		}
	}

	var err error
	converter.videoDevice, err = comQueryInterface(device, &iidID3D11VideoDevice)
	if err != nil {
		return fail(fmt.Errorf("query ID3D11VideoDevice: %w", err))
	}
	converter.videoContext, err = comQueryInterface(context, &iidID3D11VideoContext)
	if err != nil {
		return fail(fmt.Errorf("query ID3D11VideoContext: %w", err))
	}

	desc := d3d11VPContentDesc{
		InputFrameFormat: d3d11VPFrameProgressive,
		InputFrameRate:   d3d11VPRational{Numerator: uint32(cfg.FPS), Denominator: 1},
		InputWidth:       uint32(cfg.InputWidth),
		InputHeight:      uint32(cfg.InputHeight),
		OutputFrameRate:  d3d11VPRational{Numerator: uint32(cfg.FPS), Denominator: 1},
		OutputWidth:      uint32(cfg.OutputWidth),
		OutputHeight:     uint32(cfg.OutputHeight),
		Usage:            d3d11VPUsageSpeed,
	}
	hr := comCall(
		converter.videoDevice,
		id3d11VideoDeviceCreateEnumerator,
		uintptr(unsafe.Pointer(&desc)),
		uintptr(unsafe.Pointer(&converter.enumerator)),
	)
	if hresultFailed(hr) || converter.enumerator == nil {
		if !hresultFailed(hr) {
			return fail(errors.New("D3D11 video processor enumerator is nil"))
		}
		return fail(hresultError("ID3D11VideoDevice.CreateVideoProcessorEnumerator", hr))
	}

	if err := converter.requireFormat(dxgiFormatB8G8R8A8UNorm, d3d11VPFormatInput, "BGRA input"); err != nil {
		return fail(err)
	}
	if err := converter.requireFormat(outputDXGI, d3d11VPFormatOutput, string(outputFormat)+" output"); err != nil {
		return fail(err)
	}

	hr = comCall(
		converter.videoDevice,
		id3d11VideoDeviceCreateProcessor,
		uintptr(converter.enumerator),
		0,
		uintptr(unsafe.Pointer(&converter.processor)),
	)
	if hresultFailed(hr) || converter.processor == nil {
		if !hresultFailed(hr) {
			return fail(errors.New("D3D11 video processor is nil"))
		}
		return fail(hresultError("ID3D11VideoDevice.CreateVideoProcessor", hr))
	}

	textureDesc := mfD3D11Texture2DDesc{
		Width:          uint32(cfg.OutputWidth),
		Height:         uint32(cfg.OutputHeight),
		MipLevels:      1,
		ArraySize:      1,
		Format:         outputDXGI,
		SampleDesc:     mfD3D11SampleDesc{Count: 1},
		Usage:          d3d11UsageDefault,
		BindFlags:      d3d11BindRenderTarget,
		CPUAccessFlags: 0,
		MiscFlags:      0,
	}
	hr = comCall(
		converter.device,
		5, // ID3D11Device::CreateTexture2D
		uintptr(unsafe.Pointer(&textureDesc)),
		0,
		uintptr(unsafe.Pointer(&converter.outputTexture)),
	)
	if hresultFailed(hr) || converter.outputTexture == nil {
		if !hresultFailed(hr) {
			return fail(fmt.Errorf("D3D11 %s output texture is nil", outputFormat))
		}
		return fail(hresultError("ID3D11Device.CreateTexture2D("+string(outputFormat)+")", hr))
	}

	outputDesc := d3d11VPOutputViewDesc{
		ViewDimension: d3d11VPOVTexture2D,
		ArraySize:     1,
	}
	hr = comCall(
		converter.videoDevice,
		id3d11VideoDeviceCreateOutputView,
		uintptr(converter.outputTexture),
		uintptr(converter.enumerator),
		uintptr(unsafe.Pointer(&outputDesc)),
		uintptr(unsafe.Pointer(&converter.outputView)),
	)
	if hresultFailed(hr) || converter.outputView == nil {
		if !hresultFailed(hr) {
			return fail(fmt.Errorf("D3D11 %s video processor output view is nil", outputFormat))
		}
		return fail(hresultError("ID3D11VideoDevice.CreateVideoProcessorOutputView("+string(outputFormat)+")", hr))
	}

	comCall(
		converter.videoContext,
		id3d11VideoContextSetStreamFrameFormat,
		uintptr(converter.processor),
		0,
		d3d11VPFrameProgressive,
	)
	return converter, nil
}

func (c *D3D11NV12Converter) requireFormat(format, required uint32, label string) error {
	var support uint32
	hr := comCall(
		c.enumerator,
		id3d11VPEnumeratorCheckFormat,
		uintptr(format),
		uintptr(unsafe.Pointer(&support)),
	)
	if hresultFailed(hr) {
		return hresultError("ID3D11VideoProcessorEnumerator.CheckVideoProcessorFormat("+label+")", hr)
	}
	if support&required == 0 {
		return fmt.Errorf("%w: D3D11 video processor does not support %s", ErrEncoderUnavailable, label)
	}
	return nil
}

func (c *D3D11NV12Converter) Device() uintptr {
	if c == nil {
		return 0
	}
	return uintptr(c.device)
}

func (c *D3D11NV12Converter) Convert(
	resource uintptr,
	subresource uint32,
	timestamp time.Duration,
) (D3D11EncodeFrame, error) {
	if c == nil || c.device == nil || c.videoDevice == nil || c.videoContext == nil ||
		c.enumerator == nil || c.processor == nil || c.outputTexture == nil || c.outputView == nil {
		return D3D11EncodeFrame{}, ErrEncoderUnavailable
	}
	if resource == 0 {
		return D3D11EncodeFrame{}, fmt.Errorf("%w: D3D11 converter input resource is nil", ErrInvalidFrame)
	}

	source := unsafe.Pointer(resource)
	var sourceDesc mfD3D11Texture2DDesc
	comCall(
		source,
		10, // ID3D11Texture2D::GetDesc
		uintptr(unsafe.Pointer(&sourceDesc)),
	)
	if sourceDesc.MipLevels == 0 {
		return D3D11EncodeFrame{}, fmt.Errorf("%w: source D3D11 texture has zero mip levels", ErrInvalidFrame)
	}
	if sourceDesc.Format != dxgiFormatB8G8R8A8UNorm {
		return D3D11EncodeFrame{}, fmt.Errorf("%w: source D3D11 texture format=%d want BGRA8", ErrInvalidFrame, sourceDesc.Format)
	}
	if int(sourceDesc.Width) != c.cfg.InputWidth || int(sourceDesc.Height) != c.cfg.InputHeight {
		return D3D11EncodeFrame{}, fmt.Errorf(
			"%w: source D3D11 texture=%dx%d want=%dx%d",
			ErrInvalidFrame, sourceDesc.Width, sourceDesc.Height, c.cfg.InputWidth, c.cfg.InputHeight,
		)
	}

	inputDesc := d3d11VPInputViewDesc{
		ViewDimension: d3d11VPIVTexture2D,
		Texture2D: d3d11VPTex2DInput{
			MipSlice:   subresource % sourceDesc.MipLevels,
			ArraySlice: subresource / sourceDesc.MipLevels,
		},
	}
	var inputView unsafe.Pointer
	hr := comCall(
		c.videoDevice,
		id3d11VideoDeviceCreateInputView,
		resource,
		uintptr(c.enumerator),
		uintptr(unsafe.Pointer(&inputDesc)),
		uintptr(unsafe.Pointer(&inputView)),
	)
	if hresultFailed(hr) || inputView == nil {
		if !hresultFailed(hr) {
			return D3D11EncodeFrame{}, errors.New("D3D11 video processor input view is nil")
		}
		return D3D11EncodeFrame{}, hresultError("ID3D11VideoDevice.CreateVideoProcessorInputView(BGRA)", hr)
	}
	defer releaseIUnknown(inputView)

	srcRect := d3d11VPRect{
		Right:  int32(c.cfg.InputWidth),
		Bottom: int32(c.cfg.InputHeight),
	}
	dstRect := d3d11VPRect{
		Right:  int32(c.cfg.OutputWidth),
		Bottom: int32(c.cfg.OutputHeight),
	}
	comCall(
		c.videoContext,
		id3d11VideoContextSetStreamSourceRect,
		uintptr(c.processor),
		0,
		1,
		uintptr(unsafe.Pointer(&srcRect)),
	)
	comCall(
		c.videoContext,
		id3d11VideoContextSetStreamDestRect,
		uintptr(c.processor),
		0,
		1,
		uintptr(unsafe.Pointer(&dstRect)),
	)

	stream := d3d11VPStream{
		Enable:       1,
		InputSurface: inputView,
	}
	hr = comCall(
		c.videoContext,
		id3d11VideoContextBlt,
		uintptr(c.processor),
		uintptr(c.outputView),
		uintptr(c.outputFrame),
		1,
		uintptr(unsafe.Pointer(&stream)),
	)
	if hresultFailed(hr) {
		return D3D11EncodeFrame{}, hresultError("ID3D11VideoContext.VideoProcessorBlt(BGRA->"+string(c.outputFormat)+")", hr)
	}
	c.outputFrame++

	return D3D11EncodeFrame{
		Device:      uintptr(c.device),
		Resource:    uintptr(c.outputTexture),
		Subresource: 0,
		Width:       c.cfg.OutputWidth,
		Height:      c.cfg.OutputHeight,
		Format:      c.outputFormat,
		Timestamp:   timestamp,
	}, nil
}

func (c *D3D11NV12Converter) Close() error {
	if c == nil {
		return nil
	}
	releaseIUnknown(c.outputView)
	releaseIUnknown(c.outputTexture)
	releaseIUnknown(c.processor)
	releaseIUnknown(c.enumerator)
	releaseIUnknown(c.videoContext)
	releaseIUnknown(c.videoDevice)
	releaseIUnknown(c.context)
	releaseIUnknown(c.device)
	c.outputView = nil
	c.outputTexture = nil
	c.processor = nil
	c.enumerator = nil
	c.videoContext = nil
	c.videoDevice = nil
	c.context = nil
	c.device = nil
	return nil
}
