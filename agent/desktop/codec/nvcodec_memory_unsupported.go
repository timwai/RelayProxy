//go:build !windows || !amd64

package codec

import (
	"context"
	"fmt"
)

func ProbeNVCodecVideoMemory(context.Context) (NVCodecVideoMemoryInfo, error) {
	return NVCodecVideoMemoryInfo{}, fmt.Errorf(
		"%w: NVIDIA video-memory telemetry is only available on Windows amd64",
		ErrDecoderUnavailable,
	)
}
