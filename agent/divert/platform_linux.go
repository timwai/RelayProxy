//go:build linux

package divert

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/florianl/go-nfqueue/v2"
	"golang.org/x/sys/unix"
)

const (
	linuxQueueNumber = 58231
	linuxInjectMark  = 0x52504f58 // "RPOX"
	linuxOutputChain = "RELAYPROXY_OUT"
	linuxInputChain  = "RELAYPROXY_IN"

	netfilterLocalIn  = 1
	netfilterLocalOut = 3
)

func platformCapabilities() Capabilities {
	caps := Capabilities{
		Platform: "linux", TCP: true, UDP: true, IPv6: true, Hostnames: true, HostnameSource: "dns",
		ProcessIdentity: true, OriginalDestination: true, ReplyInjection: true, LoopBypass: true,
	}
	if err := linuxPlatformReadiness(); err != nil {
		caps.UnavailableReason = err.Error()
	}
	return caps
}

func linuxPlatformReadiness() error {
	if os.Geteuid() != 0 {
		caps, err := linuxEffectiveCapabilities()
		if err != nil {
			return fmt.Errorf("读取 Linux capabilities 失败: %w", err)
		}
		const required = (uint64(1) << unix.CAP_NET_ADMIN) | (uint64(1) << unix.CAP_NET_RAW)
		if caps&required != required {
			return errors.New("透明代理需要 root，或同时授予 CAP_NET_ADMIN 与 CAP_NET_RAW")
		}
	}
	for _, binary := range []string{"iptables", "ip6tables"} {
		if _, err := exec.LookPath(binary); err != nil {
			return fmt.Errorf("未找到 %s（可使用基于 nftables 的 iptables 兼容前端）", binary)
		}
	}
	if _, err := os.Stat("/proc/net"); err != nil {
		return fmt.Errorf("/proc/net 不可用，无法解析进程连接: %w", err)
	}
	return nil
}

func linuxEffectiveCapabilities() (uint64, error) {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if value, ok := strings.CutPrefix(line, "CapEff:"); ok {
			return strconv.ParseUint(strings.TrimSpace(value), 16, 64)
		}
	}
	return 0, errors.New("/proc/self/status 中没有 CapEff")
}

type linuxPacketRef struct {
	mu   sync.Mutex
	id   uint32
	done bool
}

type linuxNFQueue interface {
	SetVerdict(uint32, int) error
	SetVerdictWithOption(uint32, int, ...nfqueue.VerdictOption) error
	RegisterWithErrorFunc(context.Context, nfqueue.HookFunc, nfqueue.ErrorFunc) error
	Close() error
}

func (r *linuxPacketRef) verdict(queue linuxNFQueue, verdict int) (bool, error) {
	if r == nil {
		return false, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done {
		return false, nil
	}
	if err := queue.SetVerdict(r.id, verdict); err != nil {
		return true, err
	}
	r.done = true
	return true, nil
}

type linuxQueuePacket struct {
	data []byte
	meta packetMetadata
	err  error
}

type linuxPacketDevice struct {
	queue     linuxNFQueue
	ctx       context.Context
	cancel    context.CancelFunc
	packets   chan linuxQueuePacket
	raw4      int
	raw6      int
	firewall  *linuxFirewall
	closeOnce sync.Once
	closeErr  error
	injectFn  func([]byte) error
}

func newLinuxPacketDevice(relayIPs []string) (*linuxPacketDevice, error) {
	ctx, cancel := context.WithCancel(context.Background())
	d := &linuxPacketDevice{ctx: ctx, cancel: cancel, packets: make(chan linuxQueuePacket, 1024), raw4: -1, raw6: -1}
	fail := func(err error) (*linuxPacketDevice, error) {
		cancel()
		_ = d.Close()
		return nil, err
	}

	var err error
	if d.raw4, err = openLinuxRawSocket(unix.AF_INET); err != nil {
		return fail(fmt.Errorf("创建 IPv4 注入 socket 失败: %w", err))
	}
	if d.raw6, err = openLinuxRawSocket(unix.AF_INET6); err != nil {
		return fail(fmt.Errorf("创建 IPv6 注入 socket 失败: %w", err))
	}
	d.queue, err = nfqueue.Open(&nfqueue.Config{
		NfQueue:      linuxQueueNumber,
		MaxQueueLen:  4096,
		MaxPacketLen: 0xffff,
		Copymode:     nfqueue.NfQnlCopyPacket,
		WriteTimeout: 2 * time.Second,
	})
	if err != nil {
		return fail(fmt.Errorf("打开 NFQUEUE %d 失败: %w", linuxQueueNumber, err))
	}
	if err := d.queue.RegisterWithErrorFunc(ctx, d.onPacket, d.onQueueError); err != nil {
		return fail(fmt.Errorf("绑定 NFQUEUE %d 失败: %w", linuxQueueNumber, err))
	}
	d.firewall, err = installLinuxFirewall(relayIPs)
	if err != nil {
		return fail(err)
	}
	return d, nil
}

func openLinuxRawSocket(family int) (int, error) {
	fd, err := unix.Socket(family, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.IPPROTO_RAW)
	if err != nil {
		return -1, err
	}
	fail := func(err error) (int, error) {
		_ = unix.Close(fd)
		return -1, err
	}
	if err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_MARK, linuxInjectMark); err != nil {
		return fail(err)
	}
	if family == unix.AF_INET {
		if err := unix.SetsockoptInt(fd, unix.IPPROTO_IP, unix.IP_HDRINCL, 1); err != nil {
			return fail(err)
		}
	} else if err := unix.SetsockoptInt(fd, unix.IPPROTO_IPV6, unix.IPV6_HDRINCL, 1); err != nil {
		return fail(err)
	}
	return fd, nil
}

