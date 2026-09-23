package codec

import "context"

var (
	_ func(context.Context, VideoConfig, bool) (Decoder, error)          = OpenMFH264Decoder
	_ func(context.Context, VideoConfig, bool, uintptr) (Decoder, error) = OpenMFH264DecoderWithD3D11
	_ func(context.Context, VideoConfig, bool) (Decoder, error)          = OpenMFH265Decoder
	_ func(context.Context, VideoConfig, bool, uintptr) (Decoder, error) = OpenMFH265DecoderWithD3D11
)
