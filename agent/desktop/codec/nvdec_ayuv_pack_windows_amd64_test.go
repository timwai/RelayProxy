//go:build windows && amd64

package codec

import (
	"strings"
	"testing"
)

func completeNVDECCUDAKernelAPIForTest() nvdecCUDAKernelAPI {
	return nvdecCUDAKernelAPI{
		ModuleLoadData:    1,
		ModuleGetFunction: 1,
		ModuleGetSurfRef:  1,
		SurfRefSetArray:   1,
		LaunchKernel:      1,
		CtxSynchronize:    1,
		ModuleUnload:      1,
	}
}

func TestValidateNVDECCUDAKernelAPI(t *testing.T) {
	api := completeNVDECCUDAKernelAPIForTest()
	if err := validateNVDECCUDAKernelAPI(api); err != nil {
		t.Fatal(err)
	}
	api.SurfRefSetArray = 0
	err := validateNVDECCUDAKernelAPI(api)
	if err == nil || !strings.Contains(err.Error(), "cuSurfRefSetArray") {
		t.Fatalf("missing surface bind function error=%v", err)
	}
}

func TestNVDECAYUVPackPTXContract(t *testing.T) {
	for _, required := range []string{
		".global .surfref " + nvdecAYUVPackSurfaceName,
		".entry " + nvdecAYUVPackKernelName,
		"ld.global.u8",
		"sust.b.2d.v4.b8",
	} {
		if !strings.Contains(nvdecAYUVPackPTX, required) {
			t.Fatalf("PTX is missing %q", required)
		}
	}
}

func TestNVDECPackGrid(t *testing.T) {
	gridX, gridY, err := nvdecPackGrid(1920, 1080)
	if err != nil {
		t.Fatal(err)
	}
	if gridX != 120 || gridY != 68 {
		t.Fatalf("grid=%dx%d want=120x68", gridX, gridY)
	}
	gridX, gridY, err = nvdecPackGrid(17, 17)
	if err != nil {
		t.Fatal(err)
	}
	if gridX != 2 || gridY != 2 {
		t.Fatalf("grid=%dx%d want=2x2", gridX, gridY)
	}
}

func TestNVDECAYUVPackerCloseIsIdempotentWithoutModule(t *testing.T) {
	packer := &nvdecAYUVPacker{}
	if err := packer.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := packer.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
