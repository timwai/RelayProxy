//go:build windows

package divert

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	wfpDevicePath = `\\.\RelayProxyWfp`

	wfpIOCTLGetVersion  = uint32(0x80002000)
	wfpIOCTLSetConfig   = uint32(0x80002004)
	wfpIOCTLGetEvent    = uint32(0x80002008)
	wfpIOCTLSetDecision = uint32(0x8000200c)
	wfpIOCTLInjectUDP   = uint32(0x80002010)
	wfpIOCTLHeartbeat   = uint32(0x80002014)
	wfpIOCTLStop        = uint32(0x80002018)
	wfpIOCTLRelease     = uint32(0x8000201c)
)

const wfpHeartbeatInterval = 2 * time.Second

type wfpDevice struct {
	handle windows.Handle
	closed atomic.Bool
	mu     sync.Mutex
}

func openWFPDevice() (*wfpDevice, error) {
	path, err := windows.UTF16PtrFromString(wfpDevicePath)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(path, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, fmt.Errorf("打开 RelayProxy WFP 驱动失败: %w", err)
	}
	return &wfpDevice{handle: handle}, nil
}

func (d *wfpDevice) ioctl(code uint32, in, out []byte) (uint32, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed.Load() {
		return 0, net.ErrClosed
	}
	var inPtr, outPtr *byte
	if len(in) > 0 {
		inPtr = &in[0]
	}
	if len(out) > 0 {
		outPtr = &out[0]
	}
	var returned uint32
	err := windows.DeviceIoControl(d.handle, code, inPtr, uint32(len(in)), outPtr, uint32(len(out)), &returned, nil)
	return returned, err
}

func (d *wfpDevice) version() (wfpVersion, error) {
	out := make([]byte, 16)
	n, err := d.ioctl(wfpIOCTLGetVersion, nil, out)
	if err != nil {
		return wfpVersion{}, err
	}
	return decodeWFPVersion(out[:n])
}

func (d *wfpDevice) configure(pid uint32, port4, port6 uint16) error {
	data := encodeWFPConfig(pid, port4, port6, uint32((wfpHeartbeatInterval*3)/time.Millisecond))
	_, err := d.ioctl(wfpIOCTLSetConfig, data, nil)
	return err
}

func (d *wfpDevice) decision(requestID uint64, action Action) error {
	data, err := encodeWFPDecision(requestID, action)
	if err != nil {
		return err
	}
	_, err = d.ioctl(wfpIOCTLSetDecision, data, nil)
	return err
}

func (d *wfpDevice) event(buffer []byte) (wfpEvent, error) {
	n, err := d.ioctl(wfpIOCTLGetEvent, nil, buffer)
	if err != nil {
		return wfpEvent{}, err
	}
	if n == 0 {
		return wfpEvent{}, windows.ERROR_NO_MORE_ITEMS
	}
	return decodeWFPEvent(buffer[:n])
}

func (d *wfpDevice) injectUDP(associationID uint64, payload []byte) error {
	data, err := encodeWFPUDPInjection(associationID, payload)
	if err != nil {
		return err
	}
	_, err = d.ioctl(wfpIOCTLInjectUDP, data, nil)
	return err
}

func (d *wfpDevice) heartbeat() error {
	_, err := d.ioctl(wfpIOCTLHeartbeat, nil, nil)
	return err
}

func (d *wfpDevice) stop() { _, _ = d.ioctl(wfpIOCTLStop, nil, nil) }

func (d *wfpDevice) release(requestID uint64) error {
	data, err := encodeWFPRelease(requestID)
	if err != nil {
		return err
	}
	_, err = d.ioctl(wfpIOCTLRelease, data, nil)
	return err
}

func (d *wfpDevice) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.closed.CompareAndSwap(false, true) {
		return nil
	}
	return windows.CloseHandle(d.handle)
}

func wfpPlatformReadiness() error {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return errors.New("WFP 系统透明代理需要管理员权限")
	}
	device, err := openWFPDevice()
	if err != nil {
		return fmt.Errorf("RelayProxyWfp 驱动未安装或未启动: %w", err)
	}
	defer device.Close()
	_, err = device.version()
	return err
}

type wfpUDPJob struct {
	event wfpEvent
	route *ClassifiedFlow
}

type wfpInterceptor struct {
	server    *Server
	device    *wfpDevice
	listeners []net.Listener
	ctx       context.Context
	cancel    context.CancelFunc
	running   atomic.Bool
	closeOnce sync.Once
	wg        sync.WaitGroup

	mu       sync.Mutex
	requests map[uint64]*ClassifiedFlow
	udp      map[uint64]*ClassifiedFlow
	dns      *dnsAssociations
	queues   []chan wfpUDPJob

	logMu   sync.Mutex
	lastLog time.Time
}

