//go:build windows && amd64

package codec

import (
	"context"
	"fmt"
)

func ProbeOneVPLD3D11AYUV(ctx context.Context, oneVPL OneVPLProbe) D3D11AYUVProbe {
	probe := D3D11AYUVProbe{}
	if err := ctx.Err(); err != nil {
		probe.Error = err.Error()
		return probe
	}
	if !oneVPL.HEVC444D3D11EndToEnd() {
		return probe
	}

	graphics, err := createMFDecoderD3D11()
	if err != nil {
		probe.Error = err.Error()
		return probe
	}
	defer graphics.Close()
	probe.DeviceAvailable = true

	cfg := D3D11ConvertConfig{
		InputWidth:   640,
		InputHeight:  480,
		OutputWidth:  640,
		OutputHeight: 480,
		FPS:          30,
	}
	converter, err := OpenD3D11AYUVConverter(uintptr(graphics.device), cfg)
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

	encoder, err := OpenOneVPLH265EncoderWithD3D11(ctx, videoCfg, uintptr(graphics.device))
	if err != nil {
		probe.Error = fmt.Sprintf("open oneVPL D3D11 encoder: %v", err)
		return probe
	}
	if err := encoder.Close(); err != nil {
		probe.Error = fmt.Sprintf("close oneVPL D3D11 encoder: %v", err)
		return probe
	}
	probe.OneVPLEncode = true

	decoder, err := OpenOneVPLH265DecoderWithD3D11(ctx, videoCfg, uintptr(graphics.device))
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
