//go:build windows

package divert

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const (
	windowsNetworkServiceName        = "RelayProxyNetwork"
	windowsNetworkServiceDisplayName = "RelayProxy Network Service"
	windowsNetworkPipeName           = `\\.\pipe\RelayProxyNetwork-v1`

	networkServiceModeFlagName   = "relayproxy-network-service"
	networkServiceSIDFlagName    = "relayproxy-network-service-sid"
	networkServiceHelperFlagName = "relayproxy-network-service-helper"

	networkServiceHelperInstall = "install"
	networkServiceHelperRemove  = "remove"

	networkPipeMagic   = 0x31504e52 // "RNP1" little-endian
	networkPipeVersion = 1

	networkFrameHello   = 1
	networkFrameReady   = 2
	networkFrameError   = 3
	networkFrameCapture = 4
	networkFrameInject  = 5

	networkFrameFlagOutbound = 1 << 0
	networkFrameHeaderBytes  = 24
	networkFrameMaxPayload   = 1 << 20

	pipeAccessDuplex       = 0x00000003
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

type windowsServicePacketDevice struct {
	file      *os.File
	writeMu   sync.Mutex
	closeOnce sync.Once
	closeErr  error
}

type windowsNetworkServiceHandler struct {
	allowedSID string
}

type windowsNetworkBroker struct {
	allowedSID string
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

func (b *windowsNetworkBroker) serve() error {
	for {
		select {
		case <-b.stop:
			return nil
		default:
		}

		pipe, err := createWindowsNetworkPipe(b.allowedSID)
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

		file := os.NewFile(uintptr(pipe), windowsNetworkPipeName)
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
	sddl := fmt.Sprintf("D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GRGW;;;%s)", allowedSID)
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return windows.InvalidHandle, fmt.Errorf("创建 Network Service 管道 ACL 失败: %w", err)
	}
	name, err := windows.UTF16PtrFromString(windowsNetworkPipeName)
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
	filter := strings.TrimSpace(string(hello.payload))
	if filter == "" {
		_ = writeNetworkFrame(file, nil, networkFrame{kind: networkFrameError, payload: []byte("empty WinDivert filter")})
		return errors.New("RelayProxy Network Service 收到空 WinDivert 过滤器")
	}
	handle, err := openWinDivert(filter)
	if err != nil {
		_ = writeNetworkFrame(file, nil, networkFrame{kind: networkFrameError, payload: []byte(err.Error())})
		return err
	}
	defer handle.Close()

	var writeMu sync.Mutex
	if err := writeNetworkFrame(file, &writeMu, networkFrame{kind: networkFrameReady}); err != nil {
		return err
	}

	captureDone := make(chan error, 1)
	go func() {
		buffer := make([]byte, 40+65535)
		for {
			n, addr, err := handle.Recv(buffer)
			if err != nil {
				captureDone <- err
				return
			}
			flags := uint32(0)
			if addr.outbound() {
				flags |= networkFrameFlagOutbound
			}
			frame := networkFrame{
				kind:       networkFrameCapture,
				flags:      flags,
				ifIndex:    addr.ifIndex(),
				subIfIndex: addr.subIfIndex(),
				payload:    append([]byte(nil), buffer[:n]...),
			}
			if err := writeNetworkFrame(file, &writeMu, frame); err != nil {
				captureDone <- err
				return
			}
		}
	}()

	for {
		frame, err := readNetworkFrame(file)
		if err != nil {
			_ = handle.Shutdown()
			<-captureDone
			return err
		}
		if frame.kind != networkFrameInject {
			_ = handle.Shutdown()
			<-captureDone
			return fmt.Errorf("RelayProxy Network Service 收到未知数据帧 %d", frame.kind)
		}
		var addr windivertAddress
		addr.setOutbound(frame.flags&networkFrameFlagOutbound != 0)
		addr.setIfIndex(frame.ifIndex, frame.subIfIndex)
		addr.setChecksums(len(frame.payload) > 0 && frame.payload[0]>>4 == 6)
		if err := handle.Send(frame.payload, addr); err != nil {
			_ = handle.Shutdown()
			<-captureDone
			return err
		}

		select {
		case err := <-captureDone:
			return err
		default:
		}
	}
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

func openWindowsServicePacketDevice(filter string) (*windowsServicePacketDevice, error) {
	name, err := windows.UTF16PtrFromString(windowsNetworkPipeName)
	if err != nil {
		return nil, err
	}
	var handle windows.Handle
	var lastErr error
	for attempt := 0; attempt < 20; attempt++ {
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
		time.Sleep(50 * time.Millisecond)
	}
	if lastErr != nil {
		return nil, lastErr
	}
	file := os.NewFile(uintptr(handle), windowsNetworkPipeName)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, errors.New("无法打开 RelayProxy Network Service 管道")
	}
	device := &windowsServicePacketDevice{file: file}
	if err := writeNetworkFrame(file, &device.writeMu, networkFrame{kind: networkFrameHello, payload: []byte(filter)}); err != nil {
		_ = device.Close()
		return nil, err
	}
	reply, err := readNetworkFrame(file)
	if err != nil {
		_ = device.Close()
		return nil, err
	}
	switch reply.kind {
	case networkFrameReady:
		return device, nil
	case networkFrameError:
		_ = device.Close()
		return nil, errors.New(strings.TrimSpace(string(reply.payload)))
	default:
		_ = device.Close()
		return nil, fmt.Errorf("RelayProxy Network Service 返回未知握手帧 %d", reply.kind)
	}
}

func (d *windowsServicePacketDevice) Receive(buffer []byte) (int, packetMetadata, error) {
	if d == nil || d.file == nil {
		return 0, packetMetadata{}, errors.New("RelayProxy Network Service packet device is closed")
	}
	frame, err := readNetworkFrame(d.file)
	if err != nil {
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

func (d *windowsServicePacketDevice) Send(packet []byte, meta packetMetadata) error {
	if d == nil || d.file == nil {
		return errors.New("RelayProxy Network Service packet device is closed")
	}
	flags := uint32(0)
	if meta.outbound {
		flags |= networkFrameFlagOutbound
	}
	return writeNetworkFrame(d.file, &d.writeMu, networkFrame{
		kind:       networkFrameInject,
		flags:      flags,
		ifIndex:    meta.ifIndex,
		subIfIndex: meta.subIfIndex,
		payload:    packet,
	})
}

func (d *windowsServicePacketDevice) Shutdown() error { return d.Close() }

func (d *windowsServicePacketDevice) Close() error {
	if d == nil {
		return nil
	}
	d.closeOnce.Do(func() {
		if d.file != nil {
			d.closeErr = d.file.Close()
			d.file = nil
		}
	})
	return d.closeErr
}

func EnsurePlatformService() error {
	if runtime.GOARCH != "amd64" {
		return nil
	}
	installed, running, err := windowsNetworkServiceState()
	if err == nil && installed && running {
		return nil
	}
	if windows.GetCurrentProcessToken().IsElevated() {
		return installWindowsNetworkService("")
	}
	return runElevatedNetworkServiceHelper(networkServiceHelperInstall)
}

func WindowsNetworkServiceInstalled() bool {
	installed, _, err := windowsNetworkServiceState()
	return err == nil && installed
}

func windowsNetworkServiceState() (installed, running bool, err error) {
	manager, err := mgr.Connect()
	if err != nil {
		return false, false, err
	}
	defer manager.Disconnect()
	service, err := manager.OpenService(windowsNetworkServiceName)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	defer service.Close()
	status, err := service.Query()
	if err != nil {
		return true, false, err
	}
	return true, status.State == svc.Running, nil
}

func RunWindowsNetworkServiceHelper(action, allowedSID string) error {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return errors.New("RelayProxy Network Service 安装程序未获得管理员权限")
	}
	switch strings.TrimSpace(action) {
	case networkServiceHelperInstall:
		return installWindowsNetworkService(allowedSID)
	case networkServiceHelperRemove:
		return removeWindowsNetworkService()
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
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	executable, err = filepath.Abs(executable)
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

func removeWindowsNetworkService() error {
	manager, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer manager.Disconnect()
	service, err := manager.OpenService(windowsNetworkServiceName)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return nil
	}
	if err != nil {
		return err
	}
	defer service.Close()
	status, _ := service.Query()
	if status.State != svc.Stopped {
		_, _ = service.Control(svc.Stop)
		_ = waitWindowsServiceState(service, svc.Stopped, 10*time.Second)
	}
	if err := service.Delete(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_MARKED_FOR_DELETE) {
		return fmt.Errorf("删除 RelayProxy Network Service 失败: %w", err)
	}
	return nil
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
