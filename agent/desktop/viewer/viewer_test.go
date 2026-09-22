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
