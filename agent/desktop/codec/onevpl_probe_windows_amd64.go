//go:build windows && amd64

package codec

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	oneVPLDLLName = "libvpl.dll"

	oneVPLVariantVersion = 0x0101
	oneVPLVariantTypeU32 = 5

	oneVPLImplTypeHardware = 0x0002
	oneVPLAccelD3D11       = 0x0300
	oneVPLVendorIntel      = 0x8086

	oneVPLResourceSystemSurface = 1

	oneVPLErrUnsupported = -3
	oneVPLErrNotFound    = -9
)

var (
	oneVPLCodecHEVC  = oneVPLFourCC('H', 'E', 'V', 'C')
	oneVPLFourCCAYUV = oneVPLFourCC('A', 'Y', 'U', 'V')
)

const (
	oneVPLPropImpl   = "mfxImplDescription.Impl"
	oneVPLPropAccel  = "mfxImplDescription.AccelerationMode"
	oneVPLPropVendor = "mfxImplDescription.VendorID"

	oneVPLPropHEVCEncoder      = "mfxImplDescription.mfxEncoderDescription.encoder.CodecID"
	oneVPLPropHEVCEncoderMemory = "mfxImplDescription.mfxEncoderDescription.encoder.encprofile.encmemdesc.MemHandleType"
	oneVPLPropHEVCEncoderColor = "mfxImplDescription.mfxEncoderDescription.encoder.encprofile.encmemdesc.ColorFormats"

	oneVPLPropHEVCDecoder      = "mfxImplDescription.mfxDecoderDescription.decoder.CodecID"
	oneVPLPropHEVCDecoderMemory = "mfxImplDescription.mfxDecoderDescription.decoder.decprofile.decmemdesc.MemHandleType"
	oneVPLPropHEVCDecoderColor = "mfxImplDescription.mfxDecoderDescription.decoder.decprofile.decmemdesc.ColorFormats"
)

// oneVPLVariant mirrors mfxVariant on 64-bit Windows. The 16-byte structure is
// passed indirectly by the Microsoft x64 ABI, which is why this probe is
// restricted to windows/amd64 until another ABI is explicitly validated.
type oneVPLVariant struct {
	Version uint16
	_       uint16
	Type    uint32
	Data    uint64
}

type oneVPLFilter struct {
	name  string
	value uint32
}

type oneVPLAPI struct {
	module windows.Handle

	mfxLoad                    uintptr
	mfxUnload                  uintptr
	mfxCreateConfig            uintptr
	mfxSetConfigFilterProperty uintptr
	mfxCreateSession           uintptr
	mfxClose                   uintptr
}

func oneVPLFourCC(a, b, c, d byte) uint32 {
	return uint32(a) | uint32(b)<<8 | uint32(c)<<16 | uint32(d)<<24
}

func oneVPLStatus(value uintptr) int32 {
	return int32(uint32(value))
}

func loadOneVPLAPI() (*oneVPLAPI, error) {
	module, err := windows.LoadLibrary(oneVPLDLLName)
	if err != nil {
		return nil, err
	}
	api := &oneVPLAPI{module: module}
	fail := func(err error) (*oneVPLAPI, error) {
		_ = windows.FreeLibrary(module)
		return nil, err
	}
	resolve := func(name string) (uintptr, error) {
		proc, err := windows.GetProcAddress(module, name)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", name, err)
		}
		return proc, nil
	}
	if api.mfxLoad, err = resolve("MFXLoad"); err != nil {
		return fail(err)
	}
	if api.mfxUnload, err = resolve("MFXUnload"); err != nil {
		return fail(err)
	}
	if api.mfxCreateConfig, err = resolve("MFXCreateConfig"); err != nil {
		return fail(err)
	}
	if api.mfxSetConfigFilterProperty, err = resolve("MFXSetConfigFilterProperty"); err != nil {
		return fail(err)
	}
	if api.mfxCreateSession, err = resolve("MFXCreateSession"); err != nil {
		return fail(err)
	}
	if api.mfxClose, err = resolve("MFXClose"); err != nil {
		return fail(err)
	}
	return api, nil
}

func (a *oneVPLAPI) Close() {
	if a == nil || a.module == 0 {
		return
	}
	_ = windows.FreeLibrary(a.module)
	a.module = 0
}

func (a *oneVPLAPI) newLoader() (uintptr, error) {
	if a == nil || a.mfxLoad == 0 {
		return 0, errors.New("oneVPL API is unavailable")
	}
	loader, _, _ := syscall.SyscallN(a.mfxLoad)
	if loader == 0 {
		return 0, errors.New("MFXLoad returned nil")
	}
	return loader, nil
}

func (a *oneVPLAPI) unload(loader uintptr) {
	if a == nil || loader == 0 || a.mfxUnload == 0 {
		return
	}
	syscall.SyscallN(a.mfxUnload, loader)
}

func (a *oneVPLAPI) createConfig(loader uintptr) (uintptr, error) {
	config, _, _ := syscall.SyscallN(a.mfxCreateConfig, loader)
	if config == 0 {
		return 0, errors.New("MFXCreateConfig returned nil")
	}
	return config, nil
}

func (a *oneVPLAPI) setU32(config uintptr, name string, value uint32) error {
	namePtr, err := windows.BytePtrFromString(name)
	if err != nil {
		return err
	}
	variant := oneVPLVariant{
		Version: oneVPLVariantVersion,
		Type:    oneVPLVariantTypeU32,
		Data:    uint64(value),
	}
	status, _, _ := syscall.SyscallN(
		a.mfxSetConfigFilterProperty,
		config,
		uintptr(unsafe.Pointer(namePtr)),
		uintptr(unsafe.Pointer(&variant)),
	)
	runtime.KeepAlive(namePtr)
	runtime.KeepAlive(variant)
	if got := oneVPLStatus(status); got != 0 {
		return fmt.Errorf("MFXSetConfigFilterProperty(%s) returned %d", name, got)
	}
	return nil
}

