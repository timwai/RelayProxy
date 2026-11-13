package rdp

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"relayproxy/server/repository"
)

// maxSecurityCounters bounds the total number of live rule counters across
// all rules. Eviction removes expired windows first and single hits next, so
// long-window (low-frequency) evidence survives address churn.
const maxSecurityCounters = 65536

// Audits and cleanup operations share a FIFO writer queue so that clearing
// historical records is not reversed by audit events already queued.
type rdpSecurityAuditTask struct {
	entry     *repository.RDPSecurityLog
	clearIP   string
	clearCIDR string
	clearAll  bool
	reply     chan rdpSecurityClearResult
}

type rdpSecurityClearResult struct {
	deleted int64
	err     error
}

// securityWindow is a sliding-window counter: the previous fixed bucket is
// weighted by how much of it still overlaps the sliding window. It needs
// O(1) memory per key and has no boundary blind spot, so slow sources that
// straddle two buckets are still counted.
type securityWindow struct {
	start time.Time
	cur   int
	prev  int
}

func (w *securityWindow) roll(now time.Time, window time.Duration) {
	if w.start.IsZero() {
		w.start = now
		return
	}
	elapsed := now.Sub(w.start)
	if elapsed < 0 {
		return
	}
	if elapsed >= 2*window {
		w.start, w.cur, w.prev = now, 0, 0
		return
	}
	if elapsed >= window {
		w.prev, w.cur = w.cur, 0
		w.start = w.start.Add(window)
	}
}

func (w *securityWindow) estimate(now time.Time, window time.Duration) float64 {
	weight := 1 - float64(now.Sub(w.start))/float64(window)
	if weight < 0 {
		weight = 0
	} else if weight > 1 {
		weight = 1
	}
	return float64(w.prev)*weight + float64(w.cur)
}

func (w *securityWindow) count(now time.Time, window time.Duration) int {
	return int(math.Round(w.estimate(now, window)))
}

func (w *securityWindow) expired(now time.Time, window time.Duration) bool {
	return w.start.IsZero() || now.Sub(w.start) >= 2*window
}

type compiledBan struct {
	record repository.RDPSecurityBan
	prefix netip.Prefix
}

// ruleState keeps counters per rule. A rule whose counting shape changes is
// re-seeded from SQLite instead of reusing incompatible counters.
type ruleState struct {
	rule      repository.RDPSecurityRule
	signature string
	counters  map[string]*securityWindow
}

func (st *ruleState) window() time.Duration {
	return time.Duration(st.rule.WindowSeconds) * time.Second
}

func ruleSignature(rule repository.RDPSecurityRule) string {
	return fmt.Sprintf("%t|%d|%s|%s|%t|%d|%d", rule.Enabled, rule.WindowSeconds, rule.Scope, rule.Metric, rule.CountUDP, rule.PrefixV4, rule.PrefixV6)
}

// ruleKey maps a source to the rule's counting bucket: optionally aggregated
// to a network prefix and optionally shared by all entrances.
func ruleKey(rule repository.RDPSecurityRule, ingressID string, addr netip.Addr) (string, netip.Prefix) {
	bits := rule.PrefixV4
	if addr.Is6() {
		bits = rule.PrefixV6
	}
	if bits <= 0 || bits > addr.BitLen() {
		bits = addr.BitLen()
	}
	prefix := netip.PrefixFrom(addr, bits).Masked()
	scope := ingressID
	if rule.Scope == repository.RDPRuleScopeGlobal {
		scope = ""
	}
	return scope + "|" + prefix.String(), prefix
}

// SecurityManager only protects public RDP ingress sockets. It deliberately
// does not ban authenticated Agent tunnels or P2P traffic by source NAT IP.
type SecurityManager struct {
	db           *repository.DB
	countryDB    *CountryDB
	mu           sync.Mutex
	bans         []compiledBan
	rules        []*ruleState
	counterTotal int
	logs         chan rdpSecurityAuditTask
	stopping     chan struct{}
	done         chan struct{}
	closeOnce    sync.Once
}

