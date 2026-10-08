package rdp

import (
	"context"
	"errors"
	"log"
	"net/netip"
	"sync"
	"time"

	"relayproxy/server/repository"
)

// Audits and cleanup operations share a FIFO writer queue so that clearing
// historical records is not reversed by audit events already queued.
type rdpSecurityAuditTask struct {
	entry *repository.RDPSecurityLog
	clearIP string
	clearAll bool
	reply chan rdpSecurityClearResult
}

type rdpSecurityClearResult struct {
	deleted int64
	err error
}

type securityWindow struct {
	began time.Time
	count int
}

type compiledBan struct {
	record repository.RDPSecurityBan
	prefix netip.Prefix
}

// SecurityManager only protects public RDP ingress sockets. It deliberately
// does not ban authenticated Agent tunnels or P2P traffic by source NAT IP.
type SecurityManager struct {
	db                *repository.DB
	mu                sync.Mutex
	bans              []compiledBan
	rules             []repository.RDPSecurityRule
	counters          map[string]securityWindow
	logs              chan repository.RDPSecurityLog
	stopping          chan struct{}
	done              chan struct{}
	closeOnce         sync.Once
}

func NewSecurityManager(db *repository.DB) (*SecurityManager, error) {
	s := &SecurityManager{db: db, counters: make(map[string]securityWindow),
		logs: make(chan rdpSecurityAuditTask, 4096), stopping: make(chan struct{}), done: make(chan struct{})}
	if err := s.Reload(); err != nil {
		return nil, err
	}
	go s.writeLoop()
	return s, nil
}

// Reload is called on every administrative security mutation. Expiry is
// checked on each decision even if no mutation happens.
func (s *SecurityManager) Reload() error {
	entries, err := s.db.ListActiveRDPSecurityBans()
	if err != nil {
		return err
	}
	rules, err := s.db.ListRDPSecurityRules()
	if err != nil {
		return err
	}
	bans := make([]compiledBan, 0, len(entries))
	for _, entry := range entries {
		prefix, err := netip.ParsePrefix(entry.CIDR)
		if err != nil {
			return err
		}
		bans = append(bans, compiledBan{record: entry, prefix: prefix})
	}
	s.mu.Lock()
	s.bans = bans
	s.rules = rules
	s.mu.Unlock()
	return nil
}

// Admit evaluates operator bans and existing automatic bans first, then
// counts new TCP connections against sliding fixed-window thresholds.
// UDP packets never increase the TCP connection counter.
func (s *SecurityManager) Admit(ingressID, sourceIP string, countTCP bool) (bool, string) {
	addr, err := netip.ParseAddr(sourceIP)
	if err != nil {
		return false, "INVALID_SOURCE_IP"
	}
	addr = addr.Unmap()
	now := time.Now()
	s.mu.Lock()
	allowAuto := false
	for _, ban := range s.bans {
		if ban.record.IngressID != "" && ban.record.IngressID != ingressID {
			continue
		}
		if ban.record.ExpiresAt != nil && !now.Before(*ban.record.ExpiresAt) {
			continue
		}
		if !ban.prefix.Contains(addr) {
			continue
		}
		if ban.record.Kind == "manual" {
			s.mu.Unlock()
			return false, "MANUAL_IP_BAN"
		}
		if ban.record.Kind == "allow" {
			allowAuto = true
		}
	}
	if !allowAuto {
		for _, ban := range s.bans {
			if ban.record.Kind != "auto" || (ban.record.IngressID != "" && ban.record.IngressID != ingressID) {
				continue
			}
			if ban.record.ExpiresAt != nil && !now.Before(*ban.record.ExpiresAt) {
				continue
			}
			if ban.prefix.Contains(addr) {
				s.mu.Unlock()
				return false, "AUTO_IP_BAN"
			}
		}
	}
	if !countTCP || allowAuto {
		s.mu.Unlock()
		return true, ""
	}
	// Keep the counter map bounded even under large source-address churn.
	// A hard cap is necessary: an attacker can otherwise keep creating new
	// source keys faster than the configured windows expire.
	if len(s.counters) >= 8192 {
		for key, window := range s.counters {
			if now.Sub(window.began) > 5*time.Minute {
				delete(s.counters, key)
			}
		}
		if len(s.counters) >= 8192 {
			for key := range s.counters {
				delete(s.counters, key)
				if len(s.counters) <= 4096 {
					break
				}
			}
		}
	}
	for _, rule := range s.rules {
		if !rule.Enabled || rule.Threshold < 1 || rule.WindowSeconds < 1 {
			continue
		}
		key := ingressID + "|" + addr.String() + "|" + rule.ID
		window := s.counters[key]
		if window.began.IsZero() || now.Sub(window.began) >= time.Duration(rule.WindowSeconds)*time.Second {
			window = securityWindow{began: now}
		}
		window.count++
		s.counters[key] = window
		if window.count <= rule.Threshold {
			continue
		}
		expires := now.Add(time.Duration(rule.BanSeconds) * time.Second).UTC()
		entry := repository.RDPSecurityBan{CIDR: netip.PrefixFrom(addr, addr.BitLen()).String(),
			IngressID: ingressID, Kind: "auto", Reason: rule.ID, Actor: "system", CreatedAt: now.UTC(), ExpiresAt: &expires}
		// Prevent concurrent attempts from slipping through while SQLite
		// persists the ban. Database work is done without the mutex held.
		s.bans = append(s.bans, compiledBan{record: entry, prefix: netip.PrefixFrom(addr, addr.BitLen())})
		delete(s.counters, key)
		s.mu.Unlock()
		if _, err := s.db.CreateRDPSecurityBan(entry.CIDR, ingressID, "auto", rule.ID, "system", &expires); err != nil {
			log.Printf("[RDP Security] Failed to persist automatic ban: %v", err)
		}
		return false, "AUTO_BAN_" + rule.ID
	}
	s.mu.Unlock()
	return true, ""
}

