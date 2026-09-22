//go:build !windows

package codec

import (
	"context"
)

type MFH264TransformInfo struct {
	Hardware bool
	Async    bool
	Config   VideoConfig
}

type MFH264Transform struct{}

func OpenMFH264Transform(context.Context, VideoConfig, bool) (*MFH264Transform, error) {
	return nil, ErrEncoderUnavailable
}

func (s *MFH264Transform) Info() MFH264TransformInfo {
	return MFH264TransformInfo{}
}

func (s *MFH264Transform) Close() error {
	return nil
}
