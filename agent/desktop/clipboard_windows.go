//go:build windows

package desktop

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"github.com/lxn/win"
	"golang.org/x/sys/windows"

	"relayproxy/internal/protocol"
)

var (
	clipboardKernel32DLL = windows.NewLazySystemDLL("kernel32.dll")
	clipboardShell32DLL  = windows.NewLazySystemDLL("shell32.dll")
	procGlobalSize       = clipboardKernel32DLL.NewProc("GlobalSize")
	procDragQueryFileW   = clipboardShell32DLL.NewProc("DragQueryFileW")
)

const windowsClipboardFormatHDrop = 15

func openWindowsClipboard(ctx context.Context) error {
	var lastErr error
	for attempt := 0; attempt < 10; attempt++ {
		if win.OpenClipboard(0) {
			return nil
		}
		lastErr = errors.New("OpenClipboard failed")
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return lastErr
}

func globalMemorySize(handle win.HANDLE) uintptr {
	size, _, _ := procGlobalSize.Call(uintptr(handle))
	return size
}

func readWindowsClipboardText(ctx context.Context) (string, error) {
	if !win.IsClipboardFormatAvailable(win.CF_UNICODETEXT) {
		return "", ErrClipboardTextUnavailable
	}
	if err := openWindowsClipboard(ctx); err != nil {
		return "", err
	}
	defer win.CloseClipboard()

	handle := win.GetClipboardData(win.CF_UNICODETEXT)
	if handle == 0 {
		return "", errors.New("GetClipboardData(CF_UNICODETEXT) failed")
	}
	size := globalMemorySize(handle)
	if size < 2 {
		return "", nil
	}
	if size > uintptr(protocol.MaxDesktopClipboardBytes*2+2) {
		return "", fmt.Errorf("Windows clipboard text buffer is too large: %d bytes", size)
	}

	ptr := win.GlobalLock(win.HGLOBAL(handle))
	if ptr == nil {
		return "", errors.New("GlobalLock clipboard data failed")
	}
	defer win.GlobalUnlock(win.HGLOBAL(handle))

	units := unsafe.Slice((*uint16)(ptr), int(size/2))
	text := windows.UTF16ToString(units)
	return validateClipboardText(text)
}

func writeWindowsClipboardText(ctx context.Context, text string) error {
	text, err := validateClipboardText(text)
	if err != nil {
		return err
	}
	units, err := windows.UTF16FromString(text)
	if err != nil {
		return err
	}
	bytes := uintptr(len(units) * 2)
	memory := win.GlobalAlloc(win.GMEM_MOVEABLE|win.GMEM_ZEROINIT, bytes)
	if memory == 0 {
		return errors.New("GlobalAlloc clipboard data failed")
	}
	owned := true
	defer func() {
		if owned {
			win.GlobalFree(memory)
		}
	}()

	ptr := win.GlobalLock(memory)
	if ptr == nil {
		return errors.New("GlobalLock clipboard write buffer failed")
	}
	copy(unsafe.Slice((*uint16)(ptr), len(units)), units)
	win.GlobalUnlock(memory)

	if err := openWindowsClipboard(ctx); err != nil {
		return err
	}
	defer win.CloseClipboard()
	if !win.EmptyClipboard() {
		return errors.New("EmptyClipboard failed")
	}
	if win.SetClipboardData(win.CF_UNICODETEXT, win.HANDLE(memory)) == 0 {
		return errors.New("SetClipboardData(CF_UNICODETEXT) failed")
	}
	owned = false
	return nil
}

func (c *windowsCapture) ClipboardText(ctx context.Context) (string, error) {
	if c == nil {
		return "", errors.New("Windows desktop capture is unavailable")
	}
	return readWindowsClipboardText(ctx)
}

func (c *windowsCapture) SetClipboardText(ctx context.Context, text string) error {
	if c == nil {
		return errors.New("Windows desktop capture is unavailable")
	}
	return writeWindowsClipboardText(ctx, text)
}

const (
	maxWindowsClipboardDIBBytes = 64 << 20
	maxWindowsClipboardPixels   = 16 * 1024 * 1024
)

func readWindowsClipboardDIB(ctx context.Context) ([]byte, error) {
	if !win.IsClipboardFormatAvailable(win.CF_DIB) {
		return nil, ErrClipboardImageUnavailable
	}
	if err := openWindowsClipboard(ctx); err != nil {
		return nil, err
	}
	defer win.CloseClipboard()

	handle := win.GetClipboardData(win.CF_DIB)
	if handle == 0 {
		return nil, errors.New("GetClipboardData(CF_DIB) failed")
	}
	size := globalMemorySize(handle)
	if size < 40 {
		return nil, errors.New("Windows clipboard DIB is too small")
	}
	if size > maxWindowsClipboardDIBBytes {
		return nil, fmt.Errorf("Windows clipboard DIB is too large: %d bytes", size)
	}
	ptr := win.GlobalLock(win.HGLOBAL(handle))
	if ptr == nil {
		return nil, errors.New("GlobalLock clipboard DIB failed")
	}
	defer win.GlobalUnlock(win.HGLOBAL(handle))

	raw := unsafe.Slice((*byte)(ptr), int(size))
	out := make([]byte, len(raw))
	copy(out, raw)
	return out, nil
}

func decodeWindowsClipboardDIB(raw []byte) (image.Image, error) {
	if len(raw) < 40 {
		return nil, errors.New("clipboard DIB header is truncated")
	}
	headerSize := int(binary.LittleEndian.Uint32(raw[0:4]))
	if headerSize < 40 || headerSize > len(raw) {
		return nil, fmt.Errorf("unsupported clipboard DIB header size %d", headerSize)
	}
	width := int(int32(binary.LittleEndian.Uint32(raw[4:8])))
	signedHeight := int(int32(binary.LittleEndian.Uint32(raw[8:12])))
	planes := binary.LittleEndian.Uint16(raw[12:14])
	bitCount := int(binary.LittleEndian.Uint16(raw[14:16]))
	compression := binary.LittleEndian.Uint32(raw[16:20])
	if planes != 1 || width <= 0 || signedHeight == 0 {
		return nil, errors.New("invalid clipboard DIB dimensions")
	}
	if compression != 0 {
		return nil, fmt.Errorf("unsupported clipboard DIB compression %d", compression)
	}
	if bitCount != 24 && bitCount != 32 {
		return nil, fmt.Errorf("unsupported clipboard DIB bit depth %d", bitCount)
	}
	height := signedHeight
	topDown := false
	if height < 0 {
		height = -height
		topDown = true
	}
	if height <= 0 || width > 16384 || height > 16384 || width*height > maxWindowsClipboardPixels {
		return nil, fmt.Errorf("clipboard DIB dimensions %dx%d exceed limits", width, height)
	}
	rowBytes := ((width*bitCount + 31) / 32) * 4
	needed := headerSize + rowBytes*height
	if rowBytes <= 0 || needed > len(raw) {
		return nil, errors.New("clipboard DIB pixel data is truncated")
	}

	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		srcY := y
		if !topDown {
			srcY = height - 1 - y
		}
		row := raw[headerSize+srcY*rowBytes : headerSize+(srcY+1)*rowBytes]
		for x := 0; x < width; x++ {
			off := x * (bitCount / 8)
			b, g, rr := row[off], row[off+1], row[off+2]
			a := byte(255)
			if bitCount == 32 && row[off+3] != 0 {
				a = row[off+3]
			}
			dst.SetRGBA(x, y, color.RGBA{R: rr, G: g, B: b, A: a})
		}
	}
	return dst, nil
}

