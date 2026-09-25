//go:build windows && amd64

package codec

import (
	"context"
	"errors"
	"testing"
)

func completeNVENCProductionFunctionListForTest() nvEncodeAPIFunctionList {
	return nvEncodeAPIFunctionList{
		NvEncOpenEncodeSessionEx:    1,
		NvEncGetEncodeGUIDCount:     1,
		NvEncGetEncodeGUIDs:         1,
		NvEncGetEncodeCaps:          1,
		NvEncInitializeEncoder:      1,
		NvEncCreateBitstreamBuffer:  1,
		NvEncDestroyBitstreamBuffer: 1,
		NvEncRegisterResource:       1,
		NvEncUnregisterResource:     1,
		NvEncMapInputResource:       1,
		NvEncUnmapInputResource:     1,
		NvEncEncodePicture:          1,
		NvEncLockBitstream:          1,
		NvEncUnlockBitstream:        1,
		NvEncGetSequenceParams:      1,
		NvEncReconfigureEncoder:     1,
		NvEncDestroyEncoder:         1,
	}
}

func TestValidateNVENCProductionFunctionList(t *testing.T) {
	api := completeNVENCProductionFunctionListForTest()
	if err := validateNVENCProductionFunctionList(api); err != nil {
		t.Fatalf("complete production table rejected: %v", err)
	}

	api.NvEncRegisterResource = 0
	if err := validateNVENCProductionFunctionList(api); err == nil {
		t.Fatal("production table without resource registration was accepted")
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
