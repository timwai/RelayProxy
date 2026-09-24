//go:build windows

package viewer

import (
	"fmt"
	"runtime"
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

	dxgiFormatUnknown           = 0
	dxgiFormatB8G8R8A8UNorm     = 87
	dxgiFormatNV12              = 103
	dxgiUsageRenderTargetOutput = 0x20
	dxgiSwapEffectDiscard       = 0

	d3d11VPIVDimensionTexture2D = 1
	d3d11VPOVDimensionTexture2D = 1

	d3d11VideoProcessorFormatSupportInput  = 0x1
	d3d11VideoProcessorFormatSupportOutput = 0x2

	d3d11VideoProcessorFeatureLegacy      = 0x10
	d3d11VideoProcessorFeatureAlphaStream = 0x80

	id3d10MultithreadSetMultithreadProtected  = 5
	id3d11VideoDeviceCreateVideoProcessor     = 4
	id3d11VideoDeviceCreateInputView          = 8
	id3d11VideoDeviceCreateOutputView         = 9
	id3d11VideoDeviceCreateEnumerator         = 10
	id3d11VideoProcessorEnumeratorCheckFormat = 8
	id3d11VideoProcessorEnumeratorGetCaps     = 9
	id3d11VideoContextSetStreamFrameFormat    = 27
	id3d11VideoContextSetStreamSourceRect     = 30
	id3d11VideoContextSetStreamDestRect       = 31
	id3d11VideoContextSetStreamAlpha          = 32
	id3d11VideoContextVideoProcessorBlt       = 53
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

type d3d11VideoProcessorCaps struct {
	DeviceCaps              uint32
	FeatureCaps             uint32
	FilterCaps              uint32
	InputFormatCaps         uint32
	AutoStreamCaps          uint32
	StereoCaps              uint32
	RateConversionCapsCount uint32
	MaxInputStreams         uint32
	MaxStreamStates         uint32
}

type d3d11SubresourceData struct {
	SysMem           unsafe.Pointer
	SysMemPitch      uint32
	SysMemSlicePitch uint32
}

type d3d11Rect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
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

	gpuCursor     bool
	cursorTexture unsafe.Pointer
	cursorView    unsafe.Pointer
	cursorID      string
	cursorWidth   int
	cursorHeight  int
	cursorBGRA    []byte
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

	gpuCursorCapable := false
	var caps d3d11VideoProcessorCaps
	capsHR := comCall(
		enumerator,
		id3d11VideoProcessorEnumeratorGetCaps,
		uintptr(unsafe.Pointer(&caps)),
	)
	if runtime.GOARCH == "amd64" &&
		!hresultFailed(capsHR) &&
		bgraSupport&d3d11VideoProcessorFormatSupportInput != 0 &&
		caps.MaxInputStreams >= 2 &&
		caps.MaxStreamStates >= 2 &&
		caps.FeatureCaps&d3d11VideoProcessorFeatureAlphaStream != 0 &&
		caps.FeatureCaps&d3d11VideoProcessorFeatureLegacy == 0 {
		gpuCursorCapable = true
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
	r.gpuCursor = gpuCursorCapable
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

func (r *d3d11Renderer) releaseMediaResources() {
	if r == nil {
		return
	}
	releaseCOM(r.cursorView)
	releaseCOM(r.cursorTexture)
	releaseCOM(r.videoOutputView)
	releaseCOM(r.videoProcessor)
	releaseCOM(r.videoEnumerator)
	releaseCOM(r.videoContext)
	releaseCOM(r.videoDevice)
	releaseCOM(r.backBuffer)
	releaseCOM(r.upload)
	r.cursorView = nil
	r.cursorTexture = nil
	r.videoOutputView = nil
	r.videoProcessor = nil
	r.videoEnumerator = nil
	r.videoContext = nil
	r.videoDevice = nil
	r.backBuffer = nil
	r.upload = nil
	r.cursorID = ""
	r.cursorWidth = 0
	r.cursorHeight = 0
	r.cursorBGRA = nil
	r.gpuCursor = false
	r.outputFrame = 0
}

func (r *d3d11Renderer) configureMediaResources(width, height int) error {
	if r == nil || r.device == nil || r.swapChain == nil || width <= 0 || height <= 0 {
		return ErrUnavailable
	}
	hr := comCall(
		r.swapChain,
		13, // IDXGISwapChain::ResizeBuffers
		0,
		uintptr(width),
		uintptr(height),
		dxgiFormatUnknown,
		0,
	)
	if hresultFailed(hr) {
		return hresultError("IDXGISwapChain.ResizeBuffers", hr)
	}

	upload, err := createUploadTexture(r.device, width, height)
	if err != nil {
		return err
	}
	backBuffer, err := getSwapChainBackBuffer(r.swapChain)
	if err != nil {
		releaseCOM(upload)
		return err
	}

	r.width = width
	r.height = height
	r.upload = upload
	r.backBuffer = backBuffer
	if protectErr := protectD3D11Multithread(r.device); protectErr == nil {
		_ = r.initVideoProcessor()
	}
	return nil
}

func (r *d3d11Renderer) Reconfigure(width, height int) error {
	if r == nil || width <= 0 || height <= 0 {
		return fmt.Errorf("%w: invalid D3D11 viewer geometry", ErrUnavailable)
	}
	if width == r.width && height == r.height {
		return nil
	}
	oldWidth, oldHeight := r.width, r.height
	r.releaseMediaResources()
	if err := r.configureMediaResources(width, height); err != nil {
		r.releaseMediaResources()
		if rollbackErr := r.configureMediaResources(oldWidth, oldHeight); rollbackErr != nil {
			return fmt.Errorf("resize D3D11 media to %dx%d: %w (rollback to %dx%d failed: %v)",
				width, height, err, oldWidth, oldHeight, rollbackErr)
		}
		return fmt.Errorf("resize D3D11 media to %dx%d: %w", width, height, err)
	}
	return nil
}

func (r *d3d11Renderer) DeviceHandle() uintptr {
	if r == nil || r.device == nil || r.videoProcessor == nil {
		return 0
	}
	return uintptr(r.device)
}

func (r *d3d11Renderer) SupportsGPUCursor() bool {
	return r != nil && r.gpuCursor && r.videoProcessor != nil
}

func cursorBitmapBGRA(shape CursorBitmap, dst []byte) ([]byte, error) {
	if shape.Width <= 0 || shape.Height <= 0 || shape.Stride < shape.Width*4 ||
		len(shape.Pix) < shape.Stride*shape.Height {
		return nil, fmt.Errorf("%w: invalid cursor bitmap", ErrUnavailable)
	}
	required := shape.Width * shape.Height * 4
	if cap(dst) < required {
		dst = make([]byte, required)
	} else {
		dst = dst[:required]
	}
	for y := 0; y < shape.Height; y++ {
		srcRow := y * shape.Stride
		dstRow := y * shape.Width * 4
		for x := 0; x < shape.Width; x++ {
			si := srcRow + x*4
			di := dstRow + x*4
			dst[di] = shape.Pix[si+2]
			dst[di+1] = shape.Pix[si+1]
			dst[di+2] = shape.Pix[si]
			dst[di+3] = shape.Pix[si+3]
		}
	}
	return dst, nil
}

func (r *d3d11Renderer) ensureGPUCursor(shape CursorBitmap) error {
	if !r.SupportsGPUCursor() {
		return ErrUnavailable
	}
	if shape.ID != "" && shape.ID == r.cursorID &&
		shape.Width == r.cursorWidth && shape.Height == r.cursorHeight &&
		r.cursorTexture != nil && r.cursorView != nil {
		return nil
	}

	pixels, err := cursorBitmapBGRA(shape, r.cursorBGRA)
	if err != nil {
		return err
	}
	r.cursorBGRA = pixels

	desc := d3d11Texture2DDesc{
		Width:      uint32(shape.Width),
		Height:     uint32(shape.Height),
		MipLevels:  1,
		ArraySize:  1,
		Format:     dxgiFormatB8G8R8A8UNorm,
		SampleDesc: dxgiSampleDesc{Count: 1},
		Usage:      0, // D3D11_USAGE_DEFAULT required by video processor input views.
	}
	data := d3d11SubresourceData{
		SysMem:      unsafe.Pointer(&pixels[0]),
		SysMemPitch: uint32(shape.Width * 4),
	}
	var texture unsafe.Pointer
	hr := comCall(
		r.device,
		5, // ID3D11Device::CreateTexture2D
		uintptr(unsafe.Pointer(&desc)),
		uintptr(unsafe.Pointer(&data)),
		uintptr(unsafe.Pointer(&texture)),
	)
	if hresultFailed(hr) || texture == nil {
		if !hresultFailed(hr) {
			return ErrUnavailable
		}
		return hresultError("ID3D11Device.CreateTexture2D(cursor)", hr)
	}

	inputDesc := d3d11VideoProcessorInputViewDesc{
		ViewDimension: d3d11VPIVDimensionTexture2D,
	}
	var view unsafe.Pointer
	hr = comCall(
		r.videoDevice,
		id3d11VideoDeviceCreateInputView,
		uintptr(texture),
		uintptr(r.videoEnumerator),
		uintptr(unsafe.Pointer(&inputDesc)),
		uintptr(unsafe.Pointer(&view)),
	)
	if hresultFailed(hr) || view == nil {
		releaseCOM(texture)
		if !hresultFailed(hr) {
			return ErrUnavailable
		}
		return hresultError("ID3D11VideoDevice.CreateVideoProcessorInputView(cursor)", hr)
	}

	releaseCOM(r.cursorView)
	releaseCOM(r.cursorTexture)
	r.cursorTexture = texture
	r.cursorView = view
	r.cursorID = shape.ID
	r.cursorWidth = shape.Width
	r.cursorHeight = shape.Height
	return nil
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

func (r *d3d11Renderer) RenderD3D11(frame D3D11Frame, cursor CursorOverlay) error {
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

	streams := [2]d3d11VideoProcessorStream{{
		Enable:       1,
		InputSurface: inputView,
	}}
	streamCount := uintptr(1)

	if r.SupportsGPUCursor() && cursor.State.Visible {
		sourceRect, destRect, ok := cursorRects(r.width, r.height, cursor.State, cursor.Bitmap)
		if ok {
			if err := r.ensureGPUCursor(cursor.Bitmap); err != nil {
				return err
			}
			src := d3d11Rect{
				Left: int32(sourceRect.Min.X), Top: int32(sourceRect.Min.Y),
				Right: int32(sourceRect.Max.X), Bottom: int32(sourceRect.Max.Y),
			}
			dst := d3d11Rect{
				Left: int32(destRect.Min.X), Top: int32(destRect.Min.Y),
				Right: int32(destRect.Max.X), Bottom: int32(destRect.Max.Y),
			}
			comCall(
				r.videoContext,
				id3d11VideoContextSetStreamFrameFormat,
				uintptr(r.videoProcessor),
				1,
				0, // progressive
			)
			comCall(
				r.videoContext,
				id3d11VideoContextSetStreamSourceRect,
				uintptr(r.videoProcessor),
				1,
				1,
				uintptr(unsafe.Pointer(&src)),
			)
			comCall(
				r.videoContext,
				id3d11VideoContextSetStreamDestRect,
				uintptr(r.videoProcessor),
				1,
				1,
				uintptr(unsafe.Pointer(&dst)),
			)
			comCall(
				r.videoContext,
				id3d11VideoContextSetStreamAlpha,
				uintptr(r.videoProcessor),
				1,
				1,
				uintptr(0x3f800000), // float32(1.0)
			)
			streams[1] = d3d11VideoProcessorStream{
				Enable:       1,
				InputSurface: r.cursorView,
			}
			streamCount = 2
		}
	}

	hr = comCall(
		r.videoContext,
		id3d11VideoContextVideoProcessorBlt,
		uintptr(r.videoProcessor),
		uintptr(r.videoOutputView),
		uintptr(r.outputFrame),
		streamCount,
		uintptr(unsafe.Pointer(&streams[0])),
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
	r.releaseMediaResources()
	releaseCOM(r.context)
	releaseCOM(r.device)
	releaseCOM(r.swapChain)
	r.context = nil
	r.device = nil
	r.swapChain = nil
}
