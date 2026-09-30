package resume

import (
	"bytes"
	"errors"
	"testing"
)

func TestBindingRoundTrip(t *testing.T) {
	identity, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	want := Binding{
		Type: BindOpen, Identity: identity, Generation: 7,
		SendOffset: 1 << 40, ReceiveOffset: (1 << 40) - 10,
	}
	var wire bytes.Buffer
	if err := WriteBinding(&wire, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadBinding(&wire)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("binding mismatch: got=%+v want=%+v", got, want)
	}
	if !got.Identity.VerifyToken(identity.Token[:]) {
		t.Fatal("round-tripped token verification failed")
	}
}

func TestValidateRebindRejectsStaleOrImpossibleState(t *testing.T) {
	identity, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	current := Binding{
		Type: BindAck, Identity: identity, Generation: 3,
		SendOffset: 100, ReceiveOffset: 80,
	}
	valid := Binding{
		Type: BindOpen, Identity: identity, Generation: 4,
		SendOffset: 80, ReceiveOffset: 100,
	}
	if err := ValidateRebind(current, valid); err != nil {
		t.Fatalf("valid rebind rejected: %v", err)
	}

	stale := valid
	stale.Generation = 3
	if err := ValidateRebind(current, stale); !errors.Is(err, ErrBinding) {
		t.Fatalf("stale generation error=%v", err)
	}

	rollback := valid
	rollback.SendOffset = 79
	if err := ValidateRebind(current, rollback); !errors.Is(err, ErrBinding) {
		t.Fatalf("send rollback error=%v", err)
	}

	impossibleAck := valid
	impossibleAck.ReceiveOffset = 101
	if err := ValidateRebind(current, impossibleAck); !errors.Is(err, ErrBinding) {
		t.Fatalf("impossible ack error=%v", err)
	}
}

func TestValidateRebindRejectsWrongIdentityAndToken(t *testing.T) {
	first, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	current := Binding{Type: BindAck, Identity: first, Generation: 1}

	wrongID := Binding{Type: BindOpen, Identity: second, Generation: 2}
	if err := ValidateRebind(current, wrongID); !errors.Is(err, ErrBinding) {
		t.Fatalf("wrong stream id error=%v", err)
	}

	wrongToken := Binding{Type: BindOpen, Identity: first, Generation: 2}
	wrongToken.Identity.Token = second.Token
	if err := ValidateRebind(current, wrongToken); !errors.Is(err, ErrBinding) {
		t.Fatalf("wrong token error=%v", err)
	}
}

func TestIdentityRejectsZeroValues(t *testing.T) {
	var identity Identity
	if identity.Valid() {
		t.Fatal("zero identity accepted")
	}
	if identity.VerifyToken(make([]byte, TokenSize)) {
		t.Fatal("zero identity verified a token")
	}
}
