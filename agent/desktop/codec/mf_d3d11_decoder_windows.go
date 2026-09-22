//go:build windows

package codec

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	d3dDriverTypeHardware = 1
	d3d11SDKVersion       = 7

	d3d11CreateDeviceBGRASupport  = 0x20
	d3d11CreateDeviceVideoSupport = 0x800

	d3d11UsageStaging  = 3
	d3d11CPUAccessRead = 0x20000
	d3d11MapRead       = 1
	dxgiFormatNV12     = 103

	imfSampleGetBufferByIndex        = 40
	imfDXGIBufferGetResource         = 3
	imfDXGIBufferGetSubresourceIndex = 4
	imfDXGIDeviceManagerResetDevice  = 7

	id3d10MultithreadSetMultithreadProtected = 5
)

var (
	mfD3D11DLL                    = windows.NewLazySystemDLL("d3d11.dll")
	procD3D11CreateDevice         = mfD3D11DLL.NewProc("D3D11CreateDevice")
	procMFCreateDXGIDeviceManager = mfplatDLL.NewProc("MFCreateDXGIDeviceManager")

	mfSAD3D11Aware = windows.GUID{
		Data1: 0x206b4fc8, Data2: 0xfcf9, Data3: 0x4c51,
		Data4: [8]byte{0xaf, 0xe3, 0x97, 0x64, 0x36, 0x9e, 0x33, 0xa0},
	}
	iidID3D10Multithread = windows.GUID{
		Data1: 0x9b7e4e00, Data2: 0x342c, Data3: 0x4106,
		Data4: [8]byte{0xa1, 0x9f, 0x4f, 0x27, 0x04, 0xf6, 0x89, 0xf0},
	}
	iidIMFDXGIBuffer = windows.GUID{
		Data1: 0xe7174cfa, Data2: 0x1c9e, Data3: 0x48b1,
		Data4: [8]byte{0x88, 0x66, 0x62, 0x62, 0x26, 0xbf, 0xc2, 0x58},
	}
	iidID3D11Texture2D = windows.GUID{
		Data1: 0x6f15aaf2, Data2: 0xd208, Data3: 0x4e89,
		Data4: [8]byte{0x9a, 0xb4, 0x48, 0x95, 0x35, 0xd3, 0x4f, 0x9c},
	}
)

type mfD3D11SampleDesc struct {
	Count   uint32
	Quality uint32
}

type mfD3D11Texture2DDesc struct {
	Width          uint32
	Height         uint32
	MipLevels      uint32
	ArraySize      uint32
	Format         uint32
	SampleDesc     mfD3D11SampleDesc
	Usage          uint32
	BindFlags      uint32
	CPUAccessFlags uint32
	MiscFlags      uint32
}

type mfD3D11MappedSubresource struct {
	Data       unsafe.Pointer
	RowPitch   uint32
	DepthPitch uint32
}

type mfDecoderD3D11 struct {
	device  unsafe.Pointer
	context unsafe.Pointer
	manager unsafe.Pointer
	token   uint32
	shared  bool

	staging       unsafe.Pointer
	stagingWidth  int
	stagingHeight int
}

func finishMFDecoderD3D11(device, context unsafe.Pointer) (*mfDecoderD3D11, error) {
	if device == nil || context == nil {
		releaseIUnknown(context)
		releaseIUnknown(device)
		return nil, errors.New("D3D11 video device creation returned incomplete objects")
	}
	graphics := &mfDecoderD3D11{device: device, context: context}
	fail := func(err error) (*mfDecoderD3D11, error) {
		graphics.Close()
		return nil, err
	}

	multithread, err := comQueryInterface(device, &iidID3D10Multithread)
	if err == nil {
		protected := comCall(multithread, id3d10MultithreadSetMultithreadProtected, 1)
		releaseIUnknown(multithread)
		if protected == 0 {
			return fail(errors.New("ID3D10Multithread.SetMultithreadProtected returned FALSE"))
		}
	}

	hr, _, _ := procMFCreateDXGIDeviceManager.Call(
		uintptr(unsafe.Pointer(&graphics.token)),
		uintptr(unsafe.Pointer(&graphics.manager)),
	)
	if hresultFailed(hr) {
		return fail(hresultError("MFCreateDXGIDeviceManager", hr))
	}
	if graphics.manager == nil {
		return fail(errors.New("MFCreateDXGIDeviceManager returned nil"))
	}
	hr = comCall(
		graphics.manager,
		imfDXGIDeviceManagerResetDevice,
		uintptr(graphics.device),
		uintptr(graphics.token),
	)
	if hresultFailed(hr) {
		return fail(hresultError("IMFDXGIDeviceManager.ResetDevice", hr))
	}
	return graphics, nil
}

