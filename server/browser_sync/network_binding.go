package browsersync

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// A network binding deliberately reveals the first-party CONNECT hostname to
// the relay, unlike E2EE Cookie policy names. Both browsers must separately
// approve that exact name and a server-allowlisted Agent exit.
type NetworkBinding struct {
	RuleID       string `json:"ruleId"`
	Host         string `json:"host"`
	ExitDeviceID string `json:"exitDeviceId"`
	Status       string `json:"status"`
}

var networkHost = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)+$`)

func validNetworkHost(host string) bool {
	if host == "" || len(host) > 253 || host != strings.ToLower(host) || !networkHost.MatchString(host) ||
		net.ParseIP(host) != nil || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") ||
		strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".arpa") {
		return false
	}
	for _, part := range strings.Split(host, ".") {
		if len(part) > 63 {
			return false
		}
	}
	return true
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
	_, err = s.db.ExecContext(ctx, `INSERT INTO browser_sync_network_bindings(rule_id,host,exit_id,status,updated_at)
 VALUES(?,?,?,'offered',?)`, ruleID, host, exitID, time.Now().UTC())
	if err != nil {
		return ErrRuleDenied
	} // immutable offer: change requires full revoke/re-pair
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
