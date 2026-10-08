package rdp

import (
	"errors"
	"fmt"
	"log"
	"net/netip"
	"strings"
	"time"

	"relayproxy/internal/protocol"
	"relayproxy/server/repository"
)

type trackedRDPTCP struct {
	connectionID string
	ingressID    string
	sourceIP     string
	openedAt     time.Time
	endedAt      time.Time
}

func rdpTCPKey(hostID string, port int) string {
	return fmt.Sprintf("%s/%d", hostID, port)
}

// TrackPublicRDPTCP binds the loopback source port selected by the Host Agent
// to the Server-observed public IP. No client-provided IP is trusted.
func (s *SecurityManager) TrackPublicRDPTCP(hostID string, port int, entry repository.RDPSecurityLog) {
	if s == nil || hostID == "" || port < 1 || port > 65535 || entry.ID == "" {
		return
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneTrackedRDPTCPLocked(now)
	if len(s.authConnections) >= 16384 {
		return
	}
	key := rdpTCPKey(hostID, port)
	if len(s.authConnections[key]) >= 4 {
		// A frequently reused source port is too ambiguous for safe correlation.
		return
	}
	s.authConnections[key] = append(s.authConnections[key], trackedRDPTCP{
		connectionID: entry.ID, ingressID: entry.IngressID, sourceIP: entry.SourceIP, openedAt: entry.StartedAt.UTC(),
	})
}

func (s *SecurityManager) FinishPublicRDPTCP(hostID string, port int, connectionID string) {
	if s == nil || hostID == "" || port <= 0 || connectionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := rdpTCPKey(hostID, port)
	for i := range s.authConnections[key] {
		if s.authConnections[key][i].connectionID == connectionID {
			s.authConnections[key][i].endedAt = time.Now().UTC()
		}
	}
}

func (s *SecurityManager) pruneTrackedRDPTCPLocked(now time.Time) {
	if now.Sub(s.lastPortPrune) < 30*time.Second { return }
	s.lastPortPrune = now
	for key, entries := range s.authConnections {
		kept := entries[:0]
		for _, entry := range entries {
			if !entry.endedAt.IsZero() && now.Sub(entry.endedAt) > 90*time.Second {
				continue
			}
			if entry.endedAt.IsZero() && now.Sub(entry.openedAt) > 24*time.Hour {
				continue
			}
			kept = append(kept, entry)
		}
		if len(kept) == 0 {
			delete(s.authConnections, key)
		} else {
			s.authConnections[key] = kept
		}
	}
}

// ReportHostAuthFailure persists a real Windows Security 4625 event even when
// the source cannot be established. Only a unique host+port+time association
// to a Server-observed public TCP connection is eligible for an IP ban.
func (s *SecurityManager) ReportHostAuthFailure(hostID string, event protocol.RDPHostAuthFailure) error {
	if s == nil || s.db == nil || hostID == "" {
		return errors.New("RDP security unavailable")
	}
	if event.RecordID == 0 || event.SourcePort < 0 || event.SourcePort > 65535 ||
		(event.LogonType != 3 && event.LogonType != 10) {
		return errors.New("invalid Windows login failure event")
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(event.SourceAddress))
	if err != nil || !addr.Unmap().IsLoopback() {
		return errors.New("Windows RDP event source is not loopback")
	}
	now := time.Now().UTC()
	if event.ObservedAt.IsZero() || event.ObservedAt.Before(now.Add(-90*time.Second)) || event.ObservedAt.After(now.Add(10*time.Second)) {
		return errors.New("Windows RDP event timestamp outside accepted window")
	}
	username := []rune(event.Username)
	if len(username) > 128 {
		username = username[:128]
	}
	entry := &repository.RDPAuthFailure{
		TargetDeviceID: hostID, EventRecordID: event.RecordID, SourcePort: event.SourcePort,
		LogonType: event.LogonType, Username: string(username), Status: limitRDPEventCode(event.Status),
		SubStatus: limitRDPEventCode(event.SubStatus), OccurredAt: event.ObservedAt.UTC(), ReceivedAt: now,
	}
	s.mu.Lock()
	// Bound the amount of work any authenticated Host can cause by submitting
	// event reports. Excess reports are acknowledged without persisting or
	// triggering new bans; valid low-rate events remain idempotent in SQLite.
	reportWindow := s.authReportWindows[hostID]
	if reportWindow.began.IsZero() || now.Sub(reportWindow.began) >= time.Minute {
		reportWindow = securityWindow{began: now}
	}
	reportWindow.count++
	s.authReportWindows[hostID] = reportWindow
	if len(s.authReportWindows) > 4096 {
		for key, window := range s.authReportWindows {
			if now.Sub(window.began) > time.Minute { delete(s.authReportWindows, key) }
		}
	}
	if reportWindow.count > 240 {
		s.mu.Unlock()
		return nil
	}
	s.pruneTrackedRDPTCPLocked(now)
	candidates := s.authConnections[rdpTCPKey(hostID, event.SourcePort)]
	var matched *trackedRDPTCP
	for _, candidate := range candidates {
		if event.ObservedAt.Before(candidate.openedAt.Add(-2 * time.Second)) {
			continue
		}
		if !candidate.endedAt.IsZero() && event.ObservedAt.After(candidate.endedAt.Add(10*time.Second)) {
			continue
		}
		if matched != nil {
			matched = nil // port reused ambiguously: do not guess the public IP
			break
		}
		copy := candidate
		matched = &copy
	}
	if matched != nil {
		entry.SourceIP, entry.IngressID, entry.ConnectionID, entry.Correlated =
			matched.sourceIP, matched.ingressID, matched.connectionID, true
	}
	s.mu.Unlock()
	added, err := s.db.InsertRDPAuthFailure(entry)
	if err != nil {
		return err
	}
	if !added || !entry.Correlated {
		return nil
	}
	return s.countWindowsLoginFailure(entry)
}

func limitRDPEventCode(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 32 {
		return s[:32]
	}
	return s
}

func (s *SecurityManager) countWindowsLoginFailure(event *repository.RDPAuthFailure) error {
	ip, err := netip.ParseAddr(event.SourceIP)
	if err != nil {
		return err
	}
	ip = ip.Unmap()
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	var rule *repository.RDPSecurityRule
	for i := range s.rules {
		if s.rules[i].ID == "login_failure" && s.rules[i].Enabled {
			rule = &s.rules[i]
			break
		}
	}
	if rule == nil {
		return nil
	}
	for _, ban := range s.bans {
		if ban.record.IngressID != "" && ban.record.IngressID != event.IngressID {
			continue
		}
		if ban.record.ExpiresAt != nil && !now.Before(*ban.record.ExpiresAt) {
			continue
		}
		if !ban.prefix.Contains(ip) {
			continue
		}
		if ban.record.Kind == "allow" || ban.record.Kind == "auto" || ban.record.Kind == "manual" {
			return nil // no escalation for exempt or already banned sources
		}
	}
	if len(s.authCounters) >= 8192 {
		for key, item := range s.authCounters {
			if now.Sub(item.began) > 5*time.Minute {
				delete(s.authCounters, key)
			}
		}
		if len(s.authCounters) >= 8192 {
			clear(s.authCounters)
		}
	}
	key := event.IngressID + "|" + ip.String()
	window := s.authCounters[key]
	if window.began.IsZero() || now.Sub(window.began) >= time.Duration(rule.WindowSeconds)*time.Second {
		window = securityWindow{began: now}
	}
	window.count++
	s.authCounters[key] = window
	if window.count < rule.Threshold {
		return nil
	}
	expires := now.Add(time.Duration(rule.BanSeconds) * time.Second).UTC()
	cidr := netip.PrefixFrom(ip, ip.BitLen()).String()
	record, err := s.db.CreateRDPSecurityBan(cidr, event.IngressID, "auto", "login_failure", "system", &expires)
	if err != nil {
		return err
	}
	s.bans = append(s.bans, compiledBan{record: *record, prefix: netip.PrefixFrom(ip, ip.BitLen())})
	delete(s.authCounters, key)
	log.Printf("[RDP Security] Windows login failures reached threshold; ingress=%s ip=%s duration=%s",
		event.IngressID, ip.String(), time.Duration(rule.BanSeconds)*time.Second)
	return nil
}
