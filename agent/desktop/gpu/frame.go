package gpu

import (
	"fmt"
	"strings"
)

type Backend string

const (
	BackendD3D11 Backend = "d3d11"
)

type Format string

const (
	FormatNV12 Format = "nv12"
	FormatAYUV Format = "ayuv"
	FormatP010 Format = "p010"
	FormatBGRA Format = "bgra"
)

type Frame struct {
	Backend     Backend
	Device      uintptr
	Resource    uintptr
	Subresource uint32
	Width       int
	Height      int
	Format      Format
}

func (f Frame) Validate() error {
	if f.Backend != BackendD3D11 {
		return fmt.Errorf("unsupported GPU backend %q", f.Backend)
	}
	if f.Resource == 0 {
		return fmt.Errorf("GPU frame resource is nil")
	}
	if f.Width <= 0 || f.Height <= 0 {
		return fmt.Errorf("invalid GPU frame dimensions %dx%d", f.Width, f.Height)
	}
	switch f.Format {
	case FormatNV12, FormatAYUV, FormatP010, FormatBGRA:
	default:
		return fmt.Errorf("unsupported GPU frame format %q", f.Format)
	}
	return nil
}

func ParseFormat(value string) (Format, bool) {
	switch Format(strings.ToLower(strings.TrimSpace(value))) {
	case FormatNV12:
		return FormatNV12, true
	case FormatAYUV:
		return FormatAYUV, true
	case FormatP010:
		return FormatP010, true
	case FormatBGRA:
		return FormatBGRA, true
	default:
		return "", false
	}
}
