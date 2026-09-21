package desktop

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	desktopmedia "relayproxy/internal/desktop"
	"relayproxy/internal/protocol"
)

type targetTestStream struct {
	reader *bytes.Reader
	writer bytes.Buffer
}

func newTargetTestStream(payload []byte) *targetTestStream {
	return &targetTestStream{reader: bytes.NewReader(payload)}
}

func (s *targetTestStream) Read(p []byte) (int, error)       { return s.reader.Read(p) }
func (s *targetTestStream) Write(p []byte) (int, error)      { return s.writer.Write(p) }
func (s *targetTestStream) Close() error                     { return nil }
func (s *targetTestStream) CloseWrite() error                { return nil }
func (s *targetTestStream) SetDeadline(time.Time) error      { return nil }
func (s *targetTestStream) SetReadDeadline(time.Time) error  { return nil }
func (s *targetTestStream) SetWriteDeadline(time.Time) error { return nil }

func TestTargetDesktopMediaRejectsMissingHostBackend(t *testing.T) {
	var request bytes.Buffer
	if err := protocol.WriteJSON(&request, protocol.OpenDesktopMediaRequest{
		RequestID: "desktop-1", Mode: protocol.DesktopMediaModeDatagram, AssociationID: 9,
	}); err != nil {
		t.Fatal(err)
	}
	stream := newTargetTestStream(request.Bytes())
	err := HandleTargetMediaStreamWithHeader(context.Background(), stream, nil, &protocol.StreamHeader{Type: protocol.FrameTypeOpenDesktopMedia}, nil)
	if err == nil {
		t.Fatal("missing host backend accepted")
	}
	var response protocol.OpenDesktopMediaResponse
	if err := protocol.ReadJSON(bytes.NewReader(stream.writer.Bytes()), &response); err != nil {
		t.Fatal(err)
	}
	if response.Success || response.ErrorCode != protocol.ErrCodeConnectionRefused {
		t.Fatalf("response=%+v", response)
	}
}

var _ HostHandler = HostHandlerFunc(func(context.Context, *desktopmedia.MediaConn) error { return errors.New("unused") })
