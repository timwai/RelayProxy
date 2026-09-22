package codec

import (
	"fmt"
	"image"
)

func clamp8(value int) byte {
	if value < 0 {
		return 0
	}
	if value > 255 {
		return 255
	}
	return byte(value)
}

// RGBAtoNV12 converts an opaque RGB desktop frame to NV12 using limited-range
// BT.709 coefficients. The conversion is intentionally CPU based for the
// first H.264 bring-up path; the later D3D11 video-processor stage can replace
// it without changing the Encoder contract.
func RGBAtoNV12(src *image.RGBA, dst []byte) ([]byte, error) {
	if src == nil {
		return nil, fmt.Errorf("%w: nil RGBA image", ErrInvalidFrame)
	}
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= 0 || height <= 0 || width%2 != 0 || height%2 != 0 {
		return nil, fmt.Errorf("%w: NV12 conversion requires positive even dimensions", ErrInvalidFrame)
	}
	required := width * height * 3 / 2
	if cap(dst) < required {
		dst = make([]byte, required)
	} else {
		dst = dst[:required]
	}
	yPlane := dst[:width*height]
	uvPlane := dst[width*height:]

	rgb := func(x, y int) (int, int, int) {
		offset := src.PixOffset(bounds.Min.X+x, bounds.Min.Y+y)
		return int(src.Pix[offset]), int(src.Pix[offset+1]), int(src.Pix[offset+2])
	}
	luma := func(r, g, b int) byte {
		return clamp8(((47*r + 157*g + 16*b + 128) >> 8) + 16)
	}
	chroma := func(r, g, b int) (int, int) {
		u := ((-26*r - 87*g + 113*b + 128) >> 8) + 128
		v := ((113*r - 102*g - 11*b + 128) >> 8) + 128
		return u, v
	}

	for y := 0; y < height; y++ {
		row := y * width
		for x := 0; x < width; x++ {
			r, g, b := rgb(x, y)
			yPlane[row+x] = luma(r, g, b)
		}
	}
	for y := 0; y < height; y += 2 {
		uvRow := (y / 2) * width
		for x := 0; x < width; x += 2 {
			var uSum, vSum int
			for yy := 0; yy < 2; yy++ {
				for xx := 0; xx < 2; xx++ {
					r, g, b := rgb(x+xx, y+yy)
					u, v := chroma(r, g, b)
					uSum += u
					vSum += v
				}
			}
			uvPlane[uvRow+x] = clamp8((uSum + 2) / 4)
			uvPlane[uvRow+x+1] = clamp8((vSum + 2) / 4)
		}
	}
	return dst, nil
}


// NV12ToBGRA converts a limited-range BT.709 NV12 frame to tightly packed
// BGRA. It is the native Viewer bring-up path; the later zero-copy D3D11 video
// processor can replace this CPU conversion without changing decoder/session
// contracts.
func NV12ToBGRA(src []byte, width, height, stride int, dst []byte) ([]byte, error) {
	if width <= 0 || height <= 0 || width%2 != 0 || height%2 != 0 {
		return nil, fmt.Errorf("%w: NV12 decode requires positive even dimensions", ErrInvalidFrame)
	}
	if stride < width {
		return nil, fmt.Errorf("%w: NV12 stride is smaller than width", ErrInvalidFrame)
	}
	requiredNV12 := stride*height + stride*(height/2)
	if len(src) < requiredNV12 {
		return nil, fmt.Errorf("%w: NV12 buffer is too small", ErrInvalidFrame)
	}
	requiredBGRA := width * height * 4
	if cap(dst) < requiredBGRA {
		dst = make([]byte, requiredBGRA)
	} else {
		dst = dst[:requiredBGRA]
	}

	yPlane := src[:stride*height]
	uvPlane := src[stride*height:]
	for y := 0; y < height; y++ {
		yRow := y * stride
		uvRow := (y / 2) * stride
		outRow := y * width * 4
		for x := 0; x < width; x++ {
			yy := int(yPlane[yRow+x]) - 16
			if yy < 0 {
				yy = 0
			}
			uv := uvRow + (x &^ 1)
			u := int(uvPlane[uv]) - 128
			v := int(uvPlane[uv+1]) - 128
			r := (298*yy + 459*v + 128) >> 8
			g := (298*yy - 55*u - 136*v + 128) >> 8
			b := (298*yy + 541*u + 128) >> 8
			di := outRow + x*4
			dst[di] = clamp8(b)
			dst[di+1] = clamp8(g)
			dst[di+2] = clamp8(r)
			dst[di+3] = 0xff
		}
	}
	return dst, nil
}
