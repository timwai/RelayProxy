//go:build !windows || !amd64

package codec

import "context"

func ProbeOneVPLHEVC444(ctx context.Context) OneVPLProbe {
	probe := OneVPLProbe{}
	if err := ctx.Err(); err != nil {
		probe.Error = err.Error()
		return probe
	}
	probe.Error = "oneVPL HEVC 4:4:4 probing is currently implemented only on Windows amd64"
	return probe
}
