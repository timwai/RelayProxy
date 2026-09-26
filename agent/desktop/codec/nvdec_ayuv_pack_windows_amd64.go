//go:build windows && amd64

package codec

import (
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	nvdecAYUVPackBlockX uint32 = 16
	nvdecAYUVPackBlockY uint32 = 16

	nvdecAYUVPackKernelName = "relay_nvdec_yuv444_to_ayuv"
	nvdecAYUVPackSurfaceName = "relay_nvdec_ayuv_surface"
)

// NVDEC YUV444 is three full-height 8-bit planes in Y,U,V order.
// DXGI_FORMAT_AYUV is V,U,Y,A in memory. This PTX performs only the
// planar-to-packed GPU shuffle and writes alpha=255.
const nvdecAYUVPackPTX = ".version 6.0\n" +
	".target sm_50\n" +
	".address_size 64\n" +
	"\n" +
	".global .surfref relay_nvdec_ayuv_surface;\n" +
	"\n" +
	".visible .entry relay_nvdec_yuv444_to_ayuv(\n" +
	"    .param .u64 src_ptr,\n" +
	"    .param .u32 src_pitch,\n" +
	"    .param .u32 surface_height,\n" +
	"    .param .u32 width,\n" +
	"    .param .u32 height\n" +
	")\n" +
	"{\n" +
	"    .reg .pred %p<3>;\n" +
	"    .reg .b16 %rs<5>;\n" +
	"    .reg .b32 %r<16>;\n" +
	"    .reg .b64 %rd<12>;\n" +
	"\n" +
	"    ld.param.u64 %rd1, [src_ptr];\n" +
	"    ld.param.u32 %r1, [src_pitch];\n" +
	"    ld.param.u32 %r2, [surface_height];\n" +
	"    ld.param.u32 %r3, [width];\n" +
	"    ld.param.u32 %r4, [height];\n" +
	"\n" +
	"    mov.u32 %r5, %ntid.x;\n" +
	"    mov.u32 %r6, %ctaid.x;\n" +
	"    mov.u32 %r7, %tid.x;\n" +
	"    mad.lo.u32 %r8, %r6, %r5, %r7;\n" +
	"\n" +
	"    mov.u32 %r9, %ntid.y;\n" +
	"    mov.u32 %r10, %ctaid.y;\n" +
	"    mov.u32 %r11, %tid.y;\n" +
	"    mad.lo.u32 %r12, %r10, %r9, %r11;\n" +
	"\n" +
	"    setp.ge.u32 %p1, %r8, %r3;\n" +
	"    setp.ge.u32 %p2, %r12, %r4;\n" +
	"    or.pred %p1, %p1, %p2;\n" +
	"    @%p1 bra DONE;\n" +
	"\n" +
	"    cvta.to.global.u64 %rd2, %rd1;\n" +
	"    mul.wide.u32 %rd3, %r1, %r12;\n" +
	"    cvt.u64.u32 %rd4, %r8;\n" +
	"    add.u64 %rd5, %rd3, %rd4;\n" +
	"    mul.wide.u32 %rd6, %r1, %r2;\n" +
	"    add.u64 %rd7, %rd2, %rd5;\n" +
	"    add.u64 %rd8, %rd7, %rd6;\n" +
	"    add.u64 %rd9, %rd8, %rd6;\n" +
	"\n" +
	"    ld.global.u8 %r13, [%rd7];\n" +
	"    ld.global.u8 %r14, [%rd8];\n" +
	"    ld.global.u8 %r15, [%rd9];\n" +
	"    cvt.u16.u32 %rs1, %r15;\n" +
	"    cvt.u16.u32 %rs2, %r14;\n" +
	"    cvt.u16.u32 %rs3, %r13;\n" +
	"    mov.u16 %rs4, 255;\n" +
	"\n" +
	"    shl.b32 %r8, %r8, 2;\n" +
	"    sust.b.2d.v4.b8.trap [relay_nvdec_ayuv_surface, {%r8, %r12}], {%rs1, %rs2, %rs3, %rs4};\n" +
	"\n" +
	"DONE:\n" +
	"    ret;\n" +
	"}\n"

type nvdecCUDAKernelAPI struct {
	ModuleLoadData    uintptr
	ModuleGetFunction uintptr
	ModuleGetSurfRef  uintptr
	SurfRefSetArray   uintptr
	LaunchKernel      uintptr
	CtxSynchronize    uintptr
	ModuleUnload      uintptr
}

type nvdecAYUVPacker struct {
	mu sync.Mutex

	session  *nvdecD3D11Session
	api      nvdecCUDAKernelAPI
	module   uintptr
	function uintptr
	surfRef  uintptr
	closed   bool
}

