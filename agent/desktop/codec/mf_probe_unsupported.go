//go:build !windows

package codec

import "context"

func ProbeH264MediaFoundation(context.Context) H264Probe {
	return H264Probe{Error: "Media Foundation is only available on Windows"}
}