func NewSecurityManager(db *repository.DB) (*SecurityManager, error) {
	return NewSecurityManagerWithGeoIP(db, "")
}

// NewSecurityManagerWithGeoIP enforces a China-mainland-only admission policy
// when a validated offline database path is configured. No database means
// the existing RDP policy is unchanged.
func NewSecurityManagerWithGeoIP(db *repository.DB, countryCSVPath string) (*SecurityManager, error) {
	var countryDB *CountryDB
	if countryCSVPath != "" {
		var err error
		countryDB, err = LoadCountryDB(countryCSVPath)
		if err != nil {
			return nil, fmt.Errorf("RDP geo country policy unavailable: %w", err)
		}
	}
	s := &SecurityManager{db: db, countryDB: countryDB,
		logs: make(chan rdpSecurityAuditTask, 4096), stopping: make(chan struct{}), done: make(chan struct{})}
	if err := s.Reload(); err != nil {
		return nil, err
	}
	go s.writeLoop()
	return s, nil
}

// SetCountryDB swaps an immutable verified GeoIP index without stopping ingress.
func (s *SecurityManager) SetCountryDB(db *CountryDB) {
	s.mu.Lock()
	s.countryDB = db
	s.mu.Unlock()
}

// Reload is called on every administrative security mutation. Expiry is
// checked on each decision even if no mutation happens. Counters of
// unchanged rules are kept; new or reshaped rules are seeded from the
// retained audit so restarts do not reset low-frequency evidence.
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
	previous := make(map[string]*ruleState, len(s.rules))
	for _, st := range s.rules {
		previous[st.rule.ID] = st
	}
	s.mu.Unlock()

	now := time.Now()
	escalationRules := make(map[string]bool)
	for _, rule := range rules {
		if rule.Metric == repository.RDPRuleMetricBans {
			escalationRules[rule.ID] = true
		}
	}
	seeds := make(map[string]map[string]*securityWindow)
	for _, rule := range rules {
		if old, ok := previous[rule.ID]; ok && old.signature == ruleSignature(rule) {
			continue
		}
		if !rule.Enabled {
			continue
		}
		seed, err := s.seedRule(rule, now, escalationRules)
		if err != nil {
			// A failed seed only loses history; enforcement continues.
			log.Printf("[RDP Security] failed to restore counters for rule %s: %v", rule.ID, err)
			seed = make(map[string]*securityWindow)
		}
		seeds[rule.ID] = seed
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	states := make([]*ruleState, 0, len(rules))
	total := 0
	for _, rule := range rules {
		signature := ruleSignature(rule)
		counters := seeds[rule.ID]
		if old, ok := previous[rule.ID]; ok && old.signature == signature {
			counters = old.counters
		}
		if counters == nil {
			counters = make(map[string]*securityWindow)
		}
		total += len(counters)
		states = append(states, &ruleState{rule: rule, signature: signature, counters: counters})
	}
	s.bans = bans
	s.rules = states
	s.counterTotal = total
	return nil
}

