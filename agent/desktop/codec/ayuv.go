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


// AYUVToI444 unpacks DXGI AYUV system-memory rows (V,U,Y,A) into tightly
// packed planar 8-bit I444. The alpha byte is intentionally ignored.
func AYUVToI444(src []byte, width, height, stride int, dst []byte) ([]byte, error) {
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("%w: AYUV decode requires positive dimensions", ErrInvalidFrame)
	}
	if stride < width*4 || len(src) < stride*height {
		return nil, fmt.Errorf("%w: AYUV buffer is too small", ErrInvalidFrame)
	}
	planeBytes := width * height
	required := planeBytes * 3
	if cap(dst) < required {
		dst = make([]byte, required)
	} else {
		dst = dst[:required]
	}
	yPlane := dst[:planeBytes]
	uPlane := dst[planeBytes : planeBytes*2]
	vPlane := dst[planeBytes*2:]
	for y := 0; y < height; y++ {
		srcRow := y * stride
		dstRow := y * width
		for x := 0; x < width; x++ {
			srcIndex := srcRow + x*4
			dstIndex := dstRow + x
			vPlane[dstIndex] = src[srcIndex]
			uPlane[dstIndex] = src[srcIndex+1]
			yPlane[dstIndex] = src[srcIndex+2]
		}
	}
	return dst, nil
}
