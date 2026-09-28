//go:build windows

package divert

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const (
	windowsNetworkServiceName          = "RelayProxyNetwork"
	windowsNetworkServiceDisplayName   = "RelayProxy Network Service"
	windowsNetworkPipeName             = `\\.\pipe\RelayProxyNetwork-v4`
	windowsTransparentFirewallRuleName = "RelayProxy Transparent Proxy"

	networkServiceModeFlagName   = "relayproxy-network-service"
	networkServiceSIDFlagName    = "relayproxy-network-service-sid"
	networkServiceHelperFlagName = "relayproxy-network-service-helper"

	networkServiceHelperInstall = "install"
	networkServiceHelperRemove  = "remove"

	networkPipeMagic   = 0x31504e52 // "RNP1" little-endian
	networkPipeVersion = 4

	networkFrameHello   = 1
	networkFrameReady   = 2
	networkFrameError   = 3
	networkFrameCapture  = 4
	networkFrameInject   = 5
	networkFrameComplete = 6

	networkFrameFlagOutbound = 1 << 17
	networkFrameHeaderBytes  = 24
	networkFrameMaxPayload   = 1 << 20

	pipeAccessDuplex        = 0x00000003
	pipeRejectRemoteClients = 0x00000008

	shellExecuteNoCloseProcess = 0x00000040
	shellExecuteNoAsync        = 0x00000100
	shellShowHidden            = 0
)

type networkFrame struct {
	kind       uint16
	flags      uint32
	ifIndex    uint32
	subIfIndex uint32
	payload    []byte
}

type networkHello struct {
	Filter   string   `json:"filter"`
	TCPPorts []uint16 `json:"tcpPorts"`
}

type windowsServicePacketDevice struct {
	filter      string
	tcpPorts    []uint16
	fileMu      sync.RWMutex
	reconnectMu sync.Mutex
	file        *os.File
	writeMu     sync.Mutex
	closed      atomic.Bool
	closeOnce   sync.Once
	closeErr    error
}

type windowsNetworkServiceHandler struct {
	allowedSID string
}

type windowsNetworkBroker struct {
	allowedSID string
	pipeName   string
	stopOnce   sync.Once
	stop       chan struct{}
	mu         sync.Mutex
	pipe       windows.Handle
}

type networkShellExecuteInfo struct {
	Size       uint32
	Mask       uint32
	Window     uintptr
	Verb       *uint16
	File       *uint16
	Parameters *uint16
	Directory  *uint16
	Show       int32
	Instance   uintptr
	IDList     uintptr
	Class      *uint16
	ClassKey   windows.Handle
	HotKey     uint32
	Icon       windows.Handle
	Process    windows.Handle
}

var (
	networkShell32      = windows.NewLazySystemDLL("shell32.dll")
	networkShellExecute = networkShell32.NewProc("ShellExecuteExW")
)

func NetworkServiceModeFlagName() string   { return networkServiceModeFlagName }
func NetworkServiceSIDFlagName() string    { return networkServiceSIDFlagName }
func NetworkServiceHelperFlagName() string { return networkServiceHelperFlagName }

func RunWindowsNetworkService(allowedSID string) error {
	allowedSID = strings.TrimSpace(allowedSID)
	if allowedSID == "" {
		return errors.New("RelayProxy Network Service 缺少允许连接的用户 SID")
	}
	if _, err := windows.StringToSid(allowedSID); err != nil {
		return fmt.Errorf("RelayProxy Network Service 用户 SID 无效: %w", err)
	}
	return svc.Run(windowsNetworkServiceName, &windowsNetworkServiceHandler{allowedSID: allowedSID})
}

func (h *windowsNetworkServiceHandler) Execute(_ []string, requests <-chan svc.ChangeRequest, statuses chan<- svc.Status) (bool, uint32) {
	statuses <- svc.Status{State: svc.StartPending}
	broker := &windowsNetworkBroker{allowedSID: h.allowedSID, stop: make(chan struct{}), pipe: windows.InvalidHandle}
	brokerDone := make(chan error, 1)
	go func() { brokerDone <- broker.serve() }()

	statuses <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case err := <-brokerDone:
			if err != nil {
				return true, 1
			}
			return false, 0
		case request := <-requests:
			switch request.Cmd {
			case svc.Interrogate:
				statuses <- request.CurrentStatus
			case svc.Stop, svc.Shutdown:
				statuses <- svc.Status{State: svc.StopPending}
				broker.close()
				select {
				case <-brokerDone:
				case <-time.After(5 * time.Second):
				}
				return false, 0
			}
		}
	}
}

func (b *windowsNetworkBroker) networkPipeName() string {
	if b != nil && strings.TrimSpace(b.pipeName) != "" {
		return b.pipeName
	}
	return windowsNetworkPipeName
}

func (b *windowsNetworkBroker) serve() error {
	for {
		select {
		case <-b.stop:
			return nil
		default:
		}

		pipe, err := createWindowsNetworkPipeNamed(b.networkPipeName(), b.allowedSID)
		if err != nil {
			select {
			case <-b.stop:
				return nil
			default:
				return err
			}
		}
		b.setPipe(pipe)
		err = windows.ConnectNamedPipe(pipe, nil)
		if err != nil && !errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
			b.clearPipe(pipe)
			_ = windows.CloseHandle(pipe)
			select {
			case <-b.stop:
				return nil
			default:
				return fmt.Errorf("RelayProxy Network Service 接受命名管道失败: %w", err)
			}
		}

		file := os.NewFile(uintptr(pipe), b.networkPipeName())
		if file == nil {
			b.clearPipe(pipe)
			_ = windows.CloseHandle(pipe)
			return errors.New("RelayProxy Network Service 无法包装命名管道句柄")
		}
		err = serveWindowsNetworkSession(file)
		b.clearPipe(pipe)
		_ = file.Close()
		if err != nil {
			select {
			case <-b.stop:
				return nil
			default:
				// A client disconnect or malformed request must not terminate the
				// machine-wide service. The next Agent can connect immediately.
				continue
			}
		}
	}
}

