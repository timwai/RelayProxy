package traffic

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"time"
)

func TestDownloadIODistinguishesPendingReadFromBlockedLocalWrite(t *testing.T) {
	registry := NewRegistry(1, 1)
	record := registry.Start(Metadata{Protocol: "tcp"})
	source, feed := io.Pipe()
	drain, destination := io.Pipe()
	t.Cleanup(func() { source.Close(); feed.Close(); drain.Close(); destination.Close() })
	done := make(chan error, 1)
	go func() {
		_, err := CopyDownload(destination, source, make([]byte, 4096), record)
		done <- err
	}()
	waitPhase := func(phase string) *DownloadIO {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			snapshot := registry.Snapshot().Connections[0].DownloadIO
			if snapshot != nil && snapshot.Phase == phase {
				return snapshot
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatalf("download never reached %s", phase)
		return nil
	}
	first := waitPhase("read")
	time.Sleep(5 * time.Millisecond)
	second := waitPhase("read")
	if second.ReadMS <= first.ReadMS || second.WriteMS != 0 {
		t.Fatalf("pending read not measured: first=%+v second=%+v", first, second)
	}
	payload := bytes.Repeat([]byte("x"), 1024)
	if _, err := feed.Write(payload); err != nil {
		t.Fatal(err)
	}
	first = waitPhase("write")
	time.Sleep(5 * time.Millisecond)
	second = waitPhase("write")
	if second.WriteMS <= first.WriteMS || second.ReadBytes != 1024 || second.WriteBytes != 0 {
		t.Fatalf("blocked local write not measured: first=%+v second=%+v", first, second)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(drain, got); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("download changed: %q, %v", got, err)
	}
	feed.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("copy did not finish after EOF")
	}
	record.Finish("closed", nil)
	snapshot := registry.Snapshot().Connections[0]
	if snapshot.DownloadIO.Phase != "idle" || snapshot.DownloadIO.WriteBytes != 1024 || snapshot.Download != 0 {
		t.Fatalf("copy state or duplicate accounting: %+v", snapshot)
	}
	snapshot.DownloadIO.Phase = "changed"
	if registry.Snapshot().Connections[0].DownloadIO.Phase != "idle" {
		t.Fatal("snapshot aliases stored diagnostics")
	}
}

type shortDownloadWriter struct{}

func (shortDownloadWriter) Write(p []byte) (int, error) { return min(2, len(p)), nil }

func TestDownloadIOPreservesPartialWrites(t *testing.T) {
	r := NewRegistry(1, 1)
	n, err := CopyDownload(shortDownloadWriter{}, bytes.NewBufferString("abcd"), make([]byte, 32), r.Start(Metadata{}))
	if n != 2 || !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("partial write: %d %v", n, err)
	}
	s := r.Snapshot().Connections[0].DownloadIO
	if s.ReadBytes != 4 || s.WriteBytes != 2 || s.Phase != "idle" {
		t.Fatalf("partial write stats: %+v", s)
	}
}

func BenchmarkCopyDownload1MiB(b *testing.B) {
	payload := make([]byte, 1<<20)
	for _, observed := range []bool{false, true} {
		name := "raw"
		if observed {
			name = "observed"
		}
		b.Run(name, func(b *testing.B) {
			var record *Record
			if observed {
				record = NewRegistry(1, 1).Start(Metadata{})
			}
			buf := make([]byte, 128<<10)
			b.SetBytes(int64(len(payload)))
			b.ReportAllocs()
			for b.Loop() {
				// Match the production stream's generic read/write copy path.
				_, err := CopyDownload(struct{ io.Writer }{io.Discard}, struct{ io.Reader }{bytes.NewReader(payload)}, buf, record)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