func startWFPInterceptor(s *Server) (systemInterceptor, error) {
	listeners := make([]net.Listener, 0, 2)
	for _, target := range []struct {
		network string
		address string
	}{{"tcp4", "127.0.0.1:0"}, {"tcp6", "[::1]:0"}} {
		listener, err := net.Listen(target.network, target.address)
		if err != nil {
			for _, opened := range listeners {
				_ = opened.Close()
			}
			return nil, fmt.Errorf("创建 WFP 本地 %s 代理监听失败: %w", target.network, err)
		}
		listeners = append(listeners, listener)
	}
	port4 := listeners[0].Addr().(*net.TCPAddr).AddrPort().Port()
	port6 := listeners[1].Addr().(*net.TCPAddr).AddrPort().Port()

	device, err := openWFPDevice()
	if err != nil {
		for _, listener := range listeners {
			_ = listener.Close()
		}
		return nil, err
	}
	if _, err := device.version(); err != nil {
		_ = device.Close()
		for _, listener := range listeners {
			_ = listener.Close()
		}
		return nil, err
	}
	if err := device.configure(uint32(os.Getpid()), port4, port6); err != nil {
		_ = device.Close()
		for _, listener := range listeners {
			_ = listener.Close()
		}
		return nil, fmt.Errorf("配置 WFP 驱动失败: %w", err)
	}

	ctx, cancel := context.WithCancel(s.ctx)
	interceptor := &wfpInterceptor{
		server: s, device: device, listeners: listeners, ctx: ctx, cancel: cancel,
		requests: make(map[uint64]*ClassifiedFlow), udp: make(map[uint64]*ClassifiedFlow),
		dns: newDNSAssociations(), queues: make([]chan wfpUDPJob, 8),
	}
	for index := range interceptor.queues {
		interceptor.queues[index] = make(chan wfpUDPJob, 128)
		interceptor.wg.Add(1)
		go interceptor.forwardUDP(interceptor.queues[index])
	}
	for _, listener := range listeners {
		interceptor.wg.Add(1)
		go interceptor.acceptTCP(listener)
	}
	interceptor.wg.Add(2)
	go interceptor.readEvents()
	go interceptor.heartbeats()
	interceptor.running.Store(true)
	return interceptor, nil
}

func (i *wfpInterceptor) Running() bool { return i != nil && i.running.Load() }

func (i *wfpInterceptor) ListenAddr() string {
	if i == nil || len(i.listeners) == 0 {
		return ""
	}
	return i.listeners[0].Addr().String()
}

func (i *wfpInterceptor) report(err error) {
	if err == nil || i.ctx.Err() != nil {
		return
	}
	i.logMu.Lock()
	defer i.logMu.Unlock()
	if time.Since(i.lastLog) >= time.Second {
		i.lastLog = time.Now()
		log.Printf("[divert/wfp] %v", err)
	}
}

func (i *wfpInterceptor) heartbeats() {
	defer i.wg.Done()
	ticker := time.NewTicker(wfpHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-i.ctx.Done():
			return
		case <-ticker.C:
			if err := i.device.heartbeat(); err != nil {
				i.report(fmt.Errorf("driver heartbeat failed: %w", err))
			}
		}
	}
}

func (i *wfpInterceptor) readEvents() {
	defer i.wg.Done()
	buffer := make([]byte, wfpEventHeaderSize+wfpMaxEventPayload)
	for {
		if i.ctx.Err() != nil {
			return
		}
		event, err := i.device.event(buffer)
		if err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_ITEMS) {
				select {
				case <-i.ctx.Done():
					return
				case <-time.After(2 * time.Millisecond):
					continue
				}
			}
			if i.ctx.Err() == nil {
				i.report(fmt.Errorf("读取 WFP 事件失败: %w", err))
				i.running.Store(false)
				go i.server.Close()
			}
			return
		}
		switch event.Kind {
		case wfpEventFlow:
			i.handleFlow(event)
		case wfpEventUDPData:
			i.handleUDPEvent(event)
		case wfpEventDNS:
			i.handleDNSEvent(event)
		case wfpEventClose:
			i.handleClose(event)
		default:
			i.report(fmt.Errorf("未知 WFP 事件类型 %d", event.Kind))
		}
	}
}