func loadNVDECCUDAKernelAPI(module windows.Handle) (nvdecCUDAKernelAPI, error) {
	if module == 0 {
		return nvdecCUDAKernelAPI{}, ErrDecoderUnavailable
	}
	resolve := func(name string) (uintptr, error) {
		proc, err := windows.GetProcAddress(module, name)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", name, err)
		}
		return proc, nil
	}

	var api nvdecCUDAKernelAPI
	var err error
	if api.ModuleLoadData, err = resolve("cuModuleLoadData"); err != nil {
		return nvdecCUDAKernelAPI{}, err
	}
	if api.ModuleGetFunction, err = resolve("cuModuleGetFunction"); err != nil {
		return nvdecCUDAKernelAPI{}, err
	}
	if api.ModuleGetSurfRef, err = resolve("cuModuleGetSurfRef"); err != nil {
		return nvdecCUDAKernelAPI{}, err
	}
	if api.SurfRefSetArray, err = resolve("cuSurfRefSetArray"); err != nil {
		return nvdecCUDAKernelAPI{}, err
	}
	if api.LaunchKernel, err = resolve("cuLaunchKernel"); err != nil {
		return nvdecCUDAKernelAPI{}, err
	}
	if api.CtxSynchronize, err = resolve("cuCtxSynchronize"); err != nil {
		return nvdecCUDAKernelAPI{}, err
	}
	if api.ModuleUnload, err = resolve("cuModuleUnload"); err != nil {
		return nvdecCUDAKernelAPI{}, err
	}
	if err := validateNVDECCUDAKernelAPI(api); err != nil {
		return nvdecCUDAKernelAPI{}, err
	}
	return api, nil
}

func validateNVDECCUDAKernelAPI(api nvdecCUDAKernelAPI) error {
	required := []struct {
		name string
		proc uintptr
	}{
		{"cuModuleLoadData", api.ModuleLoadData},
		{"cuModuleGetFunction", api.ModuleGetFunction},
		{"cuModuleGetSurfRef", api.ModuleGetSurfRef},
		{"cuSurfRefSetArray", api.SurfRefSetArray},
		{"cuLaunchKernel", api.LaunchKernel},
		{"cuCtxSynchronize", api.CtxSynchronize},
		{"cuModuleUnload", api.ModuleUnload},
	}
	for _, entry := range required {
		if entry.proc == 0 {
			return fmt.Errorf("%s is unavailable", entry.name)
		}
	}
	return nil
}

func nulTerminatedASCII(value string) []byte {
	out := make([]byte, len(value)+1)
	copy(out, value)
	return out
}

func nvdecPackGrid(width, height int) (uint32, uint32, error) {
	if width <= 0 || height <= 0 {
		return 0, 0, fmt.Errorf("%w: invalid NVDEC pack dimensions %dx%d", ErrDecoderUnavailable, width, height)
	}
	gridX := (uint32(width) + nvdecAYUVPackBlockX - 1) / nvdecAYUVPackBlockX
	gridY := (uint32(height) + nvdecAYUVPackBlockY - 1) / nvdecAYUVPackBlockY
	return gridX, gridY, nil
}

func newNVDECAYUVPacker(session *nvdecD3D11Session) (*nvdecAYUVPacker, error) {
	if session == nil {
		return nil, ErrDecoderUnavailable
	}

	session.mu.Lock()
	if session.closed || session.cuda == nil || session.cuda.module == 0 {
		session.mu.Unlock()
		return nil, ErrDecoderUnavailable
	}
	cudaModule := session.cuda.module
	session.mu.Unlock()

	api, err := loadNVDECCUDAKernelAPI(cudaModule)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve CUDA kernel API: %v", ErrDecoderUnavailable, err)
	}
	packer := &nvdecAYUVPacker{session: session, api: api}
	ptx := nulTerminatedASCII(nvdecAYUVPackPTX)
	kernelName := nulTerminatedASCII(nvdecAYUVPackKernelName)
	surfaceName := nulTerminatedASCII(nvdecAYUVPackSurfaceName)

	err = session.withCUDAContextLock(func() error {
		status := cudaDriverCall(
			api.ModuleLoadData,
			uintptr(unsafe.Pointer(&packer.module)),
			uintptr(unsafe.Pointer(&ptx[0])),
		)
		if status != 0 || packer.module == 0 {
			return fmt.Errorf("%w: cuModuleLoadData returned %d", ErrDecoderUnavailable, status)
		}
		if status = cudaDriverCall(
			api.ModuleGetFunction,
			uintptr(unsafe.Pointer(&packer.function)),
			packer.module,
			uintptr(unsafe.Pointer(&kernelName[0])),
		); status != 0 || packer.function == 0 {
			return fmt.Errorf("%w: cuModuleGetFunction returned %d", ErrDecoderUnavailable, status)
		}
		if status = cudaDriverCall(
			api.ModuleGetSurfRef,
			uintptr(unsafe.Pointer(&packer.surfRef)),
			packer.module,
			uintptr(unsafe.Pointer(&surfaceName[0])),
		); status != 0 || packer.surfRef == 0 {
			return fmt.Errorf("%w: cuModuleGetSurfRef returned %d", ErrDecoderUnavailable, status)
		}
		return nil
	})
	runtime.KeepAlive(ptx)
	runtime.KeepAlive(kernelName)
	runtime.KeepAlive(surfaceName)
	if err != nil {
		if packer.module != 0 {
			_ = session.withCUDAContextLock(func() error {
				status := cudaDriverCall(api.ModuleUnload, packer.module)
				packer.module = 0
				packer.function = 0
				packer.surfRef = 0
				if status != 0 {
					return fmt.Errorf("cuModuleUnload returned %d", status)
				}
				return nil
			})
		}
		return nil, err
	}
	return packer, nil
}

