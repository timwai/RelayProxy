//go:build windows

package desktop

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"log"
	"strconv"
	"sync"
	"time"
	"unsafe"

	"github.com/go-mswin/screencapture"
	"github.com/lxn/win"
	"golang.org/x/sys/windows"

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

// windowsCapture keeps virtual-desktop GDI as the compatibility default. When
// a session names a DisplayID it captures exactly that current monitor through
// a backend-neutral frame stream and exposes the same monitor geometry to the
// cursor channel. The current stream adapter implements DXGI/GDI; WGC can plug
// into the same contract without changing Host media code.
type windowsCapture struct {
	mu       sync.Mutex
	cursorMu sync.Mutex

	gdi             *gdiCapture
	stream          windowsFrameStream
	streamFactory   windowsFrameStreamFactory
	selectedDisplay *screencapture.Display
	frame           *image.RGBA
	backend         string
	closed          bool

	cursorHandle   uintptr
	cursorPNG      []byte
	cursorWidth    int
	cursorHeight   int
	cursorHotspotX int
	cursorHotspotY int
}

func newSystemCapture() (*windowsCapture, error) {
	gdi, err := newGDICapture()
	if err != nil {
		return nil, err
	}
	return &windowsCapture{
		gdi:           gdi,
		streamFactory: screencaptureFrameStreamFactory{},
		backend:       "gdi",
	}, nil
}

func windowsDesktopCapabilitySnapshot(displays []screencapture.Display, hasWGC bool) ([]protocol.DesktopCaptureCapability, []protocol.DesktopDisplayCapability) {
	captures := []protocol.DesktopCaptureCapability{{
		Backend: "gdi",
		Cursor:  true,
	}}
	hasDXGI := false
	out := make([]protocol.DesktopDisplayCapability, 0, len(displays))
	for _, display := range displays {
		if display.Duplicable() {
			hasDXGI = true
		}
		out = append(out, protocol.DesktopDisplayCapability{
			ID:      strconv.FormatUint(display.ID, 10),
			Name:    display.DeviceName,
			Width:   display.PixelWidth,
			Height:  display.PixelHeight,
			Primary: display.Primary,
		})
	}
	if hasDXGI {
		captures = append(captures, protocol.DesktopCaptureCapability{
			Backend: "dxgi",
			Cursor:  true,
		})
	}
	if hasWGC {
		captures = append(captures, protocol.DesktopCaptureCapability{
			Backend: "wgc",
			Cursor:  true,
		})
	}
	return captures, out
}

func (c *windowsCapture) DesktopAudioAvailable() bool {
	return c != nil
}

func (c *windowsCapture) DesktopCaptureCapabilities(ctx context.Context) ([]protocol.DesktopCaptureCapability, []protocol.DesktopDisplayCapability, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	displays, err := screencapture.Displays(ctx)
	if err != nil {
		return nil, nil, err
	}
	captures, capabilities := windowsDesktopCapabilitySnapshot(displays, windowsWGCAvailable())
	return captures, capabilities, nil
}

func resolveWindowsDisplay(displays []screencapture.Display, displayID string) (screencapture.Display, bool, error) {
	if displayID == "" {
		if len(displays) == 1 {
			return displays[0], false, nil
		}
		return screencapture.Display{}, false, nil
	}
	id, err := strconv.ParseUint(displayID, 10, 64)
	if err != nil || id == 0 {
		return screencapture.Display{}, true, fmt.Errorf("invalid Windows display id %q", displayID)
	}
	for _, display := range displays {
		if display.ID == id {
			return display, true, nil
		}
	}
	return screencapture.Display{}, true, fmt.Errorf("Windows display %q is no longer available", displayID)
}

func normalizedWindowsCaptureBackend(preference protocol.DesktopCaptureBackend) protocol.DesktopCaptureBackend {
	if preference == "" {
		return protocol.DesktopCaptureAuto
	}
	return preference
}

func explicitWindowsCaptureBackend(preference protocol.DesktopCaptureBackend) bool {
	return normalizedWindowsCaptureBackend(preference) != protocol.DesktopCaptureAuto
}

func windowsCaptureRequiresDisplayTarget(preference protocol.DesktopCaptureBackend) bool {
	switch normalizedWindowsCaptureBackend(preference) {
	case protocol.DesktopCaptureDXGI, protocol.DesktopCaptureWGC:
		return true
	default:
		return false
	}
}

