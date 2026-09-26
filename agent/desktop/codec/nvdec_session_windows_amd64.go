//go:build windows && amd64

package codec

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

const nvdecCUDADeviceListAll uint32 = 0x01

type nvdecFunctionList struct {
	CuvidGetDecoderCaps     uintptr
	CuvidCreateDecoder      uintptr
	CuvidDestroyDecoder     uintptr
	CuvidDecodePicture      uintptr
	CuvidMapVideoFrame64    uintptr
	CuvidUnmapVideoFrame64  uintptr
	CuvidCreateVideoParser  uintptr
	CuvidParseVideoData     uintptr
	CuvidDestroyVideoParser uintptr
	CuvidCtxLockCreate      uintptr
	CuvidCtxLockDestroy     uintptr
	CuvidCtxLock            uintptr
	CuvidCtxUnlock          uintptr
}

type nvdecD3D11Session struct {
	mu sync.Mutex

	cuda         *nvidiaCUDADriverAPI
	cuvidModule  windows.Handle
	api          nvdecFunctionList
	cudaContext  uintptr
	videoCtxLock uintptr
	cudaDevice   int32
	d3d11Device  uintptr
	closed       bool
}

func loadNVDECFunctionList(module windows.Handle) (nvdecFunctionList, error) {
	if module == 0 {
		return nvdecFunctionList{}, ErrDecoderUnavailable
	}
	resolve := func(name string) (uintptr, error) {
		proc, err := windows.GetProcAddress(module, name)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", name, err)
		}
		return proc, nil
	}

	var api nvdecFunctionList
	var err error
	if api.CuvidGetDecoderCaps, err = resolve("cuvidGetDecoderCaps"); err != nil {
		return nvdecFunctionList{}, err
	}
	if api.CuvidCreateDecoder, err = resolve("cuvidCreateDecoder"); err != nil {
		return nvdecFunctionList{}, err
	}
	if api.CuvidDestroyDecoder, err = resolve("cuvidDestroyDecoder"); err != nil {
		return nvdecFunctionList{}, err
	}
	if api.CuvidDecodePicture, err = resolve("cuvidDecodePicture"); err != nil {
		return nvdecFunctionList{}, err
	}
	if api.CuvidMapVideoFrame64, err = resolveWindowsProc(
		module,
		"cuvidMapVideoFrame64",
		"cuvidMapVideoFrame",
	); err != nil {
		return nvdecFunctionList{}, fmt.Errorf("cuvidMapVideoFrame64: %w", err)
	}
	if api.CuvidUnmapVideoFrame64, err = resolveWindowsProc(
		module,
		"cuvidUnmapVideoFrame64",
		"cuvidUnmapVideoFrame",
	); err != nil {
		return nvdecFunctionList{}, fmt.Errorf("cuvidUnmapVideoFrame64: %w", err)
	}
	if api.CuvidCreateVideoParser, err = resolve("cuvidCreateVideoParser"); err != nil {
		return nvdecFunctionList{}, err
	}
	if api.CuvidParseVideoData, err = resolve("cuvidParseVideoData"); err != nil {
		return nvdecFunctionList{}, err
	}
	if api.CuvidDestroyVideoParser, err = resolve("cuvidDestroyVideoParser"); err != nil {
		return nvdecFunctionList{}, err
	}
	if api.CuvidCtxLockCreate, err = resolve("cuvidCtxLockCreate"); err != nil {
		return nvdecFunctionList{}, err
	}
	if api.CuvidCtxLockDestroy, err = resolve("cuvidCtxLockDestroy"); err != nil {
		return nvdecFunctionList{}, err
	}
	if api.CuvidCtxLock, err = resolve("cuvidCtxLock"); err != nil {
		return nvdecFunctionList{}, err
	}
	if api.CuvidCtxUnlock, err = resolve("cuvidCtxUnlock"); err != nil {
		return nvdecFunctionList{}, err
	}
	if err := validateNVDECFunctionList(api); err != nil {
		return nvdecFunctionList{}, err
	}
	return api, nil
}