// seedRule rebuilds a rule's counters from SQLite: connection rules from the
// 30-day connection audit, escalation rules from automatic ban history.
// Seeded counts populate the previous bucket and decay over one window.
func (s *SecurityManager) seedRule(rule repository.RDPSecurityRule, now time.Time, escalationRules map[string]bool) (map[string]*securityWindow, error) {
	since := now.Add(-time.Duration(rule.WindowSeconds) * time.Second)
	counts := make(map[string]int)
	switch rule.Metric {
	case repository.RDPRuleMetricBans:
		bans, err := s.db.ListRDPAutoBansSince(since)
		if err != nil {
			return nil, err
		}
		for _, ban := range bans {
			if escalationRules[ban.Reason] {
				continue
			}
			prefix, err := netip.ParsePrefix(ban.CIDR)
			if err != nil {
				continue
			}
			key, _ := ruleKey(rule, ban.IngressID, prefix.Addr().Unmap())
			counts[key]++
		}
	default:
		rows, err := s.db.CountRDPSecurityLogsSince(since, rule.CountUDP)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			addr, err := netip.ParseAddr(row.SourceIP)
			if err != nil {
				continue
			}
			key, _ := ruleKey(rule, row.IngressID, addr.Unmap())
			counts[key] += row.Count
		}
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	// Keep the strongest evidence when history exceeds the per-rule budget.
	limit := maxSecurityCounters / 4
	if len(keys) > limit {
		sort.Slice(keys, func(i, j int) bool { return counts[keys[i]] > counts[keys[j]] })
		keys = keys[:limit]
	}
	seed := make(map[string]*securityWindow, len(keys))
	for _, key := range keys {
		seed[key] = &securityWindow{start: now, prev: counts[key]}
	}
	return seed, nil
}

// counterLocked returns the live window for a key, evicting when the global
// budget is reached.
func (s *SecurityManager) counterLocked(st *ruleState, key string, now time.Time) *securityWindow {
	if window, ok := st.counters[key]; ok {
		return window
	}
	if s.counterTotal >= maxSecurityCounters {
		s.evictLocked(now)
	}
	window := &securityWindow{}
	st.counters[key] = window
	s.counterTotal++
	return window
}

func (s *SecurityManager) evictLocked(now time.Time) {
	target := maxSecurityCounters * 3 / 4
	passes := []func(*ruleState, *securityWindow) bool{
		func(st *ruleState, w *securityWindow) bool { return w.expired(now, st.window()) },
		func(st *ruleState, w *securityWindow) bool { return w.estimate(now, st.window()) < 2 },
		func(*ruleState, *securityWindow) bool { return true },
	}
	for _, evict := range passes {
		for _, st := range s.rules {
			for key, window := range st.counters {
				if s.counterTotal <= target {
					return
				}
				if evict(st, window) {
					delete(st.counters, key)
					s.counterTotal--
				}
			}
		}
	}
}

func (s *SecurityManager) deleteCounterLocked(st *ruleState, key string) {
	if _, ok := st.counters[key]; ok {
		delete(st.counters, key)
		s.counterTotal--
	}
}

// moreSevere orders triggered rules for the returned rejection reason.
func moreSevere(a, b repository.RDPSecurityRule) bool {
	if a.Permanent() != b.Permanent() {
		return a.Permanent()
	}
	if a.BanSeconds != b.BanSeconds {
		return a.BanSeconds > b.BanSeconds
	}
	return a.Scope == repository.RDPRuleScopeGlobal && b.Scope != repository.RDPRuleScopeGlobal
}

// addAutoBanLocked records a rule ban in memory immediately so concurrent
// attempts cannot slip through while SQLite persists it.
func (s *SecurityManager) addAutoBanLocked(rule repository.RDPSecurityRule, ingressID string, prefix netip.Prefix, now time.Time) repository.RDPSecurityBan {
	entry := repository.RDPSecurityBan{CIDR: prefix.String(), IngressID: ingressID, Kind: "auto", Reason: rule.ID,
		Actor: "system", CreatedAt: now.UTC(), ClearLogs: rule.ClearLogs}
	if rule.Scope == repository.RDPRuleScopeGlobal {
		entry.IngressID = ""
	}
	if !rule.Permanent() {
		expires := now.Add(time.Duration(rule.BanSeconds) * time.Second).UTC()
		entry.ExpiresAt = &expires
	}
	s.bans = append(s.bans, compiledBan{record: entry, prefix: prefix})
	return entry
}

