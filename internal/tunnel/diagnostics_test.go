package tunnel

import (
	"bytes"
	"io"
	"testing"
	"time"
)

func TestQUICDiagnosticsDescribeTheSendingEndpoint(t *testing.T) {
	client, server := sessionPair(t, "quic")
	clientStream, serverStream := streamPair(t, client, server)
	defer clientStream.Close()
	defer serverStream.Close()
	_ = clientStream.SetDeadline(time.Now().Add(3 * time.Second))
	_ = serverStream.SetDeadline(time.Now().Add(3 * time.Second))
	before := DiagnoseSession(server)
	payload := bytes.Repeat([]byte("download"), 16*1024)
	written := make(chan error, 1)
	go func() { _, err := serverStream.Write(payload); written <- err }()
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(clientStream, got); err != nil {
		t.Fatal(err)
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	after := DiagnoseSession(server)
	if !bytes.Equal(got, payload) || after.QUIC == nil || before.QUIC == nil {
		t.Fatal("missing transferred data or QUIC statistics")
	}
	if after.Local != server.LocalAddr().String() || after.Remote != server.RemoteAddr().String() {
		t.Fatalf("diagnostics do not match the sampled endpoint: %+v", after)
	}
	if after.QUIC.BytesSent-before.QUIC.BytesSent < uint64(len(payload)) || after.QUIC.PacketsSent <= before.QUIC.PacketsSent {
		t.Fatalf("server download bytes not counted as sent: before=%+v after=%+v", before.QUIC, after.QUIC)
	}
}
