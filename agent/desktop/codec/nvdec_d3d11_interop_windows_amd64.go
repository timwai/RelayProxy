//go:build windows && amd64

package codec

import (
	"errors"
	"fmt"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	nvdecCUGraphicsRegisterSurfaceLDST uint32 = 0x04
	nvdecCUGraphicsMapWriteDiscard     uint32 = 0x02
)

type nvdecCUDAInteropAPI struct {
	GraphicsD3D11RegisterResource     uintptr
	GraphicsUnregisterResource        uintptr
	GraphicsMapResources              uintptr
	GraphicsUnmapResources            uintptr
	GraphicsSubResourceGetMappedArray uintptr
	GraphicsResourceSetMapFlags       uintptr
}

type nvdecD3D11AYUVInteropSurface struct {
	mu sync.Mutex

	session          *nvdecD3D11Session
	api              nvdecCUDAInteropAPI
	texture          unsafe.Pointer
	graphicsResource uintptr
	mappedArray      uintptr
	width            int
	height           int
	mapped           bool
	closed           bool
}

func loadNVDECCUDAInteropAPI(module windows.Handle) (nvdecCUDAInteropAPI, error) {
	if module == 0 {
		return nvdecCUDAInteropAPI{}, ErrDecoderUnavailable
	}
	resolve := func(name string) (uintptr, error) {
		proc, err := windows.GetProcAddress(module, name)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", name, err)
		}
		return proc, nil
	}

	var api nvdecCUDAInteropAPI
	var err error
	if api.GraphicsD3D11RegisterResource, err = resolve("cuGraphicsD3D11RegisterResource"); err != nil {
		return nvdecCUDAInteropAPI{}, err
	}
	if api.GraphicsUnregisterResource, err = resolve("cuGraphicsUnregisterResource"); err != nil {
		return nvdecCUDAInteropAPI{}, err
	}
	if api.GraphicsMapResources, err = resolve("cuGraphicsMapResources"); err != nil {
		return nvdecCUDAInteropAPI{}, err
	}
	if api.GraphicsUnmapResources, err = resolve("cuGraphicsUnmapResources"); err != nil {
		return nvdecCUDAInteropAPI{}, err
	}
	if api.GraphicsSubResourceGetMappedArray, err = resolve("cuGraphicsSubResourceGetMappedArray"); err != nil {
		return nvdecCUDAInteropAPI{}, err
	}
	if api.GraphicsResourceSetMapFlags, err = resolve("cuGraphicsResourceSetMapFlags"); err != nil {
		return nvdecCUDAInteropAPI{}, err
	}
	if err := validateNVDECCUDAInteropAPI(api); err != nil {
		return nvdecCUDAInteropAPI{}, err
	}
	return api, nil
}

func validateNVDECCUDAInteropAPI(api nvdecCUDAInteropAPI) error {
	required := []struct {
		name string
		proc uintptr
	}{
		{"cuGraphicsD3D11RegisterResource", api.GraphicsD3D11RegisterResource},
		{"cuGraphicsUnregisterResource", api.GraphicsUnregisterResource},
		{"cuGraphicsMapResources", api.GraphicsMapResources},
		{"cuGraphicsUnmapResources", api.GraphicsUnmapResources},
		{"cuGraphicsSubResourceGetMappedArray", api.GraphicsSubResourceGetMappedArray},
		{"cuGraphicsResourceSetMapFlags", api.GraphicsResourceSetMapFlags},
	}
	for _, entry := range required {
		if entry.proc == 0 {
			return fmt.Errorf("%s is unavailable", entry.name)
		}
	}
	return nil
}

