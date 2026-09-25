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
	nvEncodeRuntimeDLL = "nvEncodeAPI64.dll"
	nvDecodeRuntimeDLL = "nvcuvid.dll"
	amfRuntimeDLL      = "amfrt64.dll"

	// The public AMF HEVC encoder API exposes Main and Main10 profiles only.
	// 4:4:4 input surfaces (for example AYUV/Y410) do not imply a 4:4:4 HEVC
	// bitstream profile, so RelayProxy must not advertise HEVC 4:4:4 encode.
	amfHEVC444EncodeLimitation = "amf_hevc_public_profiles_main_main10_only"
)

func runtimeCandidateStatus(value uintptr) int32 {
	return int32(uint32(value))
}

func formatNVENCMaxSupportedVersion(version uint32) string {
	if version == 0 {
		return ""
	}
	return fmt.Sprintf("%d.%d", version>>4, version&0x0f)
}

func formatAMFRuntimeVersion(version uint64) string {
	if version == 0 {
		return ""
	}
	return fmt.Sprintf("0x%016x", version)
}

func appendCandidateIssue(issues []string, format string, args ...any) []string {
	return append(issues, fmt.Sprintf(format, args...))
}

func probeNVIDIAH265444RuntimeCandidate(ctx context.Context) H265444RuntimeCandidate {
	probe := H265444RuntimeCandidate{
		Backend:     "nvcodec-hevc444",
		Vendor:      "nvidia",
		Implemented: false,
	}
	if err := ctx.Err(); err != nil {
		probe.Error = err.Error()
		return probe
	}

	var issues []string
	encodeModule, err := windows.LoadLibrary(nvEncodeRuntimeDLL)
	if err != nil {
		issues = appendCandidateIssue(issues, "%s: %v", nvEncodeRuntimeDLL, err)
	} else {
		defer windows.FreeLibrary(encodeModule)
		getVersion, versionErr := windows.GetProcAddress(encodeModule, "NvEncodeAPIGetMaxSupportedVersion")
		createInstance, createErr := windows.GetProcAddress(encodeModule, "NvEncodeAPICreateInstance")
		switch {
		case versionErr != nil:
			issues = appendCandidateIssue(issues, "%s/NvEncodeAPIGetMaxSupportedVersion: %v", nvEncodeRuntimeDLL, versionErr)
		case createErr != nil:
			issues = appendCandidateIssue(issues, "%s/NvEncodeAPICreateInstance: %v", nvEncodeRuntimeDLL, createErr)
		default:
			var version uint32
			status, _, _ := syscall.SyscallN(
				getVersion,
				uintptr(unsafe.Pointer(&version)),
			)
			runtime.KeepAlive(&version)
			if got := runtimeCandidateStatus(status); got != 0 {
				issues = appendCandidateIssue(issues, "NvEncodeAPIGetMaxSupportedVersion returned %d", got)
			} else {
				probe.EncodeRuntime = true
				probe.Version = formatNVENCMaxSupportedVersion(version)
				encodeProbe := probeNVIDIANVENCHEVC444(ctx, createInstance, version)
				probe.DeviceProbe = encodeProbe.Checked
				probe.DeviceCount = encodeProbe.DeviceCount
				probe.EncodeCapabilityKnown = encodeProbe.Checked
				probe.HEVC444Encode = encodeProbe.HEVC444
				if encodeProbe.Error != "" {
					issues = appendCandidateIssue(issues, "NVENC device probe: %s", encodeProbe.Error)
				}
			}
		}
	}

	if err := ctx.Err(); err != nil {
		issues = appendCandidateIssue(issues, "%v", err)
		probe.RuntimeAvailable = probe.EncodeRuntime || probe.DecodeRuntime
		probe.Error = strings.Join(issues, "; ")
		return probe
	}

	decodeModule, err := windows.LoadLibrary(nvDecodeRuntimeDLL)
	if err != nil {
		issues = appendCandidateIssue(issues, "%s: %v", nvDecodeRuntimeDLL, err)
	} else {
		defer windows.FreeLibrary(decodeModule)
		getDecoderCaps, procErr := windows.GetProcAddress(decodeModule, "cuvidGetDecoderCaps")
		if procErr != nil {
			issues = appendCandidateIssue(issues, "%s/cuvidGetDecoderCaps: %v", nvDecodeRuntimeDLL, procErr)
		} else {
			probe.DecodeRuntime = true
			deviceProbe := probeNVIDIANVDECHEVC444(ctx, getDecoderCaps)
			probe.DeviceProbe = probe.DeviceProbe || deviceProbe.Checked
			probe.DecodeCapabilityKnown = deviceProbe.Checked
			if deviceProbe.DeviceCount > probe.DeviceCount {
				probe.DeviceCount = deviceProbe.DeviceCount
			}
			probe.HEVC444Decode = deviceProbe.HEVC444
			if deviceProbe.Error != "" {
				issues = appendCandidateIssue(issues, "NVDEC device probe: %s", deviceProbe.Error)
			}
		}
	}

	probe.RuntimeAvailable = probe.EncodeRuntime || probe.DecodeRuntime
	probe.Error = strings.Join(issues, "; ")
	return probe
}

func probeAMFH265444RuntimeCandidate(ctx context.Context) H265444RuntimeCandidate {
	probe := H265444RuntimeCandidate{
		Backend:                "amf-hevc444",
		Vendor:                 "amd",
		EncodeCapabilityKnown:  true,
		HEVC444Encode:          false,
		Implemented:            false,
		Limitation:             amfHEVC444EncodeLimitation,
	}
	if err := ctx.Err(); err != nil {
		probe.Error = err.Error()
		return probe
	}

	module, err := windows.LoadLibrary(amfRuntimeDLL)
	if err != nil {
		probe.Error = fmt.Sprintf("%s: %v", amfRuntimeDLL, err)
		return probe
	}
	defer windows.FreeLibrary(module)

	queryVersion, err := windows.GetProcAddress(module, "AMFQueryVersion")
	if err != nil {
		probe.Error = fmt.Sprintf("%s/AMFQueryVersion: %v", amfRuntimeDLL, err)
		return probe
	}
	if _, err := windows.GetProcAddress(module, "AMFInit"); err != nil {
		probe.Error = fmt.Sprintf("%s/AMFInit: %v", amfRuntimeDLL, err)
		return probe
	}

	var version uint64
	status, _, _ := syscall.SyscallN(
		queryVersion,
		uintptr(unsafe.Pointer(&version)),
	)
	runtime.KeepAlive(&version)
	if got := runtimeCandidateStatus(status); got != 0 {
		probe.Error = fmt.Sprintf("AMFQueryVersion returned %d", got)
		return probe
	}

	probe.RuntimeAvailable = true
	// AMF exposes encoder and decoder components through the same AMFInit /
	// AMFFactory runtime. These flags mean the shared runtime entry point is
	// available for a future integration; they do not claim that this GPU
	// supports HEVC 4:4:4 encode or decode.
	probe.EncodeRuntime = true
	probe.DecodeRuntime = true
	probe.Version = formatAMFRuntimeVersion(version)
	return probe
}

func probePlatformH265444RuntimeCandidates(ctx context.Context) []H265444RuntimeCandidate {
	return []H265444RuntimeCandidate{
		probeNVIDIAH265444RuntimeCandidate(ctx),
		probeAMFH265444RuntimeCandidate(ctx),
	}
}