func createMFDecoderD3D11() (*mfDecoderD3D11, error) {
	featureLevels := [...]uint32{
		0xb000, // D3D_FEATURE_LEVEL_11_0
		0xa100, // D3D_FEATURE_LEVEL_10_1
		0xa000, // D3D_FEATURE_LEVEL_10_0
	}
	var device, context unsafe.Pointer
	var selectedLevel uint32
	hr, _, _ := procD3D11CreateDevice.Call(
		0,
		d3dDriverTypeHardware,
		0,
		d3d11CreateDeviceBGRASupport|d3d11CreateDeviceVideoSupport,
		uintptr(unsafe.Pointer(&featureLevels[0])),
		uintptr(len(featureLevels)),
		d3d11SDKVersion,
		uintptr(unsafe.Pointer(&device)),
		uintptr(unsafe.Pointer(&selectedLevel)),
		uintptr(unsafe.Pointer(&context)),
	)
	if hresultFailed(hr) {
		return nil, hresultError("D3D11CreateDevice(video)", hr)
	}
	return finishMFDecoderD3D11(device, context)
}

func createMFDecoderD3D11FromDevice(deviceHandle uintptr) (*mfDecoderD3D11, error) {
	if deviceHandle == 0 {
		return createMFDecoderD3D11()
	}
	device := unsafe.Pointer(deviceHandle)
	comCall(device, 1) // IUnknown::AddRef; graphics owns this reference.

	var context unsafe.Pointer
	comCall(
		device,
		40, // ID3D11Device::GetImmediateContext
		uintptr(unsafe.Pointer(&context)),
	)
	if context == nil {
		releaseIUnknown(device)
		return nil, errors.New("external D3D11 device returned nil immediate context")
	}
	graphics, err := finishMFDecoderD3D11(device, context)
	if err != nil {
		return nil, err
	}
	graphics.shared = true
	return graphics, nil
}

func (g *mfDecoderD3D11) Attach(transform unsafe.Pointer) (bool, error) {
	if g == nil || g.manager == nil {
		return false, nil
	}
	attributes, err := transformAttributes(transform)
	if err != nil {
		return false, nil
	}
	defer releaseIUnknown(attributes)

	aware, err := attributeGetUINT32(attributes, &mfSAD3D11Aware)
	if err != nil || aware == 0 {
		return false, nil
	}
	if err := processTransformMessageParam(
		transform,
		mftMessageSetD3DManager,
		uintptr(g.manager),
	); err != nil {
		return false, err
	}
	return true, nil
}

func (g *mfDecoderD3D11) ensureStaging(width, height int) error {
	if g == nil || g.device == nil || width <= 0 || height <= 0 {
		return errors.New("D3D11 decoder staging texture is unavailable")
	}
	if g.staging != nil && g.stagingWidth == width && g.stagingHeight == height {
		return nil
	}
	releaseIUnknown(g.staging)
	g.staging = nil

	desc := mfD3D11Texture2DDesc{
		Width:          uint32(width),
		Height:         uint32(height),
		MipLevels:      1,
		ArraySize:      1,
		Format:         dxgiFormatNV12,
		SampleDesc:     mfD3D11SampleDesc{Count: 1},
		Usage:          d3d11UsageStaging,
		CPUAccessFlags: d3d11CPUAccessRead,
	}
	var texture unsafe.Pointer
	hr := comCall(
		g.device,
		5, // ID3D11Device::CreateTexture2D
		uintptr(unsafe.Pointer(&desc)),
		0,
		uintptr(unsafe.Pointer(&texture)),
	)
	if hresultFailed(hr) {
		return hresultError("ID3D11Device.CreateTexture2D(NV12 staging)", hr)
	}
	if texture == nil {
		return errors.New("D3D11 staging texture creation returned nil")
	}
	g.staging = texture
	g.stagingWidth = width
	g.stagingHeight = height
	return nil
}

func sampleBufferByIndex(sample unsafe.Pointer, index uint32) (unsafe.Pointer, error) {
	var buffer unsafe.Pointer
	hr := comCall(
		sample,
		imfSampleGetBufferByIndex,
		uintptr(index),
		uintptr(unsafe.Pointer(&buffer)),
	)
	if hresultFailed(hr) {
		return nil, hresultError("IMFSample.GetBufferByIndex", hr)
	}
	if buffer == nil {
		return nil, errors.New("IMFSample.GetBufferByIndex returned nil")
	}
	return buffer, nil
}

func decoderSampleSurface(sample unsafe.Pointer, graphics *mfDecoderD3D11, width, height int) (*D3D11Surface, error) {
	buffer, err := sampleBufferByIndex(sample, 0)
	if err != nil {
		return nil, err
	}
	defer releaseIUnknown(buffer)

	dxgiBuffer, err := comQueryInterface(buffer, &iidIMFDXGIBuffer)
	if err != nil {
		return nil, err
	}
	defer releaseIUnknown(dxgiBuffer)

	var source unsafe.Pointer
	hr := comCall(
		dxgiBuffer,
		imfDXGIBufferGetResource,
		uintptr(unsafe.Pointer(&iidID3D11Texture2D)),
		uintptr(unsafe.Pointer(&source)),
	)
	if hresultFailed(hr) {
		return nil, hresultError("IMFDXGIBuffer.GetResource(ID3D11Texture2D)", hr)
	}
	if source == nil {
		return nil, errors.New("IMFDXGIBuffer returned nil ID3D11Texture2D")
	}

	var subresource uint32
	hr = comCall(
		dxgiBuffer,
		imfDXGIBufferGetSubresourceIndex,
		uintptr(unsafe.Pointer(&subresource)),
	)
	if hresultFailed(hr) {
		releaseIUnknown(source)
		return nil, hresultError("IMFDXGIBuffer.GetSubresourceIndex", hr)
	}
	return &D3D11Surface{
		Resource:    uintptr(source),
		Subresource: subresource,
		release: func() {
			releaseIUnknown(source)
		},
		readback: func() ([]byte, error) {
			if graphics == nil {
				return nil, ErrDecoderUnavailable
			}
			return graphics.readNV12Resource(source, subresource, width, height)
		},
	}, nil
}

