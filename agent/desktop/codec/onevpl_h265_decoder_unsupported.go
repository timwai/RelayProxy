//go:build !windows || !amd64

package codec

import "context"

func OpenOneVPLH265Decoder(context.Context, VideoConfig) (Decoder, error) {
	return nil, ErrDecoderUnavailable
}
