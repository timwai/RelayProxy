package traffic

import (
	"testing"
	"time"
)

func TestRateBucketsUseTransferTimeWithoutSnapshot(t *testing.T) {
	for _, finish := range []bool{false, true} {
		r := NewRegistry(1, 1)
		now := time.Unix(1000, 0)
		r.now = func() time.Time { return now }
		record := r.Start(Metadata{})
		omitted := r.Start(Metadata{})
		record.AddDownload(64 << 10)
		omitted.AddDownload(64 << 10)
		if s := r.Snapshot(); s.DownloadRate != 64<<10 {
			t.Fatalf("omitted rates lost: %+v", s)
		}
		record.AddUpload(32 << 10)
		now = now.Add(5 * time.Second)
		if finish {
			record.Finish("closed", nil)
		}
		s := r.Snapshot()
		if s.DownloadRate != 0 || s.UploadRate != 0 || s.Connections[0].DownloadRate != 0 || s.Connections[0].UploadRate != 0 {
			t.Fatalf("idle bytes resurrected: %+v", s)
		}
		if s.Download != 128<<10 || s.Upload != 32<<10 {
			t.Fatalf("lost totals: %+v", s)
		}
	}
}
