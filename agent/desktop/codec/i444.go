package codec

import (
	"fmt"
	"image"
)

// RGBAtoI444 converts an opaque RGB desktop frame to planar 8-bit I444 using
// the same limited-range BT.709 coefficients as the existing NV12 path. I444
// keeps one U and V sample for every luma sample, preserving colored text and
// UI edges that are softened by 4:2:0 chroma subsampling.
func RGBAtoI444(src *image.RGBA, dst []byte) ([]byte, error) {
	if src == nil {
		return nil, fmt.Errorf("%w: nil RGBA image", ErrInvalidFrame)
	}
	bounds := src.Bounds()
	return packedRGBAToI444(
		src.Pix,
		bounds.Dx(),
		bounds.Dy(),
		src.Stride,
		false,
		dst,
	)
}

// BGRAtoI444 converts a top-down BGRA desktop frame directly to tightly packed
// planar I444 while preserving the source RowPitch/stride.
func BGRAtoI444(pix []byte, width, height, stride int, dst []byte) ([]byte, error) {
	return packedRGBAToI444(pix, width, height, stride, true, dst)
}

func packedRGBAToI444(
	pix []byte,
	width, height, stride int,
	bgra bool,
	dst []byte,
) ([]byte, error) {
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("%w: I444 conversion requires positive dimensions", ErrInvalidFrame)
	}
	if stride < width*4 || len(pix) < stride*height {
		return nil, fmt.Errorf("%w: packed RGB buffer is too small", ErrInvalidFrame)
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
			offset := srcRow + x*4
			var r, g, b int
			if bgra {
				b = int(pix[offset])
				g = int(pix[offset+1])
				r = int(pix[offset+2])
			} else {
				r = int(pix[offset])
				g = int(pix[offset+1])
				b = int(pix[offset+2])
			}
			index := dstRow + x
			yPlane[index] = clamp8(((47*r + 157*g + 16*b + 128) >> 8) + 16)
			uPlane[index] = clamp8(((-26*r - 87*g + 113*b + 128) >> 8) + 128)
			vPlane[index] = clamp8(((113*r - 102*g - 11*b + 128) >> 8) + 128)
		}
	}
	return dst, nil
}

// I444ToBGRA converts planar limited-range BT.709 I444 into tightly packed
// BGRA. stride is the byte stride of each individual Y/U/V plane.
func I444ToBGRA(src []byte, width, height, stride int, dst []byte) ([]byte, error) {
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("%w: I444 decode requires positive dimensions", ErrInvalidFrame)
	}
	if stride < width {
		return nil, fmt.Errorf("%w: I444 stride is smaller than width", ErrInvalidFrame)
	}
	planeBytes := stride * height
	if len(src) < planeBytes*3 {
		return nil, fmt.Errorf("%w: I444 buffer is too small", ErrInvalidFrame)
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
			index := srcRow + x
			yy := int(yPlane[index]) - 16
			if yy < 0 {
				yy = 0
			}
			u := int(uPlane[index]) - 128
			v := int(vPlane[index]) - 128
			r := (298*yy + 459*v + 128) >> 8
			g := (298*yy - 55*u - 136*v + 128) >> 8
			b := (298*yy + 541*u + 128) >> 8
			offset := dstRow + x*4
			dst[offset] = clamp8(b)
			dst[offset+1] = clamp8(g)
			dst[offset+2] = clamp8(r)
			dst[offset+3] = 0xff
		}
	}
	return dst, nil
}
