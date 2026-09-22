//go:build !windows

package codec

import "context"

func OpenMFH264Decoder(context.Context, VideoConfig, bool) (Decoder, error) {
	return nil, ErrDecoderUnavailable
}
