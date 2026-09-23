//go:build !windows

package codec

import "time"

type D3D11NV12Converter struct{}

func OpenD3D11NV12Converter(uintptr, D3D11ConvertConfig) (*D3D11NV12Converter, error) {
	return nil, ErrEncoderUnavailable
}

func (c *D3D11NV12Converter) Device() uintptr {
	return 0
}

func (c *D3D11NV12Converter) Convert(uintptr, uint32, time.Duration) (D3D11EncodeFrame, error) {
	return D3D11EncodeFrame{}, ErrEncoderUnavailable
}

func (c *D3D11NV12Converter) Close() error {
	return nil
}
