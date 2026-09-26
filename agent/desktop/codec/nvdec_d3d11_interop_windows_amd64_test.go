//go:build windows && amd64

package codec

import (
	"strings"
	"testing"
)

func completeNVDECCUDAInteropAPIForTest() nvdecCUDAInteropAPI {
	return nvdecCUDAInteropAPI{
		GraphicsD3D11RegisterResource:     1,
		GraphicsUnregisterResource:        1,
		GraphicsMapResources:              1,
		GraphicsUnmapResources:            1,
		GraphicsSubResourceGetMappedArray: 1,
		GraphicsResourceSetMapFlags:       1,
	}
}

func TestValidateNVDECCUDAInteropAPI(t *testing.T) {
	api := completeNVDECCUDAInteropAPIForTest()
	if err := validateNVDECCUDAInteropAPI(api); err != nil {
		t.Fatal(err)
	}
	api.GraphicsSubResourceGetMappedArray = 0
	err := validateNVDECCUDAInteropAPI(api)
	if err == nil || !strings.Contains(err.Error(), "cuGraphicsSubResourceGetMappedArray") {
		t.Fatalf("missing mapped-array function error=%v", err)
	}
}

func TestNVDECCUDAInteropFlags(t *testing.T) {
	if nvdecCUGraphicsRegisterSurfaceLDST != 0x04 {
		t.Fatalf("SURFACE_LDST flag=%#x", nvdecCUGraphicsRegisterSurfaceLDST)
	}
	if nvdecCUGraphicsMapWriteDiscard != 0x02 {
		t.Fatalf("WRITE_DISCARD flag=%#x", nvdecCUGraphicsMapWriteDiscard)
	}
}

func TestNVDECD3D11AYUVInteropSurfaceCloseIsIdempotentWithoutHandles(t *testing.T) {
	surface := &nvdecD3D11AYUVInteropSurface{}
	if err := surface.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := surface.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if surface.Texture() != 0 {
		t.Fatal("closed interop surface exposed D3D11 texture")
	}
	if width, height := surface.Dimensions(); width != 0 || height != 0 {
		t.Fatalf("closed dimensions=%dx%d", width, height)
	}
}
