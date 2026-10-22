package tunnel

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/apernet/quic-go"
)

const addressRaceDelay = 200 * time.Millisecond

// Resolve with the caller's deadline, preserving IPv6 zones and the resolver's
// preferred family. Interleaving prevents a broken family blocking the other.
func resolveQUICAddresses(ctx context.Context, address string) ([]net.UDPAddr, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return nil, "", err
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return nil, "", fmt.Errorf("invalid UDP port %q", portText)
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, host, err
	}
	var primary, secondary []net.UDPAddr
	seen := make(map[string]bool)
	firstV4 := len(ips) > 0 && ips[0].IP.To4() != nil
	for _, ip := range ips {
		key := ip.String()
		if seen[key] {
			continue
		}
		seen[key] = true
		a := net.UDPAddr{IP: ip.IP, Zone: ip.Zone, Port: port}
		if (ip.IP.To4() != nil) == firstV4 {
			primary = append(primary, a)
		} else {
			secondary = append(secondary, a)
		}
	}
	var addresses []net.UDPAddr
	for i := 0; i < len(primary) || i < len(secondary); i++ {
		if i < len(primary) {
			addresses = append(addresses, primary[i])
		}
		if i < len(secondary) {
			addresses = append(addresses, secondary[i])
		}
	}
	if len(addresses) == 0 {
		return nil, host, errors.New("DNS returned no addresses")
	}
	// Bound the number of sockets opened by an untrusted DNS answer.
	if len(addresses) > 16 {
		addresses = addresses[:16]
	}
	return addresses, host, nil
}

type addressDialResult struct {
	session *QUICSession
	err     error
}

func raceQUICAddresses(ctx context.Context, addresses []net.UDPAddr, delay time.Duration, dial func(context.Context, net.UDPAddr) (*QUICSession, error)) (*QUICSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(addresses) == 0 {
		return nil, errors.New("no QUIC addresses")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan addressDialResult, len(addresses))
	next, pending := 0, 0
	start := func() {
		addr := addresses[next]
		next++
		pending++
		go func() { s, err := dial(ctx, addr); results <- addressDialResult{s, err} }()
	}
	cleanup := func(count int) {
		go func() {
			for range count {
				r := <-results
				if r.session != nil {
					_ = r.session.Close()
				}
			}
		}()
	}
	start()
	timer := time.NewTimer(delay)
	defer timer.Stop()
	var failures []error
	for pending > 0 {
		select {
		case <-ctx.Done():
			cleanup(pending)
			return nil, ctx.Err()
		case <-timer.C:
			if next < len(addresses) {
				start()
				timer.Reset(delay)
			}
		case r := <-results:
			pending--
			if r.err == nil && ctx.Err() == nil {
				cancel()
				cleanup(pending)
				return r.session, nil
			}
			if r.session != nil {
				_ = r.session.Close()
			}
			failures = append(failures, r.err)
			if next < len(addresses) {
				start()
				timer.Reset(delay)
			}
		}
	}
	return nil, fmt.Errorf("all QUIC addresses failed: %w", errors.Join(failures...))
}

func dialQUICAddresses(ctx context.Context, address string, tlsConfig *tls.Config, config *quic.Config, direct bool) (*QUICSession, error) {
	start := time.Now()
	addresses, host, err := resolveQUICAddresses(ctx, address)
	dnsTime := time.Since(start)
	if err != nil {
		return nil, fmt.Errorf("resolve QUIC address: %w", err)
	}
	tlsConfig = tlsConfig.Clone()
	if tlsConfig.ServerName == "" {
		tlsConfig.ServerName = strings.Trim(host, "[]")
	}
	handshakeStart := time.Now()
	session, err := raceQUICAddresses(ctx, addresses, addressRaceDelay, func(attemptCtx context.Context, remote net.UDPAddr) (*QUICSession, error) {
		network := "udp6"
		if remote.IP.To4() != nil {
			network = "udp4"
		}
		pc, err := net.ListenUDP(network, nil)
		if err != nil {
			return nil, err
		}
		if direct {
			TuneDirectQUICUDPConn(pc)
		} else {
			TuneQUICUDPConn(pc)
		}
		conn, err := quic.Dial(attemptCtx, pc, &remote, tlsConfig.Clone(), config.Clone())
		if err != nil {
			_ = pc.Close()
			return nil, err
		}
		return newOwnedQUICSession(conn, pc), nil
	})
	if session != nil {
		session.dialTimings = &DialTimings{DNSMS: dnsTime.Milliseconds(), HandshakeMS: time.Since(handshakeStart).Milliseconds(), TotalMS: time.Since(start).Milliseconds(), AddressCount: len(addresses)}
	}
	return session, err
}
