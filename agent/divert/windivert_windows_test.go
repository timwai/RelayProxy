//go:build windows

package divert

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestWinDivertAddressABI(t *testing.T) {
	var address windivertAddress
	if unsafe.Sizeof(address) != 80 || unsafe.Offsetof(address.Data) != 16 || unsafe.Offsetof(address.Flags) != 8 {
		t.Fatal("WinDivert 2.2 address ABI mismatch")
	}
	address.setOutbound(true)
	address.setIfIndex(0x12345678, 0x99887766)
	address.setChecksums(true)
	if !address.outbound() || address.Flags != 0xf20000 || address.ifIndex() != 0x12345678 || address.subIfIndex() != 0x99887766 {
		t.Fatalf("invalid address metadata: %+v", address)
	}
	address.setOutbound(false)
	address.setChecksums(false)
	if address.outbound() || address.Flags != 0xe00000 || binary.LittleEndian.Uint32(address.Data[:4]) != 0x12345678 {
		t.Fatal("inbound IPv4 metadata corrupted")
	}
}

func TestWinDivertRequiresCompleteFilesBesideExecutable(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "relay-agent.exe")
	if _, err := winDivertFiles(executable); err == nil {
		t.Fatal("missing dependency accepted")
	}
	if _, err := winDivertFiles("relay-agent.exe"); err == nil {
		t.Fatal("relative executable accepted")
	}
	dependency := filepath.Join(dir, "windivert")
	if err := os.Mkdir(dependency, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dependency, "WinDivert.dll"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := winDivertFiles(executable); err == nil {
		t.Fatal("DLL without matching driver accepted")
	}
	if err := os.WriteFile(filepath.Join(dependency, "WinDivert64.sys"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := winDivertFiles(executable)
	if err != nil || got != filepath.Join(dependency, "WinDivert.dll") {
		t.Fatalf("trusted subdirectory not found: %s %v", got, err)
	}
}

func TestWinDivertClosedHandleCannotCallDLL(t *testing.T) {
	h := &windivertHandle{}
	h.closed.Store(true)
	if _, _, err := h.Recv(make([]byte, 40)); err == nil {
		t.Fatal("closed receive accepted")
	}
	if err := h.Send([]byte{0x45}, windivertAddress{}); err == nil {
		t.Fatal("closed send accepted")
	}
	if err := h.Shutdown(); err != nil {
		t.Fatal(err)
	}
}

// This check calls only the official DLL's pure filter helpers.
// It does not open a WinDivert handle and does not require administrator rights.
func TestWinDivertNativeFilter(t *testing.T) {
	path := os.Getenv("RELAYPROXY_WINDIVERT_DLL")
	if path == "" {
		if runtime.GOARCH != "amd64" {
			t.Skip("embedded native runtime requires Windows amd64")
		}
		files, err := bundledWinDivertFiles()
		if err != nil {
			t.Fatal(err)
		}
		directory := t.TempDir()
		if err := materializeWinDivert(directory, files); err != nil {
			t.Fatal(err)
		}
		path = filepath.Join(directory, "WinDivert.dll")
	}
	if !filepath.IsAbs(path) {
		t.Fatal("native DLL test requires an absolute path")
	}
	dll, err := windows.LoadLibraryEx(path, 0, windows.LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR|windows.LOAD_LIBRARY_SEARCH_SYSTEM32)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.FreeLibrary(dll)
	compile, err := windows.GetProcAddress(dll, "WinDivertHelperCompileFilter")
	if err != nil {
		t.Fatal(err)
	}
	evaluate, err := windows.GetProcAddress(dll, "WinDivertHelperEvalFilter")
	if err != nil {
		t.Fatal(err)
	}
	filter, err := syscall.BytePtrFromString(windowsInterceptFilter(45001, 45002))
	if err != nil {
		t.Fatal(err)
	}
	var position uint32
	ok, _, callErr := syscall.SyscallN(compile, uintptr(unsafe.Pointer(filter)), 0, 0, 0, 0, uintptr(unsafe.Pointer(&position)))
	if ok == 0 {
		t.Fatalf("native filter rejected at byte %d: %v", position, callErr)
	}
	for _, ipv6 := range []bool{false, true} {
		for _, protocol := range []Protocol{ProtoTCP, ProtoUDP} {
			data, _ := packetTestFixture(ipv6, protocol, nil, false)
			address := windivertAddress{}
			address.setOutbound(true)
			address.setChecksums(ipv6)
			got, _, _ := syscall.SyscallN(evaluate, uintptr(unsafe.Pointer(filter)), uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)), uintptr(unsafe.Pointer(&address)))
			if got == 0 {
				t.Fatalf("native filter skipped %s ipv6=%v", protocol, ipv6)
			}
			address.Flags |= 1 << 18
			got, _, _ = syscall.SyscallN(evaluate, uintptr(unsafe.Pointer(filter)), uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)), uintptr(unsafe.Pointer(&address)))
			if got != 0 {
				t.Fatal("native filter intercepted local loopback traffic")
			}
			runtime.KeepAlive(data)
		}
	}
	runtime.KeepAlive(filter)
}
