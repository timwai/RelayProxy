//go:build linux

package divert

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/netip"
	"reflect"
	"strings"
	"testing"

	"github.com/florianl/go-nfqueue/v2"
)

type testLinuxQueue struct {
	verdicts []struct {
		id      uint32
		verdict int
		altered bool
	}
}

func (q *testLinuxQueue) SetVerdict(id uint32, verdict int) error {
	q.verdicts = append(q.verdicts, struct {
		id      uint32
		verdict int
		altered bool
	}{id: id, verdict: verdict})
	return nil
}
func (q *testLinuxQueue) SetVerdictWithOption(id uint32, verdict int, options ...nfqueue.VerdictOption) error {
	q.verdicts = append(q.verdicts, struct {
		id      uint32
		verdict int
		altered bool
	}{id: id, verdict: verdict, altered: len(options) > 0})
	return nil
}
func (*testLinuxQueue) RegisterWithErrorFunc(context.Context, nfqueue.HookFunc, nfqueue.ErrorFunc) error {
	return nil
}
func (*testLinuxQueue) Close() error { return nil }

func TestParseLinuxProcEndpoint(t *testing.T) {
	tests := []struct {
		raw  string
		ipv6 bool
		want netip.AddrPort
	}{
		{"0100007F:1F90", false, netip.MustParseAddrPort("127.0.0.1:8080")},
		{"00000000000000000000000001000000:0035", true, netip.MustParseAddrPort("[::1]:53")},
		{"B80D0120000000000000000001000000:01BB", true, netip.MustParseAddrPort("[2001:db8::1]:443")},
	}
	for _, test := range tests {
		got, err := parseLinuxProcEndpoint(test.raw, test.ipv6)
		if err != nil || got != test.want {
			t.Fatalf("parseLinuxProcEndpoint(%q) = %s, %v; want %s", test.raw, got, err, test.want)
		}
	}
}

func TestParseLinuxSocketTableExactAndUDPWildcard(t *testing.T) {
	table := "  sl  local_address rem_address st tx_queue rx_queue tr tm->when retrnsmt uid timeout inode\n" +
		"  0: 0100007F:C350 00000000:0000 07 0:0 00:0 0 1000 0 11111\n" +
		"  1: 0100007F:C350 08080808:0035 01 0:0 00:0 0 1000 0 22222\n" +
		"  2: 00000000:C351 00000000:0000 07 0:0 00:0 0 1000 0 33333\n"
	source := netip.MustParseAddrPort("127.0.0.1:50000")
	exact := netip.MustParseAddrPort("8.8.8.8:53")
	inode, err := parseLinuxSocketTableReader(bufio.NewScanner(strings.NewReader(table)), ProtoUDP, source, exact)
	if err != nil || inode != 22222 {
		t.Fatalf("exact inode = %d, %v", inode, err)
	}
	wildcardDestination := netip.MustParseAddrPort("1.1.1.1:53")
	inode, err = parseLinuxSocketTableReader(bufio.NewScanner(strings.NewReader(table)), ProtoUDP, source, wildcardDestination)
	if err != nil || inode != 11111 {
		t.Fatalf("wildcard inode = %d, %v", inode, err)
	}
	wildcardSource := netip.MustParseAddrPort("192.0.2.50:50001")
	inode, err = parseLinuxSocketTableReader(bufio.NewScanner(strings.NewReader(table)), ProtoUDP, wildcardSource, exact)
	if err != nil || inode != 33333 {
		t.Fatalf("wildcard local inode = %d, %v", inode, err)
	}
}

