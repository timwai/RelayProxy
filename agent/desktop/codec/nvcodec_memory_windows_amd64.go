//go:build windows && amd64

package codec

import (
	"context"
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const nvcodecDXGIMemorySegmentLocal uintptr = 0

var nvcodecValidationIIDIDXGIAdapter3 = windows.GUID{
	Data1: 0x645967a4, Data2: 0x1392, Data3: 0x4310,
	Data4: [8]byte{0xa7, 0x98, 0x80, 0x53, 0xce, 0x3e, 0x93, 0xfd},
}

type nvcodecDXGIVideoMemoryInfo struct {
	Budget                  uint64
	CurrentUsage            uint64
	AvailableForReservation uint64
	CurrentReservation      uint64
}

func ProbeNVCodecVideoMemory(ctx context.Context) (NVCodecVideoMemoryInfo, error) {
	if err := ctx.Err(); err != nil {
		return NVCodecVideoMemoryInfo{}, err
	}

	device, deviceContext, identity, err := createNVCodecValidationD3D11Device()
	if err != nil {
		return NVCodecVideoMemoryInfo{}, err
	}
	defer releaseIUnknown(deviceContext)
	defer releaseIUnknown(device)

	dxgiDevice, err := comQueryInterface(device, &nvcodecValidationIIDIDXGIDevice)
	if err != nil {
		return NVCodecVideoMemoryInfo{}, fmt.Errorf(
			"%w: query IDXGIDevice for video-memory telemetry: %v",
			ErrDecoderUnavailable,
			err,
		)
	}
	defer releaseIUnknown(dxgiDevice)

	var adapter unsafe.Pointer
	hr := comCall(
		dxgiDevice,
		7, // IDXGIDevice::GetAdapter
		uintptr(unsafe.Pointer(&adapter)),
	)
	if hresultFailed(hr) {
		return NVCodecVideoMemoryInfo{}, fmt.Errorf(
			"%w: %v",
			ErrDecoderUnavailable,
			hresultError("IDXGIDevice.GetAdapter", hr),
		)
	}
	if adapter == nil {
		return NVCodecVideoMemoryInfo{}, fmt.Errorf("%w: IDXGIDevice.GetAdapter returned nil", ErrDecoderUnavailable)
	}
	defer releaseIUnknown(adapter)

	adapter3, err := comQueryInterface(adapter, &nvcodecValidationIIDIDXGIAdapter3)
	if err != nil {
		return NVCodecVideoMemoryInfo{}, fmt.Errorf(
			"%w: query IDXGIAdapter3 for video-memory telemetry: %v",
			ErrDecoderUnavailable,
			err,
		)
	}
	defer releaseIUnknown(adapter3)

	var info nvcodecDXGIVideoMemoryInfo
	hr = comCall(
		adapter3,
		14, // IDXGIAdapter3::QueryVideoMemoryInfo
		0,  // single-GPU node
		nvcodecDXGIMemorySegmentLocal,
		uintptr(unsafe.Pointer(&info)),
	)
	if hresultFailed(hr) {
		return NVCodecVideoMemoryInfo{}, fmt.Errorf(
			"%w: %v",
			ErrDecoderUnavailable,
			hresultError("IDXGIAdapter3.QueryVideoMemoryInfo(local)", hr),
		)
	}
	if err := ctx.Err(); err != nil {
		return NVCodecVideoMemoryInfo{}, err
	}
	return NVCodecVideoMemoryInfo{
		Identity:                     identity,
		BudgetBytes:                  info.Budget,
		CurrentUsageBytes:            info.CurrentUsage,
		AvailableForReservationBytes: info.AvailableForReservation,
		CurrentReservationBytes:      info.CurrentReservation,
		SampledAtUnixMs:              time.Now().UnixMilli(),
	}, nil
}