func validateNVDECFunctionList(api nvdecFunctionList) error {
	required := []struct {
		name string
		proc uintptr
	}{
		{"cuvidGetDecoderCaps", api.CuvidGetDecoderCaps},
		{"cuvidCreateDecoder", api.CuvidCreateDecoder},
		{"cuvidDestroyDecoder", api.CuvidDestroyDecoder},
		{"cuvidDecodePicture", api.CuvidDecodePicture},
		{"cuvidMapVideoFrame64", api.CuvidMapVideoFrame64},
		{"cuvidUnmapVideoFrame64", api.CuvidUnmapVideoFrame64},
		{"cuvidCreateVideoParser", api.CuvidCreateVideoParser},
		{"cuvidParseVideoData", api.CuvidParseVideoData},
		{"cuvidDestroyVideoParser", api.CuvidDestroyVideoParser},
		{"cuvidCtxLockCreate", api.CuvidCtxLockCreate},
		{"cuvidCtxLockDestroy", api.CuvidCtxLockDestroy},
		{"cuvidCtxLock", api.CuvidCtxLock},
		{"cuvidCtxUnlock", api.CuvidCtxUnlock},
	}
	for _, entry := range required {
		if entry.proc == 0 {
			return fmt.Errorf("%s is unavailable", entry.name)
		}
	}
	return nil
}

func openNVDECHEVC444D3D11Session(
	ctx context.Context,
	device uintptr,
) (*nvdecD3D11Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if device == 0 {
		return nil, fmt.Errorf("%w: NVDEC D3D11 device is nil", ErrDecoderUnavailable)
	}

	cuda, err := loadNVIDIACUDADriverAPI()
	if err != nil {
		return nil, fmt.Errorf("%w: load %s: %v", ErrDecoderUnavailable, nvidiaCUDADriverDLL, err)
	}
	cleanupCUDA := true
	defer func() {
		if cleanupCUDA {
			cuda.Close()
		}
	}()

	cuD3D11GetDevices, err := resolveWindowsProc(cuda.module, "cuD3D11GetDevices")
	if err != nil {
		return nil, fmt.Errorf("%w: cuD3D11GetDevices: %v", ErrDecoderUnavailable, err)
	}

	cuvidModule, err := windows.LoadLibrary(nvDecodeRuntimeDLL)
	if err != nil {
		return nil, fmt.Errorf("%w: load %s: %v", ErrDecoderUnavailable, nvDecodeRuntimeDLL, err)
	}
	cleanupCuvid := true
	defer func() {
		if cleanupCuvid {
			_ = windows.FreeLibrary(cuvidModule)
		}
	}()

	api, err := loadNVDECFunctionList(cuvidModule)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve NVDEC API: %v", ErrDecoderUnavailable, err)
	}

	session := &nvdecD3D11Session{
		cuda:        cuda,
		cuvidModule: cuvidModule,
		api:         api,
		d3d11Device: device,
	}
	// Ownership transfers to session before CUDA context creation so every
	// later failure has exactly one cleanup path.
	cleanupCUDA = false
	cleanupCuvid = false
	cleanupSession := true
	defer func() {
		if cleanupSession {
			_ = session.Close()
		}
	}()

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if status := cudaDriverCall(cuda.cuInit, 0); status != 0 {
		return nil, fmt.Errorf("%w: cuInit returned %d", ErrDecoderUnavailable, status)
	}

	var deviceCount uint32
	var cudaDevices [8]int32
	status := cudaDriverCall(
		cuD3D11GetDevices,
		uintptr(unsafe.Pointer(&deviceCount)),
		uintptr(unsafe.Pointer(&cudaDevices[0])),
		uintptr(len(cudaDevices)),
		device,
		uintptr(nvdecCUDADeviceListAll),
	)
	runtime.KeepAlive(&deviceCount)
	runtime.KeepAlive(&cudaDevices)
	runtime.KeepAlive(device)
	if status != 0 {
		return nil, fmt.Errorf("%w: cuD3D11GetDevices returned %d", ErrDecoderUnavailable, status)
	}
	if deviceCount != 1 {
		return nil, fmt.Errorf(
			"%w: NVDEC zero-copy requires exactly one CUDA device for the D3D11 device, got %d",
			ErrDecoderUnavailable,
			deviceCount,
		)
	}
	session.cudaDevice = cudaDevices[0]

	if status = cudaDriverCall(
		cuda.cuCtxCreate,
		uintptr(unsafe.Pointer(&session.cudaContext)),
		0,
		uintptr(session.cudaDevice),
	); status != 0 || session.cudaContext == 0 {
		return nil, fmt.Errorf("%w: cuCtxCreate returned %d", ErrDecoderUnavailable, status)
	}
	runtime.KeepAlive(&session.cudaContext)

	if status = cudaDriverCall(
		api.CuvidCtxLockCreate,
		uintptr(unsafe.Pointer(&session.videoCtxLock)),
		session.cudaContext,
	); status != 0 || session.videoCtxLock == 0 {
		return nil, fmt.Errorf("%w: cuvidCtxLockCreate returned %d", ErrDecoderUnavailable, status)
	}
	runtime.KeepAlive(&session.videoCtxLock)

	if status = cudaDriverCall(cuda.cuCtxSetCurrent, 0); status != 0 {
		return nil, fmt.Errorf("%w: cuCtxSetCurrent(NULL) returned %d", ErrDecoderUnavailable, status)
	}

	cleanupSession = false
	return session, nil
}

