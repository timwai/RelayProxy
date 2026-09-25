//go:build windows && amd64

package codec

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const nvencDeviceTypeDirectX int32 = 0

// nvencD3D11Session owns the NVENC codec session and the loaded encode runtime.
// The D3D11 device itself is borrowed from Relay Desktop and must remain alive
// until Close returns.
type nvencD3D11Session struct {
	mu sync.Mutex

	module  windows.Handle
	api     nvEncodeAPIFunctionList
	encoder uintptr
	device  uintptr
	closed  bool
}

func validateNVENCProductionFunctionList(api nvEncodeAPIFunctionList) error {
	required := []struct {
		name string
		proc uintptr
	}{
		{"nvEncOpenEncodeSessionEx", api.NvEncOpenEncodeSessionEx},
		{"nvEncGetEncodeGUIDCount", api.NvEncGetEncodeGUIDCount},
		{"nvEncGetEncodeGUIDs", api.NvEncGetEncodeGUIDs},
		{"nvEncGetEncodeCaps", api.NvEncGetEncodeCaps},
		{"nvEncInitializeEncoder", api.NvEncInitializeEncoder},
		{"nvEncCreateBitstreamBuffer", api.NvEncCreateBitstreamBuffer},
		{"nvEncDestroyBitstreamBuffer", api.NvEncDestroyBitstreamBuffer},
		{"nvEncRegisterResource", api.NvEncRegisterResource},
		{"nvEncUnregisterResource", api.NvEncUnregisterResource},
		{"nvEncMapInputResource", api.NvEncMapInputResource},
		{"nvEncUnmapInputResource", api.NvEncUnmapInputResource},
		{"nvEncEncodePicture", api.NvEncEncodePicture},
		{"nvEncLockBitstream", api.NvEncLockBitstream},
		{"nvEncUnlockBitstream", api.NvEncUnlockBitstream},
		{"nvEncGetSequenceParams", api.NvEncGetSequenceParams},
		{"nvEncReconfigureEncoder", api.NvEncReconfigureEncoder},
		{"nvEncDestroyEncoder", api.NvEncDestroyEncoder},
	}
	for _, entry := range required {
		if entry.proc == 0 {
			return fmt.Errorf("%s is unavailable", entry.name)
		}
	}
	return nil
}

func loadNVENCProductionAPI() (windows.Handle, nvEncodeAPIFunctionList, error) {
	module, err := windows.LoadLibrary(nvEncodeRuntimeDLL)
	if err != nil {
		return 0, nvEncodeAPIFunctionList{}, fmt.Errorf("%s: %w", nvEncodeRuntimeDLL, err)
	}
	fail := func(err error) (windows.Handle, nvEncodeAPIFunctionList, error) {
		_ = windows.FreeLibrary(module)
		return 0, nvEncodeAPIFunctionList{}, err
	}

	getVersion, err := windows.GetProcAddress(module, "NvEncodeAPIGetMaxSupportedVersion")
	if err != nil {
		return fail(fmt.Errorf("NvEncodeAPIGetMaxSupportedVersion: %w", err))
	}
	createInstance, err := windows.GetProcAddress(module, "NvEncodeAPICreateInstance")
	if err != nil {
		return fail(fmt.Errorf("NvEncodeAPICreateInstance: %w", err))
	}

	var maxSupportedVersion uint32
	status, _, _ := syscall.SyscallN(
		getVersion,
		uintptr(unsafe.Pointer(&maxSupportedVersion)),
	)
	runtime.KeepAlive(&maxSupportedVersion)
	if got := runtimeCandidateStatus(status); got != 0 {
		return fail(fmt.Errorf("NvEncodeAPIGetMaxSupportedVersion returned %d", got))
	}
	if maxSupportedVersion < nvencMaxVersionCode {
		return fail(fmt.Errorf(
			"NVENC driver API %s is older than required ABI %d.%d",
			formatNVENCMaxSupportedVersion(maxSupportedVersion),
			nvencAPIMajorVersion,
			nvencAPIMinorVersion,
		))
	}

	api, err := createNVENCFunctionList(createInstance)
	if err != nil {
		return fail(err)
	}
	if err := validateNVENCProductionFunctionList(api); err != nil {
		return fail(err)
	}
	return module, api, nil
}

func openNVENCHEVC444D3D11Session(
	ctx context.Context,
	device uintptr,
) (*nvencD3D11Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if device == 0 {
		return nil, fmt.Errorf("%w: NVENC D3D11 device is nil", ErrEncoderUnavailable)
	}

	module, api, err := loadNVENCProductionAPI()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrEncoderUnavailable, err)
	}
	session := &nvencD3D11Session{
		module: module,
		api:    api,
		device: device,
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = session.Close()
		}
	}()

	params := nvencOpenEncodeSessionExParams{
		Version:    nvencStructVersion(1),
		DeviceType: nvencDeviceTypeDirectX,
		Device:     device,
		APIVersion: nvencAPIVersion,
	}
	status := nvencCall(
		api.NvEncOpenEncodeSessionEx,
		uintptr(unsafe.Pointer(&params)),
		uintptr(unsafe.Pointer(&session.encoder)),
	)
	runtime.KeepAlive(&params)
	runtime.KeepAlive(device)
	if status != 0 || session.encoder == 0 {
		return nil, fmt.Errorf(
			"%w: nvEncOpenEncodeSessionEx(D3D11) returned %d",
			ErrEncoderUnavailable,
			status,
		)
	}

	checked, supported, err := probeNVENCHEVC444OnSession(api, session.encoder)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrEncoderUnavailable, err)
	}
	if !checked {
		return nil, fmt.Errorf("%w: NVENC HEVC capability query did not complete", ErrEncoderUnavailable)
	}
	if !supported {
		return nil, fmt.Errorf("%w: NVENC device does not support HEVC 4:4:4 encode", ErrEncoderUnavailable)
	}

	cleanup = false
	return session, nil
}

func (s *nvencD3D11Session) Encoder() uintptr {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0
	}
	return s.encoder
}

func (s *nvencD3D11Session) API() (nvEncodeAPIFunctionList, error) {
	if s == nil {
		return nvEncodeAPIFunctionList{}, ErrEncoderUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.encoder == 0 {
		return nvEncodeAPIFunctionList{}, ErrEncoderUnavailable
	}
	return s.api, nil
}

func (s *nvencD3D11Session) Device() uintptr {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0
	}
	return s.device
}

func (s *nvencD3D11Session) Close() error {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	module := s.module
	api := s.api
	encoder := s.encoder
	s.module = 0
	s.api = nvEncodeAPIFunctionList{}
	s.encoder = 0
	s.device = 0
	s.mu.Unlock()

	var closeErr error
	if encoder != 0 {
		if api.NvEncDestroyEncoder == 0 {
			closeErr = errors.New("nvEncDestroyEncoder is unavailable during Close")
		} else if status := nvencCall(api.NvEncDestroyEncoder, encoder); status != 0 {
			closeErr = fmt.Errorf("nvEncDestroyEncoder returned %d", status)
		}
	}
	if module != 0 {
		if err := windows.FreeLibrary(module); err != nil {
			closeErr = errors.Join(closeErr, fmt.Errorf("free %s: %w", nvEncodeRuntimeDLL, err))
		}
	}
	return closeErr
}