func TestLinuxFirewallRulesCaptureOnlyOutboundAndDNSReplies(t *testing.T) {
	var commands [][]string
	f := &linuxFirewall{dnsGuard: true, run: func(binary string, args ...string) error {
		commands = append(commands, append([]string{binary}, args...))
		return nil
	}}
	if err := f.installFamily("/usr/sbin/iptables", []string{"192.0.2.10", "2001:db8::10"}); err != nil {
		t.Fatal(err)
	}
	wantFragments := [][]string{
		{"/usr/sbin/iptables", "-t", "mangle", "-A", linuxOutputChain, "-p", "tcp", "--dport", "53", "-j", "NFQUEUE", "--queue-num", "58231"},
		{"/usr/sbin/iptables", "-t", "mangle", "-A", linuxOutputChain, "-p", "udp", "--dport", "53", "-j", "NFQUEUE", "--queue-num", "58231"},
		{"/usr/sbin/iptables", "-t", "mangle", "-A", linuxOutputChain, "-p", "tcp", "--dport", "53", "-j", "RETURN"},
		{"/usr/sbin/iptables", "-t", "mangle", "-A", linuxOutputChain, "-p", "udp", "--dport", "53", "-j", "RETURN"},
		{"/usr/sbin/iptables", "-t", "mangle", "-A", linuxOutputChain, "-p", "tcp", "!", "--dport", "53", "-d", "192.0.2.10", "-j", "RETURN"},
		{"/usr/sbin/iptables", "-t", "mangle", "-A", linuxOutputChain, "-p", "udp", "!", "--dport", "53", "-d", "192.0.2.10", "-j", "RETURN"},
		{"/usr/sbin/iptables", "-t", "mangle", "-A", linuxOutputChain, "-p", "tcp", "-j", "NFQUEUE", "--queue-num", "58231", "--queue-bypass"},
		{"/usr/sbin/iptables", "-t", "mangle", "-A", linuxInputChain, "-p", "udp", "--sport", "53", "-j", "NFQUEUE", "--queue-num", "58231", "--queue-bypass"},
	}
	for _, want := range wantFragments {
		found := false
		for _, got := range commands {
			if reflect.DeepEqual(got, want) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing firewall command: %v\nall: %v", want, commands)
		}
	}
	// DNS/53 must enter a non-bypassing queue BEFORE any Relay-IP bypass.
	// This prevents a recursive DNS server on the Relay IP escaping capture.
	udpQueue, relayReturn := -1, -1
	for j, command := range commands {
		if reflect.DeepEqual(command, []string{"/usr/sbin/iptables", "-t", "mangle", "-A", linuxOutputChain, "-p", "udp", "--dport", "53", "-j", "NFQUEUE", "--queue-num", "58231"}) {
			udpQueue = j
		}
		if reflect.DeepEqual(command, []string{"/usr/sbin/iptables", "-t", "mangle", "-A", linuxOutputChain, "-p", "udp", "!", "--dport", "53", "-d", "192.0.2.10", "-j", "RETURN"}) {
			relayReturn = j
		}
	}
	if udpQueue < 0 || relayReturn <= udpQueue {
		t.Fatalf("Linux DNS interception must precede relay bypass: %d %d: %v", udpQueue, relayReturn, commands)
	}
	for _, command := range commands {
		if len(command) >= 8 && reflect.DeepEqual(command[4:8], []string{linuxInputChain, "-p", "tcp", "-j"}) {
			t.Fatalf("all inbound TCP traffic was queued: %v", command)
		}
	}
}

func TestLinuxDNSQueueNormalModeAndHotUpdates(t *testing.T) {
	var commands [][]string
	f := &linuxFirewall{
		iptables: "iptables", ip6tables: "ip6tables",
		run: func(binary string, args ...string) error {
			commands = append(commands, append([]string{binary}, args...))
			return nil
		},
	}
	if err := f.installFamily("iptables", nil); err != nil {
		t.Fatal(err)
	}
	wantQueue := []string{"iptables", "-t", "mangle", "-A", linuxOutputChain,
		"-p", "udp", "--dport", "53", "-j", "NFQUEUE", "--queue-num", "58231", "--queue-bypass"}
	present := false
	for _, got := range commands {
		if reflect.DeepEqual(got, wantQueue) {
			present = true
		}
	}
	if !present {
		t.Fatal("normal Linux mode unexpectedly enables fail-closed DNS queue")
	}
	commands = nil
	if err := f.setDNSGuardMode(true); err != nil {
		t.Fatal(err)
	}
	if !f.dnsGuard || len(commands) != 4 {
		t.Fatalf("enabling protection did not update both IP families: %+v", commands)
	}
	for _, command := range commands {
		for _, option := range command {
			if option == "--queue-bypass" {
				t.Fatalf("protected DNS rule retains bypass flag: %v", command)
			}
		}
	}
	commands = nil
	if err := f.setDNSGuardMode(false); err != nil {
		t.Fatal(err)
	}
	if f.dnsGuard || len(commands) != 4 {
		t.Fatalf("disabling protection did not update both families: %+v", commands)
	}
	for _, command := range commands {
		if command[len(command)-1] != "--queue-bypass" {
			t.Fatalf("normal mode DNS queue should not blackhole when NFQUEUE exits: %v", command)
		}
	}
}

