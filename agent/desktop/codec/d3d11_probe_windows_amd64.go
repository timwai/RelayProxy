//go:build windows && amd64

package codec

import (
	"context"
	"errors"
	"fmt"
	"unsafe"
)

func createD3D11AYUVProbeDevice() (unsafe.Pointer, unsafe.Pointer, error) {
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
		return nil, nil, hresultError("D3D11CreateDevice(AYUV probe)", hr)
	}
	if device == nil || context == nil {
		releaseIUnknown(context)
		releaseIUnknown(device)
		return nil, nil, errors.New("D3D11 AYUV probe returned incomplete device objects")
	}
	return device, context, nil
}

func ProbeOneVPLD3D11AYUV(ctx context.Context, oneVPL OneVPLProbe) D3D11AYUVProbe {
	probe := D3D11AYUVProbe{}
	if err := ctx.Err(); err != nil {
		probe.Error = err.Error()
		return probe
	}
	if !oneVPL.HEVC444D3D11EndToEnd() {
		return probe
	}

	device, immediateContext, err := createD3D11AYUVProbeDevice()
	if err != nil {
		probe.Error = err.Error()
		return probe
	}
	defer releaseIUnknown(immediateContext)
	defer releaseIUnknown(device)
	probe.DeviceAvailable = true

	cfg := D3D11ConvertConfig{
		InputWidth:   640,
		InputHeight:  480,
		OutputWidth:  640,
		OutputHeight: 480,
		FPS:          30,
	}
	converter, err := OpenD3D11AYUVConverter(uintptr(device), cfg)
	if err != nil {
		probe.Error = err.Error()
		return probe
	}
	defer converter.Close()
	probe.BGRAInput = true
	probe.AYUVOutput = true

	if err := converter.requireFormat(dxgiFormatAYUV, d3d11VPFormatInput, "AYUV input"); err != nil {
		probe.Error = err.Error()
		return probe
	}
	probe.AYUVInput = true
	if err := converter.requireFormat(dxgiFormatB8G8R8A8UNorm, d3d11VPFormatOutput, "BGRA output"); err != nil {
		probe.Error = err.Error()
		return probe
	}
	probe.BGRAOutput = true

	videoCfg := DefaultVideoConfig()
	videoCfg.Width = 640
	videoCfg.Height = 480
	videoCfg.FPS = 30
	videoCfg.Chroma = Chroma444
	videoCfg.BitDepth = 8

	encoder, err := OpenOneVPLH265EncoderWithD3D11(ctx, videoCfg, uintptr(device))
	if err != nil {
		probe.Error = fmt.Sprintf("open oneVPL D3D11 encoder: %v", err)
		return probe
	}
	if err := encoder.Close(); err != nil {
		probe.Error = fmt.Sprintf("close oneVPL D3D11 encoder: %v", err)
		return probe
	}
	probe.OneVPLEncode = true

	decoder, err := OpenOneVPLH265DecoderWithD3D11(ctx, videoCfg, uintptr(device))
	if err != nil {
		probe.Error = fmt.Sprintf("open oneVPL D3D11 decoder: %v", err)
		return probe
	}
	if err := decoder.Close(); err != nil {
		probe.Error = fmt.Sprintf("close oneVPL D3D11 decoder: %v", err)
		return probe
	}
	probe.OneVPLDecode = true
	return probe
}
