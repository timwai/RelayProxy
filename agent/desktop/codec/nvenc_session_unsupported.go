//go:build !windows || !amd64

package codec

import (
	"context"
	"fmt"
)

type nvencD3D11Session struct{}

func openNVENCHEVC444D3D11Session(context.Context, uintptr) (*nvencD3D11Session, error) {
	return nil, fmt.Errorf("%w: NVENC D3D11 is only available on Windows amd64", ErrEncoderUnavailable)
}

func (s *nvencD3D11Session) Close() error {
	return nil
}
