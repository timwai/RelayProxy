package rdp

import (
    "log"
    "net/netip"
    "sync"
    "time"

    "relayproxy/server/repository"
)

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
    db *repository.DB
    mu sync.Mutex
    bans []compiledBan
    rules []repository.RDPSecurityRule
    counters map[string]securityWindow
    logs chan repository.RDPSecurityLog
    stopping chan struct{}
    done chan struct{}
    closeOnce sync.Once
}

func NewSecurityManager(db *repository.DB) (*SecurityManager,error) {
    s := &SecurityManager{db:db,counters:make(map[string]securityWindow),
        logs:make(chan repository.RDPSecurityLog,4096),stopping:make(chan struct{}),done:make(chan struct{})}
    if err := s.Reload(); err != nil { return nil,err }
    go s.writeLoop()
    return s,nil
}

// Reload is called on every administrative security mutation. Expiry is
// checked on each decision even if no mutation happens.
func (s *SecurityManager) Reload() error {
    entries,err := s.db.ListActiveRDPSecurityBans()
    if err != nil { return err }
    rules,err := s.db.ListRDPSecurityRules()
    if err != nil { return err }
    bans := make([]compiledBan,0,len(entries))
    for _,entry := range entries {
        prefix,err := netip.ParsePrefix(entry.CIDR)
        if err != nil { return err }
        bans = append(bans,compiledBan{record:entry,prefix:prefix})
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
func (s *SecurityManager) Admit(ingressID,sourceIP string, countTCP bool) (bool,string) {
    addr,err := netip.ParseAddr(sourceIP)
    if err != nil { return false,"INVALID_SOURCE_IP" }
    addr = addr.Unmap()
    now := time.Now()
    s.mu.Lock()
    allowAuto := false
    for _,ban := range s.bans {
        if ban.record.IngressID != "" && ban.record.IngressID != ingressID { continue }
        if ban.record.ExpiresAt != nil && !now.Before(*ban.record.ExpiresAt) { continue }
        if !ban.prefix.Contains(addr) { continue }
        if ban.record.Kind == "manual" {
            s.mu.Unlock()
            return false,"MANUAL_IP_BAN"
        }
        if ban.record.Kind == "allow" { allowAuto = true }
    }
    if !allowAuto {
        for _,ban := range s.bans {
            if ban.record.Kind != "auto" || (ban.record.IngressID != "" && ban.record.IngressID != ingressID) { continue }
            if ban.record.ExpiresAt != nil && !now.Before(*ban.record.ExpiresAt) { continue }
            if ban.prefix.Contains(addr) {
                s.mu.Unlock()
                return false,"AUTO_IP_BAN"
            }
        }
    }
    if !countTCP || allowAuto {
        s.mu.Unlock()
        return true,""
    }
    // Keep the counter map bounded even under large source-address churn.
    if len(s.counters) > 8192 {
        for key,window := range s.counters {
            if now.Sub(window.began) > 5*time.Minute { delete(s.counters,key) }
        }
    }
    for _,rule := range s.rules {
        if !rule.Enabled || rule.Threshold < 1 || rule.WindowSeconds < 1 { continue }
        key := ingressID + "|" + addr.String() + "|" + rule.ID
        window := s.counters[key]
        if window.began.IsZero() || now.Sub(window.began) >= time.Duration(rule.WindowSeconds)*time.Second {
            window = securityWindow{began:now}
        }
        window.count++
        s.counters[key] = window
        if window.count <= rule.Threshold { continue }
        expires := now.Add(time.Duration(rule.BanSeconds)*time.Second).UTC()
        entry := repository.RDPSecurityBan{CIDR:netip.PrefixFrom(addr,addr.BitLen()).String(),
            IngressID:ingressID,Kind:"auto",Reason:rule.ID,Actor:"system",CreatedAt:now.UTC(),ExpiresAt:&expires}
        // Prevent concurrent attempts from slipping through while SQLite
        // persists the ban. Database work is done without the mutex held.
        s.bans = append(s.bans,compiledBan{record:entry,prefix:netip.PrefixFrom(addr,addr.BitLen())})
        delete(s.counters,key)
        s.mu.Unlock()
        if _,err := s.db.CreateRDPSecurityBan(entry.CIDR,ingressID,"auto",rule.ID,"system",&expires); err != nil {
            log.Printf("[RDP Security] Failed to persist automatic ban: %v",err)
        }
        return false,"AUTO_BAN_"+rule.ID
    }
    s.mu.Unlock()
    return true,""
}

// Record never blocks the incoming RDP connection on a SQLite transaction.
// If the bounded queue is full, the attempted audit write is logged.
func (s *SecurityManager) Record(entry repository.RDPSecurityLog) {
    if s == nil { return }
    select {
    case s.logs <- entry:
    default:
        log.Printf("[RDP Security] audit queue full, dropping connection record")
    }
}

func (s *SecurityManager) writeLoop() {
    defer close(s.done)
    ticker := time.NewTicker(24*time.Hour)
    defer ticker.Stop()
    write := func(entry repository.RDPSecurityLog) {
        if err := s.db.InsertRDPSecurityLog(entry); err != nil {
            log.Printf("[RDP Security] Failed to write connection audit: %v",err)
        }
    }
    for {
        select {
        case entry := <-s.logs:
            write(entry)
        case <-ticker.C:
            if err := s.db.PruneRDPSecurityLogs(time.Now().Add(-30*24*time.Hour)); err != nil {
                log.Printf("[RDP Security] Failed to prune expired logs: %v",err)
            }
        case <-s.stopping:
            for {
                select {
                case entry := <-s.logs: write(entry)
                default: return
                }
            }
        }
    }
}

func (s *SecurityManager) Close() {
    if s == nil { return }
    s.closeOnce.Do(func(){ close(s.stopping); <-s.done })
}
