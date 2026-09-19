package acl

import (
	"context"
	"fmt"
	"testing"
)

func benchmarkPolicy(ruleCount int) Policy {
	p := Policy{
		ID: "bench",
		Name: "benchmark",
		AllowInternet: true,
		AllowPrivateNetwork: true,
		AccessMode: AccessModeDeny,
		AccessHosts: []string{"*.blocked.example", ".internal.example", "exact.example"},
		AccessCIDRs: []string{"198.51.100.0/24", "203.0.113.10-203.0.113.20"},
	}
	for i := 0; i < ruleCount; i++ {
		p.Rules = append(p.Rules, Rule{
			Priority: ruleCount - i,
			Action: ActionDeny,
			Protocol: "tcp",
			TargetType: TargetDomain,
			TargetValue: fmt.Sprintf("blocked-%d.example", i),
		})
	}
	return p
}

func BenchmarkPolicySnapshot50Rules(b *testing.B) {
	checker, err := NewChecker(benchmarkPolicy(50))
	if err != nil { b.Fatal(err) }
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = checker.Policy()
	}
}

func BenchmarkNewChecker50Rules(b *testing.B) {
	p := benchmarkPolicy(50)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := NewChecker(p); err != nil { b.Fatal(err) }
	}
}

func BenchmarkCheckHost50Rules(b *testing.B) {
	checker, err := NewChecker(benchmarkPolicy(50))
	if err != nil { b.Fatal(err) }
	ctx := context.Background()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if err := checker.CheckHostProtocol(ctx, "allowed.example", 443, "tcp"); err != nil {
			b.Fatal(err)
		}
	}
}
