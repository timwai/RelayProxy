//go:build !windows

package codec

import "context"

type MFH265Encoder struct{}

func OpenMFH265Encoder(context.Context, VideoConfig, bool) (*MFH265Encoder, error) {
	return nil, ErrEncoderUnavailable
}

func OpenMFH265EncoderWithD3D11(context.Context, VideoConfig, bool, uintptr) (*MFH265Encoder, error) {
	return nil, ErrEncoderUnavailable
}

func (e *MFH265Encoder) Encode(context.Context, RawFrame) ([]EncodedPacket, error) {
	return nil, ErrEncoderUnavailable
}

func (e *MFH265Encoder) EncodeD3D11(context.Context, D3D11EncodeFrame) ([]EncodedPacket, error) {
	return nil, ErrEncoderUnavailable
}

func (e *MFH265Encoder) ForceIDR(context.Context) error {
	return ErrEncoderUnavailable
}

func (e *MFH265Encoder) Reconfigure(context.Context, VideoConfig) error {
	return ErrEncoderUnavailable
}

func (e *MFH265Encoder) SequenceHeader() []byte {
	return nil
}

func (e *MFH265Encoder) Stats() EncoderStats {
	return EncoderStats{}
}

func (e *MFH265Encoder) Close() error {
	return nil
}
