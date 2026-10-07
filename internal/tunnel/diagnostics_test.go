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
	if after.QUIC.CongestionController != "bbr-aggressive" {
		t.Fatalf("congestion controller=%q, want bbr-aggressive", after.QUIC.CongestionController)
	}
	if after.QUIC.BytesSent-before.QUIC.BytesSent < uint64(len(payload)) || after.QUIC.PacketsSent <= before.QUIC.PacketsSent {
		t.Fatalf("server download bytes not counted as sent: before=%+v after=%+v", before.QUIC, after.QUIC)
	}
}

func TestQUICDiagnosticsRateSamplingAndLossPercent(t *testing.T) {
	session := &QUICSession{}
	start := time.Unix(100, 0)
	if send, recv := session.sampleByteRates(start, 1000, 2000); send != 0 || recv != 0 {
		t.Fatalf("first sample rates = %d/%d, want 0/0", send, recv)
	}
	if send, recv := session.sampleByteRates(start.Add(100*time.Millisecond), 2000, 4000); send != 0 || recv != 0 {
		t.Fatalf("sub-interval sample rates = %d/%d, want cached 0/0", send, recv)
	}
	send, recv := session.sampleByteRates(start.Add(time.Second), 3000, 6000)
	if send != 2000 || recv != 4000 {
		t.Fatalf("sample rates = %d/%d, want 2000/4000", send, recv)
	}
	if got := lossPercent(5, 200); got != 2.5 {
		t.Fatalf("loss percent = %v, want 2.5", got)
	}
	if got := lossPercent(1, 0); got != 0 {
		t.Fatalf("zero-total loss percent = %v, want 0", got)
	}
}


func TestQUICDiagnosticsTrackBrutalController(t *testing.T) {
	client, server := sessionPair(t, "quic")
	if !UseBrutal(server, 12_500_000, false) {
		t.Fatal("failed to switch QUIC session to Brutal")
	}
	diagnostics := DiagnoseSession(server)
	if diagnostics == nil || diagnostics.QUIC == nil {
		t.Fatal("missing QUIC diagnostics")
	}
	if diagnostics.QUIC.CongestionController != "brutal" {
		t.Fatalf("controller=%q, want brutal", diagnostics.QUIC.CongestionController)
	}
	if diagnostics.QUIC.CongestionTargetBPS != 12_500_000 {
		t.Fatalf("target=%d, want 12500000", diagnostics.QUIC.CongestionTargetBPS)
	}
	_ = client
}
