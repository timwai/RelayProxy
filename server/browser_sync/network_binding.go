package browsersync

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// A network binding deliberately reveals the first-party CONNECT hostname to
// the relay, unlike E2EE Cookie policy names. Both browsers must separately
// approve that exact name; the selected online Agent must share the same Identity
// and hold proxy.exit permission. Locally opted-in browser-sharing Agents use
// their dedicated frame; all other authorized remote exits use standard TCP.
type NetworkBinding struct {
	RuleID       string `json:"ruleId"`
	Host         string `json:"host"`
	ExitDeviceID string `json:"exitDeviceId"`
	Status       string `json:"status"`
}

var networkHost = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)*$`)

func validNetworkHost(host string) bool {
	_, _, err := parseNetworkTarget(host)
	return err == nil
}

// Legacy bindings contain a bare hostname and mean TCP/443. New bindings may
// name any legal DNS/IP target and TCP port, without changing stored old rules.
func parseNetworkTarget(target string) (string, uint16, error) {
	host, port := target, 443
	bracketed := strings.HasPrefix(target, "[")
	if strings.Contains(target, ":") && !(strings.HasPrefix(target, "[") && strings.HasSuffix(target, "]")) {
		var rawPort string
		var err error
		host, rawPort, err = net.SplitHostPort(target)
		if err != nil {
			return "", 0, ErrRuleDenied
		}
		port, err = strconv.Atoi(rawPort)
		if err != nil || port < 1 || port > 65535 || strings.Trim(rawPort, "0123456789") != "" {
			return "", 0, ErrRuleDenied
		}
	} else if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String(), uint16(port), nil
	}
	if bracketed || host == "" || len(host) > 253 || host != strings.ToLower(host) || !networkHost.MatchString(host) {
		return "", 0, ErrRuleDenied
	}
	for _, part := range strings.Split(host, ".") {
		if len(part) > 63 {
			return "", 0, ErrRuleDenied
		}
	}
	return host, uint16(port), nil
}

func networkTargetKey(host string, port uint16) string {
	if port == 443 {
		if strings.Contains(host, ":") {
			return "[" + host + "]"
		}
		return host
	}
	return net.JoinHostPort(host, strconv.Itoa(int(port)))
}

func normalizeNetworkTarget(target string) (string, error) {
	host, port, err := parseNetworkTarget(target)
	if err != nil {
		return "", err
	}
	return networkTargetKey(host, port), nil
}

func (s *Store) EnsureNetworkSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS browser_sync_network_bindings(
 rule_id TEXT PRIMARY KEY REFERENCES browser_sync_rules(id) ON DELETE CASCADE,
 host TEXT NOT NULL, exit_id TEXT NOT NULL, status TEXT NOT NULL,
 updated_at TIMESTAMP NOT NULL,
 CHECK(status IN ('offered','active','revoked'))
 )`)
	return err
}

func (s *Store) OfferNetworkBinding(ctx context.Context, sourceID, ruleID, host, exitID string) error {
	if !validNetworkHost(host) || len(exitID) < 5 || len(exitID) > 128 || exitID == "server" {
		return ErrRuleDenied
	}
	host, _ = normalizeNetworkTarget(host)
	if _, err := uuid.Parse(ruleID); err != nil {
		return ErrRuleDenied
	}
	source, err := s.deviceAllowed(ctx, sourceID)
	if err != nil || !source.Send {
		return ErrRuleDenied
	}
	rule, err := s.ActiveRule(ctx, ruleID)
	if err != nil || !rule.Active || !rule.KeyFingerprintsVerified || !rule.SourceApproved ||
		!rule.TargetApproved || rule.SourceBrowserDeviceID != sourceID {
		return ErrRuleDenied
	}
	target, err := s.deviceAllowed(ctx, rule.TargetBrowserDeviceID)
	if err != nil || !target.Receive || target.IdentityID != source.IdentityID {
		return ErrRuleDenied
	}
	// Re-offering is permitted ONLY after an explicit revoke. Never update
	// an outstanding offer or an active binding in place: B must confirm each
	// new hostname/exit combination independently before proxying any traffic.
	result, err := s.db.ExecContext(ctx, `INSERT INTO browser_sync_network_bindings(rule_id,host,exit_id,status,updated_at)
 VALUES(?,?,?,'offered',?)
 ON CONFLICT(rule_id) DO UPDATE SET host=excluded.host,
 exit_id=excluded.exit_id,status='offered',updated_at=excluded.updated_at
 WHERE browser_sync_network_bindings.status='revoked'`,
		ruleID, host, exitID, time.Now().UTC())
	if err != nil {
		return ErrRuleDenied
	}
	updated, err := result.RowsAffected()
	if err != nil || updated != 1 {
		return ErrRuleDenied
	}
	return nil
}

