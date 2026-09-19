//go:build windows

package divert

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// WinDivert 2.2 WINDIVERT_ADDRESS: NETWORK contains interface indices, NOT a
// PID. Keep this ABI separate from the OS connection-table process lookup.
type windivertAddress struct {
	Timestamp int64
	Flags     uint32
	Reserved  uint32
	Data      [64]byte
}

func (a windivertAddress) outbound() bool { return a.Flags&(1<<17) != 0 }
func (a *windivertAddress) setOutbound(value bool) {
	a.Flags &^= 1 << 17
	if value {
		a.Flags |= 1 << 17
	}
}
func (a windivertAddress) ifIndex() uint32    { return binary.LittleEndian.Uint32(a.Data[:4]) }
func (a windivertAddress) subIfIndex() uint32 { return binary.LittleEndian.Uint32(a.Data[4:8]) }
func (a *windivertAddress) setIfIndex(index, subIndex uint32) {
	binary.LittleEndian.PutUint32(a.Data[:4], index)
	binary.LittleEndian.PutUint32(a.Data[4:8], subIndex)
}
func (a *windivertAddress) setChecksums(ipv6 bool) {
	a.Flags &^= 1 << 20
	if ipv6 {
		a.Flags |= 1 << 20
	}
	a.Flags |= (1 << 21) | (1 << 22) | (1 << 23)
}

type windivertAPI struct {
	dll                               windows.Handle
	open, recv, send, shutdown, close uintptr
}

type windivertHandle struct {
	api       *windivertAPI
	handle    windows.Handle
	ioMu      sync.RWMutex
	closed    atomic.Bool
	closeOnce sync.Once
	closeErr  error
}

func winDivertFiles(executable string) (string, error) {
	if !filepath.IsAbs(executable) {
		return "", errors.New("WinDivert 需要绝对路径的客户端程序")
	}
	for _, directory := range []string{filepath.Dir(executable), filepath.Join(filepath.Dir(executable), "windivert")} {
		dll, driver := filepath.Join(directory, "WinDivert.dll"), filepath.Join(directory, "WinDivert64.sys")
		valid := true
		for _, path := range []string{dll, driver} {
			info, err := os.Stat(path)
			if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
				valid = false
				break
			}
		}
		if valid {
			return dll, nil
		}
	}
	return "", errors.New("客户端旁未找到外置 WinDivert.dll / WinDivert64.sys")
}

// winDivertPlatformReadiness validates only the legacy x64 fallback. The
// platform-level readiness selector lives in platform_windows.go.
func winDivertPlatformReadiness() error {
	if runtime.GOARCH != "amd64" {
		return errors.New("系统透明代理目前需要 Windows x64 客户端")
	}
	var failures []string
	if !windows.GetCurrentProcessToken().IsElevated() {
		failures = append(failures, "请以管理员身份重新启动客户端")
	}
	executable, err := os.Executable()
	if err == nil {
		err = winDivertDependencies(executable)
	}
	if err != nil {
		failures = append(failures, err.Error())
	}
	if len(failures) != 0 {
		return errors.New(strings.Join(failures, "；"))
	}
	return nil
}

func loadWinDivert() (*windivertAPI, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	path, err := winDivertFiles(executable)
	if err != nil {
		path, err = extractEmbeddedWinDivert()
		if err != nil {
			return nil, err
		}
	}
	// Never search the current directory or PATH for a privileged DLL.
	dll, err := windows.LoadLibraryEx(path, 0, windows.LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR|windows.LOAD_LIBRARY_SEARCH_SYSTEM32)
	if err != nil {
		return nil, fmt.Errorf("加载 WinDivert.dll 失败: %w", err)
	}
	api := &windivertAPI{dll: dll}
	for name, target := range map[string]*uintptr{
		"WinDivertOpen": &api.open, "WinDivertRecv": &api.recv,
		"WinDivertSend": &api.send, "WinDivertShutdown": &api.shutdown, "WinDivertClose": &api.close,
	} {
		*target, err = windows.GetProcAddress(dll, name)
		if err != nil {
			_ = windows.FreeLibrary(dll)
			return nil, fmt.Errorf("WinDivert 2.2 接口 %s 不可用: %w", name, err)
		}
	}
	return api, nil
}