func (s *nvdecD3D11Session) CUDADevice() int32 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0
	}
	return s.cudaDevice
}

func (s *nvdecD3D11Session) CUDAContext() uintptr {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0
	}
	return s.cudaContext
}

func (s *nvdecD3D11Session) VideoContextLock() uintptr {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0
	}
	return s.videoCtxLock
}

func (s *nvdecD3D11Session) API() (nvdecFunctionList, error) {
	if s == nil {
		return nvdecFunctionList{}, ErrDecoderUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.cudaContext == 0 || s.videoCtxLock == 0 {
		return nvdecFunctionList{}, ErrDecoderUnavailable
	}
	return s.api, nil
}

func (s *nvdecD3D11Session) Close() error {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	cuda := s.cuda
	cuvidModule := s.cuvidModule
	api := s.api
	cudaContext := s.cudaContext
	videoCtxLock := s.videoCtxLock
	s.cuda = nil
	s.cuvidModule = 0
	s.api = nvdecFunctionList{}
	s.cudaContext = 0
	s.videoCtxLock = 0
	s.cudaDevice = 0
	s.d3d11Device = 0
	s.mu.Unlock()

	var closeErr error
	if cuda != nil && cudaContext != 0 {
		runtime.LockOSThread()
		if status := cudaDriverCall(cuda.cuCtxSetCurrent, cudaContext); status != 0 {
			closeErr = errors.Join(closeErr, fmt.Errorf("cuCtxSetCurrent returned %d", status))
		}
		if videoCtxLock != 0 && api.CuvidCtxLockDestroy != 0 {
			if status := cudaDriverCall(api.CuvidCtxLockDestroy, videoCtxLock); status != 0 {
				closeErr = errors.Join(closeErr, fmt.Errorf("cuvidCtxLockDestroy returned %d", status))
			}
		}
		if status := cudaDriverCall(cuda.cuCtxDestroy, cudaContext); status != 0 {
			closeErr = errors.Join(closeErr, fmt.Errorf("cuCtxDestroy returned %d", status))
		}
		runtime.UnlockOSThread()
	}
	if cuvidModule != 0 {
		if err := windows.FreeLibrary(cuvidModule); err != nil {
			closeErr = errors.Join(closeErr, fmt.Errorf("free %s: %w", nvDecodeRuntimeDLL, err))
		}
	}
	if cuda != nil {
		cuda.Close()
	}
	return closeErr
}