func encodeWindowsClipboardDIB(src image.Image) ([]byte, error) {
	if src == nil {
		return nil, errors.New("clipboard image is nil")
	}
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= 0 || height <= 0 || width > 16384 || height > 16384 || width*height > maxWindowsClipboardPixels {
		return nil, fmt.Errorf("clipboard image dimensions %dx%d exceed limits", width, height)
	}
	rowBytes := width * 4
	raw := make([]byte, 40+rowBytes*height)
	binary.LittleEndian.PutUint32(raw[0:4], 40)
	binary.LittleEndian.PutUint32(raw[4:8], uint32(int32(width)))
	binary.LittleEndian.PutUint32(raw[8:12], uint32(int32(height)))
	binary.LittleEndian.PutUint16(raw[12:14], 1)
	binary.LittleEndian.PutUint16(raw[14:16], 32)
	binary.LittleEndian.PutUint32(raw[20:24], uint32(rowBytes*height))
	for y := 0; y < height; y++ {
		dstY := height - 1 - y
		row := raw[40+dstY*rowBytes : 40+(dstY+1)*rowBytes]
		for x := 0; x < width; x++ {
			rr, g, b, a := src.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			off := x * 4
			row[off] = byte(b >> 8)
			row[off+1] = byte(g >> 8)
			row[off+2] = byte(rr >> 8)
			row[off+3] = byte(a >> 8)
		}
	}
	return raw, nil
}