func (b *windowsNetworkBroker) setPipe(pipe windows.Handle) {
	b.mu.Lock()
	b.pipe = pipe
	b.mu.Unlock()
}

func (b *windowsNetworkBroker) clearPipe(pipe windows.Handle) {
	b.mu.Lock()
	if b.pipe == pipe {
		b.pipe = windows.InvalidHandle
	}
	b.mu.Unlock()
}

func (b *windowsNetworkBroker) close() {
	b.stopOnce.Do(func() {
		close(b.stop)
		b.mu.Lock()
		pipe := b.pipe
		b.pipe = windows.InvalidHandle
		b.mu.Unlock()
		if pipe != windows.InvalidHandle {
			_ = windows.CloseHandle(pipe)
		}
	})
}

func createWindowsNetworkPipe(allowedSID string) (windows.Handle, error) {
	return createWindowsNetworkPipeNamed(windowsNetworkPipeName, allowedSID)
}

func createWindowsNetworkPipeNamed(pipeName, allowedSID string) (windows.Handle, error) {
	sddl := fmt.Sprintf("D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GRGW;;;%s)", allowedSID)
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return windows.InvalidHandle, fmt.Errorf("创建 Network Service 管道 ACL 失败: %w", err)
	}
	name, err := windows.UTF16PtrFromString(pipeName)
	if err != nil {
		return windows.InvalidHandle, err
	}
	attributes := windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}
	pipe, err := windows.CreateNamedPipe(
		name,
		pipeAccessDuplex,
		pipeRejectRemoteClients,
		1,
		networkFrameMaxPayload,
		networkFrameMaxPayload,
		0,
		&attributes,
	)
	if err != nil {
		return windows.InvalidHandle, err
	}
	return pipe, nil
}

func serveWindowsNetworkSession(file *os.File) error {
	hello, err := readNetworkFrame(file)
	if err != nil {
		return err
	}
	if hello.kind != networkFrameHello {
		_ = writeNetworkFrame(file, nil, networkFrame{kind: networkFrameError, payload: []byte("expected hello frame")})
		return errors.New("RelayProxy Network Service 收到无效握手")
	}
	var request networkHello
	if err := json.Unmarshal(hello.payload, &request); err != nil {
		message := "invalid Network Service hello payload"
		_ = writeNetworkFrame(file, nil, networkFrame{kind: networkFrameError, payload: []byte(message)})
		return fmt.Errorf("%s: %w", message, err)
	}
	request.Filter = strings.TrimSpace(request.Filter)
	if request.Filter == "" {
		_ = writeNetworkFrame(file, nil, networkFrame{kind: networkFrameError, payload: []byte("empty WinDivert filter")})
		return errors.New("RelayProxy Network Service 收到空 WinDivert 过滤器")
	}
	ports, err := normalizeWindowsTransparentFirewallPorts(request.TCPPorts)
	if err != nil {
		_ = writeNetworkFrame(file, nil, networkFrame{kind: networkFrameError, payload: []byte(err.Error())})
		return err
	}
	if err := installWindowsTransparentFirewallRule(ports); err != nil {
		message := "配置透明代理 Windows Firewall 入站规则失败: " + err.Error()
		_ = writeNetworkFrame(file, nil, networkFrame{kind: networkFrameError, payload: []byte(message)})
		return errors.New(message)
	}
	defer removeWindowsTransparentFirewallRule()

	handle, err := openTrustedWinDivert(request.Filter)
	if err != nil {
		_ = writeNetworkFrame(file, nil, networkFrame{kind: networkFrameError, payload: []byte(err.Error())})
		return err
	}
	defer handle.Close()

	var writeMu sync.Mutex
	if err := writeNetworkFrame(file, &writeMu, networkFrame{kind: networkFrameReady}); err != nil {
		return err
	}

	// This pipe is intentionally synchronous. Never block on ReadFile while a
	// second goroutine tries to WriteFile on the same handle: that can deadlock
	// a full-duplex named pipe and blackhole every packet already diverted from
	// the host. Process one capture transaction at a time instead:
	//   Recv -> Capture -> zero/more Inject -> Complete -> next Recv.
	buffer := make([]byte, 40+65535)
	for {
		n, captured, err := handle.Recv(buffer)
		if err != nil {
			return err
		}
		flags := uint32(0)
		if captured.outbound() {
			flags |= networkFrameFlagOutbound
		}
		if err := writeNetworkFrame(file, &writeMu, networkFrame{
			kind:       networkFrameCapture,
			flags:      flags,
			ifIndex:    captured.ifIndex(),
			subIfIndex: captured.subIfIndex(),
			payload:    append([]byte(nil), buffer[:n]...),
		}); err != nil {
			return err
		}

		for {
			frame, err := readNetworkFrame(file)
			if err != nil {
				return err
			}
			switch frame.kind {
			case networkFrameInject:
				var addr windivertAddress
				addr.setOutbound(frame.flags&networkFrameFlagOutbound != 0)
				addr.setIfIndex(frame.ifIndex, frame.subIfIndex)
				if err := handle.Send(frame.payload, addr); err != nil {
					return err
				}
			case networkFrameComplete:
				goto nextCapture
			default:
				return fmt.Errorf("RelayProxy Network Service 收到未知事务帧 %d", frame.kind)
			}
		}
	nextCapture:
	}
}