func TestLinuxDNSGuardPartialReplacementRetriesOnlyFailedRules(t *testing.T) {
	var commands [][]string
	failOne := true
	f := &linuxFirewall{
		iptables: "iptables", ip6tables: "ip6tables",
		run: func(binary string, args ...string) error {
			command := append([]string{binary}, args...)
			commands = append(commands, command)
			if failOne && binary == "ip6tables" && len(args) > 9 && args[4] == "4" {
				failOne = false
				return errors.New("injected IPv6 UDP rule failure")
			}
			return nil
		},
	}
	if err := f.setDNSGuardMode(true); err == nil {
		t.Fatal("partially updated DNS firewall was reported as protected")
	}
	if f.dnsGuard {
		t.Fatal("partial DNS guard must not claim a complete state")
	}
	if len(commands) != 4 {
		t.Fatalf("initial transition must attempt all families: %v", commands)
	}
	if len(f.dnsRuleStates) != 4 || f.dnsRuleStates["ip6tables/udp"] {
		t.Fatalf("failed rule was incorrectly marked installed: %+v", f.dnsRuleStates)
	}
	commands = nil
	if err := f.setDNSGuardMode(true); err != nil {
		t.Fatal(err)
	}
	if len(commands) != 1 || commands[0][0] != "ip6tables" {
		t.Fatalf("partial transition retried already committed rules: %v", commands)
	}
	if !f.dnsGuard {
		t.Fatal("all four protected rules installed but state not updated")
	}
	commands = nil
	if err := f.setDNSGuardMode(false); err != nil {
		t.Fatal(err)
	}
	if len(commands) != 4 || f.dnsGuard {
		t.Fatalf("normal mode did not restore all four rules: %+v", commands)
	}
}

func TestLinuxDNSGuardCannotReinstallAfterFirewallClose(t *testing.T) {
	var calls int
	f := &linuxFirewall{
		iptables: "iptables", ip6tables: "ip6tables",
		run: func(string, ...string) error { calls++; return nil },
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	before := calls
	if err := f.setDNSGuardMode(true); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("closed firewall was modified: %v", err)
	}
	if calls != before {
		t.Fatalf("closed firewall issued %d new commands", calls-before)
	}
}

func TestLinuxPacketVerdictsAreFinalizedExactlyOnce(t *testing.T) {
	queue := &testLinuxQueue{}
	injected := 0
	device := &linuxPacketDevice{queue: queue, raw4: -1, raw6: -1, injectFn: func([]byte) error { injected++; return nil }}

	direct := packetMetadata{outbound: true, capturedOutbound: true, platformToken: &linuxPacketRef{id: 10}}
	if err := device.Accept(direct); err != nil {
		t.Fatal(err)
	}
	if err := device.Finalize(direct); err != nil {
		t.Fatal(err)
	}
	if len(queue.verdicts) != 1 || queue.verdicts[0].verdict != nfqueue.NfAccept || queue.verdicts[0].altered {
		t.Fatalf("direct verdicts = %+v", queue.verdicts)
	}

	rewritten := packetMetadata{outbound: false, capturedOutbound: true, platformToken: &linuxPacketRef{id: 11}}
	if err := device.Send([]byte{0x45}, rewritten); err != nil {
		t.Fatal(err)
	}
	if err := device.Finalize(rewritten); err != nil {
		t.Fatal(err)
	}
	if len(queue.verdicts) != 2 || queue.verdicts[1].verdict != nfqueue.NfDrop || injected != 1 {
		t.Fatalf("rewritten verdicts = %+v, injected = %d", queue.verdicts, injected)
	}

	altered := packetMetadata{outbound: true, capturedOutbound: true, platformToken: &linuxPacketRef{id: 12}}
	if err := device.Send([]byte{0x45}, altered); err != nil {
		t.Fatal(err)
	}
	if err := device.Finalize(altered); err != nil {
		t.Fatal(err)
	}
	if len(queue.verdicts) != 3 || queue.verdicts[2].verdict != nfqueue.NfAccept || !queue.verdicts[2].altered {
		t.Fatalf("altered verdicts = %+v", queue.verdicts)
	}
}
