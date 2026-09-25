//go:build windows && amd64

package codec

import (
	"fmt"
	"runtime"
	"sync"
	"unsafe"
)

const (
	nvencInputResourceTypeDirectX int32 = 0
	nvencBufferFormatAYUV         int32 = 0x04000000
	nvencBufferUsageInputImage    int32 = 0
)

type nvencRegisterResource struct {
	Version            uint32
	ResourceType       int32
	Width              uint32
	Height             uint32
	Pitch              uint32
	SubResourceIndex   uint32
	ResourceToRegister uintptr
	RegisteredResource uintptr
	BufferFormat       int32
	BufferUsage        int32
	InputFencePoint    uintptr
	ChromaOffset       [2]uint32
	ChromaOffsetIn     [2]uint32
	Reserved1          [244]uint32
	Reserved2          [61]uintptr
}

type nvencMapInputResource struct {
	Version            uint32
	SubResourceIndex   uint32
	InputResource      uintptr
	RegisteredResource uintptr
	MappedResource     uintptr
	MappedBufferFormat int32
	Reserved1          [251]uint32
	Reserved2          [63]uintptr
}

type nvencCreateBitstreamBuffer struct {
	Version            uint32
	Size               uint32
	MemoryHeap         int32
	Reserved           uint32
	BitstreamBuffer    uintptr
	BitstreamBufferPtr uintptr
	Reserved1          [58]uint32
	Reserved2          [64]uintptr
}

type nvencD3D11InputResource struct {
	mu sync.Mutex

	session    *nvencD3D11Session
	registered uintptr
	mapped     uintptr
	mappedFmt  int32
	closed     bool
}

type nvencBitstreamBuffer struct {
	mu sync.Mutex

	session *nvencD3D11Session
	handle  uintptr
	closed  bool
}

