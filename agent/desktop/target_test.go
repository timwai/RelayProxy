package desktop

import (
	"bytes"
	"context"
	"errors"
	"net"
	"testing"
	"time"

	desktopmedia "relayproxy/internal/desktop"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

type targetTestStream struct {
	read  chan []byte
	write chan []byte
	done  chan struct{}
}

func (s *targetTestStream) Read(p []byte) (int, error) {
	select {
	case data := <-s.read:
		return copy(p, data), nil
	case <-s.done:
		return 0, net.ErrClosed
	}
}
func (s *targetTestStream) Write(p []byte) (int, error) {
	data := append([]byte(nil), p...)
	select {
	case s.write <- data:
		return len(p), nil
	case <-s.done:
		return 0, net.ErrClosed
	}
}
func (s *targetTestStream) Close() error {
	select {
	case <-s.done:
	default:
		close(s.done)
	}
	return nil
}
func (s *targetTestStream) CloseWrite() error                 { return nil }
func (s *targetTestStream) SetDeadline(time.Time) error       { return nil }
func (s *targetTestStream) SetReadDeadline(time.Time) error   { return nil }
func (s *targetTestStream) SetWriteDeadline(time.Time) error  { return nil }

func TestTargetDesktopMediaRejectsMissingHostBackend(t *testing.T) {
	stream := &targetTestStream{read: make(chan []byte, 1), write: make(chan []byte, 1), done: make(chan struct{})}
	var request bytes.Buffer
	if err := protocol.WriteJSON(&request, protocol.OpenDesktopMediaRequest{
		RequestID: "desktop-1", Mode: protocol.DesktopMediaModeDatagram, AssociationID: 9,
	}); err != nil {
		t.Fatal(err)
	}
	stream.read <- request.Bytes()
	err := HandleTargetMediaStreamWithHeader(context.Background(), stream, nil, &protocol.StreamHeader{Type: protocol.FrameTypeOpenDesktopMedia}, nil)
	if err == nil {
		t.Fatal("missing host backend accepted")
	}
	select {
	case raw := <-stream.write:
		var response protocol.OpenDesktopMediaResponse
		if err := protocol.ReadJSON(bytes.NewReader(raw), &response); err != nil {
			t.Fatal(err)
		}
		if response.Success || response.ErrorCode != protocol.ErrCodeConnectionRefused {
			t.Fatalf("response=%+v", response)
		}
	default:
		t.Fatal("missing backend response not written")
	}
}

var _ HostHandler = HostHandlerFunc(func(context.Context, *desktopmedia.MediaConn) error { return errors.New("unused") })
