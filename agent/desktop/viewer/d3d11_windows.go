//go:build windows

package viewer

import (
	"fmt"
	"syscall"
	"unsafe"

	"github.com/lxn/win"
	"golang.org/x/sys/windows"
)

const (
	d3dDriverTypeHardware = 1
	d3dDriverTypeWarp     = 5

	d3d11SDKVersion               = 7
	d3d11CreateDeviceBGRASupport  = 0x20
	d3d11CreateDeviceVideoSupport = 0x800
	d3d11UsageDynamic             = 2
	d3d11CPUAccessWrite           = 0x10000
	d3d11MapWriteDiscard          = 4

	dxgiFormatB8G8R8A8UNorm     = 87
	dxgiFormatNV12               = 103
	dxgiUsageRenderTargetOutput = 0x20
	dxgiSwapEffectDiscard       = 0

	d3d11VPIVDimensionTexture2D = 1
	d3d11VPOVDimensionTexture2D = 1

	d3d11VideoProcessorFormatSupportInput  = 0x1
	d3d11VideoProcessorFormatSupportOutput = 0x2

	id3d10MultithreadSetMultithreadProtected = 5
	id3d11VideoDeviceCreateVideoProcessor    = 4
	id3d11VideoDeviceCreateInputView         = 8
	id3d11VideoDeviceCreateOutputView        = 9
	id3d11VideoDeviceCreateEnumerator        = 10
	id3d11VideoProcessorEnumeratorCheckFormat = 8
	id3d11VideoContextSetStreamFrameFormat   = 27
	id3d11VideoContextVideoProcessorBlt      = 53
)

var (
	d3d11DLL                          = windows.NewLazySystemDLL("d3d11.dll")
	procD3D11CreateDeviceAndSwapChain = d3d11DLL.NewProc("D3D11CreateDeviceAndSwapChain")
	iidID3D11Texture2D                = windows.GUID{
		Data1: 0x6f15aaf2, Data2: 0xd208, Data3: 0x4e89,
		Data4: [8]byte{0x9a, 0xb4, 0x48, 0x95, 0x35, 0xd3, 0x4f, 0x9c},
	}
	iidID3D11VideoDevice = windows.GUID{
		Data1: 0x10ec4d5b, Data2: 0x975a, Data3: 0x4689,
		Data4: [8]byte{0xb9, 0xe4, 0xd0, 0xaa, 0xc3, 0x0f, 0xe3, 0x33},
	}
	iidID3D11VideoContext = windows.GUID{
		Data1: 0x61f21c45, Data2: 0x3c0e, Data3: 0x4a74,
		Data4: [8]byte{0x9c, 0xea, 0x67, 0x10, 0x0d, 0x9a, 0xd5, 0xe4},
	}
	iidID3D10Multithread = windows.GUID{
		Data1: 0x9b7e4e00, Data2: 0x342c, Data3: 0x4106,
		Data4: [8]byte{0xa1, 0x9f, 0x4f, 0x27, 0x04, 0xf6, 0x89, 0xf0},
	}
)

type dxgiRational struct {
	Numerator   uint32
	Denominator uint32
}

type dxgiModeDesc struct {
	Width            uint32
	Height           uint32
	RefreshRate      dxgiRational
	Format           uint32
	ScanlineOrdering uint32
	Scaling          uint32
}

type dxgiSampleDesc struct {
	Count   uint32
	Quality uint32
}

type dxgiSwapChainDesc struct {
	BufferDesc   dxgiModeDesc
	SampleDesc   dxgiSampleDesc
	BufferUsage  uint32
	BufferCount  uint32
	OutputWindow win.HWND
	Windowed     int32
	SwapEffect   uint32
	Flags        uint32
}

type d3d11Texture2DDesc struct {
	Width          uint32
	Height         uint32
	MipLevels      uint32
	ArraySize      uint32
	Format         uint32
	SampleDesc     dxgiSampleDesc
	Usage          uint32
	BindFlags      uint32
	CPUAccessFlags uint32
	MiscFlags      uint32
}

