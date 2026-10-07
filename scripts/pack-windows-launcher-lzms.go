//go:build ignore

// Packs a WinUI host behind the rename-safe launcher using Windows' built-in
// LZMS Compression API. This script is intentionally Windows-only.
package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"
)

const (
	footerMagic                 = "RELAYPROXY_GUI4!"
	lzmsAlgorithm               = 5
	compressInfoClassBlockSize = 1
	lzmsBlockSize       uint32 = 64 * 1024 * 1024
)

var (
	cabinetDLL       = syscall.NewLazyDLL("cabinet.dll")
	createCompressor       = cabinetDLL.NewProc("CreateCompressor")
	setCompressorInformation = cabinetDLL.NewProc("SetCompressorInformation")
	compressProc           = cabinetDLL.NewProc("Compress")
	closeCompressor        = cabinetDLL.NewProc("CloseCompressor")
)

func main() {
	launcher := flag.String("launcher", "", "launcher stub executable")
	payload := flag.String("payload", "", "fixed-name WinUI host payload")
	output := flag.String("output", "", "final executable")
	flag.Parse()

	if err := pack(*launcher, *payload, *output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func pack(launcher, payload, output string) error {
	if launcher == "" || payload == "" || output == "" {
		return fmt.Errorf("launcher, payload and output are required")
	}
	launcher, _ = filepath.Abs(launcher)
	payload, _ = filepath.Abs(payload)
	output, _ = filepath.Abs(output)
	if launcher == output || payload == output {
		return fmt.Errorf("output must differ from launcher and payload")
	}

	stub, err := os.ReadFile(launcher)
	if err != nil {
		return fmt.Errorf("launcher stub: %w", err)
	}
	raw, err := os.ReadFile(payload)
	if err != nil {
		return fmt.Errorf("WinUI payload: %w", err)
	}
	if len(raw) == 0 {
		return fmt.Errorf("WinUI payload is empty")
	}

	compressed, err := compressLZMS(raw)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(raw)

	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return err
	}
	out, err := os.Create(output)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := out.Write(stub); err != nil {
		return err
	}
	if _, err := out.Write(compressed); err != nil {
		return err
	}
	if _, err := out.Write([]byte(footerMagic)); err != nil {
		return err
	}
	var sizes [16]byte
	binary.LittleEndian.PutUint64(sizes[:8], uint64(len(compressed)))
	binary.LittleEndian.PutUint64(sizes[8:], uint64(len(raw)))
	if _, err := out.Write(sizes[:]); err != nil {
		return err
	}
	if _, err := out.Write(hash[:]); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}

	final, err := os.Stat(output)
	if err != nil {
		return err
	}
	fmt.Printf("[lzms-launcher] stub: %.2f MiB\n", float64(len(stub))/(1<<20))
	fmt.Printf("[lzms-launcher] WinUI payload raw: %.2f MiB\n", float64(len(raw))/(1<<20))
	fmt.Printf("[lzms-launcher] WinUI payload LZMS: %.2f MiB (%.1f%%)\n", float64(len(compressed))/(1<<20), float64(len(compressed))/float64(len(raw))*100)
	fmt.Printf("[lzms-launcher] final rename-safe EXE: %.2f MiB\n", float64(final.Size())/(1<<20))
	fmt.Printf("[lzms-launcher] payload sha256: %s\n", hex.EncodeToString(hash[:]))
	return nil
}

func compressLZMS(raw []byte) ([]byte, error) {
	var handle uintptr
	ok, _, callErr := createCompressor.Call(
		uintptr(lzmsAlgorithm),
		0,
		uintptr(unsafe.Pointer(&handle)),
	)
	if ok == 0 {
		return nil, fmt.Errorf("Windows LZMS CreateCompressor failed: %v", callErr)
	}
	defer closeCompressor.Call(handle)

	blockSize := lzmsBlockSize
	ok, _, callErr = setCompressorInformation.Call(
		handle,
		uintptr(compressInfoClassBlockSize),
		uintptr(unsafe.Pointer(&blockSize)),
		unsafe.Sizeof(blockSize),
	)
	if ok == 0 {
		return nil, fmt.Errorf("Windows LZMS set 64 MiB block size failed: %v", callErr)
	}

	var needed uintptr
	_, _, firstErr := compressProc.Call(
		handle,
		uintptr(unsafe.Pointer(&raw[0])),
		uintptr(len(raw)),
		0,
		0,
		uintptr(unsafe.Pointer(&needed)),
	)
	runtime.KeepAlive(raw)
	if needed == 0 {
		return nil, fmt.Errorf("Windows LZMS size query failed: %v", firstErr)
	}

	buffer := make([]byte, int(needed))
	var written uintptr
	ok, _, callErr = compressProc.Call(
		handle,
		uintptr(unsafe.Pointer(&raw[0])),
		uintptr(len(raw)),
		uintptr(unsafe.Pointer(&buffer[0])),
		uintptr(len(buffer)),
		uintptr(unsafe.Pointer(&written)),
	)
	runtime.KeepAlive(raw)
	runtime.KeepAlive(buffer)
	if ok == 0 {
		return nil, fmt.Errorf("Windows LZMS compression failed: %v", callErr)
	}
	if written > uintptr(len(buffer)) {
		return nil, fmt.Errorf("Windows LZMS returned invalid size %d > %d", written, len(buffer))
	}
	return buffer[:int(written)], nil
}