func (a *oneVPLAPI) createSession(loader uintptr) (uintptr, int32) {
	var session uintptr
	status, _, _ := syscall.SyscallN(
		a.mfxCreateSession,
		loader,
		0,
		uintptr(unsafe.Pointer(&session)),
	)
	return session, oneVPLStatus(status)
}

func (a *oneVPLAPI) closeSession(session uintptr) {
	if a == nil || session == 0 || a.mfxClose == 0 {
		return
	}
	syscall.SyscallN(a.mfxClose, session)
}

func oneVPLSessionAvailable(
	ctx context.Context,
	api *oneVPLAPI,
	filters []oneVPLFilter,
) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	loader, err := api.newLoader()
	if err != nil {
		return false, err
	}
	defer api.unload(loader)

	for _, filter := range filters {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		config, err := api.createConfig(loader)
		if err != nil {
			return false, err
		}
		if err := api.setU32(config, filter.name, filter.value); err != nil {
			return false, err
		}
	}

	session, status := api.createSession(loader)
	if session != 0 {
		defer api.closeSession(session)
	}
	switch status {
	case 0:
		return session != 0, nil
	case oneVPLErrUnsupported, oneVPLErrNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("MFXCreateSession returned %d", status)
	}
}

func oneVPLBaseFilters() []oneVPLFilter {
	return []oneVPLFilter{
		{name: oneVPLPropImpl, value: oneVPLImplTypeHardware},
		{name: oneVPLPropAccel, value: oneVPLAccelD3D11},
		{name: oneVPLPropVendor, value: oneVPLVendorIntel},
	}
}

func oneVPLDirectionFilters(codecProperty, colorProperty string) []oneVPLFilter {
	filters := append([]oneVPLFilter(nil), oneVPLBaseFilters()...)
	filters = append(filters,
		oneVPLFilter{name: codecProperty, value: oneVPLCodecHEVC},
		oneVPLFilter{name: colorProperty, value: oneVPLFourCCAYUV},
	)
	return filters
}

func oneVPLDirectionMemoryFilters(
	codecProperty string,
	memoryProperty string,
	colorProperty string,
	resourceType uint32,
) []oneVPLFilter {
	filters := oneVPLDirectionFilters(codecProperty, colorProperty)
	return append(filters, oneVPLFilter{name: memoryProperty, value: resourceType})
}

// ProbeOneVPLHEVC444 asks the oneVPL dispatcher for a hardware D3D11 Intel
// implementation whose HEVC encoder/decoder advertises AYUV. It does not infer
// support from GPU model names and does not reserve an encoder session.
func ProbeOneVPLHEVC444(ctx context.Context) OneVPLProbe {
	probe := OneVPLProbe{}
	if err := ctx.Err(); err != nil {
		probe.Error = err.Error()
		return probe
	}

	api, err := loadOneVPLAPI()
	if err != nil {
		probe.Error = err.Error()
		return probe
	}
	defer api.Close()
	probe.DispatcherAvailable = true

	probe.HardwareRuntime, err = oneVPLSessionAvailable(ctx, api, oneVPLBaseFilters())
	if err != nil {
		probe.Error = err.Error()
		return probe
	}
	if !probe.HardwareRuntime {
		return probe
	}

	probe.HEVC444SystemEncode, err = oneVPLSessionAvailable(
		ctx,
		api,
		oneVPLDirectionMemoryFilters(
			oneVPLPropHEVCEncoder,
			oneVPLPropHEVCEncoderMemory,
			oneVPLPropHEVCEncoderColor,
			oneVPLResourceSystemSurface,
		),
	)
	if err != nil {
		probe.Error = err.Error()
		return probe
	}
	probe.HEVC444D3D11Encode, err = oneVPLSessionAvailable(
		ctx,
		api,
		oneVPLDirectionMemoryFilters(
			oneVPLPropHEVCEncoder,
			oneVPLPropHEVCEncoderMemory,
			oneVPLPropHEVCEncoderColor,
			oneVPLResourceDX11Texture,
		),
	)
	if err != nil {
		probe.Error = err.Error()
		return probe
	}

	probe.HEVC444SystemDecode, err = oneVPLSessionAvailable(
		ctx,
		api,
		oneVPLDirectionMemoryFilters(
			oneVPLPropHEVCDecoder,
			oneVPLPropHEVCDecoderMemory,
			oneVPLPropHEVCDecoderColor,
			oneVPLResourceSystemSurface,
		),
	)
	if err != nil {
		probe.Error = err.Error()
		return probe
	}
	probe.HEVC444D3D11Decode, err = oneVPLSessionAvailable(
		ctx,
		api,
		oneVPLDirectionMemoryFilters(
			oneVPLPropHEVCDecoder,
			oneVPLPropHEVCDecoderMemory,
			oneVPLPropHEVCDecoderColor,
			oneVPLResourceDX11Texture,
		),
	)
	if err != nil {
		probe.Error = err.Error()
		return probe
	}

	probe.HEVC444Encode = probe.HEVC444SystemEncode || probe.HEVC444D3D11Encode
	probe.HEVC444Decode = probe.HEVC444SystemDecode || probe.HEVC444D3D11Decode
	return probe
}
