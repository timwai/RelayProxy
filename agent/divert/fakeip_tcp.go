package divert

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
)

// serveFakeDNSTCP handles RFC 7766 DNS-over-TCP without ever connecting to a
// system resolver. The caller has already authenticated/redirected the stream.
func (s *Server) serveFakeDNSTCP(ctx context.Context, conn net.Conn) error {
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	for {
		// A hot policy update must apply to existing DNS/TCP keep-alive
		// sessions, not only to newly intercepted SYN packets. Never keep
		// handing out FakeIPs after the feature has been disabled.
		if s.opts.FakeIPEnabled != nil && !s.fakeIPEnabled() {
			return fmt.Errorf("fake DNS TCP disabled by active routing policy")
		}
		var prefix [2]byte
		if _, err := io.ReadFull(conn, prefix[:]); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		length := int(binary.BigEndian.Uint16(prefix[:]))
		if length < 12 || length > 8192 {
			return fmt.Errorf("fake DNS TCP query length %d is unsupported", length)
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(conn, payload); err != nil {
			return err
		}
		answer := s.fakeDNS.reply(payload, s.guard.RelayHost, s.guard.RelayIPs)
		if len(answer) == 0 || len(answer) > 65535 {
			return fmt.Errorf("fake DNS TCP question cannot be safely answered")
		}
		binary.BigEndian.PutUint16(prefix[:], uint16(len(answer)))
		buffer := net.Buffers{prefix[:], answer}
		if _, err := buffer.WriteTo(conn); err != nil {
			return err
		}
	}
}
