package tunnel

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

type bufferObservingStream struct {
	reader *bytes.Reader
	bytes.Buffer
	reads []int
}

func (s *bufferObservingStream) Read(p []byte) (int, error) {
	s.reads = append(s.reads, len(p))
	return s.reader.Read(p)
}
func (*bufferObservingStream) Close() error                     { return nil }
func (*bufferObservingStream) CloseWrite() error                { return nil }
func (*bufferObservingStream) SetDeadline(time.Time) error      { return nil }
func (*bufferObservingStream) SetReadDeadline(time.Time) error  { return nil }
func (*bufferObservingStream) SetWriteDeadline(time.Time) error { return nil }

func TestPipeBuffersPreserveSmallAndBulkTransfers(t *testing.T) {
	for _, size := range []int{4 << 10, 1 << 20} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			payload := bytes.Repeat([]byte{0x5a}, size)
			left := &bufferObservingStream{reader: bytes.NewReader(payload)}
			right := &bufferObservingStream{reader: bytes.NewReader(nil)}
			var counted int
			up, down := Pipe(context.Background(), left, right, time.Minute, func(up bool, n int) {
				if up {
					counted += n
				}
			})
			if up != int64(size) || down != 0 || counted != size || !bytes.Equal(right.Bytes(), payload) {
				t.Fatalf("transfer corrupted: up=%d down=%d counted=%d received=%d", up, down, counted, right.Len())
			}
			promoted := false
			for _, n := range left.reads {
				promoted = promoted || n == pipeBufferSize
			}
			if left.reads[0] != pipeInitialBufferSize || promoted != (size > pipeBufferSize) {
				t.Fatalf("unexpected buffer growth: %v", left.reads)
			}
			if len(right.reads) != 1 || right.reads[0] != pipeInitialBufferSize {
				t.Fatal("the quiet direction allocated a bulk buffer")
			}
		})
	}
}

func TestPipeCancellationUnblocksBothDirections(t *testing.T) {
	left, leftPeer := net.Pipe()
	right, rightPeer := net.Pipe()
	defer leftPeer.Close()
	defer rightPeer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Pipe(ctx, left, right, time.Minute, nil)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled pipe retained its pumps and buffers")
	}
	if _, err := io.WriteString(leftPeer, "closed"); err == nil {
		t.Fatal("pipe left its transport open")
	}
}
