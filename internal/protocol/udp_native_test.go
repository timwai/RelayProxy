package protocol

import (
	"bytes"
	"testing"
)

func TestNativeUDPFramingBounds(t *testing.T) {
	for _, size := range []int{0, UDPFragmentPayload, UDPFragmentPayload + 1, MaxUDPDatagramPayload} {
		original := bytes.Repeat([]byte{7}, size)
		var decoded []byte
		err := FragmentUDP(42, original, func(frame []byte) error {
			f, err := DecodeUDPFragment(frame)
			if err != nil {
				return err
			}
			decoded = append(decoded, f.Payload...)
			return nil
		})
		if err != nil || !bytes.Equal(decoded, original) {
			t.Fatalf("size=%d err=%v", size, err)
		}
	}
	if err := FragmentUDP(1, make([]byte, 65536), func([]byte) error { return nil }); err == nil {
		t.Fatal("oversized packet accepted")
	}
	if _, err := DecodeUDPFragment([]byte{0, 0, 0, 1, 0, 1, 1, 1, 7}); err == nil {
		t.Fatal("invalid fragment index accepted")
	}
}
