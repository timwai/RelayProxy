//go:build windows

package codec

import (
	"context"
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	mfVersion = 0x00020070

	mfStartupLite = 0x00000001

	mftEnumSync          = 0x00000001
	mftEnumAsync         = 0x00000002
	mftEnumHardware      = 0x00000004
	mftEnumLocal         = 0x00000010
	mftEnumSortAndFilter = 0x00000040

	rpcEChangedMode = 0x80010106
)

var (
	mfplatDLL       = windows.NewLazySystemDLL("mfplat.dll")
	ole32DLL        = windows.NewLazySystemDLL("ole32.dll")
	procMFStartup   = mfplatDLL.NewProc("MFStartup")
	procMFShutdown  = mfplatDLL.NewProc("MFShutdown")
	procMFTEnumEx   = mfplatDLL.NewProc("MFTEnumEx")
	procCoInitEx    = ole32DLL.NewProc("CoInitializeEx")
	procCoUninit    = ole32DLL.NewProc("CoUninitialize")
	procCoTaskFree  = ole32DLL.NewProc("CoTaskMemFree")
)

var (
	mftCategoryVideoEncoder = windows.GUID{
		Data1: 0xf79eac7d, Data2: 0xe545, Data3: 0x4387,
		Data4: [8]byte{0xbd, 0xee, 0xd6, 0x47, 0xd7, 0xbd, 0xe4, 0x2a},
	}
	mftCategoryVideoDecoder = windows.GUID{
		Data1: 0xd6c02d4b, Data2: 0x6833, Data3: 0x45b4,
		Data4: [8]byte{0x97, 0x1a, 0x05, 0xa4, 0xb0, 0x4b, 0xab, 0x91},
	}
	mfMediaTypeVideo = mediaTypeGUID(0x73646976) // 'vids'
	mfVideoFormatH264 = mediaTypeGUID(fourCC('H', '2', '6', '4'))
	mfVideoFormatNV12 = mediaTypeGUID(fourCC('N', 'V', '1', '2'))
)

type mftRegisterTypeInfo struct {
	MajorType windows.GUID
	Subtype   windows.GUID
}

func fourCC(a, b, c, d byte) uint32 {
	return uint32(a) | uint32(b)<<8 | uint32(c)<<16 | uint32(d)<<24
}

func mediaTypeGUID(data1 uint32) windows.GUID {
	return windows.GUID{
		Data1: data1,
		Data2: 0x0000,
		Data3: 0x0010,
		Data4: [8]byte{0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71},
	}
}

func hresultFailed(value uintptr) bool {
	return int32(uint32(value)) < 0
}

func hresultError(op string, value uintptr) error {
	return fmt.Errorf("%s failed with HRESULT 0x%08x", op, uint32(value))
}

func initializeCOM() (func(), error) {
	hr, _, _ := procCoInitEx.Call(0, 0) // COINIT_MULTITHREADED
	if !hresultFailed(hr) {
		return func() { procCoUninit.Call() }, nil
	}
	// A Wails thread may already be initialized STA. COM is still usable on
	// that thread; only the requested apartment model cannot be changed.
	if uint32(hr) == rpcEChangedMode {
		return func() {}, nil
	}
	return nil, hresultError("CoInitializeEx", hr)
}

func startupMediaFoundation() (func(), error) {
	hr, _, _ := procMFStartup.Call(mfVersion, mfStartupLite)
	if hresultFailed(hr) {
		return nil, hresultError("MFStartup", hr)
	}
	return func() { procMFShutdown.Call() }, nil
}

func releaseIUnknown(object unsafe.Pointer) {
	if object == nil {
		return
	}
	vtable := *(*unsafe.Pointer)(object)
	if vtable == nil {
		return
	}
	release := (*[3]uintptr)(vtable)[2]
	if release != 0 {
		syscall.SyscallN(release, uintptr(object))
	}
}

func enumerateMFT(category windows.GUID, flags uint32, input, output *mftRegisterTypeInfo) (int, error) {
	var activations unsafe.Pointer
	var count uint32
	hr, _, _ := procMFTEnumEx.Call(
		uintptr(unsafe.Pointer(&category)),
		uintptr(flags),
		uintptr(unsafe.Pointer(input)),
		uintptr(unsafe.Pointer(output)),
		uintptr(unsafe.Pointer(&activations)),
		uintptr(unsafe.Pointer(&count)),
	)
	if hresultFailed(hr) {
		return 0, hresultError("MFTEnumEx", hr)
	}
	if activations != nil {
		items := unsafe.Slice((*unsafe.Pointer)(activations), int(count))
		for _, item := range items {
			releaseIUnknown(item)
		}
		procCoTaskFree.Call(uintptr(activations))
	}
	return int(count), nil
}

// ProbeH264MediaFoundation verifies the transforms needed by the planned
// low-latency pipeline: NV12 -> H.264 encode and H.264 -> NV12 decode. It does
// not instantiate a transform, so it is safe to run once during desktop-host
// initialization without reserving a GPU encoder session.
func ProbeH264MediaFoundation(ctx context.Context) H264Probe {
	probe := H264Probe{}
	if err := ctx.Err(); err != nil {
		probe.Error = err.Error()
		return probe
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	uninitCOM, err := initializeCOM()
	if err != nil {
		probe.Error = err.Error()
		return probe
	}
	defer uninitCOM()

	shutdownMF, err := startupMediaFoundation()
	if err != nil {
		probe.Error = err.Error()
		return probe
	}
	defer shutdownMF()
	probe.MediaFoundation = true

	rawNV12 := mftRegisterTypeInfo{MajorType: mfMediaTypeVideo, Subtype: mfVideoFormatNV12}
	h264 := mftRegisterTypeInfo{MajorType: mfMediaTypeVideo, Subtype: mfVideoFormatH264}
	hardwareFlags := uint32(mftEnumHardware | mftEnumSortAndFilter)
	softwareFlags := uint32(mftEnumSync | mftEnumAsync | mftEnumLocal | mftEnumSortAndFilter)

	probe.HardwareEncoderCount, err = enumerateMFT(mftCategoryVideoEncoder, hardwareFlags, &rawNV12, &h264)
	if err != nil {
		probe.Error = err.Error()
		return probe
	}
	probe.HardwareDecoderCount, err = enumerateMFT(mftCategoryVideoDecoder, hardwareFlags, &h264, &rawNV12)
	if err != nil {
		probe.Error = err.Error()
		return probe
	}
	probe.SoftwareEncoderCount, err = enumerateMFT(mftCategoryVideoEncoder, softwareFlags, &rawNV12, &h264)
	if err != nil {
		probe.Error = err.Error()
		return probe
	}
	probe.SoftwareDecoderCount, err = enumerateMFT(mftCategoryVideoDecoder, softwareFlags, &h264, &rawNV12)
	if err != nil {
		probe.Error = err.Error()
	}
	return probe
}
