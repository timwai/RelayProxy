package repository

import (
	"fmt"
	"testing"
	"time"
)

func BenchmarkInsertConnectionAudits100(b *testing.B) {
	db, err := OpenDB("sqlite", "file:audit-bench?mode=memory&cache=shared")
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	now := time.Unix(1_700_000_000, 0)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		audits := make([]*ConnectionAudit, 100)
		for j := range audits {
			audits[j] = &ConnectionAudit{
				ID: fmt.Sprintf("bench-%d-%d", i, j),
				UserID: "owner",
				ClientDeviceID: "client",
				ExitDeviceID: "exit",
				Protocol: "tcp",
				TargetHost: "example.com",
				TargetPort: 443,
				StartedAt: now,
				EndedAt: now.Add(time.Second),
				BytesUp: 32 << 10,
				BytesDown: 128 << 10,
				Result: "SUCCESS",
			}
		}
		b.StartTimer()
		if err := db.InsertConnectionAudits(audits); err != nil {
			b.Fatal(err)
		}
	}
}
