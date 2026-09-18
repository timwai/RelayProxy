package client

import "testing"

func BenchmarkNextRequestID(b *testing.B) {
	d := &TunnelDialer{}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = d.nextRequestID()
	}
}