func (d *linuxPacketDevice) onPacket(attr nfqueue.Attribute) int {
	if attr.PacketID == nil || attr.Payload == nil || attr.Hook == nil {
		if attr.PacketID != nil {
			_ = d.queue.SetVerdict(*attr.PacketID, nfqueue.NfDrop)
		}
		return 0
	}
	outbound := *attr.Hook == netfilterLocalOut
	if !outbound && *attr.Hook != netfilterLocalIn {
		_ = d.queue.SetVerdict(*attr.PacketID, nfqueue.NfAccept)
		return 0
	}
	ref := &linuxPacketRef{id: *attr.PacketID}
	packet := linuxQueuePacket{
		data: *attr.Payload,
		meta: packetMetadata{outbound: outbound, capturedOutbound: outbound, platformToken: ref},
	}
	select {
	case d.packets <- packet:
	case <-d.ctx.Done():
		_, _ = ref.verdict(d.queue, nfqueue.NfAccept)
	default:
		_, _ = ref.verdict(d.queue, nfqueue.NfDrop)
	}
	return 0
}

func (d *linuxPacketDevice) onQueueError(err error) int {
	select {
	case d.packets <- linuxQueuePacket{err: err}:
	case <-d.ctx.Done():
	}
	return 1
}

func (d *linuxPacketDevice) ReceivePacket() ([]byte, packetMetadata, error) {
	select {
	case <-d.ctx.Done():
		return nil, packetMetadata{}, net.ErrClosed
	case packet := <-d.packets:
		return packet.data, packet.meta, packet.err
	}
}

func (d *linuxPacketDevice) Receive(buffer []byte) (int, packetMetadata, error) {
	packet, meta, err := d.ReceivePacket()
	if err != nil {
		return 0, packetMetadata{}, err
	}
	if len(packet) > len(buffer) {
		_ = d.Finalize(meta)
		return 0, packetMetadata{}, errors.New("NFQUEUE packet exceeds receive buffer")
	}
	return copy(buffer, packet), meta, nil
}

// Accept supplies a metadata-only NF_ACCEPT verdict. This is the direct path:
// it avoids copying the packet back to the kernel and retains GSO/checksum
// metadata that a raw userspace reinjection would otherwise destroy.
func (d *linuxPacketDevice) Accept(meta packetMetadata) error {
	ref, _ := meta.platformToken.(*linuxPacketRef)
	if ref == nil || meta.outbound != meta.capturedOutbound {
		return errors.New("cannot accept a synthetic or direction-rewritten NFQUEUE packet")
	}
	_, err := ref.verdict(d.queue, nfqueue.NfAccept)
	return err
}

