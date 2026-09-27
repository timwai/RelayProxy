//go:build windows

package desktop

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"github.com/lxn/win"
	"golang.org/x/sys/windows"
)

const (
	windowsVirtualClipboardFileGroupFormat = "FileGroupDescriptorW"
	windowsVirtualClipboardContentsFormat  = "FileContents"

	windowsTymedHGlobal = 1
	windowsTymedIStream = 4
	windowsDVAspectContent = 1

	windowsFDAttributes = 0x00000004
	windowsFDFileSize   = 0x00000040

	windowsFileAttributeDirectory = 0x00000010

	windowsVirtualDescriptorBytes    = 592
	windowsVirtualDescriptorNameUTF16 = 260
	maxWindowsVirtualDescriptors      = 4096
)

var (
	virtualClipboardUser32DLL = windows.NewLazySystemDLL("user32.dll")
	virtualClipboardOle32DLL  = windows.NewLazySystemDLL("ole32.dll")

	procRegisterClipboardFormatW        = virtualClipboardUser32DLL.NewProc("RegisterClipboardFormatW")
	procIsClipboardFormatAvailable      = virtualClipboardUser32DLL.NewProc("IsClipboardFormatAvailable")
	procGetClipboardSequenceNumber      = virtualClipboardUser32DLL.NewProc("GetClipboardSequenceNumber")
	procOleInitialize                   = virtualClipboardOle32DLL.NewProc("OleInitialize")
	procOleUninitialize                 = virtualClipboardOle32DLL.NewProc("OleUninitialize")
	procOleGetClipboard                 = virtualClipboardOle32DLL.NewProc("OleGetClipboard")
	procReleaseStgMedium                = virtualClipboardOle32DLL.NewProc("ReleaseStgMedium")
)

type windowsFormatEtc struct {
	CFFormat uint16
	PTD      uintptr
	DWAspect uint32
	LIndex   int32
	Tymed    uint32
}

type windowsStgMedium struct {
	Tymed          uint32
	Data           uintptr
	PUnkForRelease uintptr
}

type windowsVirtualClipboardDescriptor struct {
	RelativePath string
	Directory    bool
	HasSize      bool
	DeclaredSize uint64
}

func windowsClipboardSequenceNumber() uint32 {
	value, _, _ := procGetClipboardSequenceNumber.Call()
	return uint32(value)
}

func registerWindowsClipboardFormat(name string) (uint16, error) {
	ptr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	value, _, callErr := procRegisterClipboardFormatW.Call(uintptr(unsafe.Pointer(ptr)))
	if value == 0 {
		if callErr != nil && callErr != windows.ERROR_SUCCESS {
			return 0, callErr
		}
		return 0, fmt.Errorf("RegisterClipboardFormatW(%q) failed", name)
	}
	if value > 0xffff {
		return 0, fmt.Errorf("clipboard format %q returned invalid id %d", name, value)
	}
	return uint16(value), nil
}

func windowsClipboardFormatAvailable(format uint16) bool {
	value, _, _ := procIsClipboardFormatAvailable.Call(uintptr(format))
	return value != 0
}

func windowsHRESULTFailed(value uintptr) bool {
	return int32(uint32(value)) < 0
}

func windowsHRESULTError(operation string, value uintptr) error {
	return fmt.Errorf("%s failed: HRESULT 0x%08X", operation, uint32(value))
}

func windowsCOMMethod(object uintptr, index uintptr) uintptr {
	if object == 0 {
		return 0
	}
	vtable := *(*uintptr)(unsafe.Pointer(object))
	if vtable == 0 {
		return 0
	}
	return *(*uintptr)(unsafe.Pointer(vtable + index*unsafe.Sizeof(uintptr(0))))
}

func releaseWindowsCOMObject(object uintptr) {
	method := windowsCOMMethod(object, 2)
	if method == 0 {
		return
	}
	_, _, _ = syscall.SyscallN(method, object)
}

func getWindowsClipboardDataObject() (uintptr, error) {
	var object uintptr
	hr, _, _ := procOleGetClipboard.Call(uintptr(unsafe.Pointer(&object)))
	if windowsHRESULTFailed(hr) {
		return 0, windowsHRESULTError("OleGetClipboard", hr)
	}
	if object == 0 {
		return 0, ErrClipboardFilesUnavailable
	}
	return object, nil
}

