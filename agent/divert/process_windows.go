//go:build windows

package divert

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

type processIdentity struct {
	PID  uint32
	Path string
}

var (
	errProcessNotFound  = errors.New("divert: packet process not found")
	errProcessAmbiguous = errors.New("divert: packet process ownership is ambiguous")
	errProcessTable     = errors.New("divert: invalid process ownership table")
)

const (
	tcpTableOwnerPIDAll = 5
	udpTableOwnerPID    = 1
	maxProcessTableSize = 64 << 20
	processTableTries   = 5
)

type processWindowsAPI struct {
	tcp   *windows.LazyProc
	udp   *windows.LazyProc
	image *windows.LazyProc
}

// Cache API addresses, never identities: Windows can reuse a PID after exit.
var loadProcessWindowsAPI = sync.OnceValues(func() (*processWindowsAPI, error) {
	ipHelper := windows.NewLazySystemDLL("iphlpapi.dll")
	api := &processWindowsAPI{
		tcp:   ipHelper.NewProc("GetExtendedTcpTable"),
		udp:   ipHelper.NewProc("GetExtendedUdpTable"),
		image: windows.NewLazySystemDLL("kernel32.dll").NewProc("QueryFullProcessImageNameW"),
	}
	for _, proc := range []*windows.LazyProc{api.tcp, api.udp, api.image} {
		// Find returns a load error instead of letting LazyProc.Call panic.
		if err := proc.Find(); err != nil {
			return nil, fmt.Errorf("divert: load %s: %w", proc.Name, err)
		}
	}
	return api, nil
})

// lookupPacketProcess reads the current OS connection tables. source is the
// original local endpoint, before transparent-proxy address rewriting.
func lookupPacketProcess(proto Protocol, source, destination netip.AddrPort) (processIdentity, error) {
	if proto != ProtoTCP && proto != ProtoUDP {
		return processIdentity{}, fmt.Errorf("divert: unsupported process lookup protocol %q", proto)
	}
	local, err := makeProcessEndpoint(source)
	if err != nil {
		return processIdentity{}, err
	}
	remote, err := makeProcessEndpoint(destination)
	if err != nil {
		return processIdentity{}, err
	}
	if local.address.Is4() != remote.address.Is4() {
		return processIdentity{}, errors.New("divert: process lookup address families differ")
	}
	api, err := loadProcessWindowsAPI()
	if err != nil {
		return processIdentity{}, err
	}
	proc, tableClass := api.tcp, uint32(tcpTableOwnerPIDAll)
	if proto == ProtoUDP {
		proc, tableClass = api.udp, udpTableOwnerPID
	}
	families := []uint32{windows.AF_INET6}
	if local.address.Is4() {
		// IPv4 traffic may belong to an IPv6 dual-stack socket. In particular,
		// a UDP [::] binding cannot be ruled out by the IPv4 table alone.
		families = []uint32{windows.AF_INET, windows.AF_INET6}
	}
	var owners processOwnerCandidates
	for _, family := range families {
		table, err := readProcessTable(func(buffer []byte, size *uint32) uint32 {
			var pointer *byte
			if len(buffer) != 0 {
				pointer = &buffer[0]
			}
			status, _, _ := proc.Call(
				uintptr(unsafe.Pointer(pointer)), uintptr(unsafe.Pointer(size)),
				0, uintptr(family), uintptr(tableClass), 0,
			)
			// These IP Helper functions return their error code directly.
			return uint32(status)
		})
		if err != nil {
			return processIdentity{}, fmt.Errorf("divert: %s (family %d): %w", proc.Name, family, err)
		}
		if err := matchProcessTable(table, proto, family, local, remote, &owners); err != nil {
			return processIdentity{}, err
		}
	}
	pid, err := owners.processID()
	if err != nil {
		return processIdentity{}, err
	}
	path, err := api.processPath(pid)
	if err != nil {
		return processIdentity{}, err
	}
	return processIdentity{PID: pid, Path: path}, nil
}

// readProcessTable bounds both allocation and retries while connections change
// between the size query and the subsequent table query.
func readProcessTable(query func([]byte, *uint32) uint32) ([]byte, error) {
	var buffer []byte
	for range processTableTries {
		size := uint32(len(buffer))
		status := query(buffer, &size)
		if status == 0 {
			if size < 4 || size > uint32(len(buffer)) {
				return nil, fmt.Errorf("%w: returned size %d for buffer %d", errProcessTable, size, len(buffer))
			}
			return buffer[:size], nil
		}
		if windows.Errno(status) != windows.ERROR_INSUFFICIENT_BUFFER {
			return nil, windows.Errno(status)
		}
		if size < 4 || size > maxProcessTableSize || size <= uint32(len(buffer)) {
			return nil, fmt.Errorf("%w: requested size %d for buffer %d", errProcessTable, size, len(buffer))
		}
		buffer = make([]byte, size)
	}
	return nil, fmt.Errorf("process table kept growing after %d attempts: %w", processTableTries, windows.ERROR_INSUFFICIENT_BUFFER)
}

type processEndpoint struct {
	address  netip.Addr
	port     uint16
	scope    uint32
	hasScope bool
}

