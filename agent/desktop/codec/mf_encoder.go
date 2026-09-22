package codec

import (
	"fmt"
	"image"
)

func frameToNV12(frame RawFrame, dst []byte) ([]byte, error) {
	if err := frame.Validate(); err != nil {
		return nil, err
	}
	required := frame.Width * frame.Height * 3 / 2
	if cap(dst) < required {
		dst = make([]byte, required)
	} else {
		dst = dst[:required]
	}

	switch frame.Format {
	case PixelFormatRGBA:
		src := &image.RGBA{
			Pix:    frame.Pix,
			Stride: frame.Stride,
			Rect:   image.Rect(0, 0, frame.Width, frame.Height),
		}
		return RGBAtoNV12(src, dst)
	case PixelFormatBGRA:
		return BGRAtoNV12(frame.Pix, frame.Width, frame.Height, frame.Stride, dst)
	case PixelFormatNV12:
		if frame.Stride == frame.Width {
			copy(dst, frame.Pix[:required])
			return dst, nil
		}
		yBytes := frame.Width * frame.Height
		for row := 0; row < frame.Height; row++ {
			srcStart := row * frame.Stride
			copy(dst[row*frame.Width:(row+1)*frame.Width], frame.Pix[srcStart:srcStart+frame.Width])
		}
		srcUV := frame.Stride * frame.Height
		for row := 0; row < frame.Height/2; row++ {
			srcStart := srcUV + row*frame.Stride
			dstStart := yBytes + row*frame.Width
			copy(dst[dstStart:dstStart+frame.Width], frame.Pix[srcStart:srcStart+frame.Width])
		}
		return dst, nil
	default:
		return nil, fmt.Errorf("%w: unsupported pixel format %q", ErrInvalidFrame, frame.Format)
	}
}
