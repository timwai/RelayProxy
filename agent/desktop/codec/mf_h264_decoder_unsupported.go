//go:build !windows

package codec

import "context"

func OpenMFH264Decoder(context.Context, VideoConfig, bool) (Decoder, error) {
	return nil, ErrDecoderUnavailable
}

func OpenMFH264DecoderWithD3D11(context.Context, VideoConfig, bool, uintptr) (Decoder, error) {
	return nil, ErrDecoderUnavailable
}
