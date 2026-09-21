//go:build windows

package desktop

import (
	"context"
	"errors"
	"image"
	"sync"
	"unsafe"

	"github.com/lxn/win"
)

type gdiCapture struct {
	mu sync.Mutex

	x, y          int32
	width, height int32
	screenDC      win.HDC
	memoryDC      win.HDC
	bitmap        win.HBITMAP
	oldObject     win.HGDIOBJ
	info          win.BITMAPINFO
	raw           []byte
	closed        bool
}

func newSystemCapture() (*gdiCapture, error) {
	x := win.GetSystemMetrics(win.SM_XVIRTUALSCREEN)
	y := win.GetSystemMetrics(win.SM_YVIRTUALSCREEN)
	width := win.GetSystemMetrics(win.SM_CXVIRTUALSCREEN)
	height := win.GetSystemMetrics(win.SM_CYVIRTUALSCREEN)
	if width <= 0 || height <= 0 || int64(width)*int64(height) > 100_000_000 {
		return nil, errors.New("invalid Windows virtual desktop size")
	}
	screenDC := win.GetDC(0)
	if screenDC == 0 {
		return nil, errors.New("GetDC failed for Windows desktop")
	}
	memoryDC := win.CreateCompatibleDC(screenDC)
	if memoryDC == 0 {
		win.ReleaseDC(0, screenDC)
		return nil, errors.New("CreateCompatibleDC failed")
	}
	bitmap := win.CreateCompatibleBitmap(screenDC, width, height)
	if bitmap == 0 {
		win.DeleteDC(memoryDC)
		win.ReleaseDC(0, screenDC)
		return nil, errors.New("CreateCompatibleBitmap failed")
	}
	oldObject := win.SelectObject(memoryDC, win.HGDIOBJ(bitmap))
	capture := &gdiCapture{
		x: x, y: y, width: width, height: height,
		screenDC: screenDC, memoryDC: memoryDC, bitmap: bitmap, oldObject: oldObject,
		raw: make([]byte, int(width)*int(height)*4),
	}
	capture.info.BmiHeader = win.BITMAPINFOHEADER{
		BiSize:        uint32(unsafe.Sizeof(win.BITMAPINFOHEADER{})),
		BiWidth:       width,
		BiHeight:      -height,
		BiPlanes:      1,
		BiBitCount:    32,
		BiCompression: win.BI_RGB,
		BiSizeImage:   uint32(len(capture.raw)),
	}
	return capture, nil
}

func (c *gdiCapture) Capture(ctx context.Context) (*image.RGBA, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, errors.New("Windows desktop capture is closed")
	}
	if !win.BitBlt(c.memoryDC, 0, 0, c.width, c.height, c.screenDC, c.x, c.y, win.SRCCOPY|win.CAPTUREBLT) {
		return nil, errors.New("BitBlt desktop capture failed")
	}
	if len(c.raw) == 0 || win.GetDIBits(c.memoryDC, c.bitmap, 0, uint32(c.height), &c.raw[0], &c.info, win.DIB_RGB_COLORS) == 0 {
		return nil, errors.New("GetDIBits desktop capture failed")
	}
	frame := image.NewRGBA(image.Rect(0, 0, int(c.width), int(c.height)))
	for si, di := 0, 0; si+3 < len(c.raw); si, di = si+4, di+4 {
		frame.Pix[di] = c.raw[si+2]
		frame.Pix[di+1] = c.raw[si+1]
		frame.Pix[di+2] = c.raw[si]
		frame.Pix[di+3] = 0xff
	}
	return frame, nil
}

func (c *gdiCapture) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	if c.memoryDC != 0 && c.oldObject != 0 {
		win.SelectObject(c.memoryDC, c.oldObject)
	}
	if c.bitmap != 0 {
		win.DeleteObject(win.HGDIOBJ(c.bitmap))
	}
	if c.memoryDC != 0 {
		win.DeleteDC(c.memoryDC)
	}
	if c.screenDC != 0 {
		win.ReleaseDC(0, c.screenDC)
	}
	c.bitmap, c.memoryDC, c.screenDC = 0, 0, 0
	c.raw = nil
	return nil
}

func NewSystemHost() (*Host, error) {
	source, err := newSystemCapture()
	if err != nil {
		return nil, err
	}
	host, err := NewHost(source, DefaultHostConfig())
	if err != nil {
		_ = source.Close()
		return nil, err
	}
	return host, nil
}
