//go:build windows

package divert

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

type processTableFixture struct {
	name   string
	proto  Protocol
	family uint32
	source string
	target string
	hex    string
}

// Literal bytes pin the Windows ABI independently of the parsing code. Port
// DWORDs deliberately contain garbage in their unused upper two bytes.
var processTableFixtures = []processTableFixture{
	{"tcp4", ProtoTCP, windows.AF_INET, "10.20.30.40:4660", "203.0.113.7:443",
		"01000000 05000000 0a141e28 1234aabb cb007107 01bbccdd 12345678"},
	{"tcp6", ProtoTCP, windows.AF_INET6, "[2001:db8::10]:4660", "[2001:db8::20]:443",
		"01000000 20010db8000000000000000000000010 00000000 1234aabb 20010db8000000000000000000000020 00000000 01bbccdd 05000000 12345678"},
	{"udp4", ProtoUDP, windows.AF_INET, "10.20.30.40:4660", "203.0.113.7:443",
		"01000000 0a141e28 1234aabb 12345678"},
	{"udp6", ProtoUDP, windows.AF_INET6, "[2001:db8::10]:4660", "[2001:db8::20]:443",
		"01000000 20010db8000000000000000000000010 00000000 1234aabb 12345678"},
}

func processFixtureBytes(t *testing.T, fixture processTableFixture) []byte {
	t.Helper()
	table, err := hex.DecodeString(strings.Join(strings.Fields(fixture.hex), ""))
	if err != nil {
		t.Fatal(err)
	}
	return table
}

func processFixtureOwner(table []byte, proto Protocol, family uint32, source, target netip.AddrPort) (uint32, error) {
	local, err := makeProcessEndpoint(source)
	if err != nil {
		return 0, err
	}
	remote, err := makeProcessEndpoint(target)
	if err != nil {
		return 0, err
	}
	var owners processOwnerCandidates
	if err := matchProcessTable(table, proto, family, local, remote, &owners); err != nil {
		return 0, err
	}
	return owners.processID()
}

func TestProcessTableOwnerPID(t *testing.T) {
	for _, fixture := range processTableFixtures {
		t.Run(fixture.name, func(t *testing.T) {
			table := processFixtureBytes(t, fixture)
			source, target := netip.MustParseAddrPort(fixture.source), netip.MustParseAddrPort(fixture.target)
			pid, err := processFixtureOwner(table, fixture.proto, fixture.family, source, target)
			if err != nil || pid != 0x78563412 {
				t.Fatalf("owner = %d, %v", pid, err)
			}
			for _, mismatch := range []netip.AddrPort{
				netip.AddrPortFrom(source.Addr().Next(), source.Port()),
				netip.AddrPortFrom(source.Addr(), source.Port()+1),
			} {
				if _, err := processFixtureOwner(table, fixture.proto, fixture.family, mismatch, target); !errors.Is(err, errProcessNotFound) {
					t.Fatalf("source %s matched: %v", mismatch, err)
				}
			}
			for _, changedTarget := range []netip.AddrPort{
				netip.AddrPortFrom(target.Addr().Next(), target.Port()),
				netip.AddrPortFrom(target.Addr(), target.Port()+1),
			} {
				pid, err := processFixtureOwner(table, fixture.proto, fixture.family, source, changedTarget)
				if fixture.proto == ProtoTCP && !errors.Is(err, errProcessNotFound) {
					t.Fatalf("TCP remote endpoint %s matched: %v", changedTarget, err)
				}
				if fixture.proto == ProtoUDP && (err != nil || pid != 0x78563412) {
					t.Fatalf("UDP used remote endpoint %s: %d, %v", changedTarget, pid, err)
				}
			}
		})
	}
}

