package resume

import (
	"relayproxy/internal/protocol"
)

func IdentityFromProtocol(binding *protocol.TCPResumeBinding) (Identity, error) {
	if binding == nil ||
		len(binding.StreamID) != StreamIDSize ||
		len(binding.Token) != TokenSize {
		return Identity{}, ErrBinding
	}
	var identity Identity
	copy(identity.ID[:], binding.StreamID)
	copy(identity.Token[:], binding.Token)
	if !identity.Valid() {
		return Identity{}, ErrBinding
	}
	return identity, nil
}

func RequestBindingFromProtocol(binding *protocol.TCPResumeBinding) (Binding, error) {
	identity, err := IdentityFromProtocol(binding)
	if err != nil || binding.Generation == 0 {
		return Binding{}, ErrBinding
	}
	if binding.Mode != protocol.TCPResumeModeOpen && binding.Mode != protocol.TCPResumeModeRebind {
		return Binding{}, ErrBinding
	}
	return Binding{
		Type:          BindOpen,
		Identity:      identity,
		Generation:    binding.Generation,
		SendOffset:    binding.SendOffset,
		ReceiveOffset: binding.ReceiveOffset,
	}, nil
}

func ResponseBindingFromProtocol(binding *protocol.TCPResumeBinding) (Binding, error) {
	identity, err := IdentityFromProtocol(binding)
	if err != nil || binding.Generation == 0 {
		return Binding{}, ErrBinding
	}
	if binding.Mode != protocol.TCPResumeModeOpen && binding.Mode != protocol.TCPResumeModeRebind {
		return Binding{}, ErrBinding
	}
	return Binding{
		Type:          BindAck,
		Identity:      identity,
		Generation:    binding.Generation,
		SendOffset:    binding.SendOffset,
		ReceiveOffset: binding.ReceiveOffset,
	}, nil
}

func BindingToProtocol(binding Binding, mode string) (*protocol.TCPResumeBinding, error) {
	if !binding.Identity.Valid() || binding.Generation == 0 {
		return nil, ErrBinding
	}
	if mode != protocol.TCPResumeModeOpen && mode != protocol.TCPResumeModeRebind {
		return nil, ErrBinding
	}
	return &protocol.TCPResumeBinding{
		Mode:          mode,
		StreamID:      append([]byte(nil), binding.Identity.ID[:]...),
		Token:         append([]byte(nil), binding.Identity.Token[:]...),
		Generation:    binding.Generation,
		SendOffset:    binding.SendOffset,
		ReceiveOffset: binding.ReceiveOffset,
	}, nil
}
