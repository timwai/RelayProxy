//go:build !windows

package codec

import "context"

type MFH264Encoder struct{}

func OpenMFH264Encoder(context.Context, VideoConfig, bool) (*MFH264Encoder, error) {
	return nil, ErrEncoderUnavailable
}

func OpenMFH264EncoderWithD3D11(context.Context, VideoConfig, bool, uintptr) (*MFH264Encoder, error) {
	return nil, ErrEncoderUnavailable
}

func (e *MFH264Encoder) Encode(context.Context, RawFrame) ([]EncodedPacket, error) {
	return nil, ErrEncoderUnavailable
}

func (e *MFH264Encoder) EncodeD3D11(context.Context, D3D11EncodeFrame) ([]EncodedPacket, error) {
	return nil, ErrEncoderUnavailable
}

func (e *MFH264Encoder) ForceIDR(context.Context) error {
	return ErrEncoderUnavailable
}

func (e *MFH264Encoder) Reconfigure(context.Context, VideoConfig) error {
	return ErrEncoderUnavailable
}

func (e *MFH264Encoder) SequenceHeader() []byte {
	return nil
}

func (e *MFH264Encoder) Stats() EncoderStats {
	return EncoderStats{}
}

func (e *MFH264Encoder) Close() error {
	return nil
}
