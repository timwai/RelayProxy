package gpu

import "testing"

func TestFrameValidate(t *testing.T) {
	frame := Frame{
		Backend:  BackendD3D11,
		Resource: 1,
		Width:    1920,
		Height:   1080,
		Format:   FormatAYUV,
	}
	if err := frame.Validate(); err != nil {
		t.Fatal(err)
	}
	frame.Format = Format("unknown")
	if err := frame.Validate(); err == nil {
		t.Fatal("unknown GPU format was accepted")
	}
}

func TestParseFormat(t *testing.T) {
	format, ok := ParseFormat(" AYUV ")
	if !ok || format != FormatAYUV {
		t.Fatalf("format=%q ok=%t", format, ok)
	}
	if _, ok := ParseFormat("i444"); ok {
		t.Fatal("CPU-only I444 was accepted as a GPU texture format")
	}
}
