package browsersync

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"
)

var chromeExtensionIDPattern = regexp.MustCompile(`^[a-p]{32}$`)

// BrowserExtension is a separately reviewed Chrome origin, NOT an Agent or
// an approved BrowserDevice. Unknown origins can only queue an approval request.
type BrowserExtension struct {
	ID        string    `json:"id"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"createdAt"`
}

func (s *Store) EnsureExtensionSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS browser_sync_extensions (
		extension_id TEXT PRIMARY KEY, state TEXT NOT NULL DEFAULT 'pending',
		created_at TIMESTAMP NOT NULL, updated_at TIMESTAMP NOT NULL,
		CHECK(state IN ('pending','approved','revoked'))
	)`)
	return err
}

func (s *Store) RequestExtension(ctx context.Context, id string) (string, error) {
	if !chromeExtensionIDPattern.MatchString(id) {
		return "", errors.New("invalid Chrome extension ID")
	}
	// Bound the persistent unauthenticated queue independently from IP rate limits.
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM browser_sync_extensions`).Scan(&count); err != nil {
		return "", err
	}
	var state string
	err := s.db.QueryRowContext(ctx, `SELECT state FROM browser_sync_extensions WHERE extension_id=?`, id).Scan(&state)
	if err == nil {
		return state, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if count >= 500 {
		return "", errors.New("browser extension approval queue full")
	}
	now := time.Now().UTC()
	if _, err = s.db.ExecContext(ctx, `INSERT INTO browser_sync_extensions(extension_id,state,created_at,updated_at)
		VALUES(?,'pending',?,?) ON CONFLICT(extension_id) DO NOTHING`, id, now, now); err != nil {
		return "", err
	}
	err = s.db.QueryRowContext(ctx, `SELECT state FROM browser_sync_extensions WHERE extension_id=?`, id).Scan(&state)
	return state, err
}

func (s *Store) ExtensionApproved(ctx context.Context, id string) bool {
	var state string
	if err := s.db.QueryRowContext(ctx, `SELECT state FROM browser_sync_extensions WHERE extension_id=?`, id).Scan(&state); err != nil {
		return false
	}
	return state == "approved"
}

func (s *Store) Extensions(ctx context.Context) ([]BrowserExtension, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT extension_id,state,created_at FROM browser_sync_extensions ORDER BY created_at DESC LIMIT 500`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []BrowserExtension{}
	for rows.Next() {
		var item BrowserExtension
		if err := rows.Scan(&item.ID, &item.State, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) SetExtensionApproved(ctx context.Context, id string, approve bool) error {
	if !chromeExtensionIDPattern.MatchString(id) {
		return ErrNotFound
	}
	state := "revoked"
	if approve {
		state = "approved"
	}
	result, err := s.db.ExecContext(ctx, `UPDATE browser_sync_extensions SET state=?,updated_at=?
		WHERE extension_id=? AND state=?`, state, time.Now().UTC(), id, func() string {
		if approve {
			return "pending"
		}
		return "approved"
	}())
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("extension approval transition denied: %w", ErrNotFound)
	}
	return nil
}
