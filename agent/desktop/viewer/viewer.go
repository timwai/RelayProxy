package viewer

import (
	"errors"
	"fmt"
)

var ErrUnavailable = errors.New("native desktop viewer unavailable")

type Config struct {
	Title  string
	Width  int
	Height int
}

type Frame struct {
	BGRA   []byte
	Width  int
	Height int
	Stride int
}

func (f Frame) Validate() error {
	if f.Width <= 0 || f.Height <= 0 {
		return fmt.Errorf("%w: invalid frame dimensions", ErrUnavailable)
	}
	if f.Stride < f.Width*4 || len(f.BGRA) < f.Stride*f.Height {
		return fmt.Errorf("%w: BGRA frame buffer is too small", ErrUnavailable)
	}
	return nil
}

type Native interface {
	Submit(Frame) error
	Focus()
	Done() <-chan struct{}
	Close() error
}