func readWindowsClipboardFiles(ctx context.Context) ([]string, error) {
	if !win.IsClipboardFormatAvailable(windowsClipboardFormatHDrop) {
		return nil, ErrClipboardFilesUnavailable
	}
	if err := openWindowsClipboard(ctx); err != nil {
		return nil, err
	}
	defer win.CloseClipboard()
	handle := win.GetClipboardData(windowsClipboardFormatHDrop)
	if handle == 0 {
		return nil, errors.New("GetClipboardData(CF_HDROP) failed")
	}
	count, _, _ := procDragQueryFileW.Call(uintptr(handle), ^uintptr(0), 0, 0)
	if count == 0 {
		return nil, ErrClipboardFilesUnavailable
	}
	if count > maxDesktopClipboardRoots {
		return nil, fmt.Errorf("clipboard contains %d top-level items; maximum is %d", count, maxDesktopClipboardRoots)
	}
	paths := make([]string, 0, int(count))
	for i := uintptr(0); i < count; i++ {
		length, _, _ := procDragQueryFileW.Call(uintptr(handle), i, 0, 0)
		if length == 0 || length > 32767 {
			return nil, errors.New("invalid CF_HDROP file path length")
		}
		buf := make([]uint16, int(length)+1)
		got, _, _ := procDragQueryFileW.Call(
			uintptr(handle), i, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)),
		)
		if got != length {
			return nil, errors.New("DragQueryFileW returned a truncated path")
		}
		path := windows.UTF16ToString(buf)
		if strings.TrimSpace(path) == "" {
			return nil, errors.New("CF_HDROP contains an empty file path")
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func writeWindowsClipboardFiles(ctx context.Context, paths []string) error {
	if len(paths) == 0 || len(paths) > maxDesktopClipboardRoots {
		return fmt.Errorf("clipboard root count must be 1..%d", maxDesktopClipboardRoots)
	}
	var units []uint16
	for _, path := range paths {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		info, err := os.Stat(absolute)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			return fmt.Errorf("clipboard path %q is neither a regular file nor directory", absolute)
		}
		encoded, err := windows.UTF16FromString(absolute)
		if err != nil {
			return err
		}
		units = append(units, encoded...)
	}
	units = append(units, 0)

	const dropFilesHeaderBytes = 20
	totalBytes := dropFilesHeaderBytes + len(units)*2
	memory := win.GlobalAlloc(win.GMEM_MOVEABLE|win.GMEM_ZEROINIT, uintptr(totalBytes))
	if memory == 0 {
		return errors.New("GlobalAlloc CF_HDROP failed")
	}
	owned := true
	defer func() {
		if owned {
			win.GlobalFree(memory)
		}
	}()
	ptr := win.GlobalLock(memory)
	if ptr == nil {
		return errors.New("GlobalLock CF_HDROP failed")
	}
	raw := unsafe.Slice((*byte)(ptr), totalBytes)
	binary.LittleEndian.PutUint32(raw[0:4], dropFilesHeaderBytes)
	binary.LittleEndian.PutUint32(raw[16:20], 1)
	dst := unsafe.Slice((*uint16)(unsafe.Pointer(uintptr(ptr)+dropFilesHeaderBytes)), len(units))
	copy(dst, units)
	win.GlobalUnlock(memory)

	if err := openWindowsClipboard(ctx); err != nil {
		return err
	}
	defer win.CloseClipboard()
	if !win.EmptyClipboard() {
		return errors.New("EmptyClipboard failed")
	}
	if win.SetClipboardData(windowsClipboardFormatHDrop, win.HANDLE(memory)) == 0 {
		return errors.New("SetClipboardData(CF_HDROP) failed")
	}
	owned = false
	return nil
}

