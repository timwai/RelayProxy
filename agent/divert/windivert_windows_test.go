//go:build windows

package divert

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestWinDivertAddressABI(t *testing.T) {
	var address windivertAddress
	if unsafe.Sizeof(address) != 80 || unsafe.Offsetof(address.Data) != 16 || unsafe.Offsetof(address.Flags) != 8 {
		t.Fatal("WinDivert 2.2 address ABI mismatch")
	}
	address.setOutbound(true)
	address.setIfIndex(0x12345678, 0x99887766)
	address.setChecksums(true)
	if !address.outbound() || address.Flags != 0xf20000 || address.ifIndex() != 0x12345678 || address.subIfIndex() != 0x99887766 {
		t.Fatalf("invalid address metadata: %+v", address)
	}
	address.setOutbound(false)
	address.setChecksums(false)
	if address.outbound() || address.Flags != 0xe00000 || binary.LittleEndian.Uint32(address.Data[:4]) != 0x12345678 {
		t.Fatal("inbound IPv4 metadata corrupted")
	}
}

func TestWinDivertRequiresCompleteFilesBesideExecutable(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "relay-agent.exe")
	if _, err := winDivertFiles(executable); err == nil {
		t.Fatal("missing dependency accepted")
	}
	if _, err := winDivertFiles("relay-agent.exe"); err == nil {
		t.Fatal("relative executable accepted")
	}
	dependency := filepath.Join(dir, "windivert")
	if err := os.Mkdir(dependency, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dependency, "WinDivert.dll"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := winDivertFiles(executable); err == nil {
		t.Fatal("DLL without matching driver accepted")
	}
	if err := os.WriteFile(filepath.Join(dependency, "WinDivert64.sys"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := winDivertFiles(executable)
	if err != nil || got != filepath.Join(dependency, "WinDivert.dll") {
		t.Fatalf("trusted subdirectory not found: %s %v", got, err)
	}
}

func TestWinDivertClosedHandleCannotCallDLL(t *testing.T) {
	h := &windivertHandle{}
	h.closed.Store(true)
	if _, _, err := h.Recv(make([]byte, 40)); err == nil {
		t.Fatal("closed receive accepted")
	}
	if err := h.Send([]byte{0x45}, windivertAddress{}); err == nil {
		t.Fatal("closed send accepted")
	}
	if err := h.Shutdown(); err != nil {
		t.Fatal(err)
	}
}

// This check calls only the official DLL's pure filter helpers.
// It does not open a WinDivert handle and does not require administrator rights.
func TestWinDivertNativeFilter(t *testing.T) {
	path := os.Getenv("RELAYPROXY_WINDIVERT_DLL")
	if path == "" {
		if runtime.GOARCH != "amd64" {
			t.Skip("embedded native runtime requires Windows amd64")
		}
		files, err := bundledWinDivertFiles()
		if err != nil {
			t.Fatal(err)
		}
		directory := t.TempDir()
		if err := materializeWinDivert(directory, files); err != nil {
			t.Fatal(err)
		}
		path = filepath.Join(directory, "WinDivert.dll")
	}
	if !filepath.IsAbs(path) {
		t.Fatal("native DLL test requires an absolute path")
	}
	dll, err := windows.LoadLibraryEx(path, 0, windows.LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR|windows.LOAD_LIBRARY_SEARCH_SYSTEM32)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.FreeLibrary(dll)
	compile, err := windows.GetProcAddress(dll, "WinDivertHelperCompileFilter")
	if err != nil {
		t.Fatal(err)
	}
	evaluate, err := windows.GetProcAddress(dll, "WinDivertHelperEvalFilter")
	if err != nil {
		t.Fatal(err)
	}
	filter, err := syscall.BytePtrFromString(windowsInterceptFilter(45001, 45002, LoopGuard{
		RelayIPs: []string{"203.0.113.9", "2001:db8:ffff::9"}, RelayPorts: []int{443},
	}))
	if err != nil {
		t.Fatal(err)
	}
	var position uint32
	ok, _, callErr := syscall.SyscallN(compile, uintptr(unsafe.Pointer(filter)), 0, 0, 0, 0, uintptr(unsafe.Pointer(&position)))
	if ok == 0 {
		t.Fatalf("native filter rejected at byte %d: %v", position, callErr)
	}
	for _, ipv6 := range []bool{false, true} {
		for _, protocol := range []Protocol{ProtoTCP, ProtoUDP} {
			data, _ := packetTestFixture(ipv6, protocol, nil, false)
			address := windivertAddress{}
			address.setOutbound(true)
			address.setChecksums(ipv6)
			got, _, _ := syscall.SyscallN(evaluate, uintptr(unsafe.Pointer(filter)), uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)), uintptr(unsafe.Pointer(&address)))
			if got == 0 {
				t.Fatalf("native filter skipped %s ipv6=%v", protocol, ipv6)
			}

			inbound := windivertAddress{}
			inbound.setOutbound(false)
			inbound.setChecksums(ipv6)
			got, _, _ = syscall.SyscallN(evaluate, uintptr(unsafe.Pointer(filter)), uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)), uintptr(unsafe.Pointer(&inbound)))
			if got != 0 {
				t.Fatalf("native filter intercepted ordinary inbound %s ipv6=%v", protocol, ipv6)
			}
			if protocol == ProtoUDP {
				dnsData := append([]byte(nil), data...)
				_, dnsOffset := packetTestFixture(ipv6, ProtoUDP, nil, false)
				binary.BigEndian.PutUint16(dnsData[dnsOffset:dnsOffset+2], 53)
				got, _, _ = syscall.SyscallN(evaluate, uintptr(unsafe.Pointer(filter)), uintptr(unsafe.Pointer(&dnsData[0])), uintptr(len(dnsData)), uintptr(unsafe.Pointer(&inbound)))
				if got == 0 {
					t.Fatalf("native filter skipped inbound DNS response ipv6=%v", ipv6)
				}
				runtime.KeepAlive(dnsData)
			}

			address.Flags |= 1 << 18
			got, _, _ = syscall.SyscallN(evaluate, uintptr(unsafe.Pointer(filter)), uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)), uintptr(unsafe.Pointer(&address)))
			if got != 0 {
				t.Fatal("native filter intercepted local loopback traffic")
			}
			runtime.KeepAlive(data)
		}
	}
	relayFilter, err := syscall.BytePtrFromString(windowsInterceptFilter(45001, 45002, LoopGuard{
		RelayIPs: []string{"198.51.100.20", "2001:db8:2::20"}, RelayPorts: []int{443},
	}))
	if err != nil {
		t.Fatal(err)
	}
	position = 0
	ok, _, callErr = syscall.SyscallN(compile, uintptr(unsafe.Pointer(relayFilter)), 0, 0, 0, 0, uintptr(unsafe.Pointer(&position)))
	if ok == 0 {
		t.Fatalf("native relay-bypass filter rejected at byte %d: %v", position, callErr)
	}
	for _, ipv6 := range []bool{false, true} {
		for _, protocol := range []Protocol{ProtoTCP, ProtoUDP} {
			data, _ := packetTestFixture(ipv6, protocol, nil, false)
			address := windivertAddress{}
			address.setOutbound(true)
			address.setChecksums(ipv6)
			got, _, _ := syscall.SyscallN(evaluate, uintptr(unsafe.Pointer(relayFilter)), uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)), uintptr(unsafe.Pointer(&address)))
			if got != 0 {
				t.Fatalf("native filter intercepted Relay %s ipv6=%v", protocol, ipv6)
			}
			runtime.KeepAlive(data)
		}
	}
	runtime.KeepAlive(relayFilter)
	runtime.KeepAlive(filter)
}

