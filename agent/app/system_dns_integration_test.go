package app

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"relayproxy/agent/exit"
	"relayproxy/agent/routing"
	"relayproxy/internal/acl"
)

func TestExitSystemDNSRelayTransportMatrix(t *testing.T) {
	for _, clientMode := range []string{"quic_only", "tcp_only"} {
		for _, exitMode := range []string{"quic_only", "tcp_only"} {
			t.Run(clientMode+"_"+exitMode, func(t *testing.T) {
				checker, err := acl.NewChecker(acl.Policy{ID: "office", AllowInternet: true, AllowPrivateNetwork: true})
				if err != nil {
					t.Fatal(err)
				}
				var lookups atomic.Int32
				clientAgent, _, _ := startRelayPair(t, clientMode, exitMode, checker, func(a *Agent) {
					_ = a.exitHandler.Close()
					a.exitHandler = exit.NewHandler(exit.HandlerConfig{ACLChecker: checker, SystemDNSLookup: func(_ context.Context, network, host string) ([]net.IP, error) {
						lookups.Add(1)
						if host != "office.internal" {
							return nil, fmt.Errorf("unexpected DNS name %s", host)
						}
						if network == "ip4" {
							return []net.IP{net.ParseIP("10.20.30.40")}, nil
						}
						if network == "ip6" {
							return []net.IP{net.ParseIP("fd00::40")}, nil
						}
						return nil, fmt.Errorf("unexpected DNS network %s", network)
					}})
				})
				cfg := clientAgent.Config()
				cfg.Routing.Mode = routing.ModeRule
				cfg.Routing.DNSFollowRouting = true
				cfg.Routing.Rules = []routing.Rule{{Name: "office DNS", Enabled: true, Action: routing.ActionProxy, ExitID: "exit", Targets: []string{"office.internal"}, DNSResolver: "exit_system"}}
				if err := clientAgent.ApplyPolicies(cfg.Routing, cfg.DivertConfig); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				report, err := clientAgent.ProbeDNSRule(ctx, -1, "office.internal")
				if err != nil {
					t.Fatal(err)
				}
				if report.ExitID != "exit" || report.Resolver != "exit_system" || report.RoutingRule != "office DNS" || len(report.Results) != 2 {
					t.Fatalf("DNS policy lost through Relay: %+v", report)
				}
				for i, want := range []string{"10.20.30.40", "fd00::40"} {
					r := report.Results[i]
					if !r.OK || len(r.Answers) != 1 || r.Answers[0] != want {
						t.Fatalf("family %d did not resolve on exit: %+v", i, r)
					}
				}
				if lookups.Load() != 2 {
					t.Fatalf("DNS did not run exactly once per family on exit: %d", lookups.Load())
				}
			})
		}
	}
}