func (c *windowsCapture) BeginSession(ctx context.Context, cfg HostConfig) error {
	requestedBackend := normalizedWindowsCaptureBackend(cfg.CaptureBackend)
	if c.streamFactory == nil {
		return errors.New("Windows capture stream factory is unavailable")
	}
	displays, listErr := screencapture.Displays(ctx)

	var (
		nextStream   windowsFrameStream
		nextSelected *screencapture.Display
		nextBackend  = "gdi"
	)
	if listErr != nil {
		if cfg.DisplayID != "" || windowsCaptureRequiresDisplayTarget(requestedBackend) {
			return fmt.Errorf("enumerate Windows displays for capture backend %s: %w",
				normalizedWindowsCaptureBackend(cfg.CaptureBackend), listErr)
		}
		log.Printf("[Desktop] display enumeration unavailable, using virtual desktop GDI: %v", listErr)
	} else {
		target, selected, selectErr := resolveWindowsDisplay(displays, cfg.DisplayID)
		if selectErr != nil {
			return selectErr
		}
		if target.ID == 0 {
			if windowsCaptureRequiresDisplayTarget(requestedBackend) {
				return fmt.Errorf("%s capture requires selecting a specific display when multiple displays are active",
					requestedBackend)
			}
		} else {
			stream, err := c.streamFactory.Open(ctx, target, requestedBackend, cfg.MaxFPS)
			if err != nil {
				if selected || explicitWindowsCaptureBackend(requestedBackend) {
					return fmt.Errorf("capture Windows display using %s: %w",
						normalizedWindowsCaptureBackend(cfg.CaptureBackend), err)
				}
				log.Printf("[Desktop] per-display capture unavailable, using virtual desktop GDI: %v", err)
			} else {
				nextStream = stream
				nextBackend = string(stream.Backend())
				if nextBackend == "" {
					nextBackend = "gdi"
				}
				if selected {
					copy := target
					nextSelected = &copy
				}
			}
		}
	}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		if nextStream != nil {
			_ = nextStream.Close()
		}
		return errors.New("Windows desktop capture is closed")
	}
	oldStream := c.stream
	c.stream = nextStream
	c.selectedDisplay = nextSelected
	c.backend = nextBackend
	c.frame = nil
	c.mu.Unlock()

	if oldStream != nil {
		_ = oldStream.Close()
	}
	return nil
}

func (c *windowsCapture) EndSession() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closeStreamLocked()
	c.selectedDisplay = nil
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

func (c *windowsCapture) SetCaptureFPS(fps int) error {
	if fps <= 0 {
		return fmt.Errorf("invalid Windows capture fps %d", fps)
	}
	if c == nil {
		return errors.New("Windows desktop capture is unavailable")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("Windows desktop capture is closed")
	}
	if c.stream == nil {
		return nil
	}
	controller, ok := c.stream.(windowsFrameRateController)
	if !ok {
		return nil
	}
	return controller.SetFrameRateLimit(fps)
}

func (c *windowsCapture) closeStreamLocked() {
	if c.stream != nil {
		_ = c.stream.Close()
		c.stream = nil
	}
	c.frame = nil
}

func copyWindowsBGRAFrame(src windowsCaptureFrame, dst *image.RGBA) (*image.RGBA, error) {
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

func (c *windowsCapture) borrowedStreamFrameLocked(ctx context.Context) (windowsCaptureFrame, bool, error) {
	if c.stream == nil {
		return windowsCaptureFrame{}, false, screencapture.ErrBackendUnavailable
	}
	frame, fresh := c.stream.Frame()
	if frame.Valid() {
		return frame, fresh, nil
	}
	waitCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	frame, err := c.stream.WaitFrame(waitCtx)
	if err != nil {
		return windowsCaptureFrame{}, false, err
	}
	return frame, true, nil
}

func (c *windowsCapture) captureStreamLocked(ctx context.Context) (*image.RGBA, error) {
	frame, fresh, err := c.borrowedStreamFrameLocked(ctx)
	if err != nil {
		if errors.Is(err, screencapture.ErrNoFrame) && c.frame != nil {
			return c.frame, nil
		}
		return nil, err
	}
	if !fresh && c.frame != nil {
		return c.frame, nil
	}
	c.frame, err = copyWindowsBGRAFrame(frame, c.frame)
	return c.frame, err
}

func (c *windowsCapture) CaptureD3D11(ctx context.Context) (*D3D11CaptureFrame, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, false, errors.New("Windows desktop capture is closed")
	}
	if c.stream == nil {
		return nil, false, nil
	}
	stream, ok := c.stream.(windowsD3D11FrameStream)
	if !ok {
		return nil, false, nil
	}
	waitCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	frame, err := stream.WaitD3D11Frame(waitCtx)
	if err != nil {
		return nil, false, err
	}
	if frame == nil || !frame.Valid() {
		if frame != nil {
			frame.Close()
		}
		return nil, false, screencapture.ErrNoFrame
	}
	return frame, true, nil
}

