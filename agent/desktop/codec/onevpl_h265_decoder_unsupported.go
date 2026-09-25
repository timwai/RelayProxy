//go:build !windows || !amd64

package codec

import "context"

func OpenOneVPLH265Decoder(context.Context, VideoConfig) (Decoder, error) {
	return nil, ErrDecoderUnavailable
}

func OpenOneVPLH265DecoderWithD3D11(context.Context, VideoConfig, uintptr) (Decoder, error) {
	return nil, ErrDecoderUnavailable
}

func ProbeOneVPLH265DecoderD3D11(context.Context, VideoConfig, uintptr) error {
	return ErrDecoderUnavailable
}