func getWindowsDataObjectMedium(object uintptr, format windowsFormatEtc) (windowsStgMedium, error) {
	method := windowsCOMMethod(object, 3)
	if method == 0 {
		return windowsStgMedium{}, errors.New("IDataObject.GetData is unavailable")
	}
	var medium windowsStgMedium
	hr, _, _ := syscall.SyscallN(
		method,
		object,
		uintptr(unsafe.Pointer(&format)),
		uintptr(unsafe.Pointer(&medium)),
	)
	if windowsHRESULTFailed(hr) {
		return windowsStgMedium{}, windowsHRESULTError("IDataObject.GetData", hr)
	}
	return medium, nil
}

func releaseWindowsStgMedium(medium *windowsStgMedium) {
	if medium == nil {
		return
	}
	_, _, _ = procReleaseStgMedium.Call(uintptr(unsafe.Pointer(medium)))
	*medium = windowsStgMedium{}
}

func parseWindowsVirtualClipboardDescriptors(raw []byte) ([]windowsVirtualClipboardDescriptor, error) {
	if len(raw) < 4 {
		return nil, errors.New("virtual clipboard file descriptor group is truncated")
	}
	count := int(binary.LittleEndian.Uint32(raw[:4]))
	if count == 0 {
		return nil, ErrClipboardFilesUnavailable
	}
	if count > maxWindowsVirtualDescriptors {
		return nil, fmt.Errorf("virtual clipboard contains %d descriptors; maximum is %d", count, maxWindowsVirtualDescriptors)
	}
	required := 4 + count*windowsVirtualDescriptorBytes
	if required < 4 || required > len(raw) {
		return nil, errors.New("virtual clipboard file descriptor group is truncated")
	}

	descriptors := make([]windowsVirtualClipboardDescriptor, 0, count)
	seen := make(map[string]struct{}, count)
	fileCount := 0
	for index := 0; index < count; index++ {
		offset := 4 + index*windowsVirtualDescriptorBytes
		item := raw[offset : offset+windowsVirtualDescriptorBytes]
		flags := binary.LittleEndian.Uint32(item[0:4])
		attributes := binary.LittleEndian.Uint32(item[36:40])
		directory := attributes&windowsFileAttributeDirectory != 0

		nameUnits := make([]uint16, 0, windowsVirtualDescriptorNameUTF16)
		for pos := 0; pos < windowsVirtualDescriptorNameUTF16; pos++ {
			value := binary.LittleEndian.Uint16(item[72+pos*2 : 74+pos*2])
			if value == 0 {
				break
			}
			nameUnits = append(nameUnits, value)
		}
		name := windows.UTF16ToString(nameUnits)
		if directory {
			name = strings.TrimRight(name, "\\/")
		}
		name = strings.ReplaceAll(name, "\\", "/")
		relative, err := safeClipboardRelativePath(name)
		if err != nil {
			return nil, fmt.Errorf("virtual clipboard descriptor %d: %w", index, err)
		}
		key := strings.ToLower(relative)
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("duplicate virtual clipboard path %q", relative)
		}
		seen[key] = struct{}{}

		descriptor := windowsVirtualClipboardDescriptor{
			RelativePath: relative,
			Directory:    directory,
			HasSize:      flags&windowsFDFileSize != 0,
		}
		if descriptor.HasSize {
			high := uint64(binary.LittleEndian.Uint32(item[64:68]))
			low := uint64(binary.LittleEndian.Uint32(item[68:72]))
			descriptor.DeclaredSize = high<<32 | low
			if !directory && descriptor.DeclaredSize > maxDesktopClipboardFileBytes {
				return nil, fmt.Errorf("virtual clipboard file %q exceeds %d bytes", relative, maxDesktopClipboardFileBytes)
			}
		}
		if !directory {
			fileCount++
			if fileCount > maxDesktopClipboardFiles {
				return nil, fmt.Errorf("virtual clipboard file count exceeds %d", maxDesktopClipboardFiles)
			}
		}
		descriptors = append(descriptors, descriptor)
	}
	return descriptors, nil
}

