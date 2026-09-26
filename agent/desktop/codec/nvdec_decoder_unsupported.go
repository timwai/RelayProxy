//go:build !windows || !amd64

package codec

import (
	"context"
	"fmt"
)

func OpenNVDECH265DecoderWithD3D11(
	context.Context,
	VideoConfig,
	uintptr,
) (Decoder, error) {
	return nil, fmt.Errorf("%w: NVDEC HEVC 4:4:4 D3D11 is only available on Windows amd64", ErrDecoderUnavailable)
}
