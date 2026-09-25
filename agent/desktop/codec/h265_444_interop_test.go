package codec

import "testing"

func TestH265444D3D11CUDAAYUVContract(t *testing.T) {
	contract := h265444D3D11CUDAAYUVContract()
	if contract == nil {
		t.Fatal("CUDA interop contract is nil")
	}
	if contract.Bridge != H265444InteropD3D11CUDA {
		t.Fatalf("bridge=%q", contract.Bridge)
	}
	if err := contract.Validate(); err != nil {
		t.Fatalf("valid CUDA interop contract rejected: %v", err)
	}
}

func TestH265444D3D11InteropContractRejectsOwnershipAmbiguity(t *testing.T) {
	contract := h265444D3D11CUDAAYUVContract()
	contract.EncoderBorrowsInputResource = false
	if err := contract.Validate(); err == nil {
		t.Fatal("interop contract accepted ambiguous encoder input ownership")
	}

	contract = h265444D3D11CUDAAYUVContract()
	contract.DecoderOwnsOutputUntilClose = false
	if err := contract.Validate(); err == nil {
		t.Fatal("interop contract accepted decoder output without frame-close ownership")
	}
}

func TestH265444BackendLifecycleContract(t *testing.T) {
	contract := h265444SessionLifecycleContract()
	if contract == nil {
		t.Fatal("lifecycle contract is nil")
	}
	if err := contract.Validate(); err != nil {
		t.Fatalf("valid lifecycle contract rejected: %v", err)
	}

	contract.CloseReleasesOwnedResources = false
	if err := contract.Validate(); err == nil {
		t.Fatal("lifecycle contract accepted a Close that leaks owned resources")
	}
}

func TestNVCodecBackendStaysProductionDisabled(t *testing.T) {
	backend := platformNVCodecH265444Backend()
	if backend.name != H265444BackendNVCodec {
		t.Fatalf("backend name=%q", backend.name)
	}
	if backend.productionReady || backend.zeroCopyValidated {
		t.Fatalf("NVCodec scaffold unexpectedly enabled: %+v", backend)
	}
	if backend.productionGateError() == nil {
		t.Fatal("NVCodec scaffold did not remain behind the production gate")
	}
}