func readWindowsVirtualDescriptorGroup(object uintptr, format uint16) ([]windowsVirtualClipboardDescriptor, error) {
	medium, err := getWindowsDataObjectMedium(object, windowsFormatEtc{
		CFFormat: format,
		DWAspect: windowsDVAspectContent,
		LIndex:   -1,
		Tymed:    windowsTymedHGlobal,
	})
	if err != nil {
		return nil, err
	}
	defer releaseWindowsStgMedium(&medium)
	if medium.Tymed != windowsTymedHGlobal || medium.Data == 0 {
		return nil, errors.New("virtual clipboard descriptor group is not backed by HGLOBAL")
	}
	size := globalMemorySize(win.HANDLE(medium.Data))
	if size < 4 {
		return nil, errors.New("virtual clipboard descriptor HGLOBAL is empty")
	}
	if size > uintptr(4+maxWindowsVirtualDescriptors*windowsVirtualDescriptorBytes) {
		return nil, errors.New("virtual clipboard descriptor HGLOBAL exceeds safety limit")
	}
	ptr := win.GlobalLock(win.HGLOBAL(medium.Data))
	if ptr == nil {
		return nil, errors.New("GlobalLock virtual clipboard descriptors failed")
	}
	raw := append([]byte(nil), unsafe.Slice((*byte)(ptr), int(size))...)
	win.GlobalUnlock(win.HGLOBAL(medium.Data))
	return parseWindowsVirtualClipboardDescriptors(raw)
}

func writeWindowsVirtualChunk(
	ctx context.Context,
	file *os.File,
	data []byte,
	fileBytes *int64,
	totalBytes *int64,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}
	nextFile := *fileBytes + int64(len(data))
	if nextFile > maxDesktopClipboardFileBytes {
		return fmt.Errorf("virtual clipboard file exceeds %d bytes", maxDesktopClipboardFileBytes)
	}
	nextTotal := *totalBytes + int64(len(data))
	if nextTotal > maxDesktopClipboardTransferBytes {
		return fmt.Errorf("virtual clipboard transfer exceeds %d bytes", maxDesktopClipboardTransferBytes)
	}
	written, err := file.Write(data)
	if err != nil {
		return err
	}
	if written != len(data) {
		return errors.New("short write while staging virtual clipboard file")
	}
	*fileBytes = nextFile
	*totalBytes = nextTotal
	return nil
}

func copyWindowsVirtualHGlobal(
	ctx context.Context,
	medium windowsStgMedium,
	file *os.File,
	fileBytes *int64,
	totalBytes *int64,
) error {
	if medium.Data == 0 {
		return errors.New("virtual clipboard HGLOBAL content is nil")
	}
	size := globalMemorySize(win.HANDLE(medium.Data))
	if size > uintptr(maxDesktopClipboardFileBytes) {
		return fmt.Errorf("virtual clipboard file exceeds %d bytes", maxDesktopClipboardFileBytes)
	}
	ptr := win.GlobalLock(win.HGLOBAL(medium.Data))
	if ptr == nil {
		return errors.New("GlobalLock virtual clipboard file failed")
	}
	defer win.GlobalUnlock(win.HGLOBAL(medium.Data))
	if size == 0 {
		return nil
	}
	data := unsafe.Slice((*byte)(ptr), int(size))
	return writeWindowsVirtualChunk(ctx, file, data, fileBytes, totalBytes)
}

func copyWindowsVirtualIStream(
	ctx context.Context,
	stream uintptr,
	file *os.File,
	fileBytes *int64,
	totalBytes *int64,
) error {
	readMethod := windowsCOMMethod(stream, 3)
	if readMethod == 0 {
		return errors.New("virtual clipboard IStream.Read is unavailable")
	}
	buffer := make([]byte, desktopClipboardFileChunkBytes)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var read uint32
		hr, _, _ := syscall.SyscallN(
			readMethod,
			stream,
			uintptr(unsafe.Pointer(&buffer[0])),
			uintptr(len(buffer)),
			uintptr(unsafe.Pointer(&read)),
		)
		if windowsHRESULTFailed(hr) {
			return windowsHRESULTError("IStream.Read", hr)
		}
		if read > uint32(len(buffer)) {
			return errors.New("virtual clipboard IStream returned an invalid byte count")
		}
		if read > 0 {
			if err := writeWindowsVirtualChunk(ctx, file, buffer[:read], fileBytes, totalBytes); err != nil {
				return err
			}
		}
		if read == 0 || uint32(hr) == 1 {
			return nil
		}
	}
}