func (i *wfpInterceptor) handleFlow(event wfpEvent) {
	if event.RequestID == 0 || event.ProcessID > uint64(^uint32(0)) {
		i.report(errors.New("WFP flow event has invalid identity"))
		if event.RequestID != 0 {
			_ = i.device.decision(event.RequestID, ActionReject)
		}
		return
	}
	process := strings.TrimSpace(strings.TrimRight(event.ProcessPath, "\x00"))
	system := event.Flags&wfpEventFlagSystem != 0 || event.ProcessID == 4
	if process == "" && system {
		process = "System"
	}
	if process == "" {
		_ = i.device.decision(event.RequestID, ActionReject)
		i.report(fmt.Errorf("WFP flow %d has no trusted process identity", event.RequestID))
		return
	}
	flow := Flow{
		Process: process, ProcessID: uint32(event.ProcessID), Protocol: event.Protocol,
		SourceIP: event.Source.Addr().String(), SourcePort: event.Source.Port(),
		IP: event.Destination.Addr().String(), Port: event.Destination.Port(),
	}
	if host := i.dns.lookup(event.Destination.Addr()); host != "" {
		flow.Host, flow.DomainSource = host, "dns"
	}
	// Keep DNS bootstrap local on Windows. The Windows DNS Client service may
	// own the packet instead of relay-agent.exe, and proxying the resolver
	// request can deadlock a tunnel reconnect that itself needs DNS.
	if event.Protocol == ProtoUDP && event.Destination.Port() == 53 {
		if err := i.device.decision(event.RequestID, ActionDirect); err != nil {
			i.report(err)
		}
		return
	}
	for _, addr := range []netip.Addr{event.Source.Addr(), event.Destination.Addr()} {
		if addr.IsLoopback() || addr.IsMulticast() || addr.IsLinkLocalUnicast() || addr.IsUnspecified() ||
			addr == netip.AddrFrom4([4]byte{255, 255, 255, 255}) {
			if err := i.device.decision(event.RequestID, ActionDirect); err != nil {
				i.report(err)
			}
			return
		}
	}

	route, err := i.server.ClassifyFlow(flow)
	if err != nil {
		_ = i.device.decision(event.RequestID, ActionReject)
		i.report(err)
		return
	}

	if event.Protocol == ProtoUDP && route.Decision().Action != ActionReject {
		if err := i.server.PinUDPAssociation(route); err != nil {
			route.traffic.Finish("failed", err)
			_ = i.device.decision(event.RequestID, ActionReject)
			i.report(err)
			return
		}
	}

	i.mu.Lock()
	i.requests[event.RequestID] = route
	if event.Protocol == ProtoUDP && event.AssociationID != 0 {
		i.udp[event.AssociationID] = route
	}
	i.mu.Unlock()

	if err := i.device.decision(event.RequestID, route.Decision().Action); err != nil {
		route.traffic.Finish("failed", err)
		i.forget(event.RequestID, event.AssociationID)
		i.report(err)
		return
	}
	if route.Decision().Action == ActionDirect {
		route.traffic.Activate()
	}
}

func (i *wfpInterceptor) handleUDPEvent(event wfpEvent) {
	if event.AssociationID == 0 || len(event.Payload) == 0 {
		return
	}
	i.mu.Lock()
	route := i.udp[event.AssociationID]
	i.mu.Unlock()
	if route == nil {
		i.report(fmt.Errorf("UDP association %d is unknown", event.AssociationID))
		return
	}
	if route.Decision().Action != ActionProxy {
		return
	}
	queue := i.queues[int(event.AssociationID%uint64(len(i.queues)))]
	job := wfpUDPJob{event: event, route: route}
	select {
	case queue <- job:
	case <-i.ctx.Done():
	default:
		i.report(errors.New("WFP UDP forwarding queue is full; datagram dropped"))
	}
}

func (i *wfpInterceptor) forwardUDP(queue <-chan wfpUDPJob) {
	defer i.wg.Done()
	for {
		select {
		case <-i.ctx.Done():
			return
		case job := <-queue:
			i.dns.query(job.event.Source, job.event.Destination, job.event.Payload)
			err := i.server.ForwardUDP(i.ctx, job.route, job.event.Payload, func(ctx context.Context, key FlowKey, payload []byte) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				i.dns.response(key.Destination, key.Source, payload)
				return i.device.injectUDP(job.event.AssociationID, payload)
			})
			i.report(err)
		}
	}
}

func (i *wfpInterceptor) handleDNSEvent(event wfpEvent) {
	if event.Protocol != ProtoUDP || len(event.Payload) == 0 {
		return
	}
	if event.Flags&wfpEventFlagOutbound != 0 {
		i.dns.query(event.Source, event.Destination, event.Payload)
	} else {
		i.dns.response(event.Source, event.Destination, event.Payload)
	}
}

