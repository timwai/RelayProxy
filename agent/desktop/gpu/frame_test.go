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

func TestFrameLifetimeHooks(t *testing.T) {
	retains := 0
	releases := 0
	frame := WithLifetime(Frame{
		Backend:  BackendD3D11,
		Resource: 1,
		Width:    16,
		Height:   16,
		Format:   FormatAYUV,
	}, func() error {
		retains++
		return nil
	}, func() {
		releases++
	})
	if err := frame.Retain(); err != nil {
		t.Fatal(err)
	}
	frame.Release()
	if retains != 1 || releases != 1 {
		t.Fatalf("lifetime retains=%d releases=%d", retains, releases)
	}
}