func (s *Store) NetworkBinding(ctx context.Context, browserID, ruleID string) (NetworkBinding, error) {
	me, err := s.deviceAllowed(ctx, browserID)
	if err != nil || (!me.Receive && !me.Send) {
		return NetworkBinding{}, ErrRuleDenied
	}
	rule, err := s.ActiveRule(ctx, ruleID)
	if err != nil || !rule.Active || !rule.TargetApproved || !rule.SourceApproved ||
		!rule.KeyFingerprintsVerified || (rule.SourceBrowserDeviceID != browserID && rule.TargetBrowserDeviceID != browserID) {
		return NetworkBinding{}, ErrRuleDenied
	}
	var b NetworkBinding
	err = s.db.QueryRowContext(ctx, `SELECT rule_id,host,exit_id,status FROM browser_sync_network_bindings
 WHERE rule_id=?`, ruleID).Scan(&b.RuleID, &b.Host, &b.ExitDeviceID, &b.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return NetworkBinding{RuleID: ruleID, Status: "none"}, nil
	}
	if err != nil {
		return NetworkBinding{}, err
	}
	return b, nil
}

func (s *Store) AcceptNetworkBinding(ctx context.Context, targetID, ruleID, host string) error {
	target, err := s.deviceAllowed(ctx, targetID)
	if err != nil || !target.Receive || !validNetworkHost(host) {
		return ErrRuleDenied
	}
	host, _ = normalizeNetworkTarget(host)
	rule, err := s.ActiveRule(ctx, ruleID)
	if err != nil || !rule.Active || !rule.TargetApproved || !rule.SourceApproved ||
		!rule.KeyFingerprintsVerified || rule.TargetBrowserDeviceID != targetID {
		return ErrRuleDenied
	}
	source, err := s.deviceAllowed(ctx, rule.SourceBrowserDeviceID)
	if err != nil || !source.Send || source.IdentityID != target.IdentityID {
		return ErrRuleDenied
	}
	result, err := s.db.ExecContext(ctx, `UPDATE browser_sync_network_bindings SET status='active',updated_at=?
 WHERE rule_id=? AND host=? AND status='offered'`, time.Now().UTC(), ruleID, host)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return ErrRuleDenied
	}
	return nil
}

func (s *Store) RevokeNetworkBinding(ctx context.Context, browserID, ruleID string) error {
	if _, err := s.NetworkBinding(ctx, browserID, ruleID); err != nil {
		return ErrRuleDenied
	}
	result, err := s.db.ExecContext(ctx, `UPDATE browser_sync_network_bindings SET status='revoked',updated_at=?
 WHERE rule_id=? AND status!='revoked'`, time.Now().UTC(), ruleID)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return ErrRuleDenied
	}
	return nil
}

// A single browser credential may serve multiple exact hosts. This avoids
// Chrome's proxy Basic-auth cache reusing a rule-specific token for another
// CONNECT request to the same HTTPS proxy. Ambiguous host-to-exit mappings
// are rejected rather than randomly choosing an Agent.
func (s *Store) NetworkDestination(ctx context.Context, receiverID, host string) (string, error) {
	if !validNetworkHost(host) {
		return "", ErrRuleDenied
	}
	host, _ = normalizeNetworkTarget(host)
	receiver, err := s.deviceAllowed(ctx, receiverID)
	if err != nil || !receiver.Receive {
		return "", ErrRuleDenied
	}
	rows, err := s.db.QueryContext(ctx, `SELECT b.exit_id,r.source_id FROM browser_sync_network_bindings b
 JOIN browser_sync_rules r ON r.id=b.rule_id
 WHERE b.host=? AND b.status='active' AND r.status='active'
 AND r.source_approved=1 AND r.target_approved=1 AND r.keys_confirmed=1
 AND r.target_id=? LIMIT 3`, host, receiverID)
	if err != nil {
		return "", err
	}
	var selected, sourceID string
	count := 0
	for rows.Next() {
		if err := rows.Scan(&selected, &sourceID); err != nil {
			rows.Close()
			return "", err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return "", err
	}
	if err := rows.Close(); err != nil {
		return "", err
	}
	if count != 1 {
		return "", ErrRuleDenied
	}
	// Close SQL rows BEFORE checking identity: the DB may have only one
	// underlying SQLite connection, so nested queries would otherwise stall.
	source, err := s.deviceAllowed(ctx, sourceID)
	if err != nil || !source.Send || source.IdentityID != receiver.IdentityID {
		return "", ErrRuleDenied
	}
	return selected, nil
}
func (s *Store) HasActiveNetworkBinding(ctx context.Context, receiverID string) bool {
	receiver, err := s.deviceAllowed(ctx, receiverID)
	if err != nil || !receiver.Receive {
		return false
	}
	var count int
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM browser_sync_network_bindings b
 JOIN browser_sync_rules r ON r.id=b.rule_id WHERE r.target_id=?
 AND b.status='active' AND r.status='active' AND r.source_approved=1
 AND r.target_approved=1 AND r.keys_confirmed=1`, receiverID).Scan(&count)
	return err == nil && count > 0
}
