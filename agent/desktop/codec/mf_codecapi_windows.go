//go:build windows

package codec

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	iCodecAPIIsSupported  = 3
	iCodecAPIIsModifiable = 4
	iCodecAPISetValue     = 9

	variantTypeUI4 = 19
	sFalse         = 1
)

var (
	iidICodecAPI = windows.GUID{
		Data1: 0x901db4c7, Data2: 0x31ce, Data3: 0x41a2,
		Data4: [8]byte{0x85, 0xdc, 0x8f, 0xa0, 0xbf, 0x41, 0xb8, 0xda},
	}
	codecAPIForceKeyFrame = windows.GUID{
		Data1: 0x398c1b98, Data2: 0x8353, Data3: 0x475a,
		Data4: [8]byte{0x9e, 0xf2, 0x8f, 0x26, 0x5d, 0x26, 0x03, 0x45},
	}
	codecAPIMeanBitRate = windows.GUID{
		Data1: 0xf7222374, Data2: 0x2144, Data3: 0x4815,
		Data4: [8]byte{0xb5, 0x50, 0xa3, 0x7f, 0x8e, 0x12, 0xee, 0x52},
	}
)

type oleVariant struct {
	Type      uint16
	Reserved1 uint16
	Reserved2 uint16
	Reserved3 uint16
	Value     uint64
}

func variantUI4(value uint32) oleVariant {
	return oleVariant{Type: variantTypeUI4, Value: uint64(value)}
}

func codecAPISetUI4(transform unsafe.Pointer, property *windows.GUID, value uint32) error {
	api, err := comQueryInterface(transform, &iidICodecAPI)
	if err != nil {
		return fmt.Errorf("%w: ICodecAPI unavailable: %v", ErrEncoderControlUnsupported, err)
	}
	defer releaseIUnknown(api)

	hr := comCall(api, iCodecAPIIsSupported, uintptr(unsafe.Pointer(property)))
	if uint32(hr) == sFalse {
		return ErrEncoderControlUnsupported
	}
	if hresultFailed(hr) {
		return fmt.Errorf("ICodecAPI.IsSupported: %w", hresultError("ICodecAPI.IsSupported", hr))
	}

	hr = comCall(api, iCodecAPIIsModifiable, uintptr(unsafe.Pointer(property)))
	if uint32(hr) == sFalse {
		return ErrEncoderControlUnsupported
	}
	if hresultFailed(hr) {
		return fmt.Errorf("ICodecAPI.IsModifiable: %w", hresultError("ICodecAPI.IsModifiable", hr))
	}

	variant := variantUI4(value)
	hr = comCall(
		api,
		iCodecAPISetValue,
		uintptr(unsafe.Pointer(property)),
		uintptr(unsafe.Pointer(&variant)),
	)
	if uint32(hr) == sFalse {
		return ErrEncoderControlUnsupported
	}
	if hresultFailed(hr) {
		return fmt.Errorf("ICodecAPI.SetValue: %w", hresultError("ICodecAPI.SetValue", hr))
	}
	return nil
}

func forceVideoKeyFrame(transform unsafe.Pointer) error {
	return codecAPISetUI4(transform, &codecAPIForceKeyFrame, 1)
}

func setVideoMeanBitrate(transform unsafe.Pointer, bitrate int) error {
	if bitrate <= 0 {
		return errors.New("video bitrate must be positive")
	}
	return codecAPISetUI4(transform, &codecAPIMeanBitRate, uint32(bitrate))
}

func forceH264IDR(transform unsafe.Pointer) error {
	return forceVideoKeyFrame(transform)
}

func setH264MeanBitrate(transform unsafe.Pointer, bitrate int) error {
	return setVideoMeanBitrate(transform, bitrate)
}
