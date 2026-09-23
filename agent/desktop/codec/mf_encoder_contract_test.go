package codec

var (
	_ Encoder      = (*MFH264Encoder)(nil)
	_ D3D11Encoder = (*MFH264Encoder)(nil)
	_ Encoder      = (*MFH265Encoder)(nil)
	_ D3D11Encoder = (*MFH265Encoder)(nil)
)
