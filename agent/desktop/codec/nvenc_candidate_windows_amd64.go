//go:build windows && amd64

package codec

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

const (
	nvencAPIMajorVersion uint32 = 13
	nvencAPIMinorVersion uint32 = 1
	nvencAPIVersion             = nvencAPIMajorVersion | (nvencAPIMinorVersion << 24)
	nvencMaxVersionCode         = (nvencAPIMajorVersion << 4) | nvencAPIMinorVersion

	nvencDeviceTypeCUDA          int32 = 1
	nvencCapsSupportYUV444Encode int32 = 33
)

type nvencGUID struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

var nvencCodecHEVCGUID = nvencGUID{
	Data1: 0x790cdc88,
	Data2: 0x4522,
	Data3: 0x4d7b,
	Data4: [8]byte{0x94, 0x25, 0xbd, 0xa9, 0x97, 0x5f, 0x76, 0x03},
}

// nvEncodeAPIFunctionList mirrors NV_ENCODE_API_FUNCTION_LIST from
// nv-codec-headers 13.1. Keep the complete layout: NvEncodeAPICreateInstance
// writes the whole function table even though this candidate probe only calls a
// small subset of entries.
type nvEncodeAPIFunctionList struct {
	Version  uint32
	Reserved uint32

	NvEncOpenEncodeSession         uintptr
	NvEncGetEncodeGUIDCount        uintptr
	NvEncGetEncodeProfileGUIDCount uintptr
	NvEncGetEncodeProfileGUIDs     uintptr
	NvEncGetEncodeGUIDs            uintptr
	NvEncGetInputFormatCount       uintptr
	NvEncGetInputFormats           uintptr
	NvEncGetEncodeCaps             uintptr
	NvEncGetEncodePresetCount      uintptr
	NvEncGetEncodePresetGUIDs      uintptr
	NvEncGetEncodePresetConfig     uintptr
	NvEncInitializeEncoder         uintptr
	NvEncCreateInputBuffer         uintptr
	NvEncDestroyInputBuffer        uintptr
	NvEncCreateBitstreamBuffer     uintptr
	NvEncDestroyBitstreamBuffer    uintptr
	NvEncEncodePicture             uintptr
	NvEncLockBitstream             uintptr
	NvEncUnlockBitstream           uintptr
	NvEncLockInputBuffer           uintptr
	NvEncUnlockInputBuffer         uintptr
	NvEncGetEncodeStats            uintptr
	NvEncGetSequenceParams         uintptr
	NvEncRegisterAsyncEvent        uintptr
	NvEncUnregisterAsyncEvent      uintptr
	NvEncMapInputResource          uintptr
	NvEncUnmapInputResource        uintptr
	NvEncDestroyEncoder            uintptr
	NvEncInvalidateRefFrames       uintptr
	NvEncOpenEncodeSessionEx       uintptr
	NvEncRegisterResource          uintptr
	NvEncUnregisterResource        uintptr
	NvEncReconfigureEncoder        uintptr
	Reserved1                      uintptr
	NvEncCreateMVBuffer            uintptr
	NvEncDestroyMVBuffer           uintptr
	NvEncRunMotionEstimationOnly   uintptr
	NvEncGetLastErrorString        uintptr
	NvEncSetIOCudaStreams          uintptr
	NvEncGetEncodePresetConfigEx   uintptr
	NvEncGetSequenceParamEx        uintptr
	NvEncRestoreEncoderState       uintptr
	NvEncLookaheadPicture          uintptr
	Reserved2                      [275]uintptr
}

type nvencOpenEncodeSessionExParams struct {
	Version    uint32
	DeviceType int32
	Device     uintptr
	Reserved   uintptr
	APIVersion uint32
	Reserved1  [253]uint32
	Reserved2  [64]uintptr
}

type nvencCapsParam struct {
	Version     uint32
	CapsToQuery int32
	Reserved    [62]uint32
}

type nvidiaNVENCDeviceProbe struct {
	Checked     bool
	DeviceCount int
	HEVC444     bool
	Error       string
}

func nvencStructVersion(structVersion uint32) uint32 {
	return nvencAPIVersion | (structVersion << 16) | (0x7 << 28)
}

func nvencCall(proc uintptr, args ...uintptr) int32 {
	status, _, _ := syscall.SyscallN(proc, args...)
	return runtimeCandidateStatus(status)
}