// Admit evaluates operator bans and existing automatic bans first, then
// counts the attempt against every enabled connection rule. isTCP=false is a
// new UDP association, counted only by rules with CountUDP. Every rule that
// exceeds its threshold creates its own ban; each automatic ban also feeds
// escalation ("bans" metric) rules such as repeat-offender.
func (s *SecurityManager) Admit(ingressID, sourceIP string, isTCP bool) (bool, string) {
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
	// Country bans are independent of the automatic rate-limit allowlist.
	// Persist a global /32 or /128, permanent (no expiry) auto-ban before
	// admitting any public RDP TCP or UDP session from outside CN.
	if s.countryDB != nil && isPublicGeoIP(addr) {
		country := s.countryDB.Lookup(addr)
		if country == "" {
			s.mu.Unlock()
			return false, "GEO_COUNTRY_UNKNOWN"
		}
		if country != "CN" {
			reason := "GEO_NON_CN:" + country
			cidr := netip.PrefixFrom(addr, addr.BitLen()).String()
			for _, existing := range s.bans {
				if existing.record.Kind == "auto" && existing.record.IngressID == "" &&
					existing.record.CIDR == cidr && existing.record.ExpiresAt == nil &&
					strings.HasPrefix(existing.record.Reason, "GEO_NON_CN:") {
					s.mu.Unlock()
					return false, reason
				}
			}
			entry := repository.RDPSecurityBan{CIDR: cidr, Kind: "auto",
				Reason: reason, Actor: "system", CreatedAt: now.UTC()}
			s.bans = append(s.bans, compiledBan{record: entry, prefix: netip.PrefixFrom(addr, addr.BitLen())})
			s.mu.Unlock()
			if _, err := s.db.CreateRDPSecurityBan(cidr, "", "auto", reason, "system", nil); err != nil {
				log.Printf("[RDP Security] failed to persist permanent country ban for %s: %v", addr, err)
			}
			return false, reason
		}
	}
	if allowAuto {
		s.mu.Unlock()
		return true, ""
	}
	for _, ban := range s.bans {
		// A disabled GeoIP feature also disables existing geo-generated
		// automatic bans; operator bans still apply.
		if s.countryDB == nil && strings.HasPrefix(ban.record.Reason, "GEO_NON_CN:") {
			continue
		}
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

	var created []repository.RDPSecurityBan
	var strongest *repository.RDPSecurityRule
	for _, st := range s.rules {
		rule := st.rule
		if !rule.Enabled || rule.Metric != repository.RDPRuleMetricConnections || (!isTCP && !rule.CountUDP) {
			continue
		}
		key, prefix := ruleKey(rule, ingressID, addr)
		window := s.counterLocked(st, key, now)
		window.roll(now, st.window())
		window.cur++
		if window.count(now, st.window()) <= rule.Threshold {
			continue
		}
		s.deleteCounterLocked(st, key)
		created = append(created, s.addAutoBanLocked(rule, ingressID, prefix, now))
		if strongest == nil || moreSevere(rule, *strongest) {
			r := rule
			strongest = &r
		}
	}
	if strongest == nil {
		s.mu.Unlock()
		return true, ""
	}
	// One admission event that produced automatic bans counts once towards
	// each escalation rule.
	for _, st := range s.rules {
		rule := st.rule
		if !rule.Enabled || rule.Metric != repository.RDPRuleMetricBans {
			continue
		}
		key, prefix := ruleKey(rule, ingressID, addr)
		window := s.counterLocked(st, key, now)
		window.roll(now, st.window())
		window.cur++
		if window.count(now, st.window()) <= rule.Threshold {
			continue
		}
		s.deleteCounterLocked(st, key)
		created = append(created, s.addAutoBanLocked(rule, ingressID, prefix, now))
		if moreSevere(rule, *strongest) {
			r := rule
			strongest = &r
		}
	}
	s.mu.Unlock()
	// Database work is done without the mutex held.
	for _, entry := range created {
		if _, err := s.db.CreateRDPSecurityBanWithOptions(repository.RDPSecurityBanInput{CIDR: entry.CIDR, IngressID: entry.IngressID,
			Kind: "auto", Reason: entry.Reason, Actor: "system", ExpiresAt: entry.ExpiresAt, ClearLogs: entry.ClearLogs}); err != nil {
			log.Printf("[RDP Security] Failed to persist automatic ban: %v", err)
		}
		if entry.ClearLogs {
			s.enqueueClear(entry.CIDR)
		}
	}
	return false, "AUTO_BAN_" + strongest.ID
}

// suppressedLocked reports whether audit rows for a source must be dropped
// because an active ban with ClearLogs covers it. Allowlisted sources are
// never hidden behind an automatic ban.
func (s *SecurityManager) suppressedLocked(ingressID string, addr netip.Addr, now time.Time) bool {
	allowAuto, autoHit := false, false
	for _, ban := range s.bans {
		if !ban.record.ClearLogs && ban.record.Kind != "allow" {
			continue
		}
		if ban.record.IngressID != "" && ingressID != "" && ban.record.IngressID != ingressID {
			continue
		}
		if ban.record.ExpiresAt != nil && !now.Before(*ban.record.ExpiresAt) {
			continue
		}
		if !ban.prefix.Contains(addr) {
			continue
		}
		switch ban.record.Kind {
		case "allow":
			allowAuto = true
		case "manual":
			return true
		case "auto":
			if s.countryDB == nil && strings.HasPrefix(ban.record.Reason, "GEO_NON_CN:") {
				continue
			}
			autoHit = true
		}
	}
	return autoHit && !allowAuto
}

// Record never blocks the incoming RDP connection on a SQLite transaction.
// If the bounded queue is full, the attempted audit write is logged.
// Record never blocks an ingress socket on a SQLite write.
func (s *SecurityManager) Record(entry repository.RDPSecurityLog) {
	if s == nil {
		return
	}
	addr, addrErr := netip.ParseAddr(entry.SourceIP)
	if addrErr == nil {
		addr = addr.Unmap()
	}
	s.mu.Lock()
	countryDB := s.countryDB
	suppressed := addrErr == nil && s.suppressedLocked(entry.IngressID, addr, time.Now())
	s.mu.Unlock()
	if suppressed {
		return
	}
	if entry.SourceCountry == "" && addrErr == nil && isPublicGeoIP(addr) && countryDB != nil {
		entry.SourceCountry = countryDB.Lookup(addr)
	}
	select {
	case s.logs <- rdpSecurityAuditTask{entry: &entry}:
	default:
		log.Printf("[RDP Security] audit queue full, dropping connection record")
	}
}

// enqueueClear schedules a purge behind already queued audit rows without
// blocking an ingress goroutine (the UDP read loop calls Admit).
func (s *SecurityManager) enqueueClear(cidr string) {
	task, err := clearTask(cidr)
	if err != nil {
		log.Printf("[RDP Security] invalid clear target %q: %v", cidr, err)
		return
	}
	select {
	case s.logs <- task:
	default:
		go func() {
			select {
			case s.logs <- task:
			case <-s.stopping:
			}
		}()
	}
}

func clearTask(target string) (rdpSecurityAuditTask, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return rdpSecurityAuditTask{clearAll: true}, nil
	}
	if addr, err := netip.ParseAddr(target); err == nil {
		return rdpSecurityAuditTask{clearIP: addr.Unmap().String()}, nil
	}
	prefix, err := netip.ParsePrefix(target)
	if err != nil {
		return rdpSecurityAuditTask{}, errors.New("invalid IP or CIDR")
	}
	prefix = prefix.Masked()
	if prefix.IsSingleIP() {
		return rdpSecurityAuditTask{clearIP: prefix.Addr().Unmap().String()}, nil
	}
	return rdpSecurityAuditTask{clearCIDR: prefix.String()}, nil
}

