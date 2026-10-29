//go:build linux

package divert

import (
	"bytes"
	"net"
	"os"
	"testing"
	"time"
)

// Run only in an isolated network namespace/container with NET_ADMIN and
// NET_RAW. This installs real rules; ordinary unit-test runs never do so.
func TestLinuxIPv4OnlyNFQueue(t *testing.T) {
	if os.Getenv("RELAYPROXY_LINUX_NETFILTER_TEST") != "1" {
		t.Skip("requires an isolated Linux network namespace")
	}
	binaries, err := linuxPlatformReadiness()
	if err != nil {
		t.Fatal(err)
	}
	binaries.ipv6 = ""
	d, err := newLinuxPacketDevice(nil, false, binaries)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if d.raw6 != -1 || d.PacketDeviceDiagnostics().Family != "ipv4" {
		t.Fatal("IPv4-only capture opened or advertised IPv6")
	}
	if err := runLinuxFirewallCommand(binaries.ipv4, "-t", "mangle", "-C", "OUTPUT", "-j", linuxOutputChain); err != nil {
		t.Fatal(err)
	}
	if err := d.firewall.setDNSGuardMode(true); err != nil {
		t.Fatal(err)
	}
	server, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	const payload = "relayproxy-ipv4-nfqueue"
	if _, err := client.WriteTo([]byte(payload), server.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	received := make(chan error, 1)
	go func() {
		packet, meta, err := d.ReceivePacket()
		if err == nil && !bytes.Contains(packet, []byte(payload)) {
			err = net.InvalidAddrError("NFQUEUE captured the wrong packet")
		}
		if err == nil {
			err = d.Accept(meta)
		}
		received <- err
	}()
	select {
	case err := <-received:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("IPv4 packet never reached NFQUEUE")
	}
	_ = server.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 256)
	n, _, err := server.ReadFrom(buf)
	if err != nil || string(buf[:n]) != payload {
		t.Fatalf("IPv4 NF_ACCEPT did not deliver the original datagram: %q, %v", buf[:n], err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if err := runLinuxFirewallCommand(binaries.ipv4, "-t", "mangle", "-C", "OUTPUT", "-j", linuxOutputChain); err == nil {
		t.Fatal("IPv4 capture rules remained after shutdown")
	}
}
