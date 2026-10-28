//go:build linux

package gateway

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const tableName = "relayproxy_gateway"
const owner = "RelayProxy LAN gateway v1"
const routeTable = "52520"
const priority = "12520"
const mark = "0x525047"

func Supported() bool { return true }
func command(input, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(input)
	// Diagnostics and missing-table detection inspect nft/ip's English errors.
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, commandError(name, args, out, err)
	}
	return out, nil
}

func commandError(name string, args []string, out []byte, cause error) error {
	detail := strings.TrimSpace(string(out))
	lower := strings.ToLower(detail)
	hint := ""
	switch {
	case errors.Is(cause, exec.ErrNotFound):
		hint = "缺少网关工具：请安装 nftables 和 iproute2，并确保 PATH 包含 /usr/sbin 和 /sbin"
	case strings.Contains(lower, "operation not permitted"), strings.Contains(lower, "permission denied"):
		hint = "网关网络管理权限不足：Docker 容器需 root、NET_ADMIN 和 NET_RAW，且不能使用受限的 rootless 网络环境"
	case name == "nft" && strings.Contains(lower, "cache initialization failed"):
		hint = "宿主机 nftables 内核接口不可用或与当前 nft 不兼容；请检查宿主机 nf_tables 支持。Docker 容器共享宿主机内核，重装容器软件包不能补齐缺失的内核功能；网关还需要 nft TPROXY（上游 Linux 4.18 起支持，厂商回移除外）"
		var uts unix.Utsname
		if unix.Uname(&uts) == nil {
			hint += "；当前内核 " + unix.ByteSliceToString(uts.Release[:])
		}
	}
	if hint != "" {
		detail += "；" + hint
	}
	return fmt.Errorf("gateway: %s %v: %w: %s", name, args, cause, detail)
}

func missingTable(out []byte) bool {
	detail := strings.ToLower(string(out))
	// A cache failure may itself report ENOENT when nf_tables is unavailable.
	// Only an error about this exact table can mean that our table is absent.
	return !strings.Contains(detail, "cache initialization failed") &&
		strings.Contains(detail, "no such file or directory") &&
		strings.Contains(detail, "list table inet "+tableName)
}