type d3d11MappedSubresource struct {
	Data       unsafe.Pointer
	RowPitch   uint32
	DepthPitch uint32
}

type d3d11VideoProcessorContentDesc struct {
	InputFrameFormat uint32
	InputFrameRate   dxgiRational
	InputWidth       uint32
	InputHeight      uint32
	OutputFrameRate  dxgiRational
	OutputWidth      uint32
	OutputHeight     uint32
	Usage            uint32
}

type d3d11Tex2DVPIV struct {
	MipSlice   uint32
	ArraySlice uint32
}

type d3d11VideoProcessorInputViewDesc struct {
	FourCC        uint32
	ViewDimension uint32
	Texture2D     d3d11Tex2DVPIV
}

type d3d11VideoProcessorOutputViewDesc struct {
	ViewDimension uint32
	MipSlice      uint32
	FirstArray    uint32
	ArraySize     uint32
}

type d3d11VideoProcessorStream struct {
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

type d3d11Renderer struct {
	width      int
	height     int
	device     unsafe.Pointer
	context    unsafe.Pointer
	swapChain  unsafe.Pointer
	upload     unsafe.Pointer
	backBuffer unsafe.Pointer

	videoDevice     unsafe.Pointer
	videoContext    unsafe.Pointer
	videoEnumerator unsafe.Pointer
	videoProcessor  unsafe.Pointer
	videoOutputView unsafe.Pointer
	outputFrame     uint32
}

func hresultFailed(value uintptr) bool {
	return int32(uint32(value)) < 0
}

func hresultError(op string, value uintptr) error {
	return fmt.Errorf("%s failed with HRESULT 0x%08x", op, uint32(value))
}

func comMethod(object unsafe.Pointer, index int) uintptr {
	if object == nil {
		return 0
	}
	vtable := *(*unsafe.Pointer)(object)
	if vtable == nil {
		return 0
	}
	return (*[128]uintptr)(vtable)[index]
}

func comCall(object unsafe.Pointer, index int, args ...uintptr) uintptr {
	method := comMethod(object, index)
	if method == 0 {
		return uintptr(uint32(0x80004003)) // E_POINTER
	}
	params := make([]uintptr, 0, len(args)+1)
	params = append(params, uintptr(object))
	params = append(params, args...)
	result, _, _ := syscall.SyscallN(method, params...)
	return result
}

func queryCOM(object unsafe.Pointer, iid *windows.GUID) (unsafe.Pointer, error) {
	if object == nil {
		return nil, ErrUnavailable
	}
	var result unsafe.Pointer
	hr := comCall(
		object,
		0,
		uintptr(unsafe.Pointer(iid)),
		uintptr(unsafe.Pointer(&result)),
	)
	if hresultFailed(hr) {
		return nil, hresultError("IUnknown.QueryInterface", hr)
	}
	if result == nil {
		return nil, ErrUnavailable
	}
	return result, nil
}

func retainCOM(object unsafe.Pointer) {
	if object != nil {
		comCall(object, 1)
	}
}

func releaseCOM(object unsafe.Pointer) {
	if object != nil {
		comCall(object, 2)
	}
}

func createD3D11DeviceAndSwapChain(hwnd win.HWND, width, height int, driverType, flags uint32) (unsafe.Pointer, unsafe.Pointer, unsafe.Pointer, error) {
	featureLevels := [...]uint32{
		0xb100, // D3D_FEATURE_LEVEL_11_1
		0xb000, // D3D_FEATURE_LEVEL_11_0
		0xa100, // D3D_FEATURE_LEVEL_10_1
		0xa000, // D3D_FEATURE_LEVEL_10_0
	}
	desc := dxgiSwapChainDesc{
		BufferDesc: dxgiModeDesc{
			Width:  uint32(width),
			Height: uint32(height),
			Format: dxgiFormatB8G8R8A8UNorm,
		},
		SampleDesc:   dxgiSampleDesc{Count: 1},
		BufferUsage:  dxgiUsageRenderTargetOutput,
		BufferCount:  2,
		OutputWindow: hwnd,
		Windowed:     1,
		SwapEffect:   dxgiSwapEffectDiscard,
	}
	var swapChain, device, context unsafe.Pointer
	var selectedLevel uint32
	hr, _, _ := procD3D11CreateDeviceAndSwapChain.Call(
		0,
		uintptr(driverType),
		0,
		uintptr(flags),
		uintptr(unsafe.Pointer(&featureLevels[0])),
		uintptr(len(featureLevels)),
		uintptr(d3d11SDKVersion),
		uintptr(unsafe.Pointer(&desc)),
		uintptr(unsafe.Pointer(&swapChain)),
		uintptr(unsafe.Pointer(&device)),
		uintptr(unsafe.Pointer(&selectedLevel)),
		uintptr(unsafe.Pointer(&context)),
	)
	if hresultFailed(hr) {
		return nil, nil, nil, hresultError("D3D11CreateDeviceAndSwapChain", hr)
	}
	if swapChain == nil || device == nil || context == nil {
		releaseCOM(context)
		releaseCOM(device)
		releaseCOM(swapChain)
		return nil, nil, nil, fmt.Errorf("%w: D3D11 returned incomplete objects", ErrUnavailable)
	}
	return swapChain, device, context, nil
}

func createUploadTexture(device unsafe.Pointer, width, height int) (unsafe.Pointer, error) {
	desc := d3d11Texture2DDesc{
		Width:          uint32(width),
		Height:         uint32(height),
		MipLevels:      1,
		ArraySize:      1,
		Format:         dxgiFormatB8G8R8A8UNorm,
		SampleDesc:     dxgiSampleDesc{Count: 1},
		Usage:          d3d11UsageDynamic,
		CPUAccessFlags: d3d11CPUAccessWrite,
	}
	var texture unsafe.Pointer
	hr := comCall(
		device,
		5, // ID3D11Device::CreateTexture2D
		uintptr(unsafe.Pointer(&desc)),
		0,
		uintptr(unsafe.Pointer(&texture)),
	)
	if hresultFailed(hr) {
		return nil, hresultError("ID3D11Device.CreateTexture2D", hr)
	}
	if texture == nil {
		return nil, fmt.Errorf("%w: CreateTexture2D returned nil", ErrUnavailable)
	}
	return texture, nil
}

func getSwapChainBackBuffer(swapChain unsafe.Pointer) (unsafe.Pointer, error) {
	var buffer unsafe.Pointer
	hr := comCall(
		swapChain,
		9, // IDXGISwapChain::GetBuffer
		0,
		uintptr(unsafe.Pointer(&iidID3D11Texture2D)),
		uintptr(unsafe.Pointer(&buffer)),
	)
	if hresultFailed(hr) {
		return nil, hresultError("IDXGISwapChain.GetBuffer", hr)
	}
	if buffer == nil {
		return nil, fmt.Errorf("%w: swap chain back buffer is nil", ErrUnavailable)
	}
	return buffer, nil
}

func protectD3D11Multithread(device unsafe.Pointer) error {
	multithread, err := queryCOM(device, &iidID3D10Multithread)
	if err != nil {
		return err
	}
	defer releaseCOM(multithread)
	if comCall(multithread, id3d10MultithreadSetMultithreadProtected, 1) == 0 {
		return fmt.Errorf("%w: ID3D10Multithread.SetMultithreadProtected returned FALSE", ErrUnavailable)
	}
	return nil
}

func (r *d3d11Renderer) initVideoProcessor() error {
	videoDevice, err := queryCOM(r.device, &iidID3D11VideoDevice)
	if err != nil {
		return err
	}
	videoContext, err := queryCOM(r.context, &iidID3D11VideoContext)
	if err != nil {
		releaseCOM(videoDevice)
		return err
	}

	fail := func(err error) error {
		releaseCOM(videoContext)
		releaseCOM(videoDevice)
		return err
	}

	desc := d3d11VideoProcessorContentDesc{
		InputFrameFormat: 0, // D3D11_VIDEO_FRAME_FORMAT_PROGRESSIVE
		InputFrameRate:   dxgiRational{Numerator: 60, Denominator: 1},
		InputWidth:       uint32(r.width),
		InputHeight:      uint32(r.height),
		OutputFrameRate:  dxgiRational{Numerator: 60, Denominator: 1},
		OutputWidth:      uint32(r.width),
		OutputHeight:     uint32(r.height),
		Usage:            1, // D3D11_VIDEO_USAGE_OPTIMAL_SPEED
	}
	var enumerator unsafe.Pointer
	hr := comCall(
		videoDevice,
		id3d11VideoDeviceCreateEnumerator,
		uintptr(unsafe.Pointer(&desc)),
		uintptr(unsafe.Pointer(&enumerator)),
	)
	if hresultFailed(hr) || enumerator == nil {
		if !hresultFailed(hr) {
			return fail(ErrUnavailable)
		}
		return fail(hresultError("ID3D11VideoDevice.CreateVideoProcessorEnumerator", hr))
	}

	var nv12Support uint32
	hr = comCall(
		enumerator,
		id3d11VideoProcessorEnumeratorCheckFormat,
		dxgiFormatNV12,
		uintptr(unsafe.Pointer(&nv12Support)),
	)
	if hresultFailed(hr) || nv12Support&d3d11VideoProcessorFormatSupportInput == 0 {
		releaseCOM(enumerator)
		if hresultFailed(hr) {
			return fail(hresultError("ID3D11VideoProcessorEnumerator.CheckVideoProcessorFormat(NV12)", hr))
		}
		return fail(fmt.Errorf("%w: D3D11 video processor does not accept NV12", ErrUnavailable))
	}

	var bgraSupport uint32
	hr = comCall(
		enumerator,
		id3d11VideoProcessorEnumeratorCheckFormat,
		dxgiFormatB8G8R8A8UNorm,
		uintptr(unsafe.Pointer(&bgraSupport)),
	)
	if hresultFailed(hr) || bgraSupport&d3d11VideoProcessorFormatSupportOutput == 0 {
		releaseCOM(enumerator)
		if hresultFailed(hr) {
			return fail(hresultError("ID3D11VideoProcessorEnumerator.CheckVideoProcessorFormat(BGRA)", hr))
		}
		return fail(fmt.Errorf("%w: D3D11 video processor cannot output BGRA", ErrUnavailable))
	}

	var processor unsafe.Pointer
	hr = comCall(
		videoDevice,
		id3d11VideoDeviceCreateVideoProcessor,
		uintptr(enumerator),
		0,
		uintptr(unsafe.Pointer(&processor)),
	)
	if hresultFailed(hr) || processor == nil {
		releaseCOM(enumerator)
		if !hresultFailed(hr) {
			return fail(ErrUnavailable)
		}
		return fail(hresultError("ID3D11VideoDevice.CreateVideoProcessor", hr))
	}

	outputDesc := d3d11VideoProcessorOutputViewDesc{
		ViewDimension: d3d11VPOVDimensionTexture2D,
		MipSlice:      0,
	}
	var outputView unsafe.Pointer
	hr = comCall(
		videoDevice,
		id3d11VideoDeviceCreateOutputView,
		uintptr(r.backBuffer),
		uintptr(enumerator),
		uintptr(unsafe.Pointer(&outputDesc)),
		uintptr(unsafe.Pointer(&outputView)),
	)
	if hresultFailed(hr) || outputView == nil {
		releaseCOM(processor)
		releaseCOM(enumerator)
		if !hresultFailed(hr) {
			return fail(ErrUnavailable)
		}
		return fail(hresultError("ID3D11VideoDevice.CreateVideoProcessorOutputView", hr))
	}

	r.videoDevice = videoDevice
	r.videoContext = videoContext
	r.videoEnumerator = enumerator
	r.videoProcessor = processor
	r.videoOutputView = outputView
	comCall(
		r.videoContext,
		id3d11VideoContextSetStreamFrameFormat,
		uintptr(r.videoProcessor),
		0,
		0, // progressive
	)
	return nil
}

func newD3D11Renderer(hwnd win.HWND, width, height int) (*d3d11Renderer, error) {
	if hwnd == 0 || width <= 0 || height <= 0 {
		return nil, fmt.Errorf("%w: invalid D3D11 viewer geometry", ErrUnavailable)
	}

	type candidate struct {
		driver uint32
		flags  uint32
	}
	candidates := []candidate{
		{d3dDriverTypeHardware, d3d11CreateDeviceBGRASupport | d3d11CreateDeviceVideoSupport},
		{d3dDriverTypeHardware, d3d11CreateDeviceBGRASupport},
		{d3dDriverTypeWarp, d3d11CreateDeviceBGRASupport | d3d11CreateDeviceVideoSupport},
		{d3dDriverTypeWarp, d3d11CreateDeviceBGRASupport},
	}

	var swapChain, device, context unsafe.Pointer
	var err error
	for _, candidate := range candidates {
		swapChain, device, context, err = createD3D11DeviceAndSwapChain(
			hwnd, width, height, candidate.driver, candidate.flags,
		)
		if err == nil {
			break
		}
	}
	if err != nil {
		return nil, err
	}

	r := &d3d11Renderer{
		width: width, height: height,
		swapChain: swapChain, device: device, context: context,
	}
	fail := func(err error) (*d3d11Renderer, error) {
		r.Close()
		return nil, err
	}
	r.upload, err = createUploadTexture(device, width, height)
	if err != nil {
		return fail(err)
	}
	r.backBuffer, err = getSwapChainBackBuffer(swapChain)
	if err != nil {
		return fail(err)
	}

	// Shared decoding is optional. The existing CPU upload renderer remains
	// available even when this device/driver cannot expose the D3D11 video API.
	if protectErr := protectD3D11Multithread(device); protectErr == nil {
		_ = r.initVideoProcessor()
	}
	return r, nil
}

func (r *d3d11Renderer) DeviceHandle() uintptr {
	if r == nil || r.device == nil || r.videoProcessor == nil {
		return 0
	}
	return uintptr(r.device)
}

func (r *d3d11Renderer) Render(frame Frame) error {
	if r == nil || r.context == nil || r.upload == nil || r.backBuffer == nil || r.swapChain == nil {
		return ErrUnavailable
	}
	if err := frame.Validate(); err != nil {
		return err
	}
	if frame.Width != r.width || frame.Height != r.height {
		return fmt.Errorf("%w: frame dimensions changed from %dx%d to %dx%d", ErrUnavailable, r.width, r.height, frame.Width, frame.Height)
	}

	var mapped d3d11MappedSubresource
	hr := comCall(
		r.context,
		14, // ID3D11DeviceContext::Map
		uintptr(r.upload),
		0,
		d3d11MapWriteDiscard,
		0,
		uintptr(unsafe.Pointer(&mapped)),
	)
	if hresultFailed(hr) {
		return hresultError("ID3D11DeviceContext.Map", hr)
	}
	if mapped.Data == nil || int(mapped.RowPitch) < frame.Width*4 {
		comCall(r.context, 15, uintptr(r.upload), 0)
		return fmt.Errorf("%w: invalid mapped D3D11 texture", ErrUnavailable)
	}
	target := unsafe.Slice((*byte)(mapped.Data), int(mapped.RowPitch)*frame.Height)
	for row := 0; row < frame.Height; row++ {
		srcStart := row * frame.Stride
		dstStart := row * int(mapped.RowPitch)
		copy(target[dstStart:dstStart+frame.Width*4], frame.BGRA[srcStart:srcStart+frame.Width*4])
	}
	comCall(r.context, 15, uintptr(r.upload), 0)
	comCall(
		r.context,
		47, // ID3D11DeviceContext::CopyResource
		uintptr(r.backBuffer),
		uintptr(r.upload),
	)
	return r.present()
}

func (r *d3d11Renderer) RenderD3D11(frame D3D11Frame) error {
	if r == nil || r.videoDevice == nil || r.videoContext == nil ||
		r.videoEnumerator == nil || r.videoProcessor == nil ||
		r.videoOutputView == nil || r.swapChain == nil {
		return ErrUnavailable
	}
	if err := frame.Validate(); err != nil {
		return err
	}
	if frame.Width != r.width || frame.Height != r.height {
		return fmt.Errorf("%w: D3D11 frame dimensions changed from %dx%d to %dx%d", ErrUnavailable, r.width, r.height, frame.Width, frame.Height)
	}

	source := unsafe.Pointer(frame.Resource)
	var textureDesc d3d11Texture2DDesc
	comCall(
		source,
		10, // ID3D11Texture2D::GetDesc
		uintptr(unsafe.Pointer(&textureDesc)),
	)
	if textureDesc.MipLevels == 0 {
		return fmt.Errorf("%w: D3D11 decoder texture has zero mip levels", ErrUnavailable)
	}

	inputDesc := d3d11VideoProcessorInputViewDesc{
		ViewDimension: d3d11VPIVDimensionTexture2D,
		Texture2D: d3d11Tex2DVPIV{
			MipSlice:   frame.Subresource % textureDesc.MipLevels,
			ArraySlice: frame.Subresource / textureDesc.MipLevels,
		},
	}
	var inputView unsafe.Pointer
	hr := comCall(
		r.videoDevice,
		id3d11VideoDeviceCreateInputView,
		uintptr(source),
		uintptr(r.videoEnumerator),
		uintptr(unsafe.Pointer(&inputDesc)),
		uintptr(unsafe.Pointer(&inputView)),
	)
	if hresultFailed(hr) || inputView == nil {
		if !hresultFailed(hr) {
			return ErrUnavailable
		}
		return hresultError("ID3D11VideoDevice.CreateVideoProcessorInputView", hr)
	}
	defer releaseCOM(inputView)

	stream := d3d11VideoProcessorStream{
		Enable:       1,
		InputSurface: inputView,
	}
	hr = comCall(
		r.videoContext,
		id3d11VideoContextVideoProcessorBlt,
		uintptr(r.videoProcessor),
		uintptr(r.videoOutputView),
		uintptr(r.outputFrame),
		1,
		uintptr(unsafe.Pointer(&stream)),
	)
	if hresultFailed(hr) {
		return hresultError("ID3D11VideoContext.VideoProcessorBlt", hr)
	}
	r.outputFrame++
	return r.present()
}

func (r *d3d11Renderer) present() error {
	hr := comCall(
		r.swapChain,
		8, // IDXGISwapChain::Present
		0,
		0,
	)
	if hresultFailed(hr) {
		return hresultError("IDXGISwapChain.Present", hr)
	}
	return nil
}

func (r *d3d11Renderer) Close() {
	if r == nil {
		return
	}
	releaseCOM(r.videoOutputView)
	releaseCOM(r.videoProcessor)
	releaseCOM(r.videoEnumerator)
	releaseCOM(r.videoContext)
	releaseCOM(r.videoDevice)
	releaseCOM(r.backBuffer)
	releaseCOM(r.upload)
	releaseCOM(r.context)
	releaseCOM(r.device)
	releaseCOM(r.swapChain)
	r.videoOutputView = nil
	r.videoProcessor = nil
	r.videoEnumerator = nil
	r.videoContext = nil
	r.videoDevice = nil
	r.backBuffer = nil
	r.upload = nil
	r.context = nil
	r.device = nil
	r.swapChain = nil
}
