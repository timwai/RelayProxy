package direct

import (
	"testing"
	"time"

	"relayproxy/internal/protocol"
)

func TestRequestTicketUsesDedicatedControlFrame(t *testing.T) {
	stream := &registrationStream{}
	if err := protocol.WriteJSON(&stream.read, protocol.PublicDirectTicketResponse{
		Success: true,
		Ticket:  []byte("signed-ticket"),
		ExpiresAt: time.Now().Add(time.Minute).Unix(),
		PolicyRevision: 7,
		AuthorizationRevision: 11,
	}); err != nil {
		t.Fatal(err)
	}
	session := &registrationSession{stream: stream, done: make(chan struct{})}

	response, err := RequestTicket(t.Context(), session, "exit")
	if err != nil {
		t.Fatal(err)
	}
	if string(response.Ticket) != "signed-ticket" || response.PolicyRevision != 7 ||
		response.AuthorizationRevision != 11 {
		t.Fatalf("response=%+v", response)
	}

	header, err := protocol.ReadStreamHeader(&stream.write)
	if err != nil {
		t.Fatal(err)
	}
	if header.Type != protocol.FrameTypePublicDirectTicket {
		t.Fatalf("frame type=%d", header.Type)
	}
	var request protocol.PublicDirectTicketRequest
	if err := protocol.ReadJSON(&stream.write, &request); err != nil {
		t.Fatal(err)
	}
	if request.ExitDeviceID != "exit" {
		t.Fatalf("request=%+v", request)
	}
}

func TestRequestTicketRejectsServerFailure(t *testing.T) {
	stream := &registrationStream{}
	if err := protocol.WriteJSON(&stream.read, protocol.PublicDirectTicketResponse{
		Success: false, ErrorCode: protocol.ErrCodeAccessDenied, ErrorMessage: "denied",
	}); err != nil {
		t.Fatal(err)
	}
	session := &registrationSession{stream: stream, done: make(chan struct{})}
	if _, err := RequestTicket(t.Context(), session, "exit"); err == nil {
		t.Fatal("server rejection was accepted")
	}
}
