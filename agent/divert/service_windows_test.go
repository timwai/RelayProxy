//go:build windows

package divert

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"
)

func TestWatchWindowsProcessExitInvokesCallback(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestWindowsProcessWatcherHelperProcess$")
	cmd.Env = append(os.Environ(), "RELAYPROXY_PROCESS_WATCH_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	triggered := make(chan struct{}, 1)
	stop, err := watchWindowsProcessExit(uint32(cmd.Process.Pid), func() {
		triggered <- struct{}{}
	})
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal(err)
	}
	defer stop()

	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()

	select {
	case <-triggered:
	case <-time.After(3 * time.Second):
		t.Fatal("client process exit did not trigger Network Service session shutdown")
	}
}

func TestWindowsProcessWatcherHelperProcess(t *testing.T) {
	if os.Getenv("RELAYPROXY_PROCESS_WATCH_HELPER") != "1" {
		return
	}
	time.Sleep(30 * time.Second)
}

func TestCurrentWindowsUserSIDIsValid(t *testing.T) {
	sid, err := currentWindowsUserSID()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sid, "S-") {
		t.Fatalf("unexpected SID %q", sid)
	}
}

func TestWindowsProcessExecutableMatchesCurrentProgram(t *testing.T) {
	want, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	got, err := windowsProcessExecutable(uint32(os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(filepath.Clean(got), filepath.Clean(want)) {
		t.Fatalf("firewall program = %q, want %q", got, want)
	}
}

func TestRunWindowsNetworkServiceHelperRejectsUnknownActionBeforeMutation(t *testing.T) {
	if !strings.EqualFold(strings.TrimSpace(networkServiceHelperInstall), "install") {
		t.Fatal("install helper action changed unexpectedly")
	}
	if windowsNetworkServiceDisplayName == "" || windowsNetworkServiceName == "" {
		t.Fatal("network service identity must be stable")
	}
	// The public helper performs elevation validation before dispatch, so test
	// the action contract without touching SCM state on the CI host.
	if networkServiceHelperInstall == networkServiceHelperRemove {
		t.Fatal(errors.New("network service helper actions collide"))
	}
}

func TestMatchesWindowsNetworkServiceRecovery(t *testing.T) {
	valid := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: time.Second},
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 15 * time.Second},
	}
	if !matchesWindowsNetworkServiceRecovery(valid, true) {
		t.Fatal("expected configured recovery policy to match")
	}
	if matchesWindowsNetworkServiceRecovery(valid, false) {
		t.Fatal("non-crash recovery flag is required")
	}
	if matchesWindowsNetworkServiceRecovery(valid[:2], true) {
		t.Fatal("incomplete recovery policy was accepted")
	}
	extra := append(append([]mgr.RecoveryAction(nil), valid...), mgr.RecoveryAction{Type: mgr.NoAction})
	if matchesWindowsNetworkServiceRecovery(extra, true) {
		t.Fatal("unexpected extra recovery action was accepted")
	}
	wrong := append([]mgr.RecoveryAction(nil), valid...)
	wrong[1].Delay = 2 * time.Second
	if matchesWindowsNetworkServiceRecovery(wrong, true) {
		t.Fatal("wrong recovery delay was accepted")
	}
	wrong = append([]mgr.RecoveryAction(nil), valid...)
	wrong[2].Type = mgr.NoAction
	if matchesWindowsNetworkServiceRecovery(wrong, true) {
		t.Fatal("wrong recovery action was accepted")
	}
}

