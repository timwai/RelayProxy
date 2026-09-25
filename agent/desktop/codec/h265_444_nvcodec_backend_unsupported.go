//go:build !windows || !amd64

package codec

func platformNVCodecH265444Backend() h265444Backend {
	return h265444Backend{
		name:              H265444BackendNVCodec,
		productionReady:   false,
		zeroCopyValidated: false,
		lifecycle:          h265444SessionLifecycleContract(),
		interop:            h265444D3D11CUDAAYUVContract(),
	}
}