func TestProcessTableDuplicateAndUnknownOwners(t *testing.T) {
	for _, fixture := range processTableFixtures {
		for _, candidate := range []struct {
			name string
			pids []uint32
			want error
		}{
			{"duplicate", []uint32{123, 123}, nil},
			{"different", []uint32{123, 456}, errProcessAmbiguous},
			{"zero", []uint32{0}, errProcessNotFound},
			{"zero_then_known", []uint32{0, 123}, errProcessAmbiguous},
			{"known_then_zero", []uint32{123, 0}, errProcessAmbiguous},
		} {
			t.Run(fixture.name+"/"+candidate.name, func(t *testing.T) {
				row := processFixtureBytes(t, fixture)[4:]
				table := make([]byte, 4, 4+len(row)*len(candidate.pids))
				binary.LittleEndian.PutUint32(table, uint32(len(candidate.pids)))
				for _, pid := range candidate.pids {
					binary.LittleEndian.PutUint32(row[len(row)-4:], pid)
					table = append(table, row...)
				}
				pid, err := processFixtureOwner(table, fixture.proto, fixture.family, netip.MustParseAddrPort(fixture.source), netip.MustParseAddrPort(fixture.target))
				if !errors.Is(err, candidate.want) || (candidate.want == nil && pid != 123) {
					t.Fatalf("owner = %d, %v; want %v", pid, err, candidate.want)
				}
			})
		}
	}
}

func TestProcessTableWildcardAndIPv4Mapped(t *testing.T) {
	for _, fixture := range processTableFixtures {
		t.Run(fixture.name+"/wildcard", func(t *testing.T) {
			table := processFixtureBytes(t, fixture)
			addressStart, addressSize := 4, 4
			if fixture.family == windows.AF_INET6 {
				addressSize = 16
			} else if fixture.proto == ProtoTCP {
				addressStart += 4
			}
			clear(table[addressStart : addressStart+addressSize])
			source, target := netip.MustParseAddrPort(fixture.source), netip.MustParseAddrPort(fixture.target)
			pid, err := processFixtureOwner(table, fixture.proto, fixture.family, source, target)
			if fixture.proto == ProtoTCP && !errors.Is(err, errProcessNotFound) {
				t.Fatalf("TCP wildcard matched an established endpoint: %d, %v", pid, err)
			}
			if fixture.proto == ProtoUDP && (err != nil || pid != 0x78563412) {
				t.Fatalf("UDP wildcard did not match: %d, %v", pid, err)
			}
			if fixture.name == "udp6" {
				pid, err = processFixtureOwner(table, ProtoUDP, windows.AF_INET6, netip.MustParseAddrPort("10.20.30.40:4660"), netip.MustParseAddrPort("203.0.113.7:443"))
				if err != nil || pid != 0x78563412 {
					t.Fatalf("dual-stack wildcard did not match IPv4: %d, %v", pid, err)
				}
			}
		})
	}
	fixture := processTableFixture{hex: "01000000 00000000000000000000ffff0a141e28 00000000 1234aabb 00000000000000000000ffffcb007107 00000000 01bbccdd 05000000 12345678"}
	table := processFixtureBytes(t, fixture)
	for _, source := range []string{"10.20.30.40:4660", "[::ffff:10.20.30.40]:4660"} {
		pid, err := processFixtureOwner(table, ProtoTCP, windows.AF_INET6, netip.MustParseAddrPort(source), netip.MustParseAddrPort("203.0.113.7:443"))
		if err != nil || pid != 0x78563412 {
			t.Fatalf("mapped IPv4 %s: %d, %v", source, pid, err)
		}
	}
}

