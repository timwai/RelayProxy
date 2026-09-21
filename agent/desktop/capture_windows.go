//go:build windows

package desktop

import (
	"context"
	"errors"
	"image"
	"log"
	"sync"
	"time"
	"unsafe"

	"github.com/go-mswin/screencapture"
	"github.com/lxn/win"

	desktopcodec "relayproxy/agent/desktop/codec"
	"relayproxy/internal/protocol"
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
	frame         *image.RGBA
	closed        bool
}

func newGDICapture() (*gdiCapture, error) {
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
		raw:   make([]byte, int(width)*int(height)*4),
		frame: image.NewRGBA(image.Rect(0, 0, int(width), int(height))),
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
	if c.frame == nil || c.frame.Bounds().Dx() != int(c.width) || c.frame.Bounds().Dy() != int(c.height) {
		c.frame = image.NewRGBA(image.Rect(0, 0, int(c.width), int(c.height)))
	}
	for si, di := 0, 0; si+3 < len(c.raw); si, di = si+4, di+4 {
		c.frame.Pix[di] = c.raw[si+2]
		c.frame.Pix[di+1] = c.raw[si+1]
		c.frame.Pix[di+2] = c.raw[si]
		c.frame.Pix[di+3] = 0xff
	}
	return c.frame, nil
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
	c.frame = nil
	return nil
}

// windowsCapture preserves the old virtual-desktop GDI backend as a fallback
// while using DXGI Desktop Duplication for the common single-display case.
// Multi-monitor stays on GDI until Relay Desktop carries display geometry in
// the session, otherwise normalized input coordinates would target the wrong
// monitor.
type windowsCapture struct {
	mu sync.Mutex

	gdi        *gdiCapture
	dxgiTarget *screencapture.Display
	stream     *screencapture.Stream
	frame      *image.RGBA
	backend    string
	closed     bool
}

func newSystemCapture() (*windowsCapture, error) {
	gdi, err := newGDICapture()
	if err != nil {
		return nil, err
	}
	capture := &windowsCapture{gdi: gdi, backend: "gdi"}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	displays, err := screencapture.Displays(ctx)
	if err == nil && len(displays) == 1 && displays[0].Duplicable() {
		target := displays[0]
		capture.dxgiTarget = &target
	}
	return capture, nil
}

func (c *windowsCapture) BeginSession(ctx context.Context, cfg HostConfig) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("Windows desktop capture is closed")
	}
	c.closeDXGILocked()
	c.backend = "gdi"
	if c.dxgiTarget == nil {
		return nil
	}
	stream, err := screencapture.CaptureDisplay(ctx, *c.dxgiTarget, screencapture.Options{
		Backend:    screencapture.BackendDuplication,
		FPS:        float64(cfg.MaxFPS),
		QueueDepth: screencapture.MinQueueDepth,
		Timeout:    100 * time.Millisecond,
	})
	if err != nil {
		log.Printf("[Desktop] DXGI Desktop Duplication unavailable, using GDI fallback: %v", err)
		return nil
	}
	c.stream = stream
	c.backend = "dxgi"
	c.frame = nil
	return nil
}

func (c *windowsCapture) EndSession() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closeDXGILocked()
	if !c.closed {
		c.backend = "gdi"
	}
	return nil
}

func (c *windowsCapture) CaptureBackend() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.backend
}

func (c *windowsCapture) closeDXGILocked() {
	if c.stream != nil {
		_ = c.stream.Close()
		c.stream = nil
	}
	c.frame = nil
}

func copyDXGIFrame(src screencapture.Frame, dst *image.RGBA) (*image.RGBA, error) {
	if !src.Valid() {
		return nil, screencapture.ErrNoFrame
	}
	if dst == nil || dst.Bounds().Dx() != src.Width || dst.Bounds().Dy() != src.Height {
		dst = image.NewRGBA(image.Rect(0, 0, src.Width, src.Height))
	}
	for y := 0; y < src.Height; y++ {
		row := src.Row(y)
		out := dst.Pix[y*dst.Stride:]
		for x := 0; x < src.Width; x++ {
			si := x * 4
			di := x * 4
			out[di] = row[si+2]
			out[di+1] = row[si+1]
			out[di+2] = row[si]
			out[di+3] = 0xff
		}
	}
	return dst, nil
}

func (c *windowsCapture) captureDXGILocked(ctx context.Context) (*image.RGBA, error) {
	if c.stream == nil {
		return nil, screencapture.ErrBackendUnavailable
	}
	frame, fresh := c.stream.Frame()
	if !fresh || !frame.Valid() {
		if c.frame != nil {
			return c.frame, nil
		}
		waitCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
		defer cancel()
		var err error
		frame, err = c.stream.WaitFrame(waitCtx)
		if err != nil {
			return nil, err
		}
	}
	var err error
	c.frame, err = copyDXGIFrame(frame, c.frame)
	return c.frame, err
}

func (c *windowsCapture) Capture(ctx context.Context) (*image.RGBA, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, errors.New("Windows desktop capture is closed")
	}
	if c.stream != nil {
		frame, err := c.captureDXGILocked(ctx)
		if err == nil {
			return frame, nil
		}
		if !errors.Is(err, screencapture.ErrNoFrame) {
			log.Printf("[Desktop] DXGI capture failed, switching session to GDI: %v", err)
			c.closeDXGILocked()
			c.backend = "gdi"
		} else if c.frame != nil {
			return c.frame, nil
		}
	}
	return c.gdi.Capture(ctx)
}

func (c *windowsCapture) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.closeDXGILocked()
	gdi := c.gdi
	c.gdi = nil
	c.mu.Unlock()
	if gdi != nil {
		return gdi.Close()
	}
	return nil
}

func NewSystemHost() (*Host, error) {
	source, err := newSystemCapture()
	if err != nil {
		return nil, err
	}
	host, err := NewHostWithInput(source, newWindowsInputSink(), DefaultHostConfig())
	if err != nil {
		_ = source.Close()
		return nil, err
	}
	probeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	probe := desktopcodec.ProbeH264MediaFoundation(probeCtx)
	cancel()
	host.SetCodecCapabilities([]protocol.DesktopCodecCapability{probe.Capability()})
	log.Printf("[Desktop] Media Foundation H.264 probe mf=%t hwEnc=%d hwDec=%d swEnc=%d swDec=%d error=%q",
		probe.MediaFoundation, probe.HardwareEncoderCount, probe.HardwareDecoderCount,
		probe.SoftwareEncoderCount, probe.SoftwareDecoderCount, probe.Error)
	return host, nil
}