func (s *nvencD3D11Session) registerAYUVD3D11Input(frame D3D11EncodeFrame) (*nvencD3D11InputResource, error) {
	if s == nil {
		return nil, ErrEncoderUnavailable
	}
	if err := frame.Validate(); err != nil {
		return nil, err
	}
	if frame.PixelFormat() != PixelFormatAYUV {
		return nil, fmt.Errorf("%w: NVENC HEVC 4:4:4 requires AYUV D3D11 input", ErrInvalidFrame)
	}
	if frame.Device == 0 {
		return nil, fmt.Errorf("%w: NVENC D3D11 input device is nil", ErrInvalidFrame)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.encoder == 0 || s.device == 0 {
		return nil, ErrEncoderUnavailable
	}
	if frame.Device != s.device {
		return nil, fmt.Errorf(
			"%w: NVENC input device %#x does not match encoder device %#x",
			ErrInvalidFrame,
			frame.Device,
			s.device,
		)
	}

	params := nvencRegisterResource{
		Version:            nvencStructVersion(5),
		ResourceType:       nvencInputResourceTypeDirectX,
		Width:              uint32(frame.Width),
		Height:             uint32(frame.Height),
		Pitch:              0,
		SubResourceIndex:   frame.Subresource,
		ResourceToRegister: frame.Resource,
		BufferFormat:       nvencBufferFormatAYUV,
		BufferUsage:        nvencBufferUsageInputImage,
	}
	status := nvencCall(
		s.api.NvEncRegisterResource,
		s.encoder,
		uintptr(unsafe.Pointer(&params)),
	)
	runtime.KeepAlive(&params)
	runtime.KeepAlive(frame)
	if status != 0 || params.RegisteredResource == 0 {
		return nil, fmt.Errorf("nvEncRegisterResource(D3D11 AYUV) returned %d", status)
	}

	return &nvencD3D11InputResource{
		session:    s,
		registered: params.RegisteredResource,
	}, nil
}

func (r *nvencD3D11InputResource) Map() (uintptr, int32, error) {
	if r == nil {
		return 0, 0, ErrEncoderUnavailable
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.registered == 0 {
		return 0, 0, ErrEncoderUnavailable
	}
	if r.mapped != 0 {
		return r.mapped, r.mappedFmt, nil
	}
	s := r.session
	if s == nil {
		return 0, 0, ErrEncoderUnavailable
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.encoder == 0 {
		return 0, 0, ErrEncoderUnavailable
	}
	params := nvencMapInputResource{
		Version:            nvencStructVersion(4),
		RegisteredResource: r.registered,
	}
	status := nvencCall(
		s.api.NvEncMapInputResource,
		s.encoder,
		uintptr(unsafe.Pointer(&params)),
	)
	runtime.KeepAlive(&params)
	if status != 0 || params.MappedResource == 0 {
		return 0, 0, fmt.Errorf("nvEncMapInputResource returned %d", status)
	}
	if params.MappedBufferFormat != nvencBufferFormatAYUV {
		_ = nvencCall(s.api.NvEncUnmapInputResource, s.encoder, params.MappedResource)
		return 0, 0, fmt.Errorf(
			"%w: NVENC mapped D3D11 format=%#x want AYUV=%#x",
			ErrInvalidFrame,
			uint32(params.MappedBufferFormat),
			uint32(nvencBufferFormatAYUV),
		)
	}
	r.mapped = params.MappedResource
	r.mappedFmt = params.MappedBufferFormat
	return r.mapped, r.mappedFmt, nil
}

func (r *nvencD3D11InputResource) Unmap() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.mapped == 0 {
		return nil
	}
	s := r.session
	if s == nil {
		return ErrEncoderUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.encoder == 0 {
		return ErrEncoderUnavailable
	}
	mapped := r.mapped
	if status := nvencCall(s.api.NvEncUnmapInputResource, s.encoder, mapped); status != 0 {
		return fmt.Errorf("nvEncUnmapInputResource returned %d", status)
	}
	r.mapped = 0
	r.mappedFmt = 0
	return nil
}

func (r *nvencD3D11InputResource) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	s := r.session
	if s == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.encoder == 0 {
		r.mapped = 0
		r.registered = 0
		return nil
	}
	var firstErr error
	if r.mapped != 0 {
		if status := nvencCall(s.api.NvEncUnmapInputResource, s.encoder, r.mapped); status != 0 {
			firstErr = fmt.Errorf("nvEncUnmapInputResource returned %d", status)
		}
		r.mapped = 0
		r.mappedFmt = 0
	}
	if r.registered != 0 {
		if status := nvencCall(s.api.NvEncUnregisterResource, s.encoder, r.registered); status != 0 && firstErr == nil {
			firstErr = fmt.Errorf("nvEncUnregisterResource returned %d", status)
		}
		r.registered = 0
	}
	return firstErr
}

func (s *nvencD3D11Session) createBitstreamBuffer() (*nvencBitstreamBuffer, error) {
	if s == nil {
		return nil, ErrEncoderUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.encoder == 0 {
		return nil, ErrEncoderUnavailable
	}
	params := nvencCreateBitstreamBuffer{
		Version: nvencStructVersion(1),
	}
	status := nvencCall(
		s.api.NvEncCreateBitstreamBuffer,
		s.encoder,
		uintptr(unsafe.Pointer(&params)),
	)
	runtime.KeepAlive(&params)
	if status != 0 || params.BitstreamBuffer == 0 {
		return nil, fmt.Errorf("nvEncCreateBitstreamBuffer returned %d", status)
	}
	return &nvencBitstreamBuffer{
		session: s,
		handle:  params.BitstreamBuffer,
	}, nil
}

func (b *nvencBitstreamBuffer) Handle() uintptr {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0
	}
	return b.handle
}

func (b *nvencBitstreamBuffer) Close() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	s := b.session
	handle := b.handle
	b.handle = 0
	if s == nil || handle == 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.encoder == 0 {
		return nil
	}
	if status := nvencCall(s.api.NvEncDestroyBitstreamBuffer, s.encoder, handle); status != 0 {
		return fmt.Errorf("nvEncDestroyBitstreamBuffer returned %d", status)
	}
	return nil
}
