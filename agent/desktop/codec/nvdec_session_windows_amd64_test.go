//go:build windows && amd64

package codec

import (
	"strings"
	"testing"
)

func completeNVDECFunctionListForTest() nvdecFunctionList {
	return nvdecFunctionList{
		CuvidGetDecoderCaps:     1,
		CuvidCreateDecoder:      1,
		CuvidDestroyDecoder:     1,
		CuvidDecodePicture:      1,
		CuvidMapVideoFrame64:    1,
		CuvidUnmapVideoFrame64:  1,
		CuvidCreateVideoParser:  1,
		CuvidParseVideoData:     1,
		CuvidDestroyVideoParser: 1,
		CuvidCtxLockCreate:      1,
		CuvidCtxLockDestroy:     1,
		CuvidCtxLock:            1,
		CuvidCtxUnlock:          1,
	}
}

func TestValidateNVDECFunctionList(t *testing.T) {
	api := completeNVDECFunctionListForTest()
	if err := validateNVDECFunctionList(api); err != nil {
		t.Fatal(err)
	}

	api.CuvidMapVideoFrame64 = 0
	err := validateNVDECFunctionList(api)
	if err == nil || !strings.Contains(err.Error(), "cuvidMapVideoFrame64") {
		t.Fatalf("missing map function error=%v", err)
	}
}

func TestNVDECD3D11SessionCloseIsIdempotent(t *testing.T) {
	session := &nvdecD3D11Session{}
	if err := session.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if session.CUDAContext() != 0 {
		t.Fatal("closed session exposed CUDA context")
	}
	if _, err := session.API(); err != ErrDecoderUnavailable {
		t.Fatalf("closed session API error=%v", err)
	}
}

func TestNVDECCUDADeviceListAllConstant(t *testing.T) {
	if nvdecCUDADeviceListAll != 0x01 {
		t.Fatalf("CU_D3D11_DEVICE_LIST_ALL=%#x", nvdecCUDADeviceListAll)
	}
}
