package codec

import "relayproxy/internal/protocol"

type D3D11AYUVProbe struct {
	DeviceAvailable bool
	BGRAInput       bool
	AYUVOutput      bool
	AYUVInput       bool
	BGRAOutput      bool
	OneVPLEncode    bool
	OneVPLDecode    bool
	Error           string
}

func (p D3D11AYUVProbe) EndToEnd() bool {
	return p.DeviceAvailable &&
		p.BGRAInput &&
		p.AYUVOutput &&
		p.AYUVInput &&
		p.BGRAOutput &&
		p.OneVPLEncode &&
		p.OneVPLDecode
}

func D3D11AYUVGPUCapability(oneVPL OneVPLProbe, gpu D3D11AYUVProbe) *protocol.DesktopGPUCapability {
	if !oneVPL.HEVC444D3D11EndToEnd() || !gpu.EndToEnd() {
		return nil
	}
	return &protocol.DesktopGPUCapability{
		Backend:         "d3d11",
		EncodeZeroCopy:  true,
		DecodeZeroCopy:  true,
		DisplayZeroCopy: true,
		Formats:         []string{"ayuv"},
	}
}
