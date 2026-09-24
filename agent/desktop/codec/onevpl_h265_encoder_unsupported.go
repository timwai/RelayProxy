//go:build !windows || !amd64

package codec

import "context"

func OpenOneVPLH265Encoder(context.Context, VideoConfig) (SequenceHeaderEncoder, error) {
	return nil, ErrEncoderUnavailable
}