// ClearLogs is serialized behind all previously queued audit writes. A
// source-specific or global clear also suppresses late completion events for
// connections which began before the clear (so old rows cannot reappear).
// target may be empty (all), an IP address or a CIDR.
func (s *SecurityManager) ClearLogs(ctx context.Context, target string) (int64, error) {
	if s == nil {
		return 0, errors.New("RDP security unavailable")
	}
	task, err := clearTask(target)
	if err != nil {
		return 0, err
	}
	task.reply = make(chan rdpSecurityClearResult, 1)
	select {
	case s.logs <- task:
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-s.stopping:
		return 0, errors.New("RDP security is stopping")
	}
	select {
	case result := <-task.reply:
		return result.deleted, result.err
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

type prefixClear struct {
	prefix netip.Prefix
	at     time.Time
}

func (s *SecurityManager) writeLoop() {
	defer close(s.done)
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	var globalClearedAt time.Time
	sourceClearedAt := make(map[string]time.Time)
	prefixClearedAt := make([]prefixClear, 0)
	handle := func(task rdpSecurityAuditTask) {
		if task.entry != nil {
			entry := *task.entry
			cutoff := globalClearedAt
			if at := sourceClearedAt[entry.SourceIP]; at.After(cutoff) {
				cutoff = at
			}
			if len(prefixClearedAt) > 0 {
				if addr, err := netip.ParseAddr(entry.SourceIP); err == nil {
					for _, cleared := range prefixClearedAt {
						if cleared.at.After(cutoff) && cleared.prefix.Contains(addr.Unmap()) {
							cutoff = cleared.at
						}
					}
				}
			}
			if !cutoff.IsZero() && !entry.StartedAt.After(cutoff) {
				return
			}
			if err := s.db.InsertRDPSecurityLog(entry); err != nil {
				log.Printf("[RDP Security] Failed to write connection audit: %v", err)
			}
			return
		}
		var deleted int64
		var err error
		switch {
		case task.clearCIDR != "":
			deleted, err = s.db.DeleteRDPSecurityLogsByCIDR(task.clearCIDR)
		case task.clearAll:
			deleted, err = s.db.DeleteRDPSecurityLogs("")
		default:
			deleted, err = s.db.DeleteRDPSecurityLogs(task.clearIP)
		}
		if err == nil {
			clearedAt := time.Now().UTC()
			switch {
			case task.clearAll:
				globalClearedAt = clearedAt
				clear(sourceClearedAt)
				prefixClearedAt = prefixClearedAt[:0]
			case task.clearCIDR != "":
				if prefix, parseErr := netip.ParsePrefix(task.clearCIDR); parseErr == nil {
					prefixClearedAt = append(prefixClearedAt, prefixClear{prefix: prefix, at: clearedAt})
				}
			default:
				sourceClearedAt[task.clearIP] = clearedAt
			}
			// Cleanup requests are administrative, but keep per-IP memory bounded.
			if len(sourceClearedAt) > 8192 {
				clear(sourceClearedAt)
			}
			if len(prefixClearedAt) > 1024 {
				prefixClearedAt = append(prefixClearedAt[:0], prefixClearedAt[len(prefixClearedAt)-512:]...)
			}
		} else if task.reply == nil {
			log.Printf("[RDP Security] Failed to clear connection audit: %v", err)
		}
		if task.reply != nil {
			task.reply <- rdpSecurityClearResult{deleted: deleted, err: err}
		}
	}
	for {
		select {
		case task := <-s.logs:
			handle(task)
		case <-ticker.C:
			if err := s.db.PruneRDPSecurityLogs(time.Now().Add(-30 * 24 * time.Hour)); err != nil {
				log.Printf("[RDP Security] Failed to prune expired logs: %v", err)
			}
		case <-s.stopping:
			for {
				select {
				case task := <-s.logs:
					handle(task)
				default:
					return
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
