//go:build windows

package audio

import (
	"testing"

	winaudio "github.com/deploymenttheory/go-bindings-win32/bindings/win32/media/audio"
)

func TestLoopbackStreamFlagsEnablePCMConversion(t *testing.T) {
	flags := loopbackStreamFlags()
	for _, flag := range []uint32{
		uint32(winaudio.AUDCLNT_STREAMFLAGS_LOOPBACK),
		uint32(winaudio.AUDCLNT_STREAMFLAGS_AUTOCONVERTPCM),
		uint32(winaudio.AUDCLNT_STREAMFLAGS_SRC_DEFAULT_QUALITY),
	} {
		if flags&flag == 0 {
			t.Fatalf("loopback flags %#x missing %#x", flags, flag)
		}
	}
}
