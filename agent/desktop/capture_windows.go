//go:build windows

package desktop

import (
	"bytes"
	"context"
	"errors"
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
// screencapture (DXGI when available, GDI otherwise) and exposes the same
// monitor geometry to the cursor channel.
type windowsCapture struct {
	mu       sync.Mutex
	cursorMu sync.Mutex

	gdi             *gdiCapture
	stream          *screencapture.Stream
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
	return &windowsCapture{gdi: gdi, backend: "gdi"}, nil
}

func windowsDesktopCapabilitySnapshot(displays []screencapture.Display) ([]protocol.DesktopCaptureCapability, []protocol.DesktopDisplayCapability) {
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
	return captures, out
}

func (c *windowsCapture) DesktopCaptureCapabilities(ctx context.Context) ([]protocol.DesktopCaptureCapability, []protocol.DesktopDisplayCapability, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	displays, err := screencapture.Displays(ctx)
	if err != nil {
		return nil, nil, err
	}
	captures, capabilities := windowsDesktopCapabilitySnapshot(displays)
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

func (c *windowsCapture) BeginSession(ctx context.Context, cfg HostConfig) error {
	displays, listErr := screencapture.Displays(ctx)
	target, selected, selectErr := resolveWindowsDisplay(displays, cfg.DisplayID)
	if selectErr != nil {
		return selectErr
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("Windows desktop capture is closed")
	}
	c.closeStreamLocked()
	c.selectedDisplay = nil
	c.backend = "gdi"

	if listErr != nil {
		if selected {
			return fmt.Errorf("enumerate Windows displays: %w", listErr)
		}
		log.Printf("[Desktop] display enumeration unavailable, using virtual desktop GDI: %v", listErr)
		return nil
	}
	if target.ID == 0 {
		return nil
	}

	stream, err := screencapture.CaptureDisplay(ctx, target, screencapture.Options{
		Backend:    screencapture.BackendAuto,
		FPS:        float64(cfg.MaxFPS),
		QueueDepth: screencapture.MinQueueDepth,
		Timeout:    100 * time.Millisecond,
	})
	if err != nil {
		if selected {
			return fmt.Errorf("capture selected Windows display %q: %w", cfg.DisplayID, err)
		}
		log.Printf("[Desktop] per-display capture unavailable, using virtual desktop GDI: %v", err)
		return nil
	}
	c.stream = stream
	if stream.Backend() == screencapture.BackendDuplication {
		c.backend = "dxgi"
	} else {
		c.backend = "gdi"
	}
	if selected {
		copy := target
		c.selectedDisplay = &copy
	}
	c.frame = nil
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

func (c *windowsCapture) closeStreamLocked() {
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

func (c *windowsCapture) captureStreamLocked(ctx context.Context) (*image.RGBA, error) {
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
		frame, err := c.captureStreamLocked(ctx)
		if err == nil {
			return frame, nil
		}
		if !errors.Is(err, screencapture.ErrNoFrame) {
			log.Printf("[Desktop] DXGI capture failed, switching session to GDI: %v", err)
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
	screenX := win.GetSystemMetrics(win.SM_XVIRTUALSCREEN)
	screenY := win.GetSystemMetrics(win.SM_YVIRTUALSCREEN)
	screenWidth := win.GetSystemMetrics(win.SM_CXVIRTUALSCREEN)
	screenHeight := win.GetSystemMetrics(win.SM_CYVIRTUALSCREEN)
	if screenWidth <= 0 || screenHeight <= 0 {
		return protocol.DesktopCursorState{}, errors.New("invalid Windows virtual desktop geometry")
	}

	state := protocol.DesktopCursorState{
		X:            int(info.ScreenPos.X - screenX),
		Y:            int(info.ScreenPos.Y - screenY),
		ScreenWidth:  int(screenWidth),
		ScreenHeight: int(screenHeight),
		Visible:      info.Flags&cursorShowing != 0,
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