// Record never blocks the incoming RDP connection on a SQLite transaction.
// If the bounded queue is full, the attempted audit write is logged.
// Record never blocks an ingress socket on a SQLite write.
func (s *SecurityManager) Record(entry repository.RDPSecurityLog) {
	if s == nil { return }
	select {
	case s.logs <- rdpSecurityAuditTask{entry: &entry}:
	default:
		log.Printf("[RDP Security] audit queue full, dropping connection record")
	}
}

// ClearLogs is serialized behind all previously queued audit writes. A
// source-specific or global clear also suppresses late completion events for
// connections which began before the clear (so old rows cannot reappear).
func (s *SecurityManager) ClearLogs(ctx context.Context, sourceIP string) (int64, error) {
	if s == nil { return 0, errors.New("RDP security unavailable") }
	task := rdpSecurityAuditTask{clearIP:sourceIP,clearAll:sourceIP=="",reply:make(chan rdpSecurityClearResult,1)}
	select {
	case s.logs <- task:
	case <-ctx.Done(): return 0,ctx.Err()
	case <-s.stopping: return 0,errors.New("RDP security is stopping")
	}
	select {
	case result:=<-task.reply: return result.deleted,result.err
	case <-ctx.Done(): return 0,ctx.Err()
	}
}

func (s *SecurityManager) writeLoop() {
	defer close(s.done)
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	var globalClearedAt time.Time
	sourceClearedAt := make(map[string]time.Time)
	handle := func(task rdpSecurityAuditTask) {
		if task.entry != nil {
			entry:=*task.entry
			cutoff:=globalClearedAt
			if at:=sourceClearedAt[entry.SourceIP];at.After(cutoff){cutoff=at}
			if !cutoff.IsZero() && !entry.StartedAt.After(cutoff) {
				return
			}
			if err:=s.db.InsertRDPSecurityLog(entry);err!=nil{
				log.Printf("[RDP Security] Failed to write connection audit: %v",err)
			}
			return
		}
		if task.reply!=nil {
			deleted,err:=s.db.DeleteRDPSecurityLogs(task.clearIP)
			if err==nil {
				clearedAt:=time.Now().UTC()
				if task.clearAll {
					globalClearedAt=clearedAt
					clear(sourceClearedAt)
				} else {
					sourceClearedAt[task.clearIP]=clearedAt
				}
				// Cleanup requests are administrative, but keep per-IP memory bounded.
				if len(sourceClearedAt)>8192 {clear(sourceClearedAt)}
			}
			task.reply<-rdpSecurityClearResult{deleted:deleted,err:err}
		}
	}
	for {
		select {
		case task:=<-s.logs:
			handle(task)
		case <-ticker.C:
			if err := s.db.PruneRDPSecurityLogs(time.Now().Add(-30 * 24 * time.Hour)); err != nil {
				log.Printf("[RDP Security] Failed to prune expired logs: %v", err)
			}
		case <-s.stopping:
			for {
				select {
				case task:=<-s.logs: handle(task)
				default: return
				}
			}
		}
	}
}

func (s *SecurityManager) Close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() { close(s.stopping); <-s.done })
}
