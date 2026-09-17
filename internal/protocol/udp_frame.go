package protocol

import (
	"encoding/binary"
	"fmt"
	"io"
)

// Use the IPv4 UDP payload ceiling for both address families and transports.
const MaxUDPDatagramPayload = 65507

func WriteUDPDatagram(w io.Writer, payload []byte) error {
	if len(payload) > MaxUDPDatagramPayload {
		return fmt.Errorf("udp datagram payload %d exceeds %d", len(payload), MaxUDPDatagramPayload)
	}
	frame := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(payload)))
	copy(frame[4:], payload)
	if n, err := w.Write(frame); err != nil {
		return err
	} else if n != len(frame) {
		return io.ErrShortWrite
	}
	return nil
}

func ReadUDPDatagram(r io.Reader) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > MaxUDPDatagramPayload {
		return nil, fmt.Errorf("udp datagram length %d exceeds %d", n, MaxUDPDatagramPayload)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}
