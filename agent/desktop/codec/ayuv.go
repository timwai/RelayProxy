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


// AYUVToBGRA converts tightly packed DXGI AYUV (V,U,Y,A in memory) to
// tightly packed BGRA using limited-range BT.709 coefficients.
func AYUVToBGRA(src []byte, width, height, stride int, dst []byte) ([]byte, error) {
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("%w: AYUV decode requires positive dimensions", ErrInvalidFrame)
	}
	if stride < width*4 || len(src) < stride*height {
		return nil, fmt.Errorf("%w: AYUV buffer is too small", ErrInvalidFrame)
	}
	required := width * height * 4
	if cap(dst) < required {
		dst = make([]byte, required)
	} else {
		dst = dst[:required]
	}
	for y := 0; y < height; y++ {
		srcRow := y * stride
		dstRow := y * width * 4
		for x := 0; x < width; x++ {
			s := srcRow + x*4
			v := int(src[s]) - 128
			u := int(src[s+1]) - 128
			yy := int(src[s+2]) - 16
			if yy < 0 {
				yy = 0
			}
			r := (298*yy + 459*v + 128) >> 8
			g := (298*yy - 55*u - 136*v + 128) >> 8
			b := (298*yy + 541*u + 128) >> 8
			d := dstRow + x*4
			dst[d] = clamp8(b)
			dst[d+1] = clamp8(g)
			dst[d+2] = clamp8(r)
			dst[d+3] = 0xff
		}
	}
	return dst, nil
}