func (d *linuxPacketDevice) Send(packet []byte, meta packetMetadata) error {
	ref, _ := meta.platformToken.(*linuxPacketRef)
	if ref != nil && meta.outbound == meta.capturedOutbound {
		// Current direction-preserving rewrites can continue in the kernel.
		ref.mu.Lock()
		if !ref.done {
			err := d.queue.SetVerdictWithOption(ref.id, nfqueue.NfAccept, nfqueue.WithAlteredPacket(packet))
			if err == nil {
				ref.done = true
			}
			ref.mu.Unlock()
			return err
		}
		ref.mu.Unlock()
	}
	if ref != nil {
		if _, err := ref.verdict(d.queue, nfqueue.NfDrop); err != nil {
			return err
		}
	}
	return d.inject(packet)
}

func (d *linuxPacketDevice) inject(packet []byte) error {
	if d.injectFn != nil {
		return d.injectFn(packet)
	}
	if len(packet) < 20 {
		return errors.New("cannot inject a truncated IP packet")
	}
	switch packet[0] >> 4 {
	case 4:
		var address [4]byte
		copy(address[:], packet[16:20])
		return unix.Sendto(d.raw4, packet, 0, &unix.SockaddrInet4{Addr: address})
	case 6:
		if len(packet) < 40 {
			return errors.New("cannot inject a truncated IPv6 packet")
		}
		var address [16]byte
		copy(address[:], packet[24:40])
		return unix.Sendto(d.raw6, packet, 0, &unix.SockaddrInet6{Addr: address})
	default:
		return errors.New("cannot inject a packet with an unknown IP version")
	}
}

func (d *linuxPacketDevice) Finalize(meta packetMetadata) error {
	ref, _ := meta.platformToken.(*linuxPacketRef)
	_, err := ref.verdict(d.queue, nfqueue.NfDrop)
	return err
}

func (d *linuxPacketDevice) Shutdown() error {
	var err error
	if d.firewall != nil {
		err = d.firewall.Close()
	}
	d.cancel()
	return err
}

func (d *linuxPacketDevice) Close() error {
	d.closeOnce.Do(func() {
		d.cancel()
		var errs []error
		if d.firewall != nil {
			errs = append(errs, d.firewall.Close())
		}
		if d.queue != nil {
			errs = append(errs, d.queue.Close())
		}
		if d.raw4 >= 0 {
			errs = append(errs, unix.Close(d.raw4))
			d.raw4 = -1
		}
		if d.raw6 >= 0 {
			errs = append(errs, unix.Close(d.raw6))
			d.raw6 = -1
		}
		d.closeErr = errors.Join(errs...)
	})
	return d.closeErr
}

func startPlatformInterceptor(s *Server) (systemInterceptor, error) {
	if err := linuxPlatformReadiness(); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrPlatformNotReady, err)
	}
	if err := prepareLoopGuard(s); err != nil {
		return nil, err
	}
	var listeners []net.Listener
	for _, family := range []struct{ network, address string }{{"tcp4", "0.0.0.0:0"}, {"tcp6", "[::]:0"}} {
		listener, err := net.Listen(family.network, family.address)
		if err != nil {
			for _, opened := range listeners {
				_ = opened.Close()
			}
			return nil, fmt.Errorf("创建透明代理 %s 监听失败: %w", family.network, err)
		}
		listeners = append(listeners, listener)
	}
	device, err := newLinuxPacketDevice(s.guard.RelayIPs)
	if err != nil {
		for _, listener := range listeners {
			_ = listener.Close()
		}
		return nil, err
	}
	resolver := newLinuxProcessResolver()
	i := newPacketInterceptor(s, device, listeners, resolver.lookup)
	i.start()
	return i, nil
}

type linuxFirewall struct {
	iptables  string
	ip6tables string
	run       func(string, ...string) error
	closeOnce sync.Once
	closeErr  error
}

func installLinuxFirewall(relayIPs []string) (*linuxFirewall, error) {
	iptables, err := exec.LookPath("iptables")
	if err != nil {
		return nil, err
	}
	ip6tables, err := exec.LookPath("ip6tables")
	if err != nil {
		return nil, err
	}
	f := &linuxFirewall{iptables: iptables, ip6tables: ip6tables, run: runLinuxFirewallCommand}
	for _, binary := range []string{f.iptables, f.ip6tables} {
		f.cleanupFamily(binary)
		if err := f.installFamily(binary, relayIPs); err != nil {
			_ = f.Close()
			return nil, err
		}
	}
	return f, nil
}