func TestProcessTableIPv6Scope(t *testing.T) {
	fixture := processTableFixture{hex: "01000000 fe800000000000000000000000000010 0c000000 1234aabb fe800000000000000000000000000020 0d000000 01bbccdd 05000000 12345678"}
	table := processFixtureBytes(t, fixture)
	for _, test := range []struct {
		source string
		target string
		want   error
	}{
		{"[fe80::10%12]:4660", "[fe80::20%13]:443", nil},
		{"[fe80::10]:4660", "[fe80::20]:443", nil},
		{"[fe80::10%13]:4660", "[fe80::20%13]:443", errProcessNotFound},
		{"[fe80::10%12]:4660", "[fe80::20%12]:443", errProcessNotFound},
	} {
		pid, err := processFixtureOwner(table, ProtoTCP, windows.AF_INET6, netip.MustParseAddrPort(test.source), netip.MustParseAddrPort(test.target))
		if !errors.Is(err, test.want) || (test.want == nil && pid != 0x78563412) {
			t.Fatalf("%s -> %s: %d, %v; want %v", test.source, test.target, pid, err, test.want)
		}
	}
	// A missing scope must not select the first of identical addresses on
	// different interfaces when their owners differ.
	second := append([]byte(nil), table[4:]...)
	binary.LittleEndian.PutUint32(second[16:20], 14)
	binary.LittleEndian.PutUint32(second[52:56], 456)
	table = append(table, second...)
	binary.LittleEndian.PutUint32(table, 2)
	if _, err := processFixtureOwner(table, ProtoTCP, windows.AF_INET6, netip.MustParseAddrPort("[fe80::10]:4660"), netip.MustParseAddrPort("[fe80::20]:443")); !errors.Is(err, errProcessAmbiguous) {
		t.Fatalf("missing scope was guessed: %v", err)
	}
	if pid, err := processFixtureOwner(table, ProtoTCP, windows.AF_INET6, netip.MustParseAddrPort("[fe80::10%12]:4660"), netip.MustParseAddrPort("[fe80::20%13]:443")); err != nil || pid != 0x78563412 {
		t.Fatalf("explicit scope did not disambiguate: %d, %v", pid, err)
	}
}

func TestProcessTableRejectsMalformedData(t *testing.T) {
	for _, fixture := range processTableFixtures {
		t.Run(fixture.name, func(t *testing.T) {
			valid := processFixtureBytes(t, fixture)
			tooMany := append([]byte(nil), valid...)
			binary.LittleEndian.PutUint32(tooMany, ^uint32(0))
			for _, table := range [][]byte{nil, {1, 0, 0}, {1, 0, 0, 0}, valid[:len(valid)-1], tooMany} {
				_, err := processFixtureOwner(table, fixture.proto, fixture.family, netip.MustParseAddrPort(fixture.source), netip.MustParseAddrPort(fixture.target))
				if !errors.Is(err, errProcessTable) {
					t.Fatalf("accepted malformed table of length %d: %v", len(table), err)
				}
			}
			if _, err := processFixtureOwner([]byte{0, 0, 0, 0}, fixture.proto, fixture.family, netip.MustParseAddrPort(fixture.source), netip.MustParseAddrPort(fixture.target)); !errors.Is(err, errProcessNotFound) {
				t.Fatalf("empty table: %v", err)
			}
		})
	}
}

func TestProcessTableReaderBounds(t *testing.T) {
	t.Run("growing_table", func(t *testing.T) {
		calls := 0
		table, err := readProcessTable(func(buffer []byte, size *uint32) uint32 {
			calls++
			if len(buffer) < 8 {
				*size += 4
				return uint32(windows.ERROR_INSUFFICIENT_BUFFER)
			}
			*size = 4
			return 0
		})
		if err != nil || calls != 3 || len(table) != 4 {
			t.Fatalf("table length = %d, calls = %d, error = %v", len(table), calls, err)
		}
	})
	t.Run("retry_limit", func(t *testing.T) {
		calls := 0
		_, err := readProcessTable(func(_ []byte, size *uint32) uint32 {
			calls++
			*size += 4
			return uint32(windows.ERROR_INSUFFICIENT_BUFFER)
		})
		if calls != processTableTries || !errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) {
			t.Fatalf("calls = %d, error = %v", calls, err)
		}
	})
	for _, test := range []struct {
		name   string
		size   uint32
		status uint32
		want   error
	}{
		{"oversized", maxProcessTableSize + 1, uint32(windows.ERROR_INSUFFICIENT_BUFFER), errProcessTable},
		{"too_small", 3, uint32(windows.ERROR_INSUFFICIENT_BUFFER), errProcessTable},
		{"unchanged", 0, uint32(windows.ERROR_INSUFFICIENT_BUFFER), errProcessTable},
		{"invalid_success_length", 4, 0, errProcessTable},
		{"native_error", 0, uint32(windows.ERROR_ACCESS_DENIED), windows.ERROR_ACCESS_DENIED},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := readProcessTable(func(_ []byte, size *uint32) uint32 {
				*size = test.size
				return test.status
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v; want %v", err, test.want)
			}
		})
	}
}