func normalizeWindowsTransparentFirewallPorts(input []uint16) ([]uint16, error) {
	if len(input) == 0 || len(input) > 4 {
		return nil, errors.New("透明代理 Windows Firewall 端口数量无效")
	}
	seen := make(map[uint16]struct{}, len(input))
	ports := make([]uint16, 0, len(input))
	for _, port := range input {
		if port == 0 {
			return nil, errors.New("透明代理 Windows Firewall 端口不能为 0")
		}
		if _, ok := seen[port]; ok {
			continue
		}
		seen[port] = struct{}{}
		ports = append(ports, port)
	}
	if len(ports) == 0 {
		return nil, errors.New("透明代理 Windows Firewall 端口为空")
	}
	return ports, nil
}

func windowsNetshPath() (string, error) {
	systemRoot := strings.TrimSpace(os.Getenv("SystemRoot"))
	if systemRoot == "" {
		return "", errors.New("SystemRoot 环境变量为空")
	}
	path := filepath.Join(systemRoot, "System32", "netsh.exe")
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("netsh.exe 不是普通文件: %s", path)
	}
	return path, nil
}

func runWindowsNetsh(args ...string) error {
	path, err := windowsNetshPath()
	if err != nil {
		return err
	}
	command := exec.Command(path, args...)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	output, err := command.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			return err
		}
		return fmt.Errorf("%w: %s", err, message)
	}
	return nil
}

func removeWindowsTransparentFirewallRule() error {
	return runWindowsNetsh(
		"advfirewall", "firewall", "delete", "rule",
		"name="+windowsTransparentFirewallRuleName,
	)
}

func installWindowsTransparentFirewallRule(input []uint16) error {
	ports, err := normalizeWindowsTransparentFirewallPorts(input)
	if err != nil {
		return err
	}
	// A single broker session is supported. Delete a stale rule left by an
	// unclean service termination before publishing the current random ports.
	_ = removeWindowsTransparentFirewallRule()
	values := make([]string, 0, len(ports))
	for _, port := range ports {
		values = append(values, fmt.Sprintf("%d", port))
	}
	return runWindowsNetsh(
		"advfirewall", "firewall", "add", "rule",
		"name="+windowsTransparentFirewallRuleName,
		"dir=in", "action=allow", "protocol=TCP",
		"localport="+strings.Join(values, ","),
		"profile=any", "edge=no",
	)
}

func writeNetworkFrame(writer io.Writer, mu *sync.Mutex, frame networkFrame) error {
	if len(frame.payload) > networkFrameMaxPayload {
		return fmt.Errorf("RelayProxy Network Service frame payload too large: %d", len(frame.payload))
	}
	if mu != nil {
		mu.Lock()
		defer mu.Unlock()
	}
	header := make([]byte, networkFrameHeaderBytes)
	binary.LittleEndian.PutUint32(header[0:4], networkPipeMagic)
	binary.LittleEndian.PutUint16(header[4:6], networkPipeVersion)
	binary.LittleEndian.PutUint16(header[6:8], frame.kind)
	binary.LittleEndian.PutUint32(header[8:12], frame.flags)
	binary.LittleEndian.PutUint32(header[12:16], frame.ifIndex)
	binary.LittleEndian.PutUint32(header[16:20], frame.subIfIndex)
	binary.LittleEndian.PutUint32(header[20:24], uint32(len(frame.payload)))
	if err := writeNetworkBytes(writer, header); err != nil {
		return err
	}
	return writeNetworkBytes(writer, frame.payload)
}

func writeNetworkBytes(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := writer.Write(data)
		if err != nil {
			return err
		}
		if n <= 0 || n > len(data) {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func readNetworkFrame(reader io.Reader) (networkFrame, error) {
	header := make([]byte, networkFrameHeaderBytes)
	if _, err := io.ReadFull(reader, header); err != nil {
		return networkFrame{}, err
	}
	if binary.LittleEndian.Uint32(header[0:4]) != networkPipeMagic {
		return networkFrame{}, errors.New("RelayProxy Network Service frame magic mismatch")
	}
	if binary.LittleEndian.Uint16(header[4:6]) != networkPipeVersion {
		return networkFrame{}, errors.New("RelayProxy Network Service protocol version mismatch")
	}
	length := binary.LittleEndian.Uint32(header[20:24])
	if length > networkFrameMaxPayload {
		return networkFrame{}, fmt.Errorf("RelayProxy Network Service frame payload exceeds limit: %d", length)
	}
	frame := networkFrame{
		kind:       binary.LittleEndian.Uint16(header[6:8]),
		flags:      binary.LittleEndian.Uint32(header[8:12]),
		ifIndex:    binary.LittleEndian.Uint32(header[12:16]),
		subIfIndex: binary.LittleEndian.Uint32(header[16:20]),
	}
	if length != 0 {
		frame.payload = make([]byte, int(length))
		if _, err := io.ReadFull(reader, frame.payload); err != nil {
			return networkFrame{}, err
		}
	}
	return frame, nil
}

func connectWindowsServicePipe(filter string, tcpPorts []uint16) (*os.File, error) {
	return connectWindowsServicePipeNamed(windowsNetworkPipeName, filter, tcpPorts)
}

func connectWindowsServicePipeNamed(pipeName, filter string, tcpPorts []uint16) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(pipeName)
	if err != nil {
		return nil, err
	}
	var handle windows.Handle
	var lastErr error
	for attempt := 0; attempt < 100; attempt++ {
		handle, lastErr = windows.CreateFile(
			name,
			windows.GENERIC_READ|windows.GENERIC_WRITE,
			0,
			nil,
			windows.OPEN_EXISTING,
			windows.FILE_ATTRIBUTE_NORMAL,
			0,
		)
		if lastErr == nil {
			break
		}
		if !errors.Is(lastErr, windows.ERROR_PIPE_BUSY) && !errors.Is(lastErr, windows.ERROR_FILE_NOT_FOUND) {
			return nil, lastErr
		}
		time.Sleep(100 * time.Millisecond)
	}
	if lastErr != nil {
		return nil, lastErr
	}
	file := os.NewFile(uintptr(handle), pipeName)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, errors.New("无法打开 RelayProxy Network Service 管道")
	}
	helloPayload, err := json.Marshal(networkHello{Filter: filter, TCPPorts: tcpPorts})
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := writeNetworkFrame(file, nil, networkFrame{kind: networkFrameHello, payload: helloPayload}); err != nil {
		_ = file.Close()
		return nil, err
	}
	reply, err := readNetworkFrame(file)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	switch reply.kind {
	case networkFrameReady:
		return file, nil
	case networkFrameError:
		_ = file.Close()
		return nil, errors.New(strings.TrimSpace(string(reply.payload)))
	default:
		_ = file.Close()
		return nil, fmt.Errorf("RelayProxy Network Service 返回未知握手帧 %d", reply.kind)
	}
}