func copyWindowsVirtualDescriptorContent(
	ctx context.Context,
	object uintptr,
	contentsFormat uint16,
	index int,
	file *os.File,
	totalBytes *int64,
) error {
	var lastErr error
	for _, tymed := range []uint32{windowsTymedIStream, windowsTymedHGlobal} {
		medium, err := getWindowsDataObjectMedium(object, windowsFormatEtc{
			CFFormat: contentsFormat,
			DWAspect: windowsDVAspectContent,
			LIndex:   int32(index),
			Tymed:    tymed,
		})
		if err != nil {
			lastErr = err
			continue
		}
		var fileBytes int64
		switch medium.Tymed {
		case windowsTymedIStream:
			if medium.Data == 0 {
				err = errors.New("virtual clipboard IStream content is nil")
			} else {
				err = copyWindowsVirtualIStream(ctx, medium.Data, file, &fileBytes, totalBytes)
			}
		case windowsTymedHGlobal:
			err = copyWindowsVirtualHGlobal(ctx, medium, file, &fileBytes, totalBytes)
		default:
			err = fmt.Errorf("unsupported virtual clipboard storage medium %d", medium.Tymed)
		}
		releaseWindowsStgMedium(&medium)
		if err == nil {
			return nil
		}
		lastErr = err
		if _, seekErr := file.Seek(0, 0); seekErr == nil {
			_ = file.Truncate(0)
		}
	}
	if lastErr == nil {
		lastErr = errors.New("virtual clipboard file content is unavailable")
	}
	return lastErr
}

func materializeWindowsVirtualClipboard(
	ctx context.Context,
	object uintptr,
	contentsFormat uint16,
	descriptors []windowsVirtualClipboardDescriptor,
) ([]string, string, error) {
	dir, err := os.MkdirTemp("", "relayproxy-virtual-clipboard-*")
	if err != nil {
		return nil, "", err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(dir)
		}
	}()

	type rootInfo struct {
		name      string
		directory bool
	}
	roots := make([]rootInfo, 0, maxDesktopClipboardRoots)
	rootIndex := make(map[string]int)
	for _, descriptor := range descriptors {
		parts := strings.Split(descriptor.RelativePath, "/")
		rootName := parts[0]
		rootKey := strings.ToLower(rootName)
		isDirectory := descriptor.Directory || len(parts) > 1
		if existing, ok := rootIndex[rootKey]; ok {
			if isDirectory && !roots[existing].directory {
				return nil, "", fmt.Errorf("virtual clipboard root %q is both a file and directory", rootName)
			}
			if isDirectory {
				roots[existing].directory = true
			}
			continue
		}
		if len(roots) >= maxDesktopClipboardRoots {
			return nil, "", fmt.Errorf("virtual clipboard root count exceeds %d", maxDesktopClipboardRoots)
		}
		rootIndex[rootKey] = len(roots)
		roots = append(roots, rootInfo{name: rootName, directory: isDirectory})
	}

	var totalBytes int64
	for descriptorIndex, descriptor := range descriptors {
		target := filepath.Join(dir, filepath.FromSlash(descriptor.RelativePath))
		relativeCheck, err := filepath.Rel(dir, target)
		if err != nil || relativeCheck == ".." || strings.HasPrefix(relativeCheck, ".."+string(os.PathSeparator)) {
			return nil, "", fmt.Errorf("virtual clipboard path %q escapes staging directory", descriptor.RelativePath)
		}
		if descriptor.Directory {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return nil, "", err
			}
			continue
		}
		if descriptor.HasSize && descriptor.DeclaredSize > maxDesktopClipboardFileBytes {
			return nil, "", fmt.Errorf("virtual clipboard file %q exceeds size limit", descriptor.RelativePath)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return nil, "", err
		}
		file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, "", err
		}
		if descriptor.HasSize && descriptor.DeclaredSize == 0 {
			err = file.Close()
		} else {
			err = copyWindowsVirtualDescriptorContent(ctx, object, contentsFormat, descriptorIndex, file, &totalBytes)
			closeErr := file.Close()
			if err == nil {
				err = closeErr
			}
		}
		if err != nil {
			return nil, "", fmt.Errorf("stage virtual clipboard file %q: %w", descriptor.RelativePath, err)
		}
	}

	paths := make([]string, 0, len(roots))
	for _, root := range roots {
		path := filepath.Join(dir, root.name)
		if root.directory {
			if err := os.MkdirAll(path, 0o700); err != nil {
				return nil, "", err
			}
		} else if _, err := os.Stat(path); err != nil {
			return nil, "", err
		}
		paths = append(paths, path)
	}
	if len(paths) == 0 {
		return nil, "", ErrClipboardFilesUnavailable
	}
	cleanup = false
	return paths, dir, nil
}

