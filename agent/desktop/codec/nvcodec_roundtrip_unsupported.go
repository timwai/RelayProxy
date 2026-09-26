//go:build !windows || !amd64

package codec

import (
	"context"
	"fmt"
)

func ValidateNVCodecH265444RoundTrip(
	context.Context,
) (NVCodecH265444RoundTripReport, error) {
	report := NVCodecH265444RoundTripReport{
		Backend: H265444BackendNVCodec,
	}
	err := fmt.Errorf(
		"%w: NVIDIA NVENC/NVDEC round-trip validation is only available on Windows amd64",
		ErrDecoderUnavailable,
	)
	report.Error = err.Error()
	return report, err
}


func ProbeNVCodecValidationIdentity(context.Context) (NVCodecValidationIdentity, error) {
	return NVCodecValidationIdentity{}, fmt.Errorf(
		"%w: NVIDIA validation identity is only available on Windows amd64",
		ErrDecoderUnavailable,
	)
}
