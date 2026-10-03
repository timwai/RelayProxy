package tunnel

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"
)

func TestPipeMetricsIncludesBlockedCallAndTransferredBytes(t *testing.T) {
	leftApp, leftPipe := net.Pipe()
	rightPipe, rightApp := net.Pipe()
	defer leftApp.Close()
	defer rightApp.Close()
	metrics := &PipeMetrics{}
	done := make(chan struct{})
	go func() {
		PipeWithMetrics(context.Background(), leftPipe, rightPipe, time.Minute, nil, metrics)
		close(done)
	}()
	payload := bytes.Repeat([]byte("x"), 4096)
	written := make(chan error, 1)
	go func() { _, err := rightApp.Write(payload); written <- err }()
	deadline := time.Now().Add(time.Second)
	for metrics.Snapshot().Down.Phase != "write" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	s := metrics.Snapshot()
	if s.Down.Phase != "write" || s.Down.ReadBytes != uint64(len(payload)) || s.Down.WriteMS <= 0 {
		t.Fatalf("missing pending write metrics: %+v", s.Down)
	}
	got := make([]byte, len(payload))
	if _, err := leftApp.Read(got); err != nil {
		t.Fatal(err)
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	leftApp.Close()
	rightApp.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("pipe did not stop")
	}
	s = metrics.Snapshot()
	if s.Down.WriteBytes != uint64(len(payload)) || !bytes.Equal(got, payload) {
		t.Fatalf("wrong completed metrics: %+v", s.Down)
	}
}