func runLinuxFirewallCommand(binary string, args ...string) error {
	command := exec.Command(binary, append([]string{"-w", "5"}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("%s %s: %w: %s", binary, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (f *linuxFirewall) installFamily(binary string, relayIPs []string) error {
	mark := fmt.Sprintf("0x%x/0xffffffff", linuxInjectMark)
	commands := [][]string{
		{"-t", "mangle", "-N", linuxOutputChain},
		{"-t", "mangle", "-N", linuxInputChain},
		{"-t", "mangle", "-A", linuxOutputChain, "-m", "mark", "--mark", mark, "-j", "RETURN"},
		{"-t", "mangle", "-A", linuxInputChain, "-m", "mark", "--mark", mark, "-j", "RETURN"},
	}
	isIPv6 := strings.Contains(strings.ToLower(filepathBase(binary)), "ip6tables")
	for _, raw := range relayIPs {
		ip, err := netip.ParseAddr(raw)
		if err == nil && ip.Is6() == isIPv6 {
			commands = append(commands, []string{"-t", "mangle", "-A", linuxOutputChain, "-d", ip.String(), "-j", "RETURN"})
		}
	}
	for _, protocol := range []string{"tcp", "udp"} {
		commands = append(commands, []string{"-t", "mangle", "-A", linuxOutputChain, "-p", protocol, "-j", "NFQUEUE", "--queue-num", strconv.Itoa(linuxQueueNumber), "--queue-bypass"})
	}
	// Inbound packets are not rewritten. Only DNS responses are observed so
	// hostname-based policy remains available without copying every download
	// packet through userspace.
	commands = append(commands, []string{"-t", "mangle", "-A", linuxInputChain, "-p", "udp", "--sport", "53", "-j", "NFQUEUE", "--queue-num", strconv.Itoa(linuxQueueNumber), "--queue-bypass"})
	commands = append(commands,
		[]string{"-t", "mangle", "-I", "OUTPUT", "1", "-j", linuxOutputChain},
		[]string{"-t", "mangle", "-I", "INPUT", "1", "-j", linuxInputChain},
	)
	for _, args := range commands {
		if err := f.run(binary, args...); err != nil {
			f.cleanupFamily(binary)
			return fmt.Errorf("安装 Linux 透明代理规则失败: %w", err)
		}
	}
	return nil
}

func filepathBase(path string) string {
	if index := strings.LastIndexByte(path, '/'); index >= 0 {
		return path[index+1:]
	}
	return path
}

func (f *linuxFirewall) cleanupFamily(binary string) {
	_ = f.run(binary, "-t", "mangle", "-D", "OUTPUT", "-j", linuxOutputChain)
	_ = f.run(binary, "-t", "mangle", "-D", "INPUT", "-j", linuxInputChain)
	_ = f.run(binary, "-t", "mangle", "-F", linuxOutputChain)
	_ = f.run(binary, "-t", "mangle", "-F", linuxInputChain)
	_ = f.run(binary, "-t", "mangle", "-X", linuxOutputChain)
	_ = f.run(binary, "-t", "mangle", "-X", linuxInputChain)
}

func (f *linuxFirewall) removeFamily(binary string) error {
	var errs []error
	for _, args := range [][]string{
		{"-t", "mangle", "-D", "OUTPUT", "-j", linuxOutputChain},
		{"-t", "mangle", "-D", "INPUT", "-j", linuxInputChain},
		{"-t", "mangle", "-F", linuxOutputChain},
		{"-t", "mangle", "-F", linuxInputChain},
		{"-t", "mangle", "-X", linuxOutputChain},
		{"-t", "mangle", "-X", linuxInputChain},
	} {
		if err := f.run(binary, args...); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (f *linuxFirewall) Close() error {
	if f == nil {
		return nil
	}
	f.closeOnce.Do(func() {
		for _, binary := range []string{f.ip6tables, f.iptables} {
			f.closeErr = errors.Join(f.closeErr, f.removeFamily(binary))
		}
	})
	return f.closeErr
}