func (c *windowsCapture) CaptureRaw(ctx context.Context) (desktopcodec.RawFrame, bool, error) {
	if err := ctx.Err(); err != nil {
		return desktopcodec.RawFrame{}, false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return desktopcodec.RawFrame{}, false, errors.New("Windows desktop capture is closed")
	}
	if c.stream == nil {
		return desktopcodec.RawFrame{}, false, nil
	}
	frame, _, err := c.borrowedStreamFrameLocked(ctx)
	if err == nil && frame.Valid() {
		return desktopcodec.RawFrame{
			Format: desktopcodec.PixelFormatBGRA,
			Pix:    frame.Pix,
			Width:  frame.Width,
			Height: frame.Height,
			Stride: frame.Stride,
		}, true, nil
	}
	if c.selectedDisplay != nil {
		if err == nil {
			err = screencapture.ErrNoFrame
		}
		return desktopcodec.RawFrame{}, false, fmt.Errorf("selected display raw capture failed: %w", err)
	}
	if err != nil && !errors.Is(err, screencapture.ErrNoFrame) {
		log.Printf("[Desktop] raw per-display capture failed, switching session to virtual desktop GDI: %v", err)
		c.closeStreamLocked()
		c.backend = "gdi"
	}
	return desktopcodec.RawFrame{}, false, nil
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
		frame, err := c.captureStreamLocked(ctx)
		if err == nil {
			return frame, nil
		}
		if c.selectedDisplay != nil {
			if errors.Is(err, screencapture.ErrNoFrame) && c.frame != nil {
				return c.frame, nil
			}
			return nil, fmt.Errorf("selected display capture failed: %w", err)
		}
		if !errors.Is(err, screencapture.ErrNoFrame) {
			log.Printf("[Desktop] per-display capture failed, switching session to virtual desktop GDI: %v", err)
			c.closeStreamLocked()
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
	c.closeStreamLocked()
	gdi := c.gdi
	c.gdi = nil
	c.mu.Unlock()
	if gdi != nil {
		return gdi.Close()
	}
	return nil
}

type windowsCursorInfo struct {
	Size      uint32
	Flags     uint32
	HCursor   uintptr
	ScreenPos win.POINT
}

const cursorShowing = 0x00000001

var (
	cursorUser32DLL   = windows.NewLazySystemDLL("user32.dll")
	procGetCursorInfo = cursorUser32DLL.NewProc("GetCursorInfo")
)

func currentWindowsCursor() (windowsCursorInfo, error) {
	var info windowsCursorInfo
	info.Size = uint32(unsafe.Sizeof(info))
	ret, _, callErr := procGetCursorInfo.Call(uintptr(unsafe.Pointer(&info)))
	if ret == 0 {
		if callErr != nil && callErr != windows.ERROR_SUCCESS {
			return windowsCursorInfo{}, callErr
		}
		return windowsCursorInfo{}, errors.New("GetCursorInfo failed")
	}
	return info, nil
}

func cursorBitmapSize(iconInfo win.ICONINFO) (int, int) {
	var bitmap win.BITMAP
	if iconInfo.HbmColor != 0 {
		if win.GetObject(win.HGDIOBJ(iconInfo.HbmColor), unsafe.Sizeof(bitmap), unsafe.Pointer(&bitmap)) != 0 {
			if bitmap.BmWidth > 0 && bitmap.BmHeight > 0 {
				return int(bitmap.BmWidth), int(bitmap.BmHeight)
			}
		}
	}
	if iconInfo.HbmMask != 0 {
		bitmap = win.BITMAP{}
		if win.GetObject(win.HGDIOBJ(iconInfo.HbmMask), unsafe.Sizeof(bitmap), unsafe.Pointer(&bitmap)) != 0 {
			height := bitmap.BmHeight
			if iconInfo.HbmColor == 0 {
				height /= 2
			}
			if bitmap.BmWidth > 0 && height > 0 {
				return int(bitmap.BmWidth), int(height)
			}
		}
	}
	return int(win.GetSystemMetrics(win.SM_CXCURSOR)), int(win.GetSystemMetrics(win.SM_CYCURSOR))
}

func renderCursorBGRA(hCursor uintptr, width, height int, background byte) ([]byte, error) {
	if hCursor == 0 || width <= 0 || height <= 0 || width > 512 || height > 512 {
		return nil, errors.New("invalid Windows cursor dimensions")
	}
	screenDC := win.GetDC(0)
	if screenDC == 0 {
		return nil, errors.New("GetDC failed for cursor capture")
	}
	defer win.ReleaseDC(0, screenDC)

	memoryDC := win.CreateCompatibleDC(screenDC)
	if memoryDC == 0 {
		return nil, errors.New("CreateCompatibleDC failed for cursor capture")
	}
	defer win.DeleteDC(memoryDC)

	header := win.BITMAPINFOHEADER{
		BiSize:        uint32(unsafe.Sizeof(win.BITMAPINFOHEADER{})),
		BiWidth:       int32(width),
		BiHeight:      -int32(height),
		BiPlanes:      1,
		BiBitCount:    32,
		BiCompression: win.BI_RGB,
		BiSizeImage:   uint32(width * height * 4),
	}
	var bits unsafe.Pointer
	bitmap := win.CreateDIBSection(screenDC, &header, win.DIB_RGB_COLORS, &bits, 0, 0)
	if bitmap == 0 || bits == nil {
		return nil, errors.New("CreateDIBSection failed for cursor capture")
	}
	old := win.SelectObject(memoryDC, win.HGDIOBJ(bitmap))
	defer func() {
		win.SelectObject(memoryDC, old)
		win.DeleteObject(win.HGDIOBJ(bitmap))
	}()

	raw := unsafe.Slice((*byte)(bits), width*height*4)
	for i := 0; i+3 < len(raw); i += 4 {
		raw[i] = background
		raw[i+1] = background
		raw[i+2] = background
		raw[i+3] = 0
	}
	if !win.DrawIconEx(memoryDC, 0, 0, win.HICON(hCursor), int32(width), int32(height), 0, 0, win.DI_NORMAL) {
		return nil, errors.New("DrawIconEx failed for cursor capture")
	}
	return append([]byte(nil), raw...), nil
}

func cursorImageFromRenders(black, white []byte, width, height int) (*image.NRGBA, error) {
	expected := width * height * 4
	if width <= 0 || height <= 0 || len(black) < expected || len(white) < expected {
		return nil, errors.New("invalid cursor render buffers")
	}
	out := image.NewNRGBA(image.Rect(0, 0, width, height))
	clamp := func(value int) byte {
		if value < 0 {
			return 0
		}
		if value > 255 {
			return 255
		}
		return byte(value)
	}
	for si, di := 0, 0; si < expected; si, di = si+4, di+4 {
		db := int(white[si]) - int(black[si])
		dg := int(white[si+1]) - int(black[si+1])
		dr := int(white[si+2]) - int(black[si+2])
		if db < 0 {
			db = 0
		}
		if dg < 0 {
			dg = 0
		}
		if dr < 0 {
			dr = 0
		}
		alpha := 255 - (db+dg+dr)/3
		if alpha <= 0 {
			continue
		}
		out.Pix[di+3] = byte(alpha)
		out.Pix[di] = clamp(int(black[si+2]) * 255 / alpha)
		out.Pix[di+1] = clamp(int(black[si+1]) * 255 / alpha)
		out.Pix[di+2] = clamp(int(black[si]) * 255 / alpha)
	}
	return out, nil
}

func captureWindowsCursorShape(hCursor uintptr) ([]byte, int, int, int, int, error) {
	var iconInfo win.ICONINFO
	if !win.GetIconInfo(win.HICON(hCursor), &iconInfo) {
		return nil, 0, 0, 0, 0, errors.New("GetIconInfo failed for cursor")
	}
	if iconInfo.HbmColor != 0 {
		defer win.DeleteObject(win.HGDIOBJ(iconInfo.HbmColor))
	}
	if iconInfo.HbmMask != 0 {
		defer win.DeleteObject(win.HGDIOBJ(iconInfo.HbmMask))
	}
	width, height := cursorBitmapSize(iconInfo)
	black, err := renderCursorBGRA(hCursor, width, height, 0)
	if err != nil {
		return nil, 0, 0, 0, 0, err
	}
	white, err := renderCursorBGRA(hCursor, width, height, 0xff)
	if err != nil {
		return nil, 0, 0, 0, 0, err
	}
	cursorImage, err := cursorImageFromRenders(black, white, width, height)
	if err != nil {
		return nil, 0, 0, 0, 0, err
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, cursorImage); err != nil {
		return nil, 0, 0, 0, 0, err
	}
	return encoded.Bytes(), width, height, int(iconInfo.XHotspot), int(iconInfo.YHotspot), nil
}

func (c *windowsCapture) CaptureCursor(ctx context.Context) (protocol.DesktopCursorState, error) {
	if err := ctx.Err(); err != nil {
		return protocol.DesktopCursorState{}, err
	}
	info, err := currentWindowsCursor()
	if err != nil {
		return protocol.DesktopCursorState{}, err
	}

	c.mu.Lock()
	var selectedDisplay *screencapture.Display
	if c.selectedDisplay != nil {
		copy := *c.selectedDisplay
		selectedDisplay = &copy
	}
	c.mu.Unlock()

	screenX := int(win.GetSystemMetrics(win.SM_XVIRTUALSCREEN))
	screenY := int(win.GetSystemMetrics(win.SM_YVIRTUALSCREEN))
	screenWidth := int(win.GetSystemMetrics(win.SM_CXVIRTUALSCREEN))
	screenHeight := int(win.GetSystemMetrics(win.SM_CYVIRTUALSCREEN))
	if selectedDisplay != nil {
		screenX = selectedDisplay.Bounds.X
		screenY = selectedDisplay.Bounds.Y
		screenWidth = selectedDisplay.Bounds.W
		screenHeight = selectedDisplay.Bounds.H
	}
	if screenWidth <= 0 || screenHeight <= 0 {
		return protocol.DesktopCursorState{}, errors.New("invalid Windows desktop capture geometry")
	}

	cursorX := int(info.ScreenPos.X) - screenX
	cursorY := int(info.ScreenPos.Y) - screenY
	visible := info.Flags&cursorShowing != 0
	if selectedDisplay != nil &&
		(cursorX < 0 || cursorY < 0 || cursorX >= screenWidth || cursorY >= screenHeight) {
		visible = false
	}

	state := protocol.DesktopCursorState{
		X:            cursorX,
		Y:            cursorY,
		ScreenWidth:  screenWidth,
		ScreenHeight: screenHeight,
		Visible:      visible,
	}
	if info.HCursor == 0 {
		return state, nil
	}
	state.CursorID = strconv.FormatUint(uint64(info.HCursor), 16)

	c.cursorMu.Lock()
	defer c.cursorMu.Unlock()
	if c.cursorHandle != info.HCursor {
		shape, width, height, hotspotX, hotspotY, shapeErr := captureWindowsCursorShape(info.HCursor)
		c.cursorHandle = info.HCursor
		c.cursorPNG = append(c.cursorPNG[:0], shape...)
		c.cursorWidth, c.cursorHeight = width, height
		c.cursorHotspotX, c.cursorHotspotY = hotspotX, hotspotY
		if shapeErr != nil {
			log.Printf("[Desktop] capture Windows cursor shape failed: %v", shapeErr)
		}
	}
	state.PNG = append([]byte(nil), c.cursorPNG...)
	state.Width, state.Height = c.cursorWidth, c.cursorHeight
	state.HotspotX, state.HotspotY = c.cursorHotspotX, c.cursorHotspotY
	return state, nil
}

type windowsGPUFormatProbe struct {
	Format     string
	Encode     bool
	Decode     bool
	Display    bool
	EncodeErr  error
	DecodeErr  error
	DisplayErr error
}

func (p windowsGPUFormatProbe) endToEnd() bool {
	return p.Encode && p.Decode && p.Display
}

func windowsGPUProbeVideoConfig(frame *D3D11CaptureFrame, chroma desktopcodec.ChromaFormat) (desktopcodec.VideoConfig, error) {
	if frame == nil || !frame.Valid() {
		return desktopcodec.VideoConfig{}, desktopcodec.ErrInvalidFrame
	}
	cfg := desktopcodec.DefaultVideoConfig()
	cfg.Width = frame.Width
	if cfg.Width > 1280 {
		cfg.Width = 1280
	}
	cfg.Height = frame.Height
	if cfg.Height > 720 {
		cfg.Height = 720
	}
	cfg.Width &^= 1
	cfg.Height &^= 1
	cfg.Chroma = chroma
	cfg.BitDepth = 8
	return desktopcodec.NormalizeVideoConfig(cfg)
}

func probeWindowsNV12GPU(ctx context.Context, frame *D3D11CaptureFrame) windowsGPUFormatProbe {
	result := windowsGPUFormatProbe{Format: string(desktopcodec.PixelFormatNV12)}
	cfg, err := windowsGPUProbeVideoConfig(frame, desktopcodec.Chroma420)
	if err != nil {
		result.EncodeErr, result.DecodeErr, result.DisplayErr = err, err, err
		return result
	}
	convertCfg := desktopcodec.D3D11ConvertConfig{
		InputWidth:   frame.Width,
		InputHeight:  frame.Height,
		OutputWidth:  cfg.Width,
		OutputHeight: cfg.Height,
		FPS:          cfg.FPS,
	}
	converter, err := desktopcodec.OpenD3D11NV12Converter(frame.Device, convertCfg)
	if err != nil {
		result.EncodeErr = err
	} else {
		converted, convertErr := converter.Convert(frame.Resource, frame.Subresource, 0)
		if convertErr != nil {
			result.EncodeErr = convertErr
		} else {
			encoder, encoderErr := desktopcodec.OpenMFH264EncoderWithD3D11(ctx, cfg, true, frame.Device)
			if encoderErr != nil {
				result.EncodeErr = encoderErr
			} else {
				stats := encoder.Stats()
				if !stats.Hardware || stats.Backend != "media-foundation-d3d11" {
					result.EncodeErr = fmt.Errorf("Media Foundation H.264 D3D11 encoder backend=%q hardware=%t", stats.Backend, stats.Hardware)
				} else if _, encodeErr := encoder.EncodeD3D11(ctx, converted); encodeErr != nil {
					result.EncodeErr = encodeErr
				} else {
					result.Encode = true
				}
				_ = encoder.Close()
			}
		}
		_ = converter.Close()
	}

	decoder, err := desktopcodec.OpenMFH264DecoderWithD3D11(ctx, cfg, true, frame.Device)
	if err != nil {
		result.DecodeErr = err
	} else {
		backend := decoder.Backend()
		if !decoder.Hardware() || backend != "media-foundation-d3d11-zero-copy" {
			result.DecodeErr = fmt.Errorf("Media Foundation H.264 decoder backend=%q hardware=%t", backend, decoder.Hardware())
		} else {
			result.Decode = true
		}
		_ = decoder.Close()
	}

	displayCfg := desktopcodec.D3D11ConvertConfig{
		InputWidth: cfg.Width, InputHeight: cfg.Height,
		OutputWidth: cfg.Width, OutputHeight: cfg.Height,
		FPS: cfg.FPS,
	}
	if err := desktopcodec.ProbeD3D11DisplayFormat(frame.Device, displayCfg, desktopcodec.PixelFormatNV12); err != nil {
		result.DisplayErr = err
	} else {
		result.Display = true
	}
	return result
}

func probeWindowsAYUVGPU(
	ctx context.Context,
	frame *D3D11CaptureFrame,
	backends []desktopcodec.H265444BackendProbe,
) windowsGPUFormatProbe {
	result := windowsGPUFormatProbe{Format: string(desktopcodec.PixelFormatAYUV)}
	cfg, err := windowsGPUProbeVideoConfig(frame, desktopcodec.Chroma444)
	if err != nil {
		result.EncodeErr, result.DecodeErr, result.DisplayErr = err, err, err
		return result
	}
	if !desktopcodec.H265444EncodeAvailable(backends) {
		result.EncodeErr = desktopcodec.ErrEncoderUnavailable
	} else {
		convertCfg := desktopcodec.D3D11ConvertConfig{
			InputWidth:   frame.Width,
			InputHeight:  frame.Height,
			OutputWidth:  cfg.Width,
			OutputHeight: cfg.Height,
			FPS:          cfg.FPS,
		}
		converter, convertErr := desktopcodec.OpenD3D11AYUVConverter(frame.Device, convertCfg)
		if convertErr != nil {
			result.EncodeErr = convertErr
		} else {
			converted, convertErr := converter.Convert(frame.Resource, frame.Subresource, 0)
			if convertErr != nil {
				result.EncodeErr = convertErr
			} else {
				encoder, encoderErr := desktopcodec.OpenH265444EncoderWithD3D11(ctx, cfg, frame.Device)
				if encoderErr != nil {
					result.EncodeErr = encoderErr
				} else {
					stats := encoder.Stats()
					d3dEncoder, ok := encoder.(desktopcodec.D3D11Encoder)
					if !ok || !stats.Hardware {
						result.EncodeErr = fmt.Errorf("HEVC 4:4:4 encoder backend=%q hardware=%t d3d11=%t", stats.Backend, stats.Hardware, ok)
					} else if _, encodeErr := d3dEncoder.EncodeD3D11(ctx, converted); encodeErr != nil {
						result.EncodeErr = encodeErr
					} else {
						result.Encode = true
					}
					_ = encoder.Close()
				}
			}
			_ = converter.Close()
		}
	}

	if !desktopcodec.H265444DecodeAvailable(backends) {
		result.DecodeErr = desktopcodec.ErrDecoderUnavailable
	} else if err := desktopcodec.ProbeH265444DecoderD3D11(ctx, cfg, frame.Device); err != nil {
		result.DecodeErr = err
	} else {
		result.Decode = true
	}

	displayCfg := desktopcodec.D3D11ConvertConfig{
		InputWidth: cfg.Width, InputHeight: cfg.Height,
		OutputWidth: cfg.Width, OutputHeight: cfg.Height,
		FPS: cfg.FPS,
	}
	if err := desktopcodec.ProbeD3D11DisplayFormat(frame.Device, displayCfg, desktopcodec.PixelFormatAYUV); err != nil {
		result.DisplayErr = err
	} else {
		result.Display = true
	}
	return result
}

func captureWindowsGPUProbeFrame(
	ctx context.Context,
	source *windowsCapture,
) (*D3D11CaptureFrame, bool, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		frame, available, err := source.CaptureD3D11(ctx)
		if err == nil {
			if available && frame != nil && frame.Valid() {
				return frame, true, nil
			}
			if frame != nil {
				frame.Close()
			}
			return nil, available, screencapture.ErrNoFrame
		}
		if frame != nil {
			frame.Close()
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		if !errors.Is(err, screencapture.ErrNoFrame) && !errors.Is(err, context.DeadlineExceeded) {
			return nil, false, err
		}
	}
	if lastErr == nil {
		lastErr = screencapture.ErrNoFrame
	}
	return nil, false, lastErr
}

func probeWindowsGPUCapability(
	ctx context.Context,
	h265444Backends []desktopcodec.H265444BackendProbe,
) (*protocol.DesktopGPUCapability, []windowsGPUFormatProbe, error) {
	displays, err := screencapture.Displays(ctx)
	if err != nil {
		return nil, nil, err
	}
	hasWGC := windowsWGCAvailable()
	var failures []error
	for pass := 0; pass < 2; pass++ {
		for _, display := range displays {
			if (pass == 0) != display.Primary {
				continue
			}
			backend := protocol.DesktopCaptureWGC
			if display.Duplicable() {
				backend = protocol.DesktopCaptureDXGI
			} else if !hasWGC {
				continue
			}
			source, sourceErr := newSystemCapture()
			if sourceErr != nil {
				failures = append(failures, sourceErr)
				continue
			}
			cfg := DefaultHostConfig()
			cfg.DisplayID = strconv.FormatUint(display.ID, 10)
			cfg.CaptureBackend = backend
			if beginErr := source.BeginSession(ctx, cfg); beginErr != nil {
				_ = source.Close()
				failures = append(failures, beginErr)
				continue
			}
			frame, available, captureErr := captureWindowsGPUProbeFrame(ctx, source)
			if captureErr != nil || !available || frame == nil || !frame.Valid() {
				if frame != nil {
					frame.Close()
				}
				_ = source.EndSession()
				_ = source.Close()
				if captureErr != nil {
					failures = append(failures, captureErr)
				}
				continue
			}

			results := []windowsGPUFormatProbe{
				probeWindowsNV12GPU(ctx, frame),
				probeWindowsAYUVGPU(ctx, frame, h265444Backends),
			}
			frame.Close()
			_ = source.EndSession()
			_ = source.Close()

			formats := make([]string, 0, len(results))
			for _, result := range results {
				if result.endToEnd() {
					formats = append(formats, result.Format)
				}
			}
			if len(formats) == 0 {
				return nil, results, nil
			}
			return &protocol.DesktopGPUCapability{
				Backend:         "d3d11",
				EncodeZeroCopy:  true,
				DecodeZeroCopy:  true,
				DisplayZeroCopy: true,
				Formats:         formats,
			}, results, nil
		}
	}
	if len(failures) > 0 {
		return nil, nil, errors.Join(failures...)
	}
	return nil, nil, nil
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
	host.SetSessionFactory(func() (CaptureSource, InputSink, error) {
		sessionSource, err := newSystemCapture()
		if err != nil {
			return nil, nil, err
		}
		return sessionSource, newWindowsInputSink(), nil
	})
	probeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	probe := desktopcodec.ProbeH264MediaFoundation(probeCtx)
	cancel()

	hevcProbeCtx, hevcCancel := context.WithTimeout(context.Background(), 3*time.Second)
	hevcProbe := desktopcodec.ProbeH265MediaFoundation(hevcProbeCtx)
	hevcCancel()

	h265444ProbeCtx, h265444Cancel := context.WithTimeout(context.Background(), 3*time.Second)
	h265444Backends := desktopcodec.ProbeH265444Backends(h265444ProbeCtx)
	h265444Cancel()

	h265444CandidateCtx, h265444CandidateCancel := context.WithTimeout(context.Background(), 3*time.Second)
	h265444Candidates := desktopcodec.ProbeH265444RuntimeCandidates(h265444CandidateCtx)
	h265444CandidateCancel()

	gpuProbeCtx, gpuProbeCancel := context.WithTimeout(context.Background(), 6*time.Second)
	gpuCapability, gpuFormats, gpuProbeErr := probeWindowsGPUCapability(gpuProbeCtx, h265444Backends)
	gpuProbeCancel()

	codecCapabilities := []protocol.DesktopCodecCapability{probe.Capability()}
	hevcCapability, hevcAvailable := desktopcodec.H265CapabilityWith444Backends(hevcProbe, h265444Backends)
	if hevcAvailable {
		codecCapabilities = append(codecCapabilities, hevcCapability)
	}
	host.SetCodecCapabilities(codecCapabilities)
	host.SetGPUCapability(gpuCapability)

	log.Printf("[Desktop] Media Foundation H.264 probe mf=%t hwEnc=%d hwDec=%d swEnc=%d swDec=%d error=%q",
		probe.MediaFoundation, probe.HardwareEncoderCount, probe.HardwareDecoderCount,
		probe.SoftwareEncoderCount, probe.SoftwareDecoderCount, probe.Error)
	log.Printf("[Desktop] Media Foundation H.265 probe mf=%t hwEnc=%d hwDec=%d swEnc=%d swDec=%d advertised=%t error=%q",
		hevcProbe.MediaFoundation, hevcProbe.HardwareEncoderCount, hevcProbe.HardwareDecoderCount,
		hevcProbe.SoftwareEncoderCount, hevcProbe.SoftwareDecoderCount,
		hevcProbe.EncodeAvailable() || hevcProbe.DecodeAvailable(), hevcProbe.Error)
	for _, backendProbe := range h265444Backends {
		log.Printf("[Desktop] HEVC 4:4:4 backend probe backend=%s hwRuntime=%t encode=%t decode=%t endToEnd=%t advertised=%t error=%q",
			backendProbe.Backend, backendProbe.HardwareRuntime,
			backendProbe.Encode, backendProbe.Decode,
			backendProbe.EndToEnd(), hevcCapability.Chroma444, backendProbe.Error)
	}
	for _, candidate := range h265444Candidates {
		log.Printf("[Desktop] HEVC 4:4:4 candidate runtime vendor=%s backend=%s runtime=%t encodeRuntime=%t decodeRuntime=%t deviceProbe=%t devices=%d hevc444Encode=%t hevc444Decode=%t version=%q implemented=%t advertised=false error=%q",
			candidate.Vendor, candidate.Backend, candidate.RuntimeAvailable,
			candidate.EncodeRuntime, candidate.DecodeRuntime,
			candidate.DeviceProbe, candidate.DeviceCount,
			candidate.HEVC444Encode, candidate.HEVC444Decode,
			candidate.Version, candidate.Implemented, candidate.Error)
	}
	for _, gpuFormat := range gpuFormats {
		log.Printf("[Desktop] D3D11 GPU format probe format=%s encode=%t decode=%t display=%t encodeErr=%v decodeErr=%v displayErr=%v",
			gpuFormat.Format, gpuFormat.Encode, gpuFormat.Decode, gpuFormat.Display,
			gpuFormat.EncodeErr, gpuFormat.DecodeErr, gpuFormat.DisplayErr)
	}
	if gpuCapability != nil {
		log.Printf("[Desktop] D3D11 GPU capability encodeZeroCopy=%t decodeZeroCopy=%t displayZeroCopy=%t formats=%v",
			gpuCapability.EncodeZeroCopy, gpuCapability.DecodeZeroCopy, gpuCapability.DisplayZeroCopy, gpuCapability.Formats)
	} else if gpuProbeErr != nil {
		log.Printf("[Desktop] D3D11 GPU runtime probe unavailable: %v", gpuProbeErr)
	} else {
		log.Printf("[Desktop] D3D11 GPU runtime probe found no end-to-end zero-copy format")
	}
	return host, nil
}
