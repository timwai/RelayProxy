package resume

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	StreamIDSize = 16
	TokenSize    = 32
	BindSize     = 80

	bindMagic uint32 = 0x52505342 // "RPSB"
)

type BindType byte

const (
	BindOpen BindType = 1
	BindAck  BindType = 2
)

var ErrBinding = errors.New("invalid resumable stream binding")

type Identity struct {
	ID    [StreamIDSize]byte
	Token [TokenSize]byte
}

func NewIdentity() (Identity, error) {
	var id Identity
	if _, err := io.ReadFull(rand.Reader, id.ID[:]); err != nil {
		return Identity{}, err
	}
	if _, err := io.ReadFull(rand.Reader, id.Token[:]); err != nil {
		return Identity{}, err
	}
	return id, nil
}

func (i Identity) Valid() bool {
	var zeroID [StreamIDSize]byte
	var zeroToken [TokenSize]byte
	return subtle.ConstantTimeCompare(i.ID[:], zeroID[:]) == 0 &&
		subtle.ConstantTimeCompare(i.Token[:], zeroToken[:]) == 0
}

func (i Identity) VerifyToken(token []byte) bool {
	return i.Valid() && len(token) == TokenSize && subtle.ConstantTimeCompare(i.Token[:], token) == 1
}

// Binding is exchanged before framed stream data. Generation must increase for
// every replacement transport so delayed traffic from an older P2P/Relay
// stream cannot take ownership back from the current binding.
//
// SendOffset is the total number of logical bytes this side has emitted.
// ReceiveOffset is the contiguous number of peer bytes this side has accepted.
type Binding struct {
	Type          BindType
	Identity      Identity
	Generation    uint64
	SendOffset    uint64
	ReceiveOffset uint64
}

func WriteBinding(w io.Writer, binding Binding) error {
	if w == nil || !binding.Identity.Valid() || binding.Generation == 0 {
		return ErrBinding
	}
	if binding.Type != BindOpen && binding.Type != BindAck {
		return ErrBinding
	}
	var raw [BindSize]byte
	binary.BigEndian.PutUint32(raw[0:4], bindMagic)
	raw[4] = Version
	raw[5] = byte(binding.Type)
	copy(raw[8:24], binding.Identity.ID[:])
	copy(raw[24:56], binding.Identity.Token[:])
	binary.BigEndian.PutUint64(raw[56:64], binding.Generation)
	binary.BigEndian.PutUint64(raw[64:72], binding.SendOffset)
	binary.BigEndian.PutUint64(raw[72:80], binding.ReceiveOffset)
	return writeAll(w, raw[:])
}

func ReadBinding(r io.Reader) (Binding, error) {
	if r == nil {
		return Binding{}, ErrBinding
	}
	var raw [BindSize]byte
	if _, err := io.ReadFull(r, raw[:]); err != nil {
		return Binding{}, err
	}
	if binary.BigEndian.Uint32(raw[0:4]) != bindMagic || raw[4] != Version || raw[6] != 0 || raw[7] != 0 {
		return Binding{}, ErrBinding
	}
	var binding Binding
	binding.Type = BindType(raw[5])
	if binding.Type != BindOpen && binding.Type != BindAck {
		return Binding{}, ErrBinding
	}
	copy(binding.Identity.ID[:], raw[8:24])
	copy(binding.Identity.Token[:], raw[24:56])
	binding.Generation = binary.BigEndian.Uint64(raw[56:64])
	binding.SendOffset = binary.BigEndian.Uint64(raw[64:72])
	binding.ReceiveOffset = binary.BigEndian.Uint64(raw[72:80])
	if !binding.Identity.Valid() || binding.Generation == 0 {
		return Binding{}, ErrBinding
	}
	return binding, nil
}

// ValidateRebind validates the shared identity, generation and byte-offset
// invariants for a replacement transport. Callers that know whether they are
// validating a request or response should use the direction-specific wrappers.
func ValidateRebind(current, next Binding) error {
	if current.Identity.ID != next.Identity.ID {
		return fmt.Errorf("%w: stream id mismatch", ErrBinding)
	}
	if !current.Identity.VerifyToken(next.Identity.Token[:]) {
		return fmt.Errorf("%w: token mismatch", ErrBinding)
	}
	if current.Generation == ^uint64(0) || next.Generation != current.Generation+1 {
		return fmt.Errorf("%w: non-consecutive generation", ErrBinding)
	}
	if next.SendOffset < current.ReceiveOffset {
		return fmt.Errorf("%w: peer send offset rolled back", ErrBinding)
	}
	if next.ReceiveOffset > current.SendOffset {
		return fmt.Errorf("%w: peer acknowledged unsent bytes", ErrBinding)
	}
	return nil
}

func ValidateRebindRequest(current, next Binding) error {
	if current.Type != BindAck || next.Type != BindOpen {
		return fmt.Errorf("%w: invalid rebind request direction", ErrBinding)
	}
	return ValidateRebind(current, next)
}

func ValidateRebindResponse(current, next Binding) error {
	if current.Type != BindAck || next.Type != BindAck {
		return fmt.Errorf("%w: invalid rebind response direction", ErrBinding)
	}
	return ValidateRebind(current, next)
}
