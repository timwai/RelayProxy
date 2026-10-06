package traffic

import (
	"io"
	"sync/atomic"
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

const (
	downloadPhaseIdle uint32 = iota
	downloadPhaseRead
	downloadPhaseWrite
)

var downloadClockStart = time.Now()

func downloadNowNanos() int64 {
	return time.Since(downloadClockStart).Nanoseconds()
}

type downloadObserver struct {
	phase        atomic.Uint32
	startedNanos atomic.Int64
	readNanos    atomic.Uint64
	writeNanos   atomic.Uint64
	readCalls    atomic.Uint64
	writeCalls   atomic.Uint64
	readBytes    atomic.Uint64
	writeBytes   atomic.Uint64
}

func (d *downloadObserver) begin(phase string) {
	d.startedNanos.Store(downloadNowNanos())
	switch phase {
	case "read":
		d.readCalls.Add(1)
		d.phase.Store(downloadPhaseRead)
	case "write":
		d.writeCalls.Add(1)
		d.phase.Store(downloadPhaseWrite)
	default:
		d.phase.Store(downloadPhaseIdle)
	}
}

func (d *downloadObserver) end(n int) {
	now := downloadNowNanos()
	phase := d.phase.Swap(downloadPhaseIdle)
	started := d.startedNanos.Swap(0)
	var elapsed uint64
	if started > 0 && now > started {
		elapsed = uint64(now - started)
	}
	switch phase {
	case downloadPhaseRead:
		d.readNanos.Add(elapsed)
		if n > 0 {
			d.readBytes.Add(uint64(n))
		}
	case downloadPhaseWrite:
		d.writeNanos.Add(elapsed)
		if n > 0 {
			d.writeBytes.Add(uint64(n))
		}
	}
}

func (d *downloadObserver) Snapshot() *DownloadIO {
	now := downloadNowNanos()
	readNanos := d.readNanos.Load()
	writeNanos := d.writeNanos.Load()
	phase := d.phase.Load()
	started := d.startedNanos.Load()
	if started > 0 && now > started {
		elapsed := uint64(now - started)
		switch phase {
		case downloadPhaseRead:
			readNanos += elapsed
		case downloadPhaseWrite:
			writeNanos += elapsed
		}
	}
	out := &DownloadIO{
		ReadMS:     float64(readNanos) / float64(time.Millisecond),
		WriteMS:    float64(writeNanos) / float64(time.Millisecond),
		ReadCalls:  d.readCalls.Load(),
		WriteCalls: d.writeCalls.Load(),
		ReadBytes:  d.readBytes.Load(),
		WriteBytes: d.writeBytes.Load(),
		Phase:      "idle",
	}
	switch phase {
	case downloadPhaseRead:
		out.Phase = "read"
	case downloadPhaseWrite:
		out.Phase = "write"
	}
	return out
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
	observer := &downloadObserver{}
	record.registry.mu.Lock()
	if record.finished {
		record.registry.mu.Unlock()
		return io.CopyBuffer(dst, src, buf)
	}
	record.downloadIO = observer
	record.registry.mu.Unlock()
	return io.CopyBuffer(observedDownloadWriter{dst, observer}, observedDownloadReader{src, observer}, buf)
}
