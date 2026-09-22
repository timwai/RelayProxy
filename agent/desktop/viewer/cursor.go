package viewer

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"math"

	"relayproxy/internal/protocol"
)

type CursorBitmap struct {
	ID     string
	Pix    []byte
	Width  int
	Height int
	Stride int
}

func DecodeCursorPNG(id string, data []byte) (CursorBitmap, error) {
	if len(data) == 0 {
		return CursorBitmap{}, fmt.Errorf("%w: empty cursor PNG", ErrUnavailable)
	}
	src, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return CursorBitmap{}, err
	}
	bounds := src.Bounds()
	if bounds.Dx() <= 0 || bounds.Dy() <= 0 || bounds.Dx() > 512 || bounds.Dy() > 512 {
		return CursorBitmap{}, fmt.Errorf("%w: invalid cursor bitmap dimensions", ErrUnavailable)
	}
	nrgba := image.NewNRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(nrgba, nrgba.Bounds(), src, bounds.Min, draw.Src)
	return CursorBitmap{
		ID:     id,
		Pix:    append([]byte(nil), nrgba.Pix...),
		Width:  nrgba.Bounds().Dx(),
		Height: nrgba.Bounds().Dy(),
		Stride: nrgba.Stride,
	}, nil
}

func CompositeCursorBGRA(base []byte, width, height, stride int, state protocol.DesktopCursorState, shape CursorBitmap, dst []byte) ([]byte, error) {
	if width <= 0 || height <= 0 || stride < width*4 || len(base) < stride*height {
		return nil, fmt.Errorf("%w: invalid BGRA base frame", ErrUnavailable)
	}
	required := stride * height
	if cap(dst) < required {
		dst = make([]byte, required)
	} else {
		dst = dst[:required]
	}
	copy(dst, base[:required])

	if !state.Visible || state.ScreenWidth <= 0 || state.ScreenHeight <= 0 ||
		shape.Width <= 0 || shape.Height <= 0 || shape.Stride < shape.Width*4 ||
		len(shape.Pix) < shape.Stride*shape.Height {
		return dst, nil
	}

	scaleX := float64(width) / float64(state.ScreenWidth)
	scaleY := float64(height) / float64(state.ScreenHeight)
	sourceWidth := state.Width
	sourceHeight := state.Height
	if sourceWidth <= 0 {
		sourceWidth = shape.Width
	}
	if sourceHeight <= 0 {
		sourceHeight = shape.Height
	}
	cursorWidth := max(1, int(math.Round(float64(sourceWidth)*scaleX)))
	cursorHeight := max(1, int(math.Round(float64(sourceHeight)*scaleY)))
	left := int(math.Round(float64(state.X-state.HotspotX) * scaleX))
	top := int(math.Round(float64(state.Y-state.HotspotY) * scaleY))

	for y := 0; y < cursorHeight; y++ {
		dy := top + y
		if dy < 0 || dy >= height {
			continue
		}
		sy := y * shape.Height / cursorHeight
		for x := 0; x < cursorWidth; x++ {
			dx := left + x
			if dx < 0 || dx >= width {
				continue
			}
			sx := x * shape.Width / cursorWidth
			si := sy*shape.Stride + sx*4
			alpha := int(shape.Pix[si+3])
			if alpha == 0 {
				continue
			}
			di := dy*stride + dx*4
			srcR := int(shape.Pix[si])
			srcG := int(shape.Pix[si+1])
			srcB := int(shape.Pix[si+2])
			if alpha == 255 {
				dst[di] = byte(srcB)
				dst[di+1] = byte(srcG)
				dst[di+2] = byte(srcR)
				dst[di+3] = 0xff
				continue
			}
			inv := 255 - alpha
			dst[di] = byte((srcB*alpha + int(dst[di])*inv + 127) / 255)
			dst[di+1] = byte((srcG*alpha + int(dst[di+1])*inv + 127) / 255)
			dst[di+2] = byte((srcR*alpha + int(dst[di+2])*inv + 127) / 255)
			dst[di+3] = 0xff
		}
	}
	return dst, nil
}