func readWindowsVirtualClipboardFiles(ctx context.Context) ([]string, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	descriptorFormat, err := registerWindowsClipboardFormat(windowsVirtualClipboardFileGroupFormat)
	if err != nil {
		return nil, "", err
	}
	if !windowsClipboardFormatAvailable(descriptorFormat) {
		return nil, "", ErrClipboardFilesUnavailable
	}
	contentsFormat, err := registerWindowsClipboardFormat(windowsVirtualClipboardContentsFormat)
	if err != nil {
		return nil, "", err
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hr, _, _ := procOleInitialize.Call(0)
	if windowsHRESULTFailed(hr) {
		return nil, "", windowsHRESULTError("OleInitialize", hr)
	}
	defer procOleUninitialize.Call()

	object, err := getWindowsClipboardDataObject()
	if err != nil {
		return nil, "", err
	}
	defer releaseWindowsCOMObject(object)

	descriptors, err := readWindowsVirtualDescriptorGroup(object, descriptorFormat)
	if err != nil {
		return nil, "", err
	}
	return materializeWindowsVirtualClipboard(ctx, object, contentsFormat, descriptors)
}

func (c *windowsCapture) clearVirtualClipboardCacheLocked() {
	if c.virtualClipboardDir != "" {
		_ = os.RemoveAll(c.virtualClipboardDir)
	}
	c.virtualClipboardDir = ""
	c.virtualClipboardPaths = nil
	c.virtualClipboardSequence = 0
}

func (c *windowsCapture) readClipboardFilesWithVirtual(ctx context.Context) ([]string, error) {
	paths, err := readWindowsClipboardFiles(ctx)
	if err == nil {
		c.clipboardMu.Lock()
		c.clearVirtualClipboardCacheLocked()
		c.clipboardMu.Unlock()
		return paths, nil
	}
	if !errors.Is(err, ErrClipboardFilesUnavailable) {
		return nil, err
	}

	sequence := windowsClipboardSequenceNumber()
	c.clipboardMu.Lock()
	defer c.clipboardMu.Unlock()
	if sequence != 0 && sequence == c.virtualClipboardSequence {
		if len(c.virtualClipboardPaths) == 0 {
			return nil, ErrClipboardFilesUnavailable
		}
		return append([]string(nil), c.virtualClipboardPaths...), nil
	}

	c.clearVirtualClipboardCacheLocked()
	paths, dir, err := readWindowsVirtualClipboardFiles(ctx)
	c.virtualClipboardSequence = sequence
	if err != nil {
		if errors.Is(err, ErrClipboardFilesUnavailable) {
			return nil, ErrClipboardFilesUnavailable
		}
		return nil, err
	}
	if current := windowsClipboardSequenceNumber(); sequence != 0 && current != 0 && current != sequence {
		_ = os.RemoveAll(dir)
		c.virtualClipboardSequence = current
		return nil, ErrClipboardFilesUnavailable
	}
	c.virtualClipboardDir = dir
	c.virtualClipboardPaths = append([]string(nil), paths...)
	return append([]string(nil), paths...), nil
}