func (i *wfpInterceptor) handleClose(event wfpEvent) {
	i.mu.Lock()
	route := i.requests[event.RequestID]
	if event.RequestID != 0 {
		delete(i.requests, event.RequestID)
	}
	if event.AssociationID != 0 {
		if route == nil {
			route = i.udp[event.AssociationID]
		}
		delete(i.udp, event.AssociationID)
	}
	i.mu.Unlock()
	if route == nil {
		return
	}
	if route.Decision().Action == ActionDirect && len(event.Payload) >= 16 {
		upload := binary.LittleEndian.Uint64(event.Payload[0:8])
		download := binary.LittleEndian.Uint64(event.Payload[8:16])
		if upload > 0 {
			route.traffic.AddUpload(int(min(upload, uint64(^uint(0)>>1))))
		}
		if download > 0 {
			route.traffic.AddDownload(int(min(download, uint64(^uint(0)>>1))))
		}
	}
	if route.Key().Protocol == ProtoUDP && route.Decision().Action != ActionReject {
		i.server.ReleaseUDPAssociation(route)
		return
	}
	if route.Decision().Action != ActionReject {
		route.traffic.Finish("closed", nil)
	}
}

func (i *wfpInterceptor) forget(requestID, associationID uint64) {
	i.mu.Lock()
	delete(i.requests, requestID)
	if associationID != 0 {
		delete(i.udp, associationID)
	}
	i.mu.Unlock()
}

func (i *wfpInterceptor) acceptTCP(listener net.Listener) {
	defer i.wg.Done()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if i.ctx.Err() == nil {
				i.report(fmt.Errorf("WFP TCP listener stopped: %w", err))
			}
			return
		}
		tcp, ok := conn.(*net.TCPConn)
		if !ok {
			_ = conn.Close()
			continue
		}
		requestID, err := queryWFPRedirectContext(tcp)
		if err != nil {
			_ = conn.Close()
			i.report(err)
			continue
		}
		i.mu.Lock()
		route := i.requests[requestID]
		i.mu.Unlock()
		if route == nil || route.Decision().Action != ActionProxy || route.Key().Protocol != ProtoTCP {
			_ = conn.Close()
			i.report(fmt.Errorf("WFP redirected TCP request %d has no proxy route", requestID))
			continue
		}
		i.wg.Add(1)
		go func(requestID uint64, route *ClassifiedFlow, conn net.Conn) {
			defer i.wg.Done()
			defer i.forget(requestID, 0)
			defer func() { _ = i.device.release(requestID) }()
			if err := i.server.ForwardTCP(i.ctx, route, conn); err != nil {
				route.traffic.Finish("failed", err)
				i.report(err)
				return
			}
			route.traffic.Finish("closed", nil)
		}(requestID, route, conn)
	}
}

var (
	modWS2_32       = windows.NewLazySystemDLL("ws2_32.dll")
	procWSAIoctlWFP = modWS2_32.NewProc("WSAIoctl")
)

const sioQueryWFPConnectionRedirectContext = uint32(0x980000dd)

func queryWFPRedirectContext(conn *net.TCPConn) (uint64, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, err
	}
	buffer := make([]byte, 64)
	var returned uint32
	var callErr error
	err = raw.Control(func(socket uintptr) {
		ret, _, errno := procWSAIoctlWFP.Call(
			socket,
			uintptr(sioQueryWFPConnectionRedirectContext),
			0, 0,
			uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)),
			uintptr(unsafe.Pointer(&returned)),
			0, 0,
		)
		if int32(ret) == -1 {
			if errno != nil && errno != windows.ERROR_SUCCESS {
				callErr = errno
			} else {
				callErr = errors.New("WSAIoctl failed without an error code")
			}
		}
	})
	if err != nil {
		return 0, err
	}
	if callErr != nil {
		return 0, fmt.Errorf("查询 WFP redirect context 失败: %w", callErr)
	}
	if returned > uint32(len(buffer)) {
		return 0, errors.New("WFP redirect context length is invalid")
	}
	return decodeWFPRedirectContext(buffer[:returned])
}

func (i *wfpInterceptor) Close() {
	if i == nil {
		return
	}
	i.closeOnce.Do(func() {
		i.running.Store(false)
		i.cancel()
		i.device.stop()
		for _, listener := range i.listeners {
			_ = listener.Close()
		}
		i.wg.Wait()
		_ = i.device.Close()
	})
}