func ownedTable() (bool, error) {
	out, err := command("", "nft", "list", "table", "inet", tableName)
	if err != nil {
		if missingTable(out) {
			return false, nil
		}
		return false, err
	}
	if !strings.Contains(string(out), owner) {
		return false, errors.New("网关 nftables 表已被其他程序占用")
	}
	return true, nil
}
func acquireLock() (*os.File, error) {
	f, err := os.OpenFile("/run/relayproxy-gateway.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, errors.New("已有 Agent 正在运行网关")
	}
	return f, nil
}
func socketControl(_ string, _ string, raw syscall.RawConn) error {
	var sockErr error
	err := raw.Control(func(fd uintptr) {
		sockErr = unix.SetsockoptInt(int(fd), unix.SOL_IP, unix.IP_TRANSPARENT, 1)
		if sockErr == nil {
			sockErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1)
		}
	})
	if err != nil {
		return err
	}
	return sockErr
}
func transparentReply(addr netip.AddrPort, iface string) (*net.UDPConn, error) {
	lc := net.ListenConfig{Control: deviceControl(iface)}
	pc, err := lc.ListenPacket(context.Background(), "udp4", addr.String())
	if err != nil {
		return nil, err
	}
	return pc.(*net.UDPConn), nil
}
func rules(c Config, exists bool) string {
	var b strings.Builder
	if exists {
		fmt.Fprintf(&b, "delete table inet %s\n", tableName)
	}
	fmt.Fprintf(&b, "table inet %s {\n comment \"%s\"\n chain ingress { type filter hook prerouting priority mangle; policy accept;\n", tableName, owner)
	iface := strconv.Quote(c.Interface)
	allowed := strings.Join(c.SourceCIDRs, ", ")
	// Only packets arriving from the configured LAN are intercepted. Host output
	// and the tunnel's own transport never traverse this chain on that interface.
	fmt.Fprintf(&b, "iifname %s meta nfproto ipv4 ip saddr != { %s } fib daddr type != local drop\n", iface, allowed)
	fmt.Fprintf(&b, "iifname %s ip saddr { %s } meta l4proto { tcp, udp } th dport 53 tproxy ip to :%d meta mark set %s accept\n", iface, allowed, c.ListenPort(), mark)
	fmt.Fprintf(&b, "iifname %s fib daddr type local return\n", iface)
	fmt.Fprintf(&b, "iifname %s ip daddr { 0.0.0.0/8, 127.0.0.0/8, 224.0.0.0/4, 255.255.255.255 } drop\n", iface)
	fmt.Fprintf(&b, "iifname %s ip saddr { %s } meta l4proto { tcp, udp } tproxy ip to :%d meta mark set %s accept\n", iface, allowed, c.ListenPort(), mark)
	fmt.Fprintf(&b, "}\n chain guard { type filter hook forward priority -10; policy accept; iifname %s drop\n }\n}\n", iface)
	return b.String()
}
func ensureRoute() error {
	out, err := command("", "ip", "-4", "rule", "show")
	if err != nil {
		return err
	}
	found := false
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), priority+":") {
			if !strings.Contains(line, "fwmark "+mark) || !strings.Contains(line, "lookup "+routeTable) {
				return errors.New("网关策略路由优先级已占用")
			}
			found = true
		}
		if strings.Contains(line, "lookup "+routeTable) && !strings.HasPrefix(strings.TrimSpace(line), priority+":") {
			return errors.New("网关策略路由表已被其他规则引用")
		}
	}
	routes, err := command("", "ip", "-4", "route", "show", "table", routeTable)
	if err != nil && !strings.Contains(string(routes), "FIB table does not exist") {
		return err
	}
	if err != nil {
		routes = nil
	}
	for _, line := range strings.Split(strings.TrimSpace(string(routes)), "\n") {
		if line != "" && (!strings.HasPrefix(line, "local default dev lo") && !strings.HasPrefix(line, "local 0.0.0.0/0 dev lo")) {
			return errors.New("网关路由表已占用")
		}
	}
	if _, err = command("", "ip", "-4", "route", "replace", "local", "0.0.0.0/0", "dev", "lo", "table", routeTable); err != nil {
		return err
	}
	if !found {
		_, err = command("", "ip", "-4", "rule", "add", "priority", priority, "fwmark", mark, "lookup", routeTable)
	}
	return err
}
func (s *Server) Start() (err error) {
	if s.ctx.Err() != nil {
		return errors.New("gateway closed")
	}
	if s.running.Load() {
		return errors.New("gateway already running")
	}
	if !s.opts.Config.Enabled {
		return nil
	}
	if _, err = net.InterfaceByName(s.opts.Config.Interface); err != nil {
		return err
	}
	lock, err := acquireLock()
	if err != nil {
		return err
	}
	s.lock = lock
	defer func() {
		if err != nil {
			_ = s.Stop()
		}
	}()
	exists, err := ownedTable()
	if err != nil {
		return err
	}
	script := rules(s.opts.Config, exists)
	if _, err = command(script, "nft", "-c", "-f", "-"); err != nil {
		return err
	}
	lc := net.ListenConfig{Control: deviceControl(s.opts.Config.Interface)}
	s.tcp, err = lc.Listen(s.ctx, "tcp4", portAddress(s.opts.Config))
	if err != nil {
		return err
	}
	pc, err := lc.ListenPacket(s.ctx, "udp4", portAddress(s.opts.Config))
	if err != nil {
		return err
	}
	s.udp = pc.(*net.UDPConn)
	raw, err := s.udp.SyscallConn()
	if err != nil {
		return err
	}
	var optErr error
	err = raw.Control(func(fd uintptr) { optErr = unix.SetsockoptInt(int(fd), unix.SOL_IP, unix.IP_RECVORIGDSTADDR, 1) })
	if err != nil {
		return err
	}
	if optErr != nil {
		return optErr
	}
	if err = ensureRoute(); err != nil {
		return err
	}
	if _, err = command(script, "nft", "-f", "-"); err != nil {
		return err
	}
	s.running.Store(true)
	s.wg.Add(2)
	go func() {
		defer s.wg.Done()
		for {
			c, e := s.tcp.Accept()
			if e != nil {
				if s.ctx.Err() == nil {
					s.fail(e)
					s.running.Store(false)
					go s.Stop()
				}
				return
			}
			if !s.take() {
				_ = c.Close()
				continue
			}
			s.wg.Add(1)
			go s.serveTCP(c)
		}
	}()
	go func() {
		defer s.wg.Done()
		buf := make([]byte, 65535)
		oob := make([]byte, 256)
		for {
			n, on, flags, source, e := s.udp.ReadMsgUDPAddrPort(buf, oob)
			if e != nil {
				if s.ctx.Err() == nil {
					s.fail(e)
					s.running.Store(false)
					go s.Stop()
				}
				return
			}
			if flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 {
				continue
			}
			dest, e := originalDestination(oob[:on])
			if e != nil {
				s.rejected.Add(1)
				continue
			}
			s.receiveUDP(flowKey{netip.AddrPortFrom(source.Addr().Unmap(), source.Port()), dest}, buf[:n])
		}
	}()
	return nil
}
func originalDestination(oob []byte) (netip.AddrPort, error) {
	msgs, e := unix.ParseSocketControlMessage(oob)
	if e != nil {
		return netip.AddrPort{}, e
	}
	for _, m := range msgs {
		if m.Header.Level == unix.SOL_IP && m.Header.Type == unix.IP_ORIGDSTADDR && len(m.Data) >= 8 {
			ip := netip.AddrFrom4([4]byte(m.Data[4:8]))
			return netip.AddrPortFrom(ip, binary.BigEndian.Uint16(m.Data[2:4])), nil
		}
	}
	return netip.AddrPort{}, errors.New("missing original destination")
}

// Disable is explicit. Process shutdown deliberately retains the forward guard.
func Disable() error {
	lock, err := acquireLock()
	if err != nil {
		return err
	}
	defer lock.Close()
	exists, err := ownedTable()
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if _, err = command("", "nft", "delete", "table", "inet", tableName); err != nil {
		return err
	}
	if _, err = command("", "ip", "-4", "rule", "del", "priority", priority, "fwmark", mark, "lookup", routeTable); err != nil {
		return err
	}
	_, err = command("", "ip", "-4", "route", "del", "local", "0.0.0.0/0", "dev", "lo", "table", routeTable)
	return err
}

func deviceControl(iface string) func(string, string, syscall.RawConn) error {
	return func(network, address string, raw syscall.RawConn) error {
		if err := socketControl(network, address, raw); err != nil {
			return err
		}
		var optErr error
		err := raw.Control(func(fd uintptr) {
			optErr = unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, iface)
		})
		if err != nil {
			return err
		}
		return optErr
	}
}