func (p *nvdecAYUVPacker) pack(
	frame *nvdecMappedFrame,
	dstArray uintptr,
	width int,
	height int,
) error {
	if p == nil || frame == nil || dstArray == 0 {
		return ErrDecoderUnavailable
	}
	gridX, gridY, err := nvdecPackGrid(width, height)
	if err != nil {
		return err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.session == nil || p.module == 0 || p.function == 0 || p.surfRef == 0 {
		return ErrDecoderUnavailable
	}
	session := p.session
	api := p.api

	return frame.withMappedData(func(
		frameSession *nvdecD3D11Session,
		devicePtr uint64,
		pitch uint32,
		frameWidth int,
		frameHeight int,
	) error {
		if frameSession != session {
			return fmt.Errorf("%w: NVDEC pack source and destination sessions differ", ErrDecoderUnavailable)
		}
		if frameWidth != width || frameHeight != height {
			return fmt.Errorf(
				"%w: NVDEC pack frame=%dx%d destination=%dx%d",
				ErrDecoderUnavailable,
				frameWidth,
				frameHeight,
				width,
				height,
			)
		}
		if pitch < uint32(width) {
			return fmt.Errorf(
				"%w: NVDEC YUV444 pitch=%d is smaller than width=%d",
				ErrDecoderUnavailable,
				pitch,
				width,
			)
		}

		src := devicePtr
		srcPitch := pitch
		surfaceHeight := uint32(height)
		dstWidth := uint32(width)
		dstHeight := uint32(height)
		args := [...]uintptr{
			uintptr(unsafe.Pointer(&src)),
			uintptr(unsafe.Pointer(&srcPitch)),
			uintptr(unsafe.Pointer(&surfaceHeight)),
			uintptr(unsafe.Pointer(&dstWidth)),
			uintptr(unsafe.Pointer(&dstHeight)),
		}

		err := session.withCUDAContextLock(func() error {
			if status := cudaDriverCall(api.SurfRefSetArray, p.surfRef, dstArray, 0); status != 0 {
				return fmt.Errorf("%w: cuSurfRefSetArray returned %d", ErrDecoderUnavailable, status)
			}
			if status := cudaDriverCall(
				api.LaunchKernel,
				p.function,
				uintptr(gridX),
				uintptr(gridY),
				1,
				uintptr(nvdecAYUVPackBlockX),
				uintptr(nvdecAYUVPackBlockY),
				1,
				0,
				0,
				uintptr(unsafe.Pointer(&args[0])),
				0,
			); status != 0 {
				return fmt.Errorf("%w: cuLaunchKernel returned %d", ErrDecoderUnavailable, status)
			}
			if status := cudaDriverCall(api.CtxSynchronize); status != 0 {
				return fmt.Errorf("%w: cuCtxSynchronize returned %d", ErrDecoderUnavailable, status)
			}
			return nil
		})
		runtime.KeepAlive(src)
		runtime.KeepAlive(srcPitch)
		runtime.KeepAlive(surfaceHeight)
		runtime.KeepAlive(dstWidth)
		runtime.KeepAlive(dstHeight)
		runtime.KeepAlive(args)
		return err
	})
}

func (p *nvdecAYUVPacker) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	session := p.session
	api := p.api
	module := p.module
	p.session = nil
	p.module = 0
	p.function = 0
	p.surfRef = 0
	p.mu.Unlock()

	if session == nil || module == 0 {
		return nil
	}
	return session.withCUDAContextLock(func() error {
		if status := cudaDriverCall(api.ModuleUnload, module); status != 0 {
			return fmt.Errorf("cuModuleUnload returned %d", status)
		}
		return nil
	})
}