func createNVENCFunctionList(createInstance uintptr) (nvEncodeAPIFunctionList, error) {
	if createInstance == 0 {
		return nvEncodeAPIFunctionList{}, fmt.Errorf("NvEncodeAPICreateInstance is unavailable")
	}
	api := nvEncodeAPIFunctionList{
		Version: nvencStructVersion(2),
	}
	status := nvencCall(createInstance, uintptr(unsafe.Pointer(&api)))
	runtime.KeepAlive(&api)
	if status != 0 {
		return nvEncodeAPIFunctionList{}, fmt.Errorf("NvEncodeAPICreateInstance returned %d", status)
	}
	switch {
	case api.NvEncOpenEncodeSessionEx == 0:
		return nvEncodeAPIFunctionList{}, fmt.Errorf("nvEncOpenEncodeSessionEx is unavailable")
	case api.NvEncGetEncodeGUIDCount == 0:
		return nvEncodeAPIFunctionList{}, fmt.Errorf("nvEncGetEncodeGUIDCount is unavailable")
	case api.NvEncGetEncodeGUIDs == 0:
		return nvEncodeAPIFunctionList{}, fmt.Errorf("nvEncGetEncodeGUIDs is unavailable")
	case api.NvEncGetEncodeCaps == 0:
		return nvEncodeAPIFunctionList{}, fmt.Errorf("nvEncGetEncodeCaps is unavailable")
	case api.NvEncDestroyEncoder == 0:
		return nvEncodeAPIFunctionList{}, fmt.Errorf("nvEncDestroyEncoder is unavailable")
	}
	return api, nil
}

func nvencHasGUID(guids []nvencGUID, target nvencGUID) bool {
	for _, guid := range guids {
		if guid == target {
			return true
		}
	}
	return false
}

func probeNVENCHEVC444OnSession(api nvEncodeAPIFunctionList, encoder uintptr) (bool, bool, error) {
	var guidCount uint32
	if status := nvencCall(
		api.NvEncGetEncodeGUIDCount,
		encoder,
		uintptr(unsafe.Pointer(&guidCount)),
	); status != 0 {
		return false, false, fmt.Errorf("nvEncGetEncodeGUIDCount returned %d", status)
	}
	runtime.KeepAlive(&guidCount)
	if guidCount == 0 {
		return true, false, nil
	}
	if guidCount > 64 {
		return false, false, fmt.Errorf("nvEncGetEncodeGUIDCount returned unreasonable count %d", guidCount)
	}

	guids := make([]nvencGUID, guidCount)
	var actual uint32
	if status := nvencCall(
		api.NvEncGetEncodeGUIDs,
		encoder,
		uintptr(unsafe.Pointer(&guids[0])),
		uintptr(guidCount),
		uintptr(unsafe.Pointer(&actual)),
	); status != 0 {
		return false, false, fmt.Errorf("nvEncGetEncodeGUIDs returned %d", status)
	}
	runtime.KeepAlive(guids)
	runtime.KeepAlive(&actual)
	if actual > uint32(len(guids)) {
		actual = uint32(len(guids))
	}
	if !nvencHasGUID(guids[:actual], nvencCodecHEVCGUID) {
		return true, false, nil
	}

	caps := nvencCapsParam{
		Version:     nvencStructVersion(1),
		CapsToQuery: nvencCapsSupportYUV444Encode,
	}
	var supported int32

	// On Windows amd64 the Microsoft ABI passes a 16-byte GUID value indirectly.
	// SyscallN therefore receives a pointer to a stable GUID copy for the
	// by-value GUID parameter of nvEncGetEncodeCaps.
	hevcGUID := nvencCodecHEVCGUID
	status := nvencCall(
		api.NvEncGetEncodeCaps,
		encoder,
		uintptr(unsafe.Pointer(&hevcGUID)),
		uintptr(unsafe.Pointer(&caps)),
		uintptr(unsafe.Pointer(&supported)),
	)
	runtime.KeepAlive(&hevcGUID)
	runtime.KeepAlive(&caps)
	runtime.KeepAlive(&supported)
	if status != 0 {
		return false, false, fmt.Errorf("nvEncGetEncodeCaps(YUV444) returned %d", status)
	}
	return true, supported != 0, nil
}