func readWindowsClipboardContent(ctx context.Context) (protocol.DesktopClipboardState, error) {
	if win.IsClipboardFormatAvailable(windowsClipboardFormatHDrop) {
		paths, err := readWindowsClipboardFiles(ctx)
		if err == nil {
			roots := make([]protocol.DesktopClipboardRoot, 0, len(paths))
			files := make([]protocol.DesktopClipboardFile, 0, len(paths))
			for _, clipboardPath := range paths {
				info, statErr := os.Stat(clipboardPath)
				if statErr != nil {
					return protocol.DesktopClipboardState{}, statErr
				}
				name := filepath.Base(clipboardPath)
				roots = append(roots, protocol.DesktopClipboardRoot{
					Name:      name,
					Directory: info.IsDir(),
				})
				if info.Mode().IsRegular() {
					files = append(files, protocol.DesktopClipboardFile{
						Name: name,
						Path: name,
						Size: info.Size(),
					})
				}
			}
			return protocol.DesktopClipboardState{
				Kind:       protocol.DesktopClipboardKindFiles,
				Roots:      roots,
				Files:      files,
				LocalPaths: paths,
			}, nil
		}
		if !errors.Is(err, ErrClipboardFilesUnavailable) {
			return protocol.DesktopClipboardState{}, err
		}
	}
	if win.IsClipboardFormatAvailable(win.CF_UNICODETEXT) {
		text, err := readWindowsClipboardText(ctx)
		if err == nil {
			return validateClipboardContent(protocol.DesktopClipboardState{
				Kind: protocol.DesktopClipboardKindText,
				Text: text,
			})
		}
		if !errors.Is(err, ErrClipboardTextUnavailable) {
			return protocol.DesktopClipboardState{}, err
		}
	}
	raw, err := readWindowsClipboardDIB(ctx)
	if err != nil {
		return protocol.DesktopClipboardState{}, err
	}
	img, err := decodeWindowsClipboardDIB(raw)
	if err != nil {
		return protocol.DesktopClipboardState{}, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return protocol.DesktopClipboardState{}, err
	}
	return validateClipboardContent(protocol.DesktopClipboardState{
		Kind: protocol.DesktopClipboardKindPNG,
		PNG:  buf.Bytes(),
	})
}

func writeWindowsClipboardContent(ctx context.Context, content protocol.DesktopClipboardState) error {
	content, err := validateClipboardContent(content)
	if err != nil {
		return err
	}
	if content.Kind == protocol.DesktopClipboardKindText {
		return writeWindowsClipboardText(ctx, content.Text)
	}
	if content.Kind == protocol.DesktopClipboardKindFiles {
		return writeWindowsClipboardFiles(ctx, content.LocalPaths)
	}
	img, err := png.Decode(bytes.NewReader(content.PNG))
	if err != nil {
		return err
	}
	raw, err := encodeWindowsClipboardDIB(img)
	if err != nil {
		return err
	}
	memory := win.GlobalAlloc(win.GMEM_MOVEABLE|win.GMEM_ZEROINIT, uintptr(len(raw)))
	if memory == 0 {
		return errors.New("GlobalAlloc clipboard DIB failed")
	}
	owned := true
	defer func() {
		if owned {
			win.GlobalFree(memory)
		}
	}()
	ptr := win.GlobalLock(memory)
	if ptr == nil {
		return errors.New("GlobalLock clipboard DIB write buffer failed")
	}
	copy(unsafe.Slice((*byte)(ptr), len(raw)), raw)
	win.GlobalUnlock(memory)

	if err := openWindowsClipboard(ctx); err != nil {
		return err
	}
	defer win.CloseClipboard()
	if !win.EmptyClipboard() {
		return errors.New("EmptyClipboard failed")
	}
	if win.SetClipboardData(win.CF_DIB, win.HANDLE(memory)) == 0 {
		return errors.New("SetClipboardData(CF_DIB) failed")
	}
	owned = false
	return nil
}

func ReadWindowsClipboardContent(ctx context.Context) (protocol.DesktopClipboardState, error) {
	return readWindowsClipboardContent(ctx)
}

func WriteWindowsClipboardContent(ctx context.Context, content protocol.DesktopClipboardState) error {
	return writeWindowsClipboardContent(ctx, content)
}

func ReadWindowsClipboardFiles(ctx context.Context) ([]string, error) {
	return readWindowsClipboardFiles(ctx)
}

func WriteWindowsClipboardFiles(ctx context.Context, paths []string) error {
	return writeWindowsClipboardFiles(ctx, paths)
}

func (c *windowsCapture) ClipboardContent(ctx context.Context) (protocol.DesktopClipboardState, error) {
	if c == nil {
		return protocol.DesktopClipboardState{}, errors.New("Windows desktop capture is unavailable")
	}
	return readWindowsClipboardContent(ctx)
}

func (c *windowsCapture) SetClipboardContent(ctx context.Context, content protocol.DesktopClipboardState) error {
	if c == nil {
		return errors.New("Windows desktop capture is unavailable")
	}
	return writeWindowsClipboardContent(ctx, content)
}

func (c *windowsCapture) ClipboardFiles(ctx context.Context) ([]string, error) {
	if c == nil {
		return nil, errors.New("Windows desktop capture is unavailable")
	}
	return c.readClipboardFilesWithVirtual(ctx)
}

func (c *windowsCapture) SetClipboardFiles(ctx context.Context, paths []string) error {
	if c == nil {
		return errors.New("Windows desktop capture is unavailable")
	}
	return writeWindowsClipboardFiles(ctx, paths)
}
