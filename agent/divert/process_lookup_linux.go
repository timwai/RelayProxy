//go:build linux

package divert

import (
	"bufio"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type linuxSocketKey struct {
	protocol            Protocol
	source, destination netip.AddrPort
}

type linuxProcessCacheEntry struct {
	process packetProcess
	err     error
	expires time.Time
}

type linuxProcessResolver struct {
	mu    sync.Mutex
	cache map[linuxSocketKey]linuxProcessCacheEntry
	now   func() time.Time
}

func newLinuxProcessResolver() *linuxProcessResolver {
	return &linuxProcessResolver{cache: make(map[linuxSocketKey]linuxProcessCacheEntry), now: time.Now}
}

func (r *linuxProcessResolver) lookup(protocol Protocol, source, destination netip.AddrPort) (packetProcess, error) {
	source = normalizeProcEndpoint(source)
	destination = normalizeProcEndpoint(destination)
	key := linuxSocketKey{protocol: protocol, source: source, destination: destination}
	now := r.now()
	r.mu.Lock()
	if entry, ok := r.cache[key]; ok && entry.expires.After(now) {
		r.mu.Unlock()
		return entry.process, entry.err
	}
	r.mu.Unlock()

	inode, err := findLinuxSocketInode(protocol, source, destination)
	var process packetProcess
	if err == nil {
		process, err = findLinuxInodeProcess(inode)
	}
	ttl := time.Second
	if err != nil {
		ttl = 100 * time.Millisecond
	}
	r.mu.Lock()
	if len(r.cache) >= 4096 {
		for cachedKey, entry := range r.cache {
			if !entry.expires.After(now) {
				delete(r.cache, cachedKey)
			}
		}
	}
	if len(r.cache) < 4096 {
		r.cache[key] = linuxProcessCacheEntry{process: process, err: err, expires: now.Add(ttl)}
	}
	r.mu.Unlock()
	return process, err
}

func normalizeProcEndpoint(endpoint netip.AddrPort) netip.AddrPort {
	if !endpoint.IsValid() {
		return endpoint
	}
	return netip.AddrPortFrom(endpoint.Addr().Unmap().WithZone(""), endpoint.Port())
}

func findLinuxSocketInode(protocol Protocol, source, destination netip.AddrPort) (uint64, error) {
	name := string(protocol)
	if source.Addr().Is6() {
		name += "6"
	}
	file, err := os.Open(filepath.Join("/proc/net", name))
	if err != nil {
		return 0, err
	}
	defer file.Close()
	return parseLinuxSocketTable(file, protocol, source, destination)
}

type lineScanner interface {
	Scan() bool
	Text() string
	Err() error
}

func parseLinuxSocketTableReader(scanner lineScanner, protocol Protocol, source, destination netip.AddrPort) (uint64, error) {
	bestScore := 0
	var bestInode uint64
	ambiguous := false
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 10 || fields[0] == "sl" {
			continue
		}
		local, err := parseLinuxProcEndpoint(fields[1], source.Addr().Is6())
		if err != nil || local.Port() != source.Port() {
			continue
		}
		localScore := 0
		switch {
		case local == source:
			localScore = 2
		case protocol == ProtoUDP && local.Addr().IsUnspecified():
			localScore = 1
		default:
			continue
		}
		remote, err := parseLinuxProcEndpoint(fields[2], source.Addr().Is6())
		if err != nil {
			continue
		}
		inode, err := strconv.ParseUint(fields[9], 10, 64)
		if err != nil || inode == 0 {
			continue
		}
		remoteScore := 0
		switch {
		case remote == destination:
			remoteScore = 2
		case protocol == ProtoUDP && remote.Port() == 0 && remote.Addr().IsUnspecified():
			remoteScore = 1
		default:
			continue
		}
		score := localScore + remoteScore
		if score > bestScore {
			bestScore, bestInode, ambiguous = score, inode, false
		} else if score == bestScore && inode != bestInode {
			ambiguous = true
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	if ambiguous {
		return 0, fmt.Errorf("%s %s -> %s 匹配到多个本地 socket", protocol, source, destination)
	}
	if bestInode != 0 {
		return bestInode, nil
	}
	return 0, fmt.Errorf("没有找到 %s %s -> %s 的本地 socket", protocol, source, destination)
}

func parseLinuxSocketTable(file *os.File, protocol Protocol, source, destination netip.AddrPort) (uint64, error) {
	return parseLinuxSocketTableReader(bufio.NewScanner(file), protocol, source, destination)
}

func parseLinuxProcEndpoint(raw string, ipv6 bool) (netip.AddrPort, error) {
	addressHex, portHex, ok := strings.Cut(raw, ":")
	if !ok {
		return netip.AddrPort{}, errors.New("invalid /proc endpoint")
	}
	port, err := strconv.ParseUint(portHex, 16, 16)
	if err != nil {
		return netip.AddrPort{}, err
	}
	bytes, err := hex.DecodeString(addressHex)
	if err != nil || (!ipv6 && len(bytes) != 4) || (ipv6 && len(bytes) != 16) {
		return netip.AddrPort{}, errors.New("invalid /proc address")
	}
	// /proc/net stores each 32-bit word in host byte order.
	for offset := 0; offset < len(bytes); offset += 4 {
		bytes[offset], bytes[offset+3] = bytes[offset+3], bytes[offset]
		bytes[offset+1], bytes[offset+2] = bytes[offset+2], bytes[offset+1]
	}
	var address netip.Addr
	if ipv6 {
		var value [16]byte
		copy(value[:], bytes)
		address = netip.AddrFrom16(value)
	} else {
		var value [4]byte
		copy(value[:], bytes)
		address = netip.AddrFrom4(value)
	}
	return netip.AddrPortFrom(address.Unmap(), uint16(port)), nil
}

func findLinuxInodeProcess(inode uint64) (packetProcess, error) {
	target := "socket:[" + strconv.FormatUint(inode, 10) + "]"
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return packetProcess{}, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pidValue, err := strconv.ParseUint(entry.Name(), 10, 32)
		if err != nil || pidValue == 0 {
			continue
		}
		fdDir := filepath.Join("/proc", entry.Name(), "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil || link != target {
				continue
			}
			executable, err := os.Readlink(filepath.Join("/proc", entry.Name(), "exe"))
			if err != nil {
				return packetProcess{}, fmt.Errorf("读取 PID %d 可执行文件失败: %w", pidValue, err)
			}
			return packetProcess{pid: uint32(pidValue), path: strings.TrimSuffix(executable, " (deleted)")}, nil
		}
	}
	return packetProcess{}, fmt.Errorf("没有找到持有 socket inode %d 的进程", inode)
}

func LookupLocalProcess(network string, source, destination netip.AddrPort) (uint32, string, error) {
	protocol := Protocol(strings.ToLower(strings.TrimSpace(network)))
	if protocol != ProtoTCP && protocol != ProtoUDP {
		return 0, "", fmt.Errorf("unsupported network %q", network)
	}
	process, err := newLinuxProcessResolver().lookup(protocol, source, destination)
	return process.pid, process.path, err
}
