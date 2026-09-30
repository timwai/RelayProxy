package resume

import (
	"testing"

	"relayproxy/internal/protocol"
)

func TestProtocolBindingRoundTrip(t *testing.T) {
	identity, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	local := Binding{
		Type:          BindAck,
		Identity:      identity,
		Generation:    9,
		SendOffset:    1234,
		ReceiveOffset: 987,
	}
	wire, err := BindingToProtocol(local, protocol.TCPResumeModeRebind)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ResponseBindingFromProtocol(wire)
	if err != nil {
		t.Fatal(err)
	}
	if got != local {
		t.Fatalf("binding round trip got=%+v want=%+v", got, local)
	}

	request, err := RequestBindingFromProtocol(wire)
	if err != nil {
		t.Fatal(err)
	}
	if request.Type != BindOpen || request.Identity != identity ||
		request.Generation != local.Generation ||
		request.SendOffset != local.SendOffset ||
		request.ReceiveOffset != local.ReceiveOffset {
		t.Fatalf("request binding mismatch: %+v", request)
	}
}

func TestProtocolBindingRejectsZeroIdentity(t *testing.T) {
	wire := &protocol.TCPResumeBinding{
		Mode:       protocol.TCPResumeModeOpen,
		StreamID:   make([]byte, StreamIDSize),
		Token:      make([]byte, TokenSize),
		Generation: 1,
	}
	if _, err := RequestBindingFromProtocol(wire); err != ErrBinding {
		t.Fatalf("zero identity error=%v", err)
	}
}

func TestBindingToProtocolCopiesSecrets(t *testing.T) {
	identity, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	binding := Binding{Type: BindAck, Identity: identity, Generation: 1}
	wire, err := BindingToProtocol(binding, protocol.TCPResumeModeOpen)
	if err != nil {
		t.Fatal(err)
	}
	wire.StreamID[0] ^= 0xff
	wire.Token[0] ^= 0xff
	if binding.Identity != identity {
		t.Fatal("wire mutation changed in-memory identity")
	}
}