func TestWinDivertLiveBrokerStyleReinjection(t *testing.T) {
	if runtime.GOARCH != "amd64" {
		t.Skip("WinDivert live test requires Windows amd64")
	}
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("WinDivert live test requires an elevated Windows runner")
	}

	listener, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.LocalAddr().(*net.UDPAddr).Port

	handle, err := openTrustedWinDivert(fmt.Sprintf("outbound and loopback and udp.DstPort == %d", port))
	if err != nil {
		t.Skipf("WinDivert driver unavailable on runner: %v", err)
	}
	defer handle.Close()

	pump := make(chan error, 1)
	go func() {
		buffer := make([]byte, 40+65535)
		n, captured, err := handle.Recv(buffer)
		if err != nil {
			pump <- err
			return
		}
		packet, err := parseIPPacket(buffer[:n])
		if err != nil {
			pump <- err
			return
		}
		repairPacketChecksums(packet)

		// Mirror the SYSTEM broker: do not reuse capture-only state. Rebuild
		// only direction and interface metadata and let windivertHandle.Send
		// calculate native checksums before injection.
		var inject windivertAddress
		inject.setOutbound(captured.outbound())
		inject.setIfIndex(captured.ifIndex(), captured.subIfIndex())
		pump <- handle.Send(buffer[:n], inject)
	}()

	client, err := net.DialUDP("udp4", nil, listener.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	payload := []byte("relayproxy-windivert-live")
	if _, err := client.Write(payload); err != nil {
		t.Fatal(err)
	}

	if err := listener.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 128)
	n, _, err := listener.ReadFromUDP(got)
	if err != nil {
		t.Fatalf("reinjected UDP did not reach Windows socket: %v", err)
	}
	if string(got[:n]) != string(payload) {
		t.Fatalf("payload=%q want=%q", got[:n], payload)
	}
	select {
	case err := <-pump:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WinDivert reinjection pump did not finish")
	}
}

