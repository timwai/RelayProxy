package codec

import "fmt"

// I444ToAYUV packs planar 8-bit I444 into the Win32/DXGI AYUV byte layout.
// DXGI_FORMAT_AYUV maps V->R8, U->G8, Y->B8, A->A8, therefore system memory
// contains V, U, Y, A for every pixel.
func I444ToAYUV(src []byte, width, height, stride int, dst []byte) ([]byte, error) {
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("%w: AYUV conversion requires positive dimensions", ErrInvalidFrame)
	}
	if stride < width {
		return nil, fmt.Errorf("%w: I444 stride is smaller than width", ErrInvalidFrame)
	}
	planeBytes := stride * height
	if len(src) < planeBytes*3 {
		return nil, fmt.Errorf("%w: I444 buffer is too small for AYUV conversion", ErrInvalidFrame)
	}
	required := width * height * 4
	if cap(dst) < required {
		dst = make([]byte, required)
	} else {
		dst = dst[:required]
	}

	yPlane := src[:planeBytes]
	uPlane := src[planeBytes : planeBytes*2]
	vPlane := src[planeBytes*2 : planeBytes*3]
	for y := 0; y < height; y++ {
		srcRow := y * stride
		dstRow := y * width * 4
		for x := 0; x < width; x++ {
			srcIndex := srcRow + x
			dstIndex := dstRow + x*4
			dst[dstIndex] = vPlane[srcIndex]
			dst[dstIndex+1] = uPlane[srcIndex]
			dst[dstIndex+2] = yPlane[srcIndex]
			dst[dstIndex+3] = 0xff
		}
	}
	return dst, nil
}