func openWinDivert(filter string) (*windivertHandle, error) {
	if err := winDivertPlatformReadiness(); err != nil {
		return nil, err
	}
	// The native parameter is const char*, not Windows' usual UTF-16 string.
	filterBytes, err := syscall.BytePtrFromString(filter)
	if err != nil {
		return nil, fmt.Errorf("invalid WinDivert filter: %w", err)
	}
	api, err := loadWinDivert()
	if err != nil {
		return nil, err
	}
	handle, _, callErr := syscall.SyscallN(api.open, uintptr(unsafe.Pointer(filterBytes)), 0, 0, 0)
	runtime.KeepAlive(filterBytes)
	if handle == ^uintptr(0) {
		_ = windows.FreeLibrary(api.dll)
		return nil, fmt.Errorf("启动 WinDivert 失败（请检查管理员权限及驱动签名）: %w", nativeWinDivertError(callErr))
	}
	return &windivertHandle{api: api, handle: windows.Handle(handle)}, nil
}

func nativeWinDivertError(err syscall.Errno) error {
	if err == 0 {
		return errors.New("WinDivert call failed without an error code")
	}
	return err
}

func (h *windivertHandle) Recv(buffer []byte) (int, windivertAddress, error) {
	var address windivertAddress
	if len(buffer) == 0 {
		return 0, address, errors.New("empty WinDivert receive buffer")
	}
	h.ioMu.RLock()
	defer h.ioMu.RUnlock()
	if h.closed.Load() {
		return 0, address, netClosedError()
	}
	var length uint32
	ok, _, err := syscall.SyscallN(h.api.recv, uintptr(h.handle), uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), uintptr(unsafe.Pointer(&length)), uintptr(unsafe.Pointer(&address)))
	runtime.KeepAlive(buffer)
	if ok == 0 {
		return 0, address, nativeWinDivertError(err)
	}
	if int(length) > len(buffer) {
		return 0, address, errors.New("WinDivert returned an invalid packet length")
	}
	return int(length), address, nil
}

func (h *windivertHandle) Send(packet []byte, address windivertAddress) error {
	if len(packet) == 0 {
		return errors.New("empty WinDivert packet")
	}
	h.ioMu.RLock()
	defer h.ioMu.RUnlock()
	if h.closed.Load() {
		return netClosedError()
	}
	var length uint32
	ok, _, err := syscall.SyscallN(h.api.send, uintptr(h.handle), uintptr(unsafe.Pointer(&packet[0])), uintptr(len(packet)), uintptr(unsafe.Pointer(&length)), uintptr(unsafe.Pointer(&address)))
	runtime.KeepAlive(packet)
	if ok == 0 {
		return nativeWinDivertError(err)
	}
	if int(length) != len(packet) {
		return errors.New("WinDivert sent an incomplete packet")
	}
	return nil
}

func (h *windivertHandle) Shutdown() error {
	h.ioMu.RLock()
	defer h.ioMu.RUnlock()
	if h.closed.Load() {
		return nil
	}
	ok, _, err := syscall.SyscallN(h.api.shutdown, uintptr(h.handle), 1)
	if ok == 0 {
		return nativeWinDivertError(err)
	}
	return nil
}

func (h *windivertHandle) Close() error {
	h.closeOnce.Do(func() {
		h.closed.Store(true)
		// Shutdown wakes blocking receives before taking the exclusive lock.
		// Calls already using the DLL finish before it is unloaded.
		_, _, _ = syscall.SyscallN(h.api.shutdown, uintptr(h.handle), 3)
		_ = windows.CancelIoEx(h.handle, nil)
		h.ioMu.Lock()
		defer h.ioMu.Unlock()
		ok, _, err := syscall.SyscallN(h.api.close, uintptr(h.handle))
		if ok == 0 {
			h.closeErr = nativeWinDivertError(err)
			// Release the OS device even if the DLL's cleanup reported failure.
			h.closeErr = errors.Join(h.closeErr, windows.CloseHandle(h.handle))
		}
		h.closeErr = errors.Join(h.closeErr, windows.FreeLibrary(h.api.dll))
	})
	return h.closeErr
}

func netClosedError() error { return errors.New("WinDivert handle is closed") }