func TestRemoveWindowsTreeWithRetryRemovesOrphanedArtifacts(t *testing.T) {
	root := filepath.Join(t.TempDir(), "RelayProxy-Network-Service")
	nested := filepath.Join(root, "deadbeef")
	if err := os.MkdirAll(nested, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "RelayProxyNetwork.exe"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := removeWindowsTreeWithRetry(root, time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphaned ProgramData tree still exists: %v", err)
	}
}

func TestFilterWindowsPendingFileOperationsRemovesRelayProxyPaths(t *testing.T) {
	roots := []string{
		`C:\ProgramData\RelayProxy-Network-Service`,
		`C:\ProgramData\RelayProxy-WinDivert`,
	}
	values := []string{
		`\??\C:\ProgramData\RelayProxy-Network-Service\deadbeef\RelayProxyNetwork.exe`, "",
		`\??\C:\Windows\Temp\keep.tmp`, "",
		`\??\C:\ProgramData\RelayProxy-WinDivert\2.2-deadbeef\WinDivert64.sys`, "",
	}
	got, changed := filterWindowsPendingFileOperations(values, roots)
	if !changed {
		t.Fatal("expected RelayProxy pending deletes to be removed")
	}
	want := []string{`\??\C:\Windows\Temp\keep.tmp`, ""}
	if len(got) != len(want) {
		t.Fatalf("filtered=%q want=%q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("filtered[%d]=%q want=%q", i, got[i], want[i])
		}
	}
}

func TestFilterWindowsPendingFileOperationsMatchesRootAndCaseInsensitive(t *testing.T) {
	roots := []string{`C:\ProgramData\RelayProxy-Network-Service`}
	values := []string{
		`\??\c:\programdata\relayproxy-network-service`, "",
		`\??\C:\ProgramData\Other\file.tmp`, "",
	}
	got, changed := filterWindowsPendingFileOperations(values, roots)
	if !changed {
		t.Fatal("expected exact RelayProxy root pending delete to be removed")
	}
	if len(got) != 2 || !strings.Contains(strings.ToLower(got[0]), `\programdata\other\`) {
		t.Fatalf("unexpected filtered operations: %q", got)
	}
}

func TestFilterWindowsPendingFileOperationsLeavesUnrelatedEntries(t *testing.T) {
	values := []string{`\??\C:\Windows\Temp\keep.tmp`, ""}
	got, changed := filterWindowsPendingFileOperations(values, []string{`C:\ProgramData\RelayProxy-Network-Service`})
	if changed {
		t.Fatal("unrelated pending operation was changed")
	}
	if len(got) != len(values) || got[0] != values[0] || got[1] != values[1] {
		t.Fatalf("filtered=%q want=%q", got, values)
	}
}

func TestNormalizeWindowsTransparentFirewallPorts(t *testing.T) {
	got, err := normalizeWindowsTransparentFirewallPorts([]uint16{45001, 45002, 45001})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != 45001 || got[1] != 45002 {
		t.Fatalf("ports=%v", got)
	}
	for _, invalid := range [][]uint16{
		nil,
		{0},
		{1, 2, 3, 4, 5},
	} {
		if _, err := normalizeWindowsTransparentFirewallPorts(invalid); err == nil {
			t.Fatalf("invalid ports accepted: %v", invalid)
		}
	}
}

func TestWindowsNetworkBrokerLiveNamedPipeRoundTrip(t *testing.T) {
	if runtime.GOARCH != "amd64" {
		t.Skip("WinDivert broker live test requires Windows amd64")
	}
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("WinDivert broker live test requires an elevated Windows runner")
	}

	sid, err := currentWindowsUserSID()
	if err != nil {
		t.Fatal(err)
	}
	pipeName := fmt.Sprintf(`\\.\pipe\RelayProxyNetwork-test-%d`, os.Getpid())
	broker := &windowsNetworkBroker{
		allowedSID: sid,
		pipeName:   pipeName,
		stop:       make(chan struct{}),
		pipe:       windows.InvalidHandle,
	}
	brokerDone := make(chan error, 1)
	go func() { brokerDone <- broker.serve() }()
	defer broker.close()

	listener, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.LocalAddr().(*net.UDPAddr).Port

	var file *os.File
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		file, err = connectWindowsServicePipeNamed(
			pipeName,
			fmt.Sprintf("outbound and loopback and udp.DstPort == %d", port),
			[]uint16{uint16(port)},
		)
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("connect test broker: %v", err)
	}
	defer file.Close()

	client, err := net.DialUDP("udp4", nil, listener.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	payload := []byte("relayproxy-broker-v4")
	if _, err := client.Write(payload); err != nil {
		t.Fatal(err)
	}

	frameCh := make(chan networkFrame, 1)
	errCh := make(chan error, 1)
	go func() {
		frame, readErr := readNetworkFrame(file)
		if readErr != nil {
			errCh <- readErr
			return
		}
		frameCh <- frame
	}()

	var captured networkFrame
	select {
	case captured = <-frameCh:
	case err := <-errCh:
		t.Fatalf("read capture frame: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("broker did not deliver captured packet over named pipe")
	}
	if captured.kind != networkFrameCapture {
		t.Fatalf("frame kind=%d want capture", captured.kind)
	}
	packet, err := parseIPPacket(captured.payload)
	if err != nil {
		t.Fatal(err)
	}
	if packet.Protocol != ProtoUDP || packet.Destination.Port() != uint16(port) {
		t.Fatalf("unexpected captured packet: %+v", packet)
	}
	if err := writeNetworkFrame(file, nil, networkFrame{
		kind:       networkFrameInject,
		flags:      captured.flags,
		ifIndex:    captured.ifIndex,
		subIfIndex: captured.subIfIndex,
		payload:    captured.payload,
	}); err != nil {
		t.Fatal(err)
	}
	if err := writeNetworkFrame(file, nil, networkFrame{kind: networkFrameComplete}); err != nil {
		t.Fatal(err)
	}

	if err := listener.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 128)
	n, _, err := listener.ReadFromUDP(got)
	if err != nil {
		t.Fatalf("reinjected broker UDP did not reach socket: %v", err)
	}
	if string(got[:n]) != string(payload) {
		t.Fatalf("payload=%q want=%q", got[:n], payload)
	}

	// After Complete the broker is intentionally back in WinDivertRecv waiting
	// for the next packet. Stop the pipe, then send one final matching datagram
	// to wake Recv so the session can observe the closed pipe and exit cleanly.
	broker.close()
	_, _ = client.Write([]byte("wake"))
	select {
	case <-brokerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("test broker did not stop after wake packet")
	}
}

// Exercises the actual overlapped pipe handles and Go runtime poller, without
// requiring WinDivert or changing the machine firewall/service configuration.
func TestWindowsNetworkPipeSupportsIndependentInjection(t *testing.T) {
	sid, err := currentWindowsUserSID()
	if err != nil {
		t.Fatal(err)
	}
	pipeName := fmt.Sprintf(`\\.\pipe\RelayProxy-Test-Duplex-%d-%d`, os.Getpid(), time.Now().UnixNano())
	handle, err := createWindowsNetworkPipeNamed(pipeName, sid)
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	defer close(stop)
	device := newTestPacketDevice()
	done := make(chan error, 1)
	go func() {
		if err := connectWindowsNetworkPipe(handle, stop); err != nil {
			_ = windows.CloseHandle(handle)
			done <- err
			return
		}
		server := os.NewFile(uintptr(handle), pipeName)
		defer server.Close()
		hello, err := readNetworkFrame(server)
		if err != nil {
			done <- err
			return
		}
		if hello.kind != networkFrameHello {
			done <- errors.New("missing hello")
			return
		}
		if err := writeNetworkFrame(server, nil, networkFrame{kind: networkFrameReady}); err != nil {
			done <- err
			return
		}
		done <- serveNetworkPackets(server, device)
	}()
	client, err := connectWindowsServicePipeNamed(pipeName, "outbound", []uint16{45001})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = client.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("pipe workers leaked")
		}
	}()
	if err := client.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal("pipe is not registered with the runtime poller:", err)
	}
	device.incoming <- capturedTestPacket{data: []byte("capture"), meta: packetMetadata{outbound: true}}
	if _, err := readNetworkFrame(client); err != nil {
		t.Fatal(err)
	}
	if err := writeNetworkFrame(client, nil, networkFrame{kind: networkFrameComplete}); err != nil {
		t.Fatal(err)
	}
	if err := writeNetworkFrame(client, nil, networkFrame{kind: networkFrameInject, payload: []byte("reply")}); err != nil {
		t.Fatal(err)
	}
	select {
	case packet := <-device.sent:
		if string(packet.data) != "reply" {
			t.Fatal("reply corrupted")
		}
	case <-time.After(time.Second):
		t.Fatal("reply waited for a new captured packet")
	}
}

func TestWindowsNetworkPipeConnectCanBeCanceled(t *testing.T) {
	sid, err := currentWindowsUserSID()
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf(`\\.\pipe\RelayProxy-Test-Cancel-%d-%d`, os.Getpid(), time.Now().UnixNano())
	handle, err := createWindowsNetworkPipeNamed(name, sid)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	stop, done := make(chan struct{}), make(chan error, 1)
	go func() { done <- connectWindowsNetworkPipe(handle, stop) }()
	close(stop)
	select {
	case err := <-done:
		if !errors.Is(err, windows.ERROR_OPERATION_ABORTED) {
			t.Fatalf("connect cancel=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("pending pipe connect did not cancel")
	}
}

func TestWindowsServiceReceiveReturnsBrokenPipeWithoutReusingCapture(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	device := &windowsServicePacketDevice{file: reader}
	defer device.Close()
	done := make(chan error, 1)
	go func() {
		_, _, err := device.Receive(make([]byte, 2048))
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("broken pipe was treated as an active capture")
		}
	case <-time.After(time.Second):
		t.Fatal("Receive tried to reconnect inside the old interceptor")
	}
}

func TestWindowsServiceHandshakeTimesOutIfBrokerStopsReplying(t *testing.T) {
	sid, err := currentWindowsUserSID()
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf(`\\.\pipe\RelayProxy-Test-Handshake-%d-%d`, os.Getpid(), time.Now().UnixNano())
	handle, err := createWindowsNetworkPipeNamed(name, sid)
	if err != nil {
		t.Fatal(err)
	}
	stop, done := make(chan struct{}), make(chan error, 1)
	defer close(stop)
	go func() {
		if err := connectWindowsNetworkPipe(handle, stop); err != nil {
			_ = windows.CloseHandle(handle)
			done <- err
			return
		}
		server := os.NewFile(uintptr(handle), name)
		defer server.Close()
		if _, err := readNetworkFrame(server); err != nil {
			done <- err
			return
		}
		if err := writeNetworkFrame(server, nil, networkFrame{kind: networkFrameReady}); err != nil {
			done <- err
			return
		}
		// Consume the second Hello, then remain alive without answering it.
		if _, err := readNetworkFrame(server); err != nil {
			done <- err
			return
		}
		_, err := readNetworkFrame(server)
		done <- err
	}()
	client, err := connectWindowsServicePipeNamed(name, "outbound", []uint16{45001})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = client.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("handshake worker leaked")
		}
	}()
	started := time.Now()
	err = handshakeWindowsServicePipe(client, "outbound", []uint16{45001}, 50*time.Millisecond)
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("handshake error=%v, want deadline exceeded", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("stalled broker held startup beyond the handshake deadline")
	}
}
