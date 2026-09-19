package traffic

import "testing"

func BenchmarkRecordAddParallel32KiB(b *testing.B) {
	registry := NewRegistry(8192, 512)
	record := registry.Start(Metadata{Protocol: "tcp"})
	record.Activate()
	b.ReportAllocs()
	b.SetBytes(32 * 1024)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			record.AddUpload(32 * 1024)
		}
	})
	b.StopTimer()
	_ = registry.Snapshot()
}

func BenchmarkRegistrySnapshot128Active(b *testing.B) {
	registry := NewRegistry(8192, 512)
	for i := 0; i < 128; i++ {
		record := registry.Start(Metadata{Protocol: "tcp"})
		record.Activate()
		record.AddUpload(32 * 1024)
		record.AddDownload(32 * 1024)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = registry.Snapshot()
	}
}


func BenchmarkRegistryStartFinishRecentFull(b *testing.B) {
	registry := NewRegistry(8192, 512)
	for i := 0; i < 512; i++ {
		registry.Start(Metadata{Protocol: "tcp"}).Finish("closed", nil)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		registry.Start(Metadata{Protocol: "tcp"}).Finish("closed", nil)
	}
}
