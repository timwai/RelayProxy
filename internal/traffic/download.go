package traffic

import (
	"io"
	"sync"
	"time"
)

// DownloadIO measures elapsed time inside the download pump's Read and Write,
// including a call still in progress. These are cumulative wall times, not RTT
// or pure network wait: idle applications and scheduling also affect them.
type DownloadIO struct {
	ReadMS     float64 `json:"read_ms"`
	WriteMS    float64 `json:"write_ms"`
	ReadCalls  uint64  `json:"read_calls"`
	WriteCalls uint64  `json:"write_calls"`
	ReadBytes  uint64  `json:"read_bytes"`
	WriteBytes uint64  `json:"write_bytes"`
	Phase      string  `json:"phase"`
}

type downloadObserver struct {
	mu       sync.Mutex
	read     time.Duration
	write    time.Duration
	started  time.Time
	snapshot DownloadIO
}

func (d *downloadObserver) begin(phase string) {
	d.mu.Lock()
	d.started = time.Now()
	d.snapshot.Phase = phase
	if phase == "read" {
		d.snapshot.ReadCalls++
	} else {
		d.snapshot.WriteCalls++
	}
	d.mu.Unlock()
}

func (d *downloadObserver) end(n int) {
	d.mu.Lock()
	elapsed := time.Since(d.started)
	if d.snapshot.Phase == "read" {
		d.read += elapsed
		d.snapshot.ReadBytes += uint64(max(n, 0))
	} else {
		d.write += elapsed
		d.snapshot.WriteBytes += uint64(max(n, 0))
	}
	d.snapshot.Phase = "idle"
	d.mu.Unlock()
}

func (d *downloadObserver) Snapshot() *DownloadIO {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.snapshot
	read, write := d.read, d.write
	switch s.Phase {
	case "read":
		read += time.Since(d.started)
	case "write":
		write += time.Since(d.started)
	}
	s.ReadMS = float64(read) / float64(time.Millisecond)
	s.WriteMS = float64(write) / float64(time.Millisecond)
	return &s
}

type observedDownloadReader struct {
	io.Reader
	observer *downloadObserver
}

func (r observedDownloadReader) Read(p []byte) (int, error) {
	r.observer.begin("read")
	n, err := r.Reader.Read(p)
	r.observer.end(n)
	return n, err
}

type observedDownloadWriter struct {
	io.Writer
	observer *downloadObserver
}

func (w observedDownloadWriter) Write(p []byte) (int, error) {
	w.observer.begin("write")
	n, err := w.Writer.Write(p)
	w.observer.end(n)
	return n, err
}

// CopyDownload observes one download pump per record. The source is the
// upstream proxy stream; the destination is the local application's socket.
// Payload accounting remains in WrapConn and must not be counted again here.
func CopyDownload(dst io.Writer, src io.Reader, buf []byte, record *Record) (int64, error) {
	if record == nil {
		return io.CopyBuffer(dst, src, buf)
	}
	observer := &downloadObserver{snapshot: DownloadIO{Phase: "idle"}}
	record.registry.mu.Lock()
	if record.finished {
		record.registry.mu.Unlock()
		return io.CopyBuffer(dst, src, buf)
	}
	record.downloadIO = observer
	record.registry.mu.Unlock()
	return io.CopyBuffer(observedDownloadWriter{dst, observer}, observedDownloadReader{src, observer}, buf)
}
