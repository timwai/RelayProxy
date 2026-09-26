//go:build windows && amd64

package codec

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	nvcodecMemoryPhaseBefore = "before"
	nvcodecMemoryPhaseSample = "sample"
	nvcodecMemoryPhaseAfter  = "after"

	nvcodecDXGIMemorySegmentGroupLocal uintptr = 0
)

var nvcodecValidationIIDIDXGIAdapter3 = windows.GUID{
	Data1: 0x645967a4, Data2: 0x1392, Data3: 0x4310,
	Data4: [8]byte{0xa7, 0x98, 0x80, 0x53, 0xce, 0x3e, 0x93, 0xfd},
}

type nvcodecDXGIQueryVideoMemoryInfo struct {
	Budget                  uint64
	CurrentUsage            uint64
	AvailableForReservation uint64
	CurrentReservation      uint64
}

func queryNVCodecValidationVideoMemory(device unsafe.Pointer) (nvcodecDXGIQueryVideoMemoryInfo, error) {
	if device == nil {
		return nvcodecDXGIQueryVideoMemoryInfo{}, fmt.Errorf("NVIDIA memory observation: D3D11 device is nil")
	}
	dxgiDevice, err := comQueryInterface(device, &nvcodecValidationIIDIDXGIDevice)
	if err != nil {
		return nvcodecDXGIQueryVideoMemoryInfo{}, fmt.Errorf("NVIDIA memory observation: IDXGIDevice: %w", err)
	}
	defer releaseIUnknown(dxgiDevice)

	var adapter unsafe.Pointer
	hr := comCall(
		dxgiDevice,
		7, // IDXGIDevice::GetAdapter
		uintptr(unsafe.Pointer(&adapter)),
	)
	if hresultFailed(hr) {
		return nvcodecDXGIQueryVideoMemoryInfo{}, hresultError("IDXGIDevice.GetAdapter", hr)
	}
	if adapter == nil {
		return nvcodecDXGIQueryVideoMemoryInfo{}, fmt.Errorf("NVIDIA memory observation: IDXGIDevice.GetAdapter returned nil")
	}
	defer releaseIUnknown(adapter)

	adapter3, err := comQueryInterface(adapter, &nvcodecValidationIIDIDXGIAdapter3)
	if err != nil {
		return nvcodecDXGIQueryVideoMemoryInfo{}, fmt.Errorf("NVIDIA memory observation: IDXGIAdapter3 unavailable: %w", err)
	}
	defer releaseIUnknown(adapter3)

	var info nvcodecDXGIQueryVideoMemoryInfo
	hr = comCall(
		adapter3,
		14, // IDXGIAdapter3::QueryVideoMemoryInfo
		0,  // node index
		nvcodecDXGIMemorySegmentGroupLocal,
		uintptr(unsafe.Pointer(&info)),
	)
	if hresultFailed(hr) {
		return nvcodecDXGIQueryVideoMemoryInfo{}, hresultError("IDXGIAdapter3.QueryVideoMemoryInfo", hr)
	}
	return info, nil
}

func observeNVCodecValidationMemory(
	report *NVCodecH265444RoundTripReport,
	device unsafe.Pointer,
	phase string,
) {
	if report == nil {
		return
	}
	info, err := queryNVCodecValidationVideoMemory(device)
	if err != nil {
		if report.GPUMemoryObservationError == "" {
			report.GPUMemoryObservationError = err.Error()
		}
		return
	}

	report.GPUMemoryObserved = true
	if info.Budget > 0 && (report.GPUMemoryBudgetBytes == 0 || info.Budget < report.GPUMemoryBudgetBytes) {
		report.GPUMemoryBudgetBytes = info.Budget
	}
	if info.CurrentUsage > report.GPUMemoryPeakBytes {
		report.GPUMemoryPeakBytes = info.CurrentUsage
	}

	switch phase {
	case nvcodecMemoryPhaseBefore:
		report.GPUMemoryBeforeBytes = info.CurrentUsage
	case nvcodecMemoryPhaseAfter:
		report.GPUMemoryAfterBytes = info.CurrentUsage
		if info.CurrentUsage >= report.GPUMemoryBeforeBytes {
			report.GPUMemoryGrowthBytes = int64(info.CurrentUsage - report.GPUMemoryBeforeBytes)
		} else {
			report.GPUMemoryGrowthBytes = -int64(report.GPUMemoryBeforeBytes - info.CurrentUsage)
		}
	}
}
