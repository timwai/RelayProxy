package resume

import (
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"testing"
	"time"
)

func TestEndpointBuffersConsecutiveFrames(t *testing.T) {
	left, right := newEndpointPair(t, 512<<10)
	bindEndpointPair(t, left, right, 1)
	_ = left.SetDeadline(time.Now().Add(3 * time.Second))
	_ = right.SetDeadline(time.Now().Add(3 * time.Second))
	var want []byte
	for i, size := range []int{17, MaxPayload, 3, MaxPayload, 1024} {
		payload := bytes.Repeat([]byte{byte(i + 1)}, size)
		want = append(want, payload...)
		if _, err := left.Write(payload); err != nil {
			t.Fatal(err)
		}
	}
	// Delay application reads until several differently sized frames have
	// crossed the same decoder, so reused storage cannot hide corruption.
	got := make([]byte, len(want))
	if _, err := io.ReadFull(right, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("buffered frames were overwritten by later payloads")
	}
}

func newEndpointPair(t *testing.T, replayLimit int) (*Endpoint, *Endpoint) {
	t.Helper()
	identity, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	leftState, err := NewStreamState(identity, replayLimit)
	if err != nil {
		t.Fatal(err)
	}
	rightState, err := NewStreamState(identity, replayLimit)
	if err != nil {
		t.Fatal(err)
	}
	left, err := NewEndpoint(leftState)
	if err != nil {
		t.Fatal(err)
	}
	right, err := NewEndpoint(rightState)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = left.Close()
		_ = right.Close()
	})
	return left, right
}

func bindEndpointPair(t *testing.T, left, right *Endpoint, generation uint64) (net.Conn, net.Conn) {
	t.Helper()
	a, b := net.Pipe()
	if err := left.Bind(a, generation); err != nil {
		t.Fatal(err)
	}
	if err := right.Bind(b, generation); err != nil {
		t.Fatal(err)
	}
	return a, b
}