func openWindowsServicePacketDevice(filter string, tcpPorts []uint16) (*windowsServicePacketDevice, error) {
	ports := append([]uint16(nil), tcpPorts...)
	file, err := connectWindowsServicePipe(filter, ports)
	if err != nil {
		return nil, err
	}
	return &windowsServicePacketDevice{filter: filter, tcpPorts: ports, file: file}, nil
}

func (d *windowsServicePacketDevice) currentFile() *os.File {
	if d == nil {
		return nil
	}
	d.fileMu.RLock()
	file := d.file
	d.fileMu.RUnlock()
	return file
}

func (d *windowsServicePacketDevice) reconnect(expected *os.File) error {
	if d == nil || d.closed.Load() {
		return netClosedError()
	}
	d.reconnectMu.Lock()
	defer d.reconnectMu.Unlock()
	if d.closed.Load() {
		return netClosedError()
	}
	if current := d.currentFile(); current != nil && current != expected {
		return nil
	}
	d.fileMu.Lock()
	if d.file == expected && d.file != nil {
		_ = d.file.Close()
		d.file = nil
	}
	d.fileMu.Unlock()

	deadline := time.Now().Add(20 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		if d.closed.Load() {
			return netClosedError()
		}
		file, err := connectWindowsServicePipe(d.filter, d.tcpPorts)
		if err == nil {
			d.fileMu.Lock()
			if d.closed.Load() {
				d.fileMu.Unlock()
				_ = file.Close()
				return netClosedError()
			}
			d.file = file
			d.fileMu.Unlock()
			return nil
		}
		lastErr = err
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("RelayProxy Network Service 自动重连失败: %w", lastErr)
}

func (d *windowsServicePacketDevice) Receive(buffer []byte) (int, packetMetadata, error) {
	for attempt := 0; attempt < 2; attempt++ {
		file := d.currentFile()
		if file == nil {
			return 0, packetMetadata{}, errors.New("RelayProxy Network Service packet device is closed")
		}
		frame, err := readNetworkFrame(file)
		if err != nil {
			if attempt == 0 && !d.closed.Load() {
				if reconnectErr := d.reconnect(file); reconnectErr == nil {
					continue
				} else {
					return 0, packetMetadata{}, errors.Join(err, reconnectErr)
				}
			}
			return 0, packetMetadata{}, err
		}
		if frame.kind == networkFrameError {
			return 0, packetMetadata{}, errors.New(strings.TrimSpace(string(frame.payload)))
		}
		if frame.kind != networkFrameCapture {
			return 0, packetMetadata{}, fmt.Errorf("RelayProxy Network Service returned unexpected frame %d", frame.kind)
		}
		if len(frame.payload) > len(buffer) {
			return 0, packetMetadata{}, fmt.Errorf("captured packet %d exceeds receive buffer %d", len(frame.payload), len(buffer))
		}
		copy(buffer, frame.payload)
		outbound := frame.flags&networkFrameFlagOutbound != 0
		return len(frame.payload), packetMetadata{
			outbound:         outbound,
			capturedOutbound: outbound,
			ifIndex:          frame.ifIndex,
			subIfIndex:       frame.subIfIndex,
		}, nil
	}
	return 0, packetMetadata{}, errors.New("RelayProxy Network Service receive retry exhausted")
}

func (d *windowsServicePacketDevice) writeTransactionFrame(frame networkFrame) error {
	file := d.currentFile()
	if file == nil {
		return errors.New("RelayProxy Network Service packet device is closed")
	}
	// A capture transaction belongs to the current pipe instance. Never retry an
	// Inject/Complete frame on a freshly reconnected session: the new broker is
	// waiting for a different capture and replaying the old verdict would
	// desynchronize the protocol.
	return writeNetworkFrame(file, &d.writeMu, frame)
}

func (d *windowsServicePacketDevice) Send(packet []byte, meta packetMetadata) error {
	flags := uint32(0)
	if meta.outbound {
		flags |= networkFrameFlagOutbound
	}
	return d.writeTransactionFrame(networkFrame{
		kind:       networkFrameInject,
		flags:      flags,
		ifIndex:    meta.ifIndex,
		subIfIndex: meta.subIfIndex,
		payload:    packet,
	})
}

func (d *windowsServicePacketDevice) Finalize(packetMetadata) error {
	return d.writeTransactionFrame(networkFrame{kind: networkFrameComplete})
}

func (d *windowsServicePacketDevice) Shutdown() error { return d.Close() }

func (d *windowsServicePacketDevice) Close() error {
	if d == nil {
		return nil
	}
	d.closeOnce.Do(func() {
		d.closed.Store(true)
		d.fileMu.Lock()
		if d.file != nil {
			d.closeErr = d.file.Close()
			d.file = nil
		}
		d.fileMu.Unlock()
	})
	return d.closeErr
}

func EnsurePlatformService() error {
	if runtime.GOARCH != "amd64" {
		return nil
	}
	expected, expectedErr := expectedWindowsNetworkServiceExecutable()
	installed, running, binaryPath, _, stateErr := windowsNetworkServiceState()
	if expectedErr == nil && stateErr == nil && installed && running &&
		strings.Contains(strings.ToLower(binaryPath), strings.ToLower(expected)) {
		return nil
	}
	if windows.GetCurrentProcessToken().IsElevated() {
		return installWindowsNetworkService("")
	}
	return runElevatedNetworkServiceHelper(networkServiceHelperInstall)
}

func WindowsNetworkServiceInstalled() bool {
	installed, _, _, _, err := windowsNetworkServiceState()
	return err == nil && installed
}

func PlatformServiceReady() bool {
	expected, expectedErr := expectedWindowsNetworkServiceExecutable()
	installed, running, binaryPath, _, stateErr := windowsNetworkServiceState()
	return expectedErr == nil && stateErr == nil && installed && running &&
		strings.Contains(strings.ToLower(binaryPath), strings.ToLower(expected))
}

func GetPlatformServiceStatus() NetworkServiceStatus {
	status := NetworkServiceStatus{Supported: runtime.GOARCH == "amd64", State: "unsupported"}
	if runtime.GOARCH != "amd64" {
		status.Message = "Windows ARM64 暂不支持 WinDivert Network Service"
		return status
	}
	installed, running, binaryPath, pid, err := windowsNetworkServiceState()
	status.Installed = installed
	status.Running = running
	status.BinaryPath = binaryPath
	status.PID = pid
	if err != nil {
		status.State = "error"
		status.Message = err.Error()
		return status
	}
	expected, expectedErr := expectedWindowsNetworkServiceExecutable()
	status.VersionMatch = expectedErr == nil && installed &&
		strings.Contains(strings.ToLower(binaryPath), strings.ToLower(expected))
	if installed {
		status.AutoStart, status.AutoStartKnown = windowsNetworkServiceAutoStartState()
		status.RecoveryEnabled, status.RecoveryKnown = windowsNetworkServiceRecoveryState()
	}
	status.Ready = installed && running && status.VersionMatch &&
		(!status.AutoStartKnown || status.AutoStart) &&
		(!status.RecoveryKnown || status.RecoveryEnabled)
	switch {
	case !installed:
		status.State = "not_installed"
		status.Message = "Network Service 尚未安装"
	case !status.VersionMatch:
		status.State = "needs_repair"
		status.Message = "Network Service 版本与当前客户端不一致"
	case status.AutoStartKnown && !status.AutoStart:
		status.State = "needs_repair"
		status.Message = "Network Service 未配置为开机自动启动"
	case status.RecoveryKnown && !status.RecoveryEnabled:
		status.State = "needs_repair"
		status.Message = "Network Service 自动恢复策略缺失或不完整"
	case !running:
		status.State = "stopped"
		status.Message = "Network Service 已安装但未运行"
	default:
		status.State = "running"
		status.Message = "Network Service 运行正常"
	}
	return status
}

func windowsNetworkServiceAutoStartState() (enabled, known bool) {
	key, err := registry.OpenKey(
		registry.LOCAL_MACHINE,
		`SYSTEM\CurrentControlSet\Services\`+windowsNetworkServiceName,
		registry.QUERY_VALUE,
	)
	if err != nil {
		return false, false
	}
	defer key.Close()
	start, _, err := key.GetIntegerValue("Start")
	if err != nil {
		return false, false
	}
	return start == uint64(windows.SERVICE_AUTO_START), true
}

func windowsNetworkServiceRecoveryState() (enabled, known bool) {
	manager, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return false, false
	}
	defer windows.CloseServiceHandle(manager)
	name, err := windows.UTF16PtrFromString(windowsNetworkServiceName)
	if err != nil {
		return false, false
	}
	handle, err := windows.OpenService(manager, name, windows.SERVICE_QUERY_CONFIG)
	if err != nil {
		return false, false
	}
	service := &mgr.Service{Name: windowsNetworkServiceName, Handle: handle}
	defer service.Close()

	actions, err := service.RecoveryActions()
	if err != nil {
		return false, false
	}
	onNonCrash, err := service.RecoveryActionsOnNonCrashFailures()
	if err != nil {
		return false, false
	}
	return matchesWindowsNetworkServiceRecovery(actions, onNonCrash), true
}

func matchesWindowsNetworkServiceRecovery(actions []mgr.RecoveryAction, onNonCrash bool) bool {
	want := []time.Duration{1 * time.Second, 5 * time.Second, 15 * time.Second}
	if !onNonCrash || len(actions) != len(want) {
		return false
	}
	for i := range want {
		if actions[i].Type != mgr.ServiceRestart || actions[i].Delay != want[i] {
			return false
		}
	}
	return true
}

func RepairPlatformService() error {
	if runtime.GOARCH != "amd64" {
		return errors.New("当前 Windows 架构不支持 RelayProxy Network Service")
	}
	if windows.GetCurrentProcessToken().IsElevated() {
		return installWindowsNetworkService("")
	}
	return runElevatedNetworkServiceHelper(networkServiceHelperInstall)
}

func UninstallPlatformService() (NetworkServiceUninstallResult, error) {
	if runtime.GOARCH != "amd64" {
		return NetworkServiceUninstallResult{}, nil
	}
	if windows.GetCurrentProcessToken().IsElevated() {
		return removeWindowsNetworkService()
	}
	if err := runElevatedNetworkServiceHelper(networkServiceHelperRemove); err != nil {
		return NetworkServiceUninstallResult{}, err
	}
	return detectWindowsDeferredCleanup()
}

func windowsNetworkServiceState() (installed, running bool, binaryPath string, pid uint32, err error) {
	manager, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return false, false, "", 0, err
	}
	defer windows.CloseServiceHandle(manager)
	name, err := windows.UTF16PtrFromString(windowsNetworkServiceName)
	if err != nil {
		return false, false, "", 0, err
	}
	service, err := windows.OpenService(manager, name, windows.SERVICE_QUERY_STATUS)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return false, false, "", 0, nil
	}
	if err != nil {
		return false, false, "", 0, err
	}
	defer windows.CloseServiceHandle(service)
	var status windows.SERVICE_STATUS_PROCESS
	var needed uint32
	if err := windows.QueryServiceStatusEx(
		service,
		windows.SC_STATUS_PROCESS_INFO,
		(*byte)(unsafe.Pointer(&status)),
		uint32(unsafe.Sizeof(status)),
		&needed,
	); err != nil {
		return true, false, "", 0, err
	}

	// The Service Control Manager query above intentionally uses read-only
	// access so ordinary users can check readiness without UAC. ImagePath is
	// read separately from HKLM; failure to read it only means EnsurePlatformService
	// cannot prove that the installed broker matches the current executable.
	key, keyErr := registry.OpenKey(
		registry.LOCAL_MACHINE,
		`SYSTEM\CurrentControlSet\Services\`+windowsNetworkServiceName,
		registry.QUERY_VALUE,
	)
	if keyErr == nil {
		binaryPath, _, _ = key.GetStringValue("ImagePath")
		_ = key.Close()
	}
	return true, status.CurrentState == windows.SERVICE_RUNNING, binaryPath, status.ProcessId, nil
}

var expectedWindowsNetworkServiceExecutableOnce = sync.OnceValues(func() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	programData, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, 0)
	if err != nil {
		return "", err
	}
	directory := filepath.Join(programData, "RelayProxy-Network-Service", fmt.Sprintf("%x", sum[:8]))
	return filepath.Join(directory, "RelayProxyNetwork.exe"), nil
})

func expectedWindowsNetworkServiceExecutable() (string, error) {
	return expectedWindowsNetworkServiceExecutableOnce()
}

func stageWindowsNetworkServiceExecutable() (string, error) {
	source, err := os.Executable()
	if err != nil {
		return "", err
	}
	source, err = filepath.Abs(source)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	programData, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, 0)
	if err != nil {
		return "", err
	}
	root := filepath.Join(programData, "RelayProxy-Network-Service")
	if err := ensureWinDivertDirectory(root); err != nil {
		return "", fmt.Errorf("保护 Network Service 根目录失败: %w", err)
	}
	directory := filepath.Join(root, fmt.Sprintf("%x", sum[:8]))
	if err := ensureWinDivertDirectory(directory); err != nil {
		return "", fmt.Errorf("保护 Network Service 版本目录失败: %w", err)
	}
	target := filepath.Join(directory, "RelayProxyNetwork.exe")
	if err := writeWinDivertFile(target, data); err != nil {
		return "", fmt.Errorf("安装 Network Service 可执行文件失败: %w", err)
	}
	return target, nil
}

func RunWindowsNetworkServiceHelper(action, allowedSID string) error {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return errors.New("RelayProxy Network Service 安装程序未获得管理员权限")
	}
	switch strings.TrimSpace(action) {
	case networkServiceHelperInstall:
		return installWindowsNetworkService(allowedSID)
	case networkServiceHelperRemove:
		_, err := removeWindowsNetworkService()
		return err
	default:
		return fmt.Errorf("未知的 Network Service 操作 %q", action)
	}
}

func currentWindowsUserSID() (string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	return user.User.Sid.String(), nil
}

func installWindowsNetworkService(allowedSID string) error {
	executable, err := stageWindowsNetworkServiceExecutable()
	if err != nil {
		return err
	}
	allowedSID = strings.TrimSpace(allowedSID)
	if allowedSID == "" {
		allowedSID, err = currentWindowsUserSID()
		if err != nil {
			return err
		}
	}
	if _, err := windows.StringToSid(allowedSID); err != nil {
		return fmt.Errorf("Network Service 允许用户 SID 无效: %w", err)
	}
	binaryPath := syscall.EscapeArg(executable) +
		" --" + networkServiceModeFlagName +
		" --" + networkServiceSIDFlagName + "=" + allowedSID

	manager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("连接 Windows Service Control Manager 失败: %w", err)
	}
	defer manager.Disconnect()

	service, err := manager.OpenService(windowsNetworkServiceName)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		service, err = manager.CreateService(
			windowsNetworkServiceName,
			executable,
			mgr.Config{
				DisplayName: windowsNetworkServiceDisplayName,
				Description: "Privileged WinDivert packet broker for RelayProxy transparent proxy",
				StartType:   mgr.StartAutomatic,
			},
			"--"+networkServiceModeFlagName,
			"--"+networkServiceSIDFlagName+"="+allowedSID,
		)
		if err != nil {
			return fmt.Errorf("安装 RelayProxy Network Service 失败: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("打开 RelayProxy Network Service 失败: %w", err)
	} else {
		current, configErr := service.Config()
		if configErr != nil {
			service.Close()
			return configErr
		}
		if current.BinaryPathName != binaryPath || current.StartType != mgr.StartAutomatic {
			status, _ := service.Query()
			if status.State != svc.Stopped {
				_, _ = service.Control(svc.Stop)
				_ = waitWindowsServiceState(service, svc.Stopped, 10*time.Second)
			}
			current.BinaryPathName = binaryPath
			current.StartType = mgr.StartAutomatic
			current.DisplayName = windowsNetworkServiceDisplayName
			current.Description = "Privileged WinDivert packet broker for RelayProxy transparent proxy"
			if err := service.UpdateConfig(current); err != nil {
				service.Close()
				return fmt.Errorf("更新 RelayProxy Network Service 失败: %w", err)
			}
		}
	}
	defer service.Close()

	recovery := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 1 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 15 * time.Second},
	}
	if err := service.SetRecoveryActions(recovery, 24*60*60); err != nil {
		return fmt.Errorf("配置 RelayProxy Network Service 崩溃恢复失败: %w", err)
	}
	if err := service.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		return fmt.Errorf("配置 RelayProxy Network Service 非正常退出恢复失败: %w", err)
	}

	status, err := service.Query()
	if err != nil {
		return err
	}
	if status.State != svc.Running {
		if err := service.Start(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
			return fmt.Errorf("启动 RelayProxy Network Service 失败: %w", err)
		}
	}
	if err := waitWindowsServiceState(service, svc.Running, 10*time.Second); err != nil {
		return err
	}
	return nil
}

func removeWindowsNetworkService() (NetworkServiceUninstallResult, error) {
	manager, err := mgr.Connect()
	if err != nil {
		return NetworkServiceUninstallResult{}, fmt.Errorf("连接 Windows Service Control Manager 失败: %w", err)
	}
	defer manager.Disconnect()

	service, openErr := manager.OpenService(windowsNetworkServiceName)
	switch {
	case openErr == nil:
		status, queryErr := service.Query()
		if queryErr != nil {
			_ = service.Close()
			return NetworkServiceUninstallResult{}, fmt.Errorf("查询 RelayProxy Network Service 状态失败: %w", queryErr)
		}
		if status.State != svc.Stopped {
			if _, controlErr := service.Control(svc.Stop); controlErr != nil &&
				!errors.Is(controlErr, windows.ERROR_SERVICE_NOT_ACTIVE) {
				_ = service.Close()
				return NetworkServiceUninstallResult{}, fmt.Errorf("停止 RelayProxy Network Service 失败: %w", controlErr)
			}
			if waitErr := waitWindowsServiceState(service, svc.Stopped, 15*time.Second); waitErr != nil {
				_ = service.Close()
				return NetworkServiceUninstallResult{}, waitErr
			}
		}
		deleteErr := service.Delete()
		closeErr := service.Close()
		if deleteErr != nil && !errors.Is(deleteErr, windows.ERROR_SERVICE_MARKED_FOR_DELETE) {
			return NetworkServiceUninstallResult{}, fmt.Errorf("删除 RelayProxy Network Service 失败: %w", deleteErr)
		}
		if closeErr != nil {
			return NetworkServiceUninstallResult{}, fmt.Errorf("关闭 RelayProxy Network Service 句柄失败: %w", closeErr)
		}
		if err := waitWindowsServiceDeleted(manager, 15*time.Second); err != nil {
			return NetworkServiceUninstallResult{}, err
		}
	case errors.Is(openErr, windows.ERROR_SERVICE_DOES_NOT_EXIST):
		// The SCM entry may already be gone while an earlier uninstall left
		// ProgramData artifacts behind. Cleanup must still run.
	default:
		return NetworkServiceUninstallResult{}, fmt.Errorf("打开 RelayProxy Network Service 失败: %w", openErr)
	}

	result, err := cleanupWindowsNetworkServiceArtifacts()
	if err != nil {
		return NetworkServiceUninstallResult{}, err
	}
	return result, nil
}

func waitWindowsServiceDeleted(manager *mgr.Mgr, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		service, err := manager.OpenService(windowsNetworkServiceName)
		switch {
		case err == nil:
			_ = service.Close()
		case errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST):
			return nil
		case errors.Is(err, windows.ERROR_SERVICE_MARKED_FOR_DELETE):
			// SCM has accepted deletion but another handle/process still owns
			// the service object. Wait until it really disappears.
		default:
			return fmt.Errorf("确认 RelayProxy Network Service 删除状态失败: %w", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("RelayProxy Network Service 已标记删除，但未在超时内从 SCM 完全消失")
}

func cleanupWindowsNetworkServiceArtifacts() (NetworkServiceUninstallResult, error) {
	if err := removeWindowsTransparentFirewallRule(); err != nil {
		return NetworkServiceUninstallResult{}, fmt.Errorf("删除 RelayProxy Windows Firewall 规则失败: %w", err)
	}
	programData, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, 0)
	if err != nil {
		return NetworkServiceUninstallResult{}, fmt.Errorf("获取 ProgramData 路径失败: %w", err)
	}

	// The staged broker executable must be removable immediately once SCM and
	// the service process are gone. Treat a leftover broker as uninstall
	// failure rather than silently reporting success.
	serviceRoot := filepath.Join(programData, "RelayProxy-Network-Service")
	winDivertRoot := filepath.Join(programData, "RelayProxy-WinDivert")
	var deferred []string

	if err := removeWindowsTreeWithRetry(serviceRoot, 5*time.Second); err != nil {
		if scheduleErr := scheduleWindowsTreeDeleteOnReboot(serviceRoot); scheduleErr != nil {
			return NetworkServiceUninstallResult{}, fmt.Errorf("删除 Network Service 程序目录失败 %s: %v；安排重启后删除也失败: %w", serviceRoot, err, scheduleErr)
		}
		deferred = append(deferred, serviceRoot)
	}

	// WinDivert runtime belongs to the privileged transparent-proxy component.
	// Usually it can also be removed immediately. If the kernel still has a
	// runtime file mapped, schedule the remaining tree for deletion at reboot.
	if err := removeWindowsTreeWithRetry(winDivertRoot, 5*time.Second); err != nil {
		if scheduleErr := scheduleWindowsTreeDeleteOnReboot(winDivertRoot); scheduleErr != nil {
			return NetworkServiceUninstallResult{}, fmt.Errorf("删除 WinDivert 运行目录失败 %s: %v；安排重启后删除也失败: %w", winDivertRoot, err, scheduleErr)
		}
		deferred = append(deferred, winDivertRoot)
	}
	if len(deferred) != 0 {
		return NetworkServiceUninstallResult{RebootCleanup: true, CleanupPath: strings.Join(deferred, "; ")}, nil
	}
	return NetworkServiceUninstallResult{}, nil
}

func detectWindowsDeferredCleanup() (NetworkServiceUninstallResult, error) {
	installed, _, _, _, stateErr := windowsNetworkServiceState()
	if stateErr != nil {
		return NetworkServiceUninstallResult{}, fmt.Errorf("验证 Network Service 卸载状态失败: %w", stateErr)
	}
	if installed {
		return NetworkServiceUninstallResult{}, errors.New("Network Service helper 已退出，但 SCM 服务项仍然存在")
	}

	programData, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, 0)
	if err != nil {
		return NetworkServiceUninstallResult{}, err
	}
	var remaining []string
	for _, root := range []string{
		filepath.Join(programData, "RelayProxy-Network-Service"),
		filepath.Join(programData, "RelayProxy-WinDivert"),
	} {
		if _, statErr := os.Stat(root); statErr == nil {
			remaining = append(remaining, root)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return NetworkServiceUninstallResult{}, statErr
		}
	}
	if len(remaining) != 0 {
		return NetworkServiceUninstallResult{
			RebootCleanup: true,
			CleanupPath:   strings.Join(remaining, "; "),
		}, nil
	}
	return NetworkServiceUninstallResult{}, nil
}

func removeWindowsTreeWithRetry(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		lastErr = os.RemoveAll(path)
		if lastErr == nil {
			if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
				return nil
			} else if err != nil {
				lastErr = err
			} else {
				lastErr = errors.New("目录仍然存在")
			}
		}
		if time.Now().After(deadline) {
			return lastErr
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func scheduleWindowsTreeDeleteOnReboot(root string) error {
	info, err := os.Stat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return scheduleWindowsPathDeleteOnReboot(root)
	}
	var paths []string
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return err
	}
	for index := len(paths) - 1; index >= 0; index-- {
		if err := scheduleWindowsPathDeleteOnReboot(paths[index]); err != nil {
			return err
		}
	}
	return nil
}

func scheduleWindowsPathDeleteOnReboot(path string) error {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(name, nil, windows.MOVEFILE_DELAY_UNTIL_REBOOT)
}

func waitWindowsServiceState(service *mgr.Service, wanted svc.State, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status, err := service.Query()
		if err != nil {
			return err
		}
		if status.State == wanted {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("等待 RelayProxy Network Service 状态 %d 超时", wanted)
}

func runElevatedNetworkServiceHelper(action string) error {
	if action != networkServiceHelperInstall && action != networkServiceHelperRemove {
		return fmt.Errorf("未知的 Network Service 操作 %q", action)
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return err
	}
	allowedSID := ""
	if action == networkServiceHelperInstall {
		allowedSID, err = currentWindowsUserSID()
		if err != nil {
			return err
		}
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, err := windows.UTF16PtrFromString(executable)
	if err != nil {
		return err
	}
	parametersText := "--" + networkServiceHelperFlagName + "=" + action
	if allowedSID != "" {
		parametersText += " --" + networkServiceSIDFlagName + "=" + allowedSID
	}
	parameters, err := windows.UTF16PtrFromString(parametersText)
	if err != nil {
		return err
	}
	directory, err := windows.UTF16PtrFromString(filepath.Dir(executable))
	if err != nil {
		return err
	}
	info := networkShellExecuteInfo{
		Size:       uint32(unsafe.Sizeof(networkShellExecuteInfo{})),
		Mask:       shellExecuteNoCloseProcess | shellExecuteNoAsync,
		Verb:       verb,
		File:       file,
		Parameters: parameters,
		Directory:  directory,
		Show:       shellShowHidden,
	}
	ok, _, callErr := networkShellExecute.Call(uintptr(unsafe.Pointer(&info)))
	if ok == 0 {
		if errors.Is(callErr, windows.ERROR_CANCELLED) {
			return errors.New("已取消管理员授权，RelayProxy Network Service 未安装")
		}
		if callErr != nil && callErr != windows.ERROR_SUCCESS {
			return fmt.Errorf("请求 Network Service 管理员权限失败: %w", callErr)
		}
		return errors.New("请求 Network Service 管理员权限失败")
	}
	if info.Process == 0 {
		return errors.New("Network Service 安装程序未返回进程句柄")
	}
	defer windows.CloseHandle(info.Process)
	if _, err := windows.WaitForSingleObject(info.Process, windows.INFINITE); err != nil {
		return err
	}
	var exitCode uint32
	if err := windows.GetExitCodeProcess(info.Process, &exitCode); err != nil {
		return err
	}
	if exitCode != 0 {
		return fmt.Errorf("Network Service 安装程序失败，退出码 %d", exitCode)
	}
	return nil
}
