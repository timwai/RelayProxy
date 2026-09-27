//go:build windows && amd64

package codec

import (
	"context"
	"errors"
	"testing"
)

func completeNVENCProductionFunctionListForTest() nvEncodeAPIFunctionList {
	var api nvEncodeAPIFunctionList
	api.NvEncOpenEncodeSessionEx = 1
	api.NvEncGetEncodeGUIDCount = 1
	api.NvEncGetEncodeGUIDs = 1
	api.NvEncGetEncodeCaps = 1
	api.NvEncGetEncodePresetConfig = 1
	api.NvEncGetEncodePresetConfigEx = 1
	api.NvEncInitializeEncoder = 1
	api.NvEncCreateBitstreamBuffer = 1
	api.NvEncDestroyBitstreamBuffer = 1
	api.NvEncRegisterResource = 1
	api.NvEncUnregisterResource = 1
	api.NvEncMapInputResource = 1
	api.NvEncUnmapInputResource = 1
	api.NvEncEncodePicture = 1
	api.NvEncLockBitstream = 1
	api.NvEncUnlockBitstream = 1
	api.NvEncGetSequenceParams = 1
	api.NvEncReconfigureEncoder = 1
	api.NvEncDestroyEncoder = 1
	return api
}

func TestValidateNVENCProductionFunctionList(t *testing.T) {
	api := completeNVENCProductionFunctionListForTest()
	if err := validateNVENCProductionFunctionList(api, false); err != nil {
		t.Fatalf("complete production table rejected: %v", err)
	}

	api.NvEncRegisterResource = 0
	if err := validateNVENCProductionFunctionList(api, false); err == nil {
		t.Fatal("production table without resource registration was accepted")
	}
}

func TestValidateNVENCProductionFunctionListLegacy91(t *testing.T) {
	api := completeNVENCProductionFunctionListForTest()
	api.NvEncGetEncodePresetConfigEx = 0
	if err := validateNVENCProductionFunctionList(api, true); err != nil {
		t.Fatalf("legacy production table rejected: %v", err)
	}
	api.NvEncGetEncodePresetConfig = 0
	if err := validateNVENCProductionFunctionList(api, true); err == nil {
		t.Fatal("legacy production table without nvEncGetEncodePresetConfig was accepted")
	}
}

func TestNVENCAPIVersionForDriver(t *testing.T) {
	if api, legacy := nvencAPIVersionForDriver(nvencMaxVersionCode); api != nvencAPIVersion || legacy {
		t.Fatalf("modern driver selected api=%#x legacy=%v", api, legacy)
	}
	if api, legacy := nvencAPIVersionForDriver(nvencLegacyMaxVersionCode); api != nvencLegacyAPIVersion || !legacy {
		t.Fatalf("9.1 driver selected api=%#x legacy=%v", api, legacy)
	}
	if api, legacy := nvencAPIVersionForDriver(0x90); api != 0 || legacy {
		t.Fatalf("too-old driver selected api=%#x legacy=%v", api, legacy)
	}
}

func TestOpenNVENCHEVC444D3D11SessionRejectsNilDeviceBeforeRuntimeLoad(t *testing.T) {
	_, err := openNVENCHEVC444D3D11Session(context.Background(), 0)
	if !errors.Is(err, ErrEncoderUnavailable) {
		t.Fatalf("nil D3D11 device error=%v", err)
	}
}

func TestNVENCD3D11SessionCloseIsIdempotentWithoutHandles(t *testing.T) {
	session := &nvencD3D11Session{}
	if err := session.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if session.Encoder() != 0 || session.Device() != 0 {
		t.Fatalf("closed session exposed handles encoder=%#x device=%#x", session.Encoder(), session.Device())
	}
}

func TestNVENCDeviceTypeDirectXABI(t *testing.T) {
	if nvencDeviceTypeDirectX != 0 || nvencDeviceTypeCUDA != 1 {
		t.Fatalf("NVENC device types directx=%d cuda=%d", nvencDeviceTypeDirectX, nvencDeviceTypeCUDA)
	}
}