func TestEndpointSurvivesTransportRebind(t *testing.T) {
	left, right := newEndpointPair(t, 1024)
	firstA, firstB := bindEndpointPair(t, left, right, 1)

	if _, err := left.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(right, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "hello" {
		t.Fatalf("first payload=%q", buf)
	}

	_ = firstA.Close()
	_ = firstB.Close()
	select {
	case <-left.Losses():
	case <-time.After(time.Second):
		t.Fatal("left did not report transport loss")
	}

	writeDone := make(chan error, 1)
	go func() {
		_, err := left.Write([]byte(" world"))
		writeDone <- err
	}()

	select {
	case err := <-writeDone:
		t.Fatalf("write completed before rebind: %v", err)
	case <-time.After(30 * time.Millisecond):
	}

	bindEndpointPair(t, left, right, 2)

	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("write did not recover after rebind")
	}

	buf = make([]byte, 6)
	if _, err := io.ReadFull(right, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != " world" {
		t.Fatalf("rebound payload=%q", buf)
	}

	deadline := time.Now().Add(time.Second)
	for left.State().BufferedReplayBytes() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := left.State().BufferedReplayBytes(); got != 0 {
		t.Fatalf("left replay buffer=%d after ACK", got)
	}
}

func TestEndpointFullDuplexDataDoesNotDeadlockOnACK(t *testing.T) {
	left, right := newEndpointPair(t, 1024)
	bindEndpointPair(t, left, right, 1)

	leftWrite := make(chan error, 1)
	rightWrite := make(chan error, 1)
	go func() {
		_, err := left.Write([]byte("from-left"))
		leftWrite <- err
	}()
	go func() {
		_, err := right.Write([]byte("from-right"))
		rightWrite <- err
	}()

	for name, result := range map[string]<-chan error{
		"left":  leftWrite,
		"right": rightWrite,
	} {
		select {
		case err := <-result:
			if err != nil {
				t.Fatalf("%s write failed: %v", name, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s write deadlocked during simultaneous ACK", name)
		}
	}

	leftPayload := make([]byte, len("from-right"))
	if _, err := io.ReadFull(left, leftPayload); err != nil {
		t.Fatal(err)
	}
	rightPayload := make([]byte, len("from-left"))
	if _, err := io.ReadFull(right, rightPayload); err != nil {
		t.Fatal(err)
	}
	if string(leftPayload) != "from-right" || string(rightPayload) != "from-left" {
		t.Fatalf("full-duplex payloads left=%q right=%q", leftPayload, rightPayload)
	}

	deadline := time.Now().Add(time.Second)
	for (left.State().BufferedReplayBytes() != 0 || right.State().BufferedReplayBytes() != 0) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if left.State().BufferedReplayBytes() != 0 || right.State().BufferedReplayBytes() != 0 {
		t.Fatalf("ACKs did not drain replay buffers: left=%d right=%d",
			left.State().BufferedReplayBytes(), right.State().BufferedReplayBytes())
	}
}

func TestEndpointPreservesHalfClose(t *testing.T) {
	left, right := newEndpointPair(t, 1024)
	bindEndpointPair(t, left, right, 1)

	if _, err := left.Write([]byte("done")); err != nil {
		t.Fatal(err)
	}
	if err := left.CloseWrite(); err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 4)
	if _, err := io.ReadFull(right, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "done" {
		t.Fatalf("payload=%q", buf)
	}
	one := make([]byte, 1)
	if n, err := right.Read(one); n != 0 || err != io.EOF {
		t.Fatalf("post-FIN read n=%d err=%v", n, err)
	}

	deadline := time.Now().Add(time.Second)
	for !left.State().LocalFINAcknowledged() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !left.State().LocalFINAcknowledged() {
		t.Fatal("FIN was not acknowledged")
	}
}

func TestEndpointRejectsStaleGeneration(t *testing.T) {
	left, _ := newEndpointPair(t, 1024)
	a, b := net.Pipe()
	defer b.Close()
	if err := left.Bind(a, 2); err != nil {
		t.Fatal(err)
	}
	c, d := net.Pipe()
	defer d.Close()
	if err := left.Bind(c, 2); err != ErrBinding {
		t.Fatalf("stale bind error=%v", err)
	}
}

func TestEndpointRetryBindRequiresLostCurrentGeneration(t *testing.T) {
	left, _ := newEndpointPair(t, 1024)
	first, firstPeer := net.Pipe()
	if err := left.Bind(first, 1); err != nil {
		t.Fatal(err)
	}
	if err := firstPeer.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-left.Losses():
	case <-time.After(time.Second):
		t.Fatal("endpoint did not observe the first transport loss")
	}

	retry, retryPeer := net.Pipe()
	defer retryPeer.Close()
	if err := left.RetryBind(retry, 1); err != nil {
		t.Fatalf("current generation retry failed: %v", err)
	}

	duplicate, duplicatePeer := net.Pipe()
	defer duplicatePeer.Close()
	if err := left.RetryBind(duplicate, 1); !errors.Is(err, ErrBinding) {
		t.Fatalf("active generation retry error=%v", err)
	}

	future, futurePeer := net.Pipe()
	defer futurePeer.Close()
	if err := left.RetryBind(future, 2); !errors.Is(err, ErrBinding) {
		t.Fatalf("future generation retry error=%v", err)
	}
}

func TestEndpointCloseWakesBlockedWrite(t *testing.T) {
	left, _ := newEndpointPair(t, 1024)
	done := make(chan error, 1)
	go func() {
		_, err := left.Write([]byte("blocked"))
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	if err := left.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("blocked write returned nil after close")
		}
	case <-time.After(time.Second):
		t.Fatal("blocked write did not wake on close")
	}
}

func TestEndpointReadDeadline(t *testing.T) {
	left, _ := newEndpointPair(t, 1024)
	if err := left.SetReadDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	var one [1]byte
	if n, err := left.Read(one[:]); n != 0 || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("read deadline n=%d err=%v", n, err)
	}
}

func TestEndpointExpiredWriteDeadlineDoesNotQueueData(t *testing.T) {
	left, _ := newEndpointPair(t, 1024)
	if err := left.SetWriteDeadline(time.Now().Add(-time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if n, err := left.Write([]byte("late")); n != 0 || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("write deadline n=%d err=%v", n, err)
	}
	if got := left.State().BufferedReplayBytes(); got != 0 {
		t.Fatalf("expired write queued %d replay bytes", got)
	}
}

func TestEndpointCloseSendsLogicalReset(t *testing.T) {
	left, right := newEndpointPair(t, 1024)
	bindEndpointPair(t, left, right, 1)

	if _, err := left.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	var first [1]byte
	if _, err := io.ReadFull(right, first[:]); err != nil {
		t.Fatal(err)
	}
	if err := left.Close(); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		var one [1]byte
		_, err := right.Read(one[:])
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("peer read returned nil after logical reset")
		}
	case <-time.After(time.Second):
		t.Fatal("logical reset did not close peer endpoint")
	}
	deadline := time.Now().Add(time.Second)
	for !right.Closed() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !right.Closed() {
		t.Fatal("peer endpoint remained open after logical reset")
	}
}
