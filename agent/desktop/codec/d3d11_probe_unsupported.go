//go:build !windows || !amd64

package codec

import "context"

func ProbeOneVPLD3D11AYUV(context.Context, OneVPLProbe) D3D11AYUVProbe {
	return D3D11AYUVProbe{}
}
