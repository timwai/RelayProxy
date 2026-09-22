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

	d3d11SDKVersion              = 7
	d3d11CreateDeviceBGRASupport = 0x20
	d3d11UsageDynamic            = 2
	d3d11CPUAccessWrite          = 0x10000
	d3d11MapWriteDiscard         = 4

	dxgiFormatB8G8R8A8UNorm     = 87
	dxgiUsageRenderTargetOutput = 0x20
	dxgiSwapEffectDiscard       = 0
)

var (
	d3d11DLL                          = windows.NewLazySystemDLL("d3d11.dll")
	procD3D11CreateDeviceAndSwapChain = d3d11DLL.NewProc("D3D11CreateDeviceAndSwapChain")
	iidID3D11Texture2D                = windows.GUID{
		Data1: 0x6f15aaf2, Data2: 0xd208, Data3: 0x4e89,
		Data4: [8]byte{0x9a, 0xb4, 0x48, 0x95, 0x35, 0xd3, 0x4f, 0x9c},
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

type d3d11Renderer struct {
	width      int
	height     int
	device     unsafe.Pointer
	context    unsafe.Pointer
	swapChain  unsafe.Pointer
	upload     unsafe.Pointer
	backBuffer unsafe.Pointer
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

func releaseCOM(object unsafe.Pointer) {
	if object != nil {
		comCall(object, 2)
	}
}

func createD3D11DeviceAndSwapChain(hwnd win.HWND, width, height int, driverType uint32) (unsafe.Pointer, unsafe.Pointer, unsafe.Pointer, error) {
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
		uintptr(d3d11CreateDeviceBGRASupport),
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

func newD3D11Renderer(hwnd win.HWND, width, height int) (*d3d11Renderer, error) {
	if hwnd == 0 || width <= 0 || height <= 0 {
		return nil, fmt.Errorf("%w: invalid D3D11 viewer geometry", ErrUnavailable)
	}
	swapChain, device, context, err := createD3D11DeviceAndSwapChain(hwnd, width, height, d3dDriverTypeHardware)
	if err != nil {
		swapChain, device, context, err = createD3D11DeviceAndSwapChain(hwnd, width, height, d3dDriverTypeWarp)
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
	return r, nil
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
	comCall(
		r.context,
		15, // ID3D11DeviceContext::Unmap
		uintptr(r.upload),
		0,
	)
	comCall(
		r.context,
		47, // ID3D11DeviceContext::CopyResource
		uintptr(r.backBuffer),
		uintptr(r.upload),
	)
	hr = comCall(
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
	releaseCOM(r.backBuffer)
	releaseCOM(r.upload)
	releaseCOM(r.context)
	releaseCOM(r.device)
	releaseCOM(r.swapChain)
	r.backBuffer, r.upload, r.context, r.device, r.swapChain = nil, nil, nil, nil, nil
}