func (s *nvdecD3D11Session) withCUDAContextLock(fn func() error) error {
	if s == nil {
		return ErrDecoderUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.videoCtxLock == 0 || s.api.CuvidCtxLock == 0 || s.api.CuvidCtxUnlock == 0 {
		return ErrDecoderUnavailable
	}
	if status := cudaDriverCall(s.api.CuvidCtxLock, s.videoCtxLock, 0); status != 0 {
		return fmt.Errorf("%w: cuvidCtxLock returned %d", ErrDecoderUnavailable, status)
	}
	callErr := fn()
	unlockStatus := cudaDriverCall(s.api.CuvidCtxUnlock, s.videoCtxLock, 0)
	if unlockStatus != 0 {
		callErr = errors.Join(
			callErr,
			fmt.Errorf("%w: cuvidCtxUnlock returned %d", ErrDecoderUnavailable, unlockStatus),
		)
	}
	return callErr
}

func createNVDECD3D11AYUVInteropSurface(
	session *nvdecD3D11Session,
	width int,
	height int,
) (*nvdecD3D11AYUVInteropSurface, error) {
	if session == nil {
		return nil, ErrDecoderUnavailable
	}
	if width <= 0 || height <= 0 || width%2 != 0 || height%2 != 0 {
		return nil, fmt.Errorf("%w: invalid NVDEC AYUV surface dimensions %dx%d", ErrDecoderUnavailable, width, height)
	}

	session.mu.Lock()
	if session.closed || session.cuda == nil || session.d3d11Device == 0 {
		session.mu.Unlock()
		return nil, ErrDecoderUnavailable
	}
	cudaModule := session.cuda.module
	device := unsafe.Pointer(session.d3d11Device)
	session.mu.Unlock()

	api, err := loadNVDECCUDAInteropAPI(cudaModule)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve CUDA D3D11 interop API: %v", ErrDecoderUnavailable, err)
	}

	desc := mfD3D11Texture2DDesc{
		Width:          uint32(width),
		Height:         uint32(height),
		MipLevels:      1,
		ArraySize:      1,
		Format:         dxgiFormatAYUV,
		SampleDesc:     mfD3D11SampleDesc{Count: 1},
		Usage:          d3d11UsageDefault,
		BindFlags:      d3d11BindRenderTarget,
		CPUAccessFlags: 0,
		MiscFlags:      0,
	}
	var texture unsafe.Pointer
	hr := comCall(
		device,
		5, // ID3D11Device::CreateTexture2D
		uintptr(unsafe.Pointer(&desc)),
		0,
		uintptr(unsafe.Pointer(&texture)),
	)
	if hresultFailed(hr) || texture == nil {
		if texture != nil {
			releaseIUnknown(texture)
		}
		if !hresultFailed(hr) {
			return nil, fmt.Errorf("%w: D3D11 AYUV texture is nil", ErrDecoderUnavailable)
		}
		return nil, fmt.Errorf("%w: %v", ErrDecoderUnavailable, hresultError("ID3D11Device.CreateTexture2D(AYUV)", hr))
	}

	surface := &nvdecD3D11AYUVInteropSurface{
		session: session,
		api:     api,
		texture: texture,
		width:   width,
		height:  height,
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = surface.Close()
		}
	}()

	err = session.withCUDAContextLock(func() error {
		status := cudaDriverCall(
			api.GraphicsD3D11RegisterResource,
			uintptr(unsafe.Pointer(&surface.graphicsResource)),
			uintptr(texture),
			uintptr(nvdecCUGraphicsRegisterSurfaceLDST),
		)
		if status != 0 || surface.graphicsResource == 0 {
			return fmt.Errorf("%w: cuGraphicsD3D11RegisterResource returned %d", ErrDecoderUnavailable, status)
		}
		if status = cudaDriverCall(
			api.GraphicsResourceSetMapFlags,
			surface.graphicsResource,
			uintptr(nvdecCUGraphicsMapWriteDiscard),
		); status != 0 {
			return fmt.Errorf("%w: cuGraphicsResourceSetMapFlags returned %d", ErrDecoderUnavailable, status)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	cleanup = false
	return surface, nil
}

func (s *nvdecD3D11AYUVInteropSurface) Map() (uintptr, error) {
	if s == nil {
		return 0, ErrDecoderUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.session == nil || s.graphicsResource == 0 || s.texture == nil {
		return 0, ErrDecoderUnavailable
	}
	if s.mapped {
		if s.mappedArray == 0 {
			return 0, ErrDecoderUnavailable
		}
		return s.mappedArray, nil
	}

	resource := s.graphicsResource
	var mappedArray uintptr
	err := s.session.withCUDAContextLock(func() error {
		if status := cudaDriverCall(
			s.api.GraphicsMapResources,
			1,
			uintptr(unsafe.Pointer(&resource)),
			0,
		); status != 0 {
			return fmt.Errorf("%w: cuGraphicsMapResources returned %d", ErrDecoderUnavailable, status)
		}
		status := cudaDriverCall(
			s.api.GraphicsSubResourceGetMappedArray,
			uintptr(unsafe.Pointer(&mappedArray)),
			resource,
			0,
			0,
		)
		if status != 0 || mappedArray == 0 {
			_ = cudaDriverCall(
				s.api.GraphicsUnmapResources,
				1,
				uintptr(unsafe.Pointer(&resource)),
				0,
			)
			return fmt.Errorf("%w: cuGraphicsSubResourceGetMappedArray returned %d", ErrDecoderUnavailable, status)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	s.mapped = true
	s.mappedArray = mappedArray
	return mappedArray, nil
}

func (s *nvdecD3D11AYUVInteropSurface) Unmap() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.mapped {
		return nil
	}
	if s.closed || s.session == nil || s.graphicsResource == 0 {
		s.mapped = false
		s.mappedArray = 0
		return nil
	}

	resource := s.graphicsResource
	err := s.session.withCUDAContextLock(func() error {
		if status := cudaDriverCall(
			s.api.GraphicsUnmapResources,
			1,
			uintptr(unsafe.Pointer(&resource)),
			0,
		); status != 0 {
			return fmt.Errorf("cuGraphicsUnmapResources returned %d", status)
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.mapped = false
	s.mappedArray = 0
	return nil
}

func (s *nvdecD3D11AYUVInteropSurface) Texture() uintptr {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0
	}
	return uintptr(s.texture)
}

func (s *nvdecD3D11AYUVInteropSurface) Dimensions() (int, int) {
	if s == nil {
		return 0, 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, 0
	}
	return s.width, s.height
}

func (s *nvdecD3D11AYUVInteropSurface) Close() error {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	session := s.session
	api := s.api
	resource := s.graphicsResource
	mapped := s.mapped
	texture := s.texture
	s.session = nil
	s.graphicsResource = 0
	s.mapped = false
	s.mappedArray = 0
	s.texture = nil
	s.width = 0
	s.height = 0
	s.mu.Unlock()

	var closeErr error
	if session != nil && resource != 0 {
		if err := session.withCUDAContextLock(func() error {
			if mapped {
				if status := cudaDriverCall(
					api.GraphicsUnmapResources,
					1,
					uintptr(unsafe.Pointer(&resource)),
					0,
				); status != 0 {
					return fmt.Errorf("cuGraphicsUnmapResources returned %d", status)
				}
			}
			if status := cudaDriverCall(api.GraphicsUnregisterResource, resource); status != 0 {
				return fmt.Errorf("cuGraphicsUnregisterResource returned %d", status)
			}
			return nil
		}); err != nil {
			closeErr = err
		}
	}
	releaseIUnknown(texture)
	return closeErr
}