func probeNVIDIANVENCHEVC444(
	ctx context.Context,
	createInstance uintptr,
	maxSupportedVersion uint32,
) nvidiaNVENCDeviceProbe {
	result := nvidiaNVENCDeviceProbe{}
	if err := ctx.Err(); err != nil {
		result.Error = err.Error()
		return result
	}

	// The function-list ABI used here is pinned to nv-codec-headers 13.1.
	// Do not present a newer structure version to an older driver: retaining a
	// runtime-only candidate is safer than guessing cross-version ABI layout.
	if maxSupportedVersion < nvencMaxVersionCode {
		result.Error = fmt.Sprintf(
			"NVENC driver API %s is older than candidate probe ABI %d.%d",
			formatNVENCMaxSupportedVersion(maxSupportedVersion),
			nvencAPIMajorVersion, nvencAPIMinorVersion,
		)
		return result
	}

	nvencAPI, err := createNVENCFunctionList(createInstance)
	if err != nil {
		result.Error = err.Error()
		return result
	}

	cudaAPI, err := loadNVIDIACUDADriverAPI()
	if err != nil {
		result.Error = fmt.Sprintf("%s: %v", nvidiaCUDADriverDLL, err)
		return result
	}
	defer cudaAPI.Close()

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if status := cudaDriverCall(cudaAPI.cuInit, 0); status != 0 {
		result.Error = fmt.Sprintf("cuInit returned %d", status)
		return result
	}

	var deviceCount int32
	if status := cudaDriverCall(
		cudaAPI.cuDeviceGetCount,
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
			cudaAPI.cuDeviceGet,
			uintptr(unsafe.Pointer(&device)),
			uintptr(ordinal),
		); status != 0 {
			issues = append(issues, fmt.Sprintf("device %d cuDeviceGet returned %d", ordinal, status))
			continue
		}

		var cudaContext uintptr
		if status := cudaDriverCall(
			cudaAPI.cuCtxCreate,
			uintptr(unsafe.Pointer(&cudaContext)),
			0,
			uintptr(device),
		); status != 0 || cudaContext == 0 {
			issues = append(issues, fmt.Sprintf("device %d cuCtxCreate returned %d", ordinal, status))
			continue
		}

		params := nvencOpenEncodeSessionExParams{
			Version:    nvencStructVersion(1),
			DeviceType: nvencDeviceTypeCUDA,
			Device:     cudaContext,
			APIVersion: nvencAPIVersion,
		}
		var encoder uintptr
		openStatus := nvencCall(
			nvencAPI.NvEncOpenEncodeSessionEx,
			uintptr(unsafe.Pointer(&params)),
			uintptr(unsafe.Pointer(&encoder)),
		)
		runtime.KeepAlive(&params)
		runtime.KeepAlive(&encoder)

		var (
			checked   bool
			supported bool
			queryErr  error
		)
		if openStatus == 0 && encoder != 0 {
			checked, supported, queryErr = probeNVENCHEVC444OnSession(nvencAPI, encoder)
			if destroyStatus := nvencCall(nvencAPI.NvEncDestroyEncoder, encoder); destroyStatus != 0 {
				issues = append(issues, fmt.Sprintf("device %d nvEncDestroyEncoder returned %d", ordinal, destroyStatus))
			}
		} else {
			issues = append(issues, fmt.Sprintf("device %d nvEncOpenEncodeSessionEx returned %d", ordinal, openStatus))
		}

		clearStatus := cudaDriverCall(cudaAPI.cuCtxSetCurrent, 0)
		destroyContextStatus := cudaDriverCall(cudaAPI.cuCtxDestroy, cudaContext)
		if clearStatus != 0 {
			issues = append(issues, fmt.Sprintf("device %d cuCtxSetCurrent(NULL) returned %d", ordinal, clearStatus))
		}
		if destroyContextStatus != 0 {
			issues = append(issues, fmt.Sprintf("device %d cuCtxDestroy returned %d", ordinal, destroyContextStatus))
		}
		if queryErr != nil {
			issues = append(issues, fmt.Sprintf("device %d %v", ordinal, queryErr))
			continue
		}
		if checked {
			result.Checked = true
			if supported {
				result.HEVC444 = true
			}
		}
	}

	result.Error = strings.Join(issues, "; ")
	return result
}
