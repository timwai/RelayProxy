//go:build windows

package audio

import (
	"encoding/binary"
	"testing"

	winaudio "github.com/deploymenttheory/go-bindings-win32/bindings/win32/media/audio"
)

func TestPCMWaveFormatS16Stereo48K(t *testing.T) {
	format := pcmWaveFormat(PCMConfig{
		SampleRate: 48_000, Channels: 2, BitsPerSample: 16,
	})
	if got := binary.LittleEndian.Uint16(format.Data[0:2]); got != uint16(winaudio.WAVE_FORMAT_PCM) {
		t.Fatalf("format tag=%d", got)
	}
	if got := binary.LittleEndian.Uint16(format.Data[2:4]); got != 2 {
		t.Fatalf("channels=%d", got)
	}
	if got := binary.LittleEndian.Uint32(format.Data[4:8]); got != 48_000 {
		t.Fatalf("sample rate=%d", got)
	}
	if got := binary.LittleEndian.Uint32(format.Data[8:12]); got != 192_000 {
		t.Fatalf("avg bytes/sec=%d", got)
	}
	if got := binary.LittleEndian.Uint16(format.Data[12:14]); got != 4 {
		t.Fatalf("block align=%d", got)
	}
	if got := binary.LittleEndian.Uint16(format.Data[14:16]); got != 16 {
		t.Fatalf("bits/sample=%d", got)
	}
	if got := binary.LittleEndian.Uint16(format.Data[16:18]); got != 0 {
		t.Fatalf("cbSize=%d", got)
	}
}