func makeProcessEndpoint(endpoint netip.AddrPort) (processEndpoint, error) {
	if !endpoint.IsValid() || endpoint.Port() == 0 || endpoint.Addr().IsUnspecified() {
		return processEndpoint{}, errors.New("divert: invalid process lookup endpoint")
	}
	result := processEndpoint{address: endpoint.Addr().WithZone("").Unmap(), port: endpoint.Port()}
	if zone := endpoint.Addr().Zone(); zone != "" {
		index, err := strconv.ParseUint(zone, 10, 32)
		if err != nil {
			iface, lookupErr := net.InterfaceByName(zone)
			if lookupErr != nil {
				return processEndpoint{}, fmt.Errorf("divert: resolve process endpoint zone %q: %w", zone, lookupErr)
			}
			index = uint64(iface.Index)
		}
		result.scope, result.hasScope = uint32(index), true
	}
	return result, nil
}

type processOwnerCandidates struct {
	pid       uint32
	found     bool
	ambiguous bool
}

func (owners *processOwnerCandidates) add(pid uint32) {
	if owners.found && owners.pid != pid {
		owners.ambiguous = true
	}
	owners.pid, owners.found = pid, true
}

func (owners processOwnerCandidates) processID() (uint32, error) {
	if owners.ambiguous {
		return 0, errProcessAmbiguous
	}
	if !owners.found || owners.pid == 0 {
		return 0, errProcessNotFound
	}
	return owners.pid, nil
}

// matchProcessTable uses the documented OWNER_PID ABI rather than casting
// untrusted table lengths into an unsafe slice. All fields have four-byte
// alignment, including the first row after the DWORD entry count.
func matchProcessTable(table []byte, proto Protocol, family uint32, local, remote processEndpoint, owners *processOwnerCandidates) error {
	var rowSize, localOffset, localPort, remoteOffset, remotePort, pidOffset int
	switch {
	case proto == ProtoTCP && family == windows.AF_INET:
		rowSize, localOffset, localPort, remoteOffset, remotePort, pidOffset = 24, 4, 8, 12, 16, 20
	case proto == ProtoTCP && family == windows.AF_INET6:
		rowSize, localOffset, localPort, remoteOffset, remotePort, pidOffset = 56, 0, 20, 24, 44, 52
	case proto == ProtoUDP && family == windows.AF_INET:
		rowSize, localOffset, localPort, pidOffset = 12, 0, 4, 8
	case proto == ProtoUDP && family == windows.AF_INET6:
		rowSize, localOffset, localPort, pidOffset = 28, 0, 20, 24
	default:
		return fmt.Errorf("%w: protocol %q, family %d", errProcessTable, proto, family)
	}
	if len(table) < 4 || len(table) > maxProcessTableSize {
		return fmt.Errorf("%w: length %d", errProcessTable, len(table))
	}
	count := binary.LittleEndian.Uint32(table[:4])
	if uint64(count) > uint64((len(table)-4)/rowSize) {
		return fmt.Errorf("%w: %d entries in %d bytes", errProcessTable, count, len(table))
	}
	for i := 0; i < int(count); i++ {
		row := table[4+i*rowSize : 4+(i+1)*rowSize]
		if !matchProcessEndpoint(row, family, localOffset, localPort, local, proto == ProtoUDP) {
			continue
		}
		if proto == ProtoTCP && !matchProcessEndpoint(row, family, remoteOffset, remotePort, remote, false) {
			continue
		}
		owners.add(binary.LittleEndian.Uint32(row[pidOffset : pidOffset+4]))
	}
	return nil
}

func matchProcessEndpoint(row []byte, family uint32, addressOffset, portOffset int, endpoint processEndpoint, wildcard bool) bool {
	// Only the first two bytes of the DWORD port field are significant. They
	// are in network byte order; the other two bytes may be uninitialized.
	if binary.BigEndian.Uint16(row[portOffset:portOffset+2]) != endpoint.port {
		return false
	}
	var address netip.Addr
	var scope uint32
	if family == windows.AF_INET {
		address = netip.AddrFrom4([4]byte(row[addressOffset : addressOffset+4]))
	} else {
		address = netip.AddrFrom16([16]byte(row[addressOffset : addressOffset+16])).Unmap()
		scope = binary.LittleEndian.Uint32(row[addressOffset+16 : addressOffset+20])
	}
	anyAddress := wildcard && address.IsUnspecified()
	if anyAddress {
		if family == windows.AF_INET && !endpoint.address.Is4() {
			return false
		}
	} else if address != endpoint.address {
		return false
	}
	// A packet without a zone cannot distinguish interfaces. Keep all such
	// candidates so different owners produce an ambiguity error. A wildcard
	// binding with scope zero accepts every interface.
	return !endpoint.hasScope || scope == endpoint.scope || (anyAddress && scope == 0)
}

func (api *processWindowsAPI) processPath(pid uint32) (string, error) {
	if pid == uint32(os.Getpid()) {
		return os.Executable()
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", fmt.Errorf("divert: open process %d: %w", pid, err)
	}
	defer windows.CloseHandle(process)
	for capacity := 512; capacity <= 32768; capacity *= 2 {
		buffer := make([]uint16, capacity)
		size := uint32(len(buffer))
		ok, _, queryErr := api.image.Call(uintptr(process), 0, uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&size)))
		if ok != 0 {
			if size == 0 || size > uint32(len(buffer)) {
				return "", fmt.Errorf("divert: invalid image path length for process %d", pid)
			}
			return windows.UTF16ToString(buffer[:size]), nil
		}
		if !errors.Is(queryErr, windows.ERROR_INSUFFICIENT_BUFFER) {
			return "", fmt.Errorf("divert: query process %d image: %w", pid, queryErr)
		}
	}
	return "", fmt.Errorf("divert: image path for process %d exceeds Windows path limit", pid)
}
