package traffic

import (
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

func TestSnapshotsDoNotConsumeRatesAndExpireWithoutTraffic(t *testing.T) {
	r := NewRegistry(2, 2)
	now := time.Unix(1000, 0)
	r.now = func() time.Time { return now }
	c := r.Start(Metadata{Process: `C:\Apps\browser.exe`, Host: "example.com", DomainSource: "requested", Protocol: "tcp"})
	c.Activate()
	c.AddUpload(400)
	c.AddDownload(1000)
	for range 2 {
		s := r.Snapshot()
		if s.Active != 1 || s.UploadRate != 200 || s.DownloadRate != 500 || s.Connections[0].ProcessName != "browser.exe" {
			t.Fatalf("bad independent snapshot: %+v", s)
		}
	}
	now = now.Add(2 * time.Second)
	s := r.Snapshot()
	if s.UploadRate != 0 || s.DownloadRate != 0 || s.Upload != 400 || s.Download != 1000 {
		t.Fatalf("stale rate or lost totals: %+v", s)
	}
	c.Finish("failed", errors.New("dial failed"))
	c.Finish("closed", nil)
	s = r.Snapshot()
	if s.Active != 0 || s.Connections[0].State != "failed" || s.Connections[0].Duration != 2 {
		t.Fatalf("lifecycle: %+v", s)
	}
	*s.Connections[0].EndedAt = time.Time{}
	if r.Snapshot().Connections[0].EndedAt.IsZero() {
		t.Fatal("snapshot aliases internal timestamps")
	}
}

func TestRegistryCapacityKeepsTotalsAndBoundsHistory(t *testing.T) {
	r := NewRegistry(2, 2)
	a, b, c := r.Start(Metadata{}), r.Start(Metadata{}), r.Start(Metadata{})
	c.AddUpload(99)
	s := r.Snapshot()
	if len(s.Connections) != 2 || s.Active != 3 || s.Omitted != 1 || s.Upload != 99 {
		t.Fatalf("capacity: %+v", s)
	}
	a.Finish("closed", nil)
	b.Finish("closed", nil)
	c.Finish("closed", nil)
	for range 5 {
		r.Start(Metadata{}).Finish("closed", nil)
	}
	s = r.Snapshot()
	if s.Active != 0 || len(s.Connections) != 2 || s.Total != 8 || s.Upload != 99 {
		t.Fatalf("history: %+v", s)
	}
}

func TestConcurrentTrafficAccounting(t *testing.T) {
	r := NewRegistry(0, 0)
	record := r.Start(Metadata{})
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 1000 {
				record.AddUpload(7)
				record.AddDownload(11)
			}
		})
	}
	for range 10 {
		r.Snapshot()
	}
	wg.Wait()
	s := r.Snapshot()
	if s.Upload != 56000 || s.Download != 88000 {
		t.Fatalf("lost concurrent bytes: %+v", s)
	}
}

type partialConn struct{ closed, halfClosed bool }

func (c *partialConn) Read(p []byte) (int, error)     { return copy(p, "last"), io.EOF }
func (c *partialConn) Write(p []byte) (int, error)    { return min(len(p), 3), io.ErrShortWrite }
func (c *partialConn) Close() error                   { c.closed = true; return nil }
func (c *partialConn) CloseWrite() error              { c.halfClosed = true; return nil }
func (*partialConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (*partialConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (*partialConn) SetDeadline(time.Time) error      { return nil }
func (*partialConn) SetReadDeadline(time.Time) error  { return nil }
func (*partialConn) SetWriteDeadline(time.Time) error { return nil }

func TestConnectionCountsPartialIOAndPreservesHalfClose(t *testing.T) {
	r := NewRegistry(0, 0)
	record := r.Start(Metadata{Host: "192.0.2.1", DomainSource: "requested"})
	raw := &partialConn{}
	c := WrapConn(raw, record)
	if n, err := c.Write([]byte("sixsix")); n != 3 || !errors.Is(err, io.ErrShortWrite) {
		t.Fatal(n, err)
	}
	if n, err := c.Read(make([]byte, 8)); n != 4 || err != io.EOF {
		t.Fatal(n, err)
	}
	if err := c.(interface{ CloseWrite() error }).CloseWrite(); err != nil || !raw.halfClosed {
		t.Fatal("half-close lost")
	}
	if r.Snapshot().Active != 1 {
		t.Fatal("half-close prematurely finalized connection")
	}
	c.Close()
	c.Close()
	s := r.Snapshot()
	if s.Active != 0 || s.Upload != 3 || s.Download != 4 || s.Connections[0].Host != "" || s.Connections[0].IP != "192.0.2.1" || s.Connections[0].DomainSource != "unknown" {
		t.Fatalf("accounting: %+v", s)
	}
}

func TestRegistryRecentRingKeepsNewestConnections(t *testing.T) {
	r := NewRegistry(1, 3)
	for i := 0; i < 8; i++ {
		r.Start(Metadata{Host: "example.com"}).Finish("closed", nil)
	}
	s := r.Snapshot()
	if len(s.Connections) != 3 {
		t.Fatalf("recent length=%d want=3", len(s.Connections))
	}
	want := []uint64{8, 7, 6}
	for i, id := range want {
		if s.Connections[i].ID != id {
			t.Fatalf("recent[%d].ID=%d want=%d", i, s.Connections[i].ID, id)
		}
	}
}

func TestClearRecentPreservesActiveConnectionsAndTrafficTotals(t *testing.T) {
	r := NewRegistry(2, 3)
	active := r.Start(Metadata{Host: "active.example"})
	active.Activate()
	active.AddUpload(128)
	finished := r.Start(Metadata{Host: "finished.example"})
	finished.AddDownload(256)
	finished.Finish("closed", nil)

	before := r.Snapshot()
	if len(before.Connections) != 2 || before.Total != 2 || before.Upload != 128 || before.Download != 256 {
		t.Fatalf("unexpected pre-clear snapshot: %+v", before)
	}

	r.ClearRecent()
	after := r.Snapshot()
	if len(after.Connections) != 1 || after.Connections[0].ID != active.connection.ID || after.Active != 1 {
		t.Fatalf("clear removed or changed active connection: %+v", after)
	}
	if after.Total != 1 || after.Omitted != 0 {
		t.Fatalf("clear did not reset the connection history counters: %+v", after)
	}
	if after.Upload != 128 || after.Download != 256 {
		t.Fatalf("clear reset process traffic totals: %+v", after)
	}

	active.Finish("closed", nil)
	final := r.Snapshot()
	if len(final.Connections) != 1 || final.Connections[0].ID != active.connection.ID || final.Active != 0 || final.Total != 1 {
		t.Fatalf("active connection did not enter fresh history after clear: %+v", final)
	}
}
