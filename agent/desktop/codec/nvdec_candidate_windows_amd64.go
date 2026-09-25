//go:build windows && amd64

package codec

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	nvidiaCUDADriverDLL        = "nvcuda.dll"
	nvVideoCodecHEVC     int32 = 8
	nvVideoChroma444     int32 = 3
)

// nvcuvidDecodeCaps mirrors CUVIDDECODECAPS from the NVIDIA Video Codec SDK.
// The API has no explicit structure-version field, so keep the reserved fields
// to preserve the SDK ABI even though RelayProxy only reads IsSupported and the
// resolution limits.
type nvcuvidDecodeCaps struct {
	CodecType      int32
	ChromaFormat   int32
	BitDepthMinus8 uint32
	Reserved1      [3]uint32
	IsSupported    uint8
	Reserved2      [3]uint8
	MaxWidth       uint32
	MaxHeight      uint32
	MaxMBCount     uint32
	MinWidth       uint16
	MinHeight      uint16
	Reserved3      [11]uint32
}

type nvidiaNVDECDeviceProbe struct {
	Checked     bool
	DeviceCount int
	HEVC444     bool
	Error       string
}

type nvidiaCUDADriverAPI struct {
	module windows.Handle

	cuInit           uintptr
	cuDeviceGetCount uintptr
	cuDeviceGet      uintptr
	cuCtxCreate      uintptr
	cuCtxSetCurrent  uintptr
	cuCtxDestroy     uintptr
}

func resolveWindowsProc(module windows.Handle, names ...string) (uintptr, error) {
	var lastErr error
	for _, name := range names {
		proc, err := windows.GetProcAddress(module, name)
		if err == nil {
			return proc, nil
		}
		lastErr = err
	}
	return 0, lastErr
}

func loadNVIDIACUDADriverAPI() (*nvidiaCUDADriverAPI, error) {
	module, err := windows.LoadLibrary(nvidiaCUDADriverDLL)
	if err != nil {
		return nil, err
	}
	api := &nvidiaCUDADriverAPI{module: module}
	fail := func(err error) (*nvidiaCUDADriverAPI, error) {
		_ = windows.FreeLibrary(module)
		return nil, err
	}
	if api.cuInit, err = resolveWindowsProc(module, "cuInit"); err != nil {
		return fail(fmt.Errorf("cuInit: %w", err))
	}
	if api.cuDeviceGetCount, err = resolveWindowsProc(module, "cuDeviceGetCount"); err != nil {
		return fail(fmt.Errorf("cuDeviceGetCount: %w", err))
	}
	if api.cuDeviceGet, err = resolveWindowsProc(module, "cuDeviceGet"); err != nil {
		return fail(fmt.Errorf("cuDeviceGet: %w", err))
	}
	if api.cuCtxCreate, err = resolveWindowsProc(module, "cuCtxCreate_v2", "cuCtxCreate"); err != nil {
		return fail(fmt.Errorf("cuCtxCreate: %w", err))
	}
	if api.cuCtxSetCurrent, err = resolveWindowsProc(module, "cuCtxSetCurrent"); err != nil {
		return fail(fmt.Errorf("cuCtxSetCurrent: %w", err))
	}
	if api.cuCtxDestroy, err = resolveWindowsProc(module, "cuCtxDestroy_v2", "cuCtxDestroy"); err != nil {
		return fail(fmt.Errorf("cuCtxDestroy: %w", err))
	}
	return api, nil
}

func (a *nvidiaCUDADriverAPI) Close() {
	if a == nil || a.module == 0 {
		return
	}
	_ = windows.FreeLibrary(a.module)
	a.module = 0
}

func cudaDriverCall(proc uintptr, args ...uintptr) int32 {
	status, _, _ := syscall.SyscallN(proc, args...)
	return runtimeCandidateStatus(status)
}

func probeNVIDIANVDECHEVC444(
	ctx context.Context,
	cuvidGetDecoderCaps uintptr,
) nvidiaNVDECDeviceProbe {
	result := nvidiaNVDECDeviceProbe{}
	if err := ctx.Err(); err != nil {
		result.Error = err.Error()
		return result
	}
	if cuvidGetDecoderCaps == 0 {
		result.Error = "cuvidGetDecoderCaps is unavailable"
		return result
	}

	api, err := loadNVIDIACUDADriverAPI()
	if err != nil {
		result.Error = fmt.Sprintf("%s: %v", nvidiaCUDADriverDLL, err)
		return result
	}
	defer api.Close()

	// CUDA contexts are current to an OS thread. Keep the short-lived probe on
	// one thread from context creation through cuvidGetDecoderCaps and teardown.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if status := cudaDriverCall(api.cuInit, 0); status != 0 {
		result.Error = fmt.Sprintf("cuInit returned %d", status)
		return result
	}

	var deviceCount int32
	if status := cudaDriverCall(
		api.cuDeviceGetCount,
		uintptr(unsafe.Pointer(&deviceCount)),
	); status != 0 {
		result.Error = fmt.Sprintf("cuDeviceGetCount returned %d", status)
		return result
	}
	runtime.KeepAlive(&deviceCount)
	if deviceCount <= 0 {
		result.Error = "no CUDA devices"
		return result
	}
	result.DeviceCount = int(deviceCount)

	var issues []string
	for ordinal := int32(0); ordinal < deviceCount; ordinal++ {
		if err := ctx.Err(); err != nil {
			issues = append(issues, err.Error())
			break
		}

		var device int32
		if status := cudaDriverCall(
			api.cuDeviceGet,
			uintptr(unsafe.Pointer(&device)),
			uintptr(ordinal),
		); status != 0 {
			issues = append(issues, fmt.Sprintf("device %d cuDeviceGet returned %d", ordinal, status))
			continue
		}

		var cudaContext uintptr
		if status := cudaDriverCall(
			api.cuCtxCreate,
			uintptr(unsafe.Pointer(&cudaContext)),
			0,
			uintptr(device),
		); status != 0 || cudaContext == 0 {
			issues = append(issues, fmt.Sprintf("device %d cuCtxCreate returned %d", ordinal, status))
			continue
		}

		caps := nvcuvidDecodeCaps{
			CodecType:      nvVideoCodecHEVC,
			ChromaFormat:   nvVideoChroma444,
			BitDepthMinus8: 0,
		}
		status := cudaDriverCall(
			cuvidGetDecoderCaps,
			uintptr(unsafe.Pointer(&caps)),
		)
		clearStatus := cudaDriverCall(api.cuCtxSetCurrent, 0)
		destroyStatus := cudaDriverCall(api.cuCtxDestroy, cudaContext)
		runtime.KeepAlive(&caps)
		runtime.KeepAlive(&cudaContext)

		if status != 0 {
			issues = append(issues, fmt.Sprintf("device %d cuvidGetDecoderCaps returned %d", ordinal, status))
			continue
		}
		if clearStatus != 0 {
			issues = append(issues, fmt.Sprintf("device %d cuCtxSetCurrent(NULL) returned %d", ordinal, clearStatus))
		}
		if destroyStatus != 0 {
			issues = append(issues, fmt.Sprintf("device %d cuCtxDestroy returned %d", ordinal, destroyStatus))
		}

		result.Checked = true
		if caps.IsSupported != 0 {
			result.HEVC444 = true
		}
	}

	result.Error = strings.Join(issues, "; ")
	return result
}
