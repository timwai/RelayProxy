package viewer

import "testing"

func TestFrameValidate(t *testing.T) {
	valid := Frame{BGRA: make([]byte, 4*8*4), Width: 8, Height: 4, Stride: 32}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid frame: %v", err)
	}
	invalid := valid
	invalid.Stride = 8
	if err := invalid.Validate(); err == nil {
		t.Fatal("short stride was accepted")
	}
}

func TestD3D11FrameValidate(t *testing.T) {
	valid := D3D11Frame{Resource: 1, Width: 1920, Height: 1080}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid D3D11 frame: %v", err)
	}
	if err := (D3D11Frame{Width: 1920, Height: 1080}).Validate(); err == nil {
		t.Fatal("zero D3D11 resource was accepted")
	}
}