func (g *mfDecoderD3D11) readNV12Resource(source unsafe.Pointer, subresource uint32, width, height int) ([]byte, error) {
	if g == nil || g.context == nil || source == nil {
		return nil, errors.New("D3D11 decoder context is unavailable")
	}
	if err := g.ensureStaging(width, height); err != nil {
		return nil, err
	}

	comCall(
		g.context,
		46, // ID3D11DeviceContext::CopySubresourceRegion
		uintptr(g.staging),
		0,
		0,
		0,
		0,
		uintptr(source),
		uintptr(subresource),
		0,
	)

	var mapped mfD3D11MappedSubresource
	hr := comCall(
		g.context,
		14, // ID3D11DeviceContext::Map
		uintptr(g.staging),
		0,
		d3d11MapRead,
		0,
		uintptr(unsafe.Pointer(&mapped)),
	)
	if hresultFailed(hr) {
		return nil, hresultError("ID3D11DeviceContext.Map(NV12 staging)", hr)
	}
	if mapped.Data == nil || int(mapped.RowPitch) < width {
		comCall(g.context, 15, uintptr(g.staging), 0)
		return nil, errors.New("D3D11 NV12 staging map returned invalid row pitch")
	}

	rows := height + height/2
	mappedBytes := unsafe.Slice((*byte)(mapped.Data), int(mapped.RowPitch)*rows)
	out := make([]byte, width*rows)
	for row := 0; row < rows; row++ {
		copy(
			out[row*width:(row+1)*width],
			mappedBytes[row*int(mapped.RowPitch):row*int(mapped.RowPitch)+width],
		)
	}
	comCall(g.context, 15, uintptr(g.staging), 0)
	return out, nil
}

func (g *mfDecoderD3D11) readNV12Sample(sample unsafe.Pointer, width, height int) ([]byte, error) {
	if g == nil || g.context == nil {
		return nil, errors.New("D3D11 decoder context is unavailable")
	}
	buffer, err := sampleBufferByIndex(sample, 0)
	if err != nil {
		return nil, err
	}
	defer releaseIUnknown(buffer)

	dxgiBuffer, err := comQueryInterface(buffer, &iidIMFDXGIBuffer)
	if err != nil {
		return nil, err
	}
	defer releaseIUnknown(dxgiBuffer)

	var source unsafe.Pointer
	hr := comCall(
		dxgiBuffer,
		imfDXGIBufferGetResource,
		uintptr(unsafe.Pointer(&iidID3D11Texture2D)),
		uintptr(unsafe.Pointer(&source)),
	)
	if hresultFailed(hr) {
		return nil, hresultError("IMFDXGIBuffer.GetResource(ID3D11Texture2D)", hr)
	}
	if source == nil {
		return nil, errors.New("IMFDXGIBuffer returned nil ID3D11Texture2D")
	}
	defer releaseIUnknown(source)

	var subresource uint32
	hr = comCall(
		dxgiBuffer,
		imfDXGIBufferGetSubresourceIndex,
		uintptr(unsafe.Pointer(&subresource)),
	)
	if hresultFailed(hr) {
		return nil, hresultError("IMFDXGIBuffer.GetSubresourceIndex", hr)
	}
	return g.readNV12Resource(source, subresource, width, height)
}

func (g *mfDecoderD3D11) Close() {
	if g == nil {
		return
	}
	releaseIUnknown(g.staging)
	releaseIUnknown(g.manager)
	releaseIUnknown(g.context)
	releaseIUnknown(g.device)
	g.staging = nil
	g.manager = nil
	g.context = nil
	g.device = nil
}

func decoderSampleBytes(sample unsafe.Pointer, info MFH264DecoderInfo, graphics *mfDecoderD3D11) ([]byte, error) {
	if graphics != nil && info.D3D11Aware {
		if data, err := graphics.readNV12Sample(sample, info.Config.Width, info.Config.Height); err == nil {
			return data, nil
		}
	}
	data, err := sampleBytes(sample)
	if err != nil {
		if graphics != nil && info.D3D11Aware {
			return nil, fmt.Errorf("read D3D11/system-memory decoder sample: %w", err)
		}
		return nil, err
	}
	return data, nil
}