func requireCurrentPacketProcess(t *testing.T, proto Protocol, source, target netip.AddrPort) {
	t.Helper()
	identity, err := lookupPacketProcess(proto, source, target)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if identity.PID != uint32(os.Getpid()) || !strings.EqualFold(filepath.Clean(identity.Path), filepath.Clean(executable)) {
		t.Fatalf("identity = %+v; want PID %d, path %q", identity, os.Getpid(), executable)
	}
}

func TestLookupPacketProcessTCP(t *testing.T) {
	for _, test := range []struct{ network, address string }{{"tcp4", "127.0.0.1:0"}, {"tcp6", "[::1]:0"}} {
		t.Run(test.network, func(t *testing.T) {
			listener, err := net.Listen(test.network, test.address)
			if err != nil {
				if test.network == "tcp6" && (errors.Is(err, windows.WSAEAFNOSUPPORT) || errors.Is(err, windows.WSAEADDRNOTAVAIL)) {
					t.Skipf("IPv6 is unavailable: %v", err)
				}
				t.Fatal(err)
			}
			defer listener.Close()
			client, err := net.DialTimeout(test.network, listener.Addr().String(), 3*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			if err := listener.(*net.TCPListener).SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
				t.Fatal(err)
			}
			server, err := listener.Accept()
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			source, target := client.LocalAddr().(*net.TCPAddr).AddrPort(), client.RemoteAddr().(*net.TCPAddr).AddrPort()
			requireCurrentPacketProcess(t, ProtoTCP, source, target)
			// The accepted half uses the reverse tuple and has the same owner.
			requireCurrentPacketProcess(t, ProtoTCP, target, source)
		})
	}
}

func TestLookupPacketProcessUDP(t *testing.T) {
	for _, test := range []struct{ name, network, bind, source, target string }{
		{"ipv4", "udp4", "127.0.0.1:0", "127.0.0.1", "127.0.0.2:53"},
		{"ipv4_wildcard", "udp4", "0.0.0.0:0", "127.0.0.1", "127.0.0.2:53"},
		{"ipv6", "udp6", "[::1]:0", "::1", "[::1]:53"},
		{"ipv6_wildcard", "udp6", "[::]:0", "::1", "[::1]:53"},
		{"dual_stack_ipv4", "udp", "[::]:0", "127.0.0.1", "127.0.0.2:53"},
	} {
		t.Run(test.name, func(t *testing.T) {
			connection, err := net.ListenPacket(test.network, test.bind)
			if err != nil {
				if strings.Contains(test.bind, "[") && (errors.Is(err, windows.WSAEAFNOSUPPORT) || errors.Is(err, windows.WSAEADDRNOTAVAIL)) {
					t.Skipf("IPv6 is unavailable: %v", err)
				}
				t.Fatal(err)
			}
			defer connection.Close()
			source := netip.AddrPortFrom(netip.MustParseAddr(test.source), uint16(connection.LocalAddr().(*net.UDPAddr).Port))
			requireCurrentPacketProcess(t, ProtoUDP, source, netip.MustParseAddrPort(test.target))
		})
	}
}

func TestProcessImageChild(t *testing.T) {
	if os.Getenv("RELAYPROXY_PROCESS_IMAGE_CHILD") == "1" {
		_, _ = io.Copy(io.Discard, os.Stdin)
	}
}

func TestProcessImagePath(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, executable, "-test.run=^TestProcessImageChild$")
	child.Env = append(os.Environ(), "RELAYPROXY_PROCESS_IMAGE_CHILD=1")
	child.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	input, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		input.Close()
		t.Fatal(err)
	}
	defer func() {
		input.Close()
		if err := child.Wait(); err != nil {
			t.Errorf("child process: %v", err)
		}
	}()
	api, err := loadProcessWindowsAPI()
	if err != nil {
		t.Fatal(err)
	}
	path, err := api.processPath(uint32(child.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(path) {
		t.Fatalf("child image is not an absolute path: %q", path)
	}
	actualFile, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	expectedFile, err := os.Stat(executable)
	if err != nil {
		t.Fatal(err)
	}
	// QueryFullProcessImageName can resolve a junction that os.Executable
	// preserves, so compare file identity instead of path spelling.
	if !os.SameFile(actualFile, expectedFile) {
		t.Fatalf("child image = %q; want file %q", path, executable)
	}
}