func TestWinDivertLiveTCPReflection(t *testing.T) {
	if runtime.GOARCH != "amd64" {
		t.Skip("WinDivert live TCP test requires Windows amd64")
	}
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("WinDivert live TCP test requires an elevated Windows runner")
	}

	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4zero})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	proxyPort := uint16(listener.Addr().(*net.TCPAddr).Port)
	fakeIP := netip.MustParseAddr("198.51.100.20")
	const fakePort uint16 = 443

	filter := fmt.Sprintf(
		"outbound and tcp and ((ip.DstAddr == %s and tcp.DstPort == %d) or tcp.SrcPort == %d)",
		fakeIP, fakePort, proxyPort,
	)
	handle, err := openTrustedWinDivert(filter)
	if err != nil {
		t.Skipf("WinDivert driver unavailable on runner: %v", err)
	}
	defer handle.Close()

	stop := make(chan struct{})
	pumpDone := make(chan error, 1)
	go func() {
		buffer := make([]byte, 40+65535)
		var originalSource netip.AddrPort
		for {
			n, address, err := handle.Recv(buffer)
			if err != nil {
				select {
				case <-stop:
					pumpDone <- nil
				default:
					pumpDone <- err
				}
				return
			}
			packet, err := parseIPPacket(buffer[:n])
			if err != nil {
				pumpDone <- err
				return
			}
			switch {
			case packet.Destination.Addr() == fakeIP && packet.Destination.Port() == fakePort:
				if !originalSource.IsValid() {
					originalSource = packet.Source
				}
				if err := rewriteIPPacket(
					buffer[:n],
					netip.AddrPortFrom(fakeIP, packet.Source.Port()),
					netip.AddrPortFrom(packet.Source.Addr(), proxyPort),
				); err != nil {
					pumpDone <- err
					return
				}
				address.setOutbound(false)
			case packet.Source.Port() == proxyPort:
				if !originalSource.IsValid() {
					pumpDone <- fmt.Errorf("proxy response arrived before original TCP source was learned")
					return
				}
				if err := rewriteIPPacket(
					buffer[:n],
					netip.AddrPortFrom(fakeIP, fakePort),
					originalSource,
				); err != nil {
					pumpDone <- err
					return
				}
				address.setOutbound(false)
			default:
				pumpDone <- fmt.Errorf("unexpected live TCP packet %s -> %s", packet.Source, packet.Destination)
				return
			}
			if err := handle.Send(buffer[:n], address); err != nil {
				pumpDone <- err
				return
			}
		}
	}()

	type dialResult struct {
		conn net.Conn
		err  error
	}
	dialDone := make(chan dialResult, 1)
	go func() {
		conn, err := net.DialTimeout("tcp4", net.JoinHostPort(fakeIP.String(), "443"), 5*time.Second)
		dialDone <- dialResult{conn: conn, err: err}
	}()
	acceptDone := make(chan dialResult, 1)
	go func() {
		conn, err := listener.Accept()
		acceptDone <- dialResult{conn: conn, err: err}
	}()

	var client, proxy net.Conn
	select {
	case result := <-dialDone:
		if result.err != nil {
			close(stop)
			_ = handle.Shutdown()
			t.Fatalf("reflected client dial failed: %v", result.err)
		}
		client = result.conn
	case <-time.After(7 * time.Second):
		close(stop)
		_ = handle.Shutdown()
		t.Fatal("reflected client dial timed out")
	}
	defer client.Close()

	select {
	case result := <-acceptDone:
		if result.err != nil {
			close(stop)
			_ = handle.Shutdown()
			t.Fatalf("transparent listener accept failed: %v", result.err)
		}
		proxy = result.conn
	case <-time.After(7 * time.Second):
		close(stop)
		_ = handle.Shutdown()
		t.Fatal("transparent listener did not receive reflected TCP connection")
	}
	defer proxy.Close()
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	_ = proxy.SetDeadline(time.Now().Add(5 * time.Second))

	if _, err := client.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	request := make([]byte, 4)
	if _, err := io.ReadFull(proxy, request); err != nil {
		t.Fatalf("proxy did not receive reflected TCP payload: %v", err)
	}
	if string(request) != "ping" {
		t.Fatalf("request=%q", request)
	}
	if _, err := proxy.Write([]byte("pong")); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 4)
	if _, err := io.ReadFull(client, reply); err != nil {
		t.Fatalf("client did not receive reflected TCP reply: %v", err)
	}
	if string(reply) != "pong" {
		t.Fatalf("reply=%q", reply)
	}

	close(stop)
	_ = handle.Shutdown()
	select {
	case err := <-pumpDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("live TCP reflection pump did not stop")
	}
}
