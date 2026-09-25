//go:build windows

package codec

import "testing"

func TestD3D11OutputDXGI(t *testing.T) {
	tests := []struct {
		format PixelFormat
		want   uint32
	}{
		{format: PixelFormatNV12, want: dxgiFormatNV12},
		{format: PixelFormatAYUV, want: dxgiFormatAYUV},
	}
	for _, tt := range tests {
		got, ok := d3d11OutputDXGI(tt.format)
		if !ok || got != tt.want {
			t.Fatalf("format=%s dxgi=%d ok=%t want=%d", tt.format, got, ok, tt.want)
		}
	}
	if _, ok := d3d11OutputDXGI(PixelFormatBGRA); ok {
		t.Fatal("BGRA was accepted as a converter output")
	}
}
