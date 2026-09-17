package service

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"relayproxy/server/repository"
)

const originalTestPassword = "original-admin-password"

func passwordTestService(t *testing.T) (*AuthService, string) {
	t.Helper()
	t.Setenv("RELAY_ADMIN_PASSWORD", originalTestPassword)
	path := filepath.Join(t.TempDir(), "auth.db")
	db, err := repository.OpenDB("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewAuthService(db), path
}

func passwordTestLogin(t *testing.T, s *AuthService, username, password string) (string, *repository.User) {
	t.Helper()
	token, user, err := s.Login(username, password)
	if err != nil {
		t.Fatal(err)
	}
	return token, user
}

func TestChangePasswordPersistsAndRevokesOnlyAccountSessions(t *testing.T) {
	s, path := passwordTestService(t)
	first, original := passwordTestLogin(t, s, "admin", originalTestPassword)
	second, _ := passwordTestLogin(t, s, "admin", originalTestPassword)
	other := &repository.User{Username: "other", PasswordHash: original.PasswordHash, Role: "user", Status: "active"}
	if err := s.db.CreateUser(other); err != nil {
		t.Fatal(err)
	}
	otherToken, _ := passwordTestLogin(t, s, "other", originalTestPassword)
	if err := s.db.UpsertDevice(&repository.Device{ID: "paired-device", OwnerUserID: original.ID, Name: "device", DeviceMode: "CLIENT", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	newPassword := "  管理密码 更新2026 🔑  "
	if err := s.ChangePassword(first, originalTestPassword, newPassword); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{first, second} {
		if _, ok := s.Authenticate(token); ok {
			t.Fatal("a previous management session survived the password change")
		}
	}
	if id, ok := s.Authenticate(otherToken); !ok || id != other.ID {
		t.Fatal("another account's session was revoked")
	}
	devices, err := s.db.ListDevices()
	if err != nil || len(devices) != 1 || devices[0].ApprovalState != repository.EnrollmentApproved {
		t.Fatalf("device approval changed: devices=%v err=%v", devices, err)
	}
	updated, err := s.db.GetUserByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}
	if updated.PasswordHash == original.PasswordHash || !strings.HasPrefix(updated.PasswordHash, "$argon2id$") {
		t.Fatal("password was not replaced with an Argon2id hash")
	}
	if updated.ID != original.ID || updated.Role != original.Role || updated.Status != original.Status || !updated.UpdatedAt.After(original.UpdatedAt) {
		t.Fatal("account metadata was unexpectedly changed or timestamp was not updated")
	}
	if _, _, err := s.Login("admin", originalTestPassword); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("old password still works: %v", err)
	}
	passwordTestLogin(t, s, "admin", newPassword)
	if _, _, err := s.Login("admin", strings.TrimSpace(newPassword)); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("password whitespace was not preserved: %v", err)
	}
	if err := s.ChangePassword(second, newPassword, "replayed-change"); !errors.Is(err, ErrAuthSessionExpired) {
		t.Fatalf("revoked session changed password: %v", err)
	}
	// Simulate a login that verified the old hash before the change and only
	// reaches session publication after the change completed.
	if token, err := s.createSession(original); token != "" || !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("an in-flight old login restored access: %v", err)
	}
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := repository.OpenDB("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restarted := NewAuthService(reopened)
	passwordTestLogin(t, restarted, "admin", newPassword)
	if _, _, err := restarted.Login("admin", originalTestPassword); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("initial environment password replaced the saved password on restart: %v", err)
	}
}

func TestChangePasswordRejectsInvalidInputWithoutMutation(t *testing.T) {
	s, _ := passwordTestService(t)
	token, original := passwordTestLogin(t, s, "admin", originalTestPassword)
	cases := []struct {
		name, current, next string
		want                error
	}{
		{"empty", originalTestPassword, "", ErrInvalidNewPassword},
		{"short", originalTestPassword, "short", ErrInvalidNewPassword},
		{"long", originalTestPassword, strings.Repeat("a", 129), ErrInvalidNewPassword},
		{"unicode-short", originalTestPassword, strings.Repeat("🔑", 7), ErrInvalidNewPassword},
		{"unicode-long", originalTestPassword, strings.Repeat("🔑", 129), ErrInvalidNewPassword},
		{"blank", originalTestPassword, strings.Repeat("\u3000", 8), ErrInvalidNewPassword},
		{"invalid-utf8", originalTestPassword, "invalid\xffpassword", ErrInvalidNewPassword},
		{"wrong-current", "wrong-password", "valid-new-password", ErrInvalidCurrentPassword},
		{"same", originalTestPassword, originalTestPassword, ErrPasswordUnchanged},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.ChangePassword(token, tc.current, tc.next); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			stored, err := s.db.GetUserByUsername("admin")
			if err != nil || stored.PasswordHash != original.PasswordHash {
				t.Fatal("invalid input modified the stored password")
			}
			if _, ok := s.Authenticate(token); !ok {
				t.Fatal("invalid input revoked the existing session")
			}
		})
	}
	current := originalTestPassword
	for _, next := range []string{strings.Repeat("🔑", 8), strings.Repeat("界", 128)} {
		if err := s.ChangePassword(token, current, next); err != nil {
			t.Fatalf("valid Unicode length boundary rejected: %v", err)
		}
		token, _ = passwordTestLogin(t, s, "admin", next)
		current = next
	}
}

func TestChangePasswordWriteFailurePreservesCredentialsAndSessions(t *testing.T) {
	s, _ := passwordTestService(t)
	first, original := passwordTestLogin(t, s, "admin", originalTestPassword)
	second, _ := passwordTestLogin(t, s, "admin", originalTestPassword)
	if _, err := s.db.Exec(`CREATE TRIGGER reject_password_update BEFORE UPDATE OF password_hash ON users
		BEGIN SELECT RAISE(ABORT, 'simulated storage failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.ChangePassword(first, originalTestPassword, "valid-new-password"); err == nil {
		t.Fatal("storage failure reported success")
	}
	stored, err := s.db.GetUserByUsername("admin")
	if err != nil || stored.PasswordHash != original.PasswordHash {
		t.Fatal("failed write changed credentials")
	}
	for _, token := range []string{first, second} {
		if _, ok := s.Authenticate(token); !ok {
			t.Fatal("failed write revoked a session")
		}
	}
	passwordTestLogin(t, s, "admin", originalTestPassword)
}

func TestChangePasswordRateLimitIsSharedAcrossAccountSessions(t *testing.T) {
	s, _ := passwordTestService(t)
	first, user := passwordTestLogin(t, s, "admin", originalTestPassword)
	second, _ := passwordTestLogin(t, s, "admin", originalTestPassword)
	tokens := []string{first, second}
	for attempt := 0; attempt < loginMaxFailures; attempt++ {
		if err := s.ChangePassword(tokens[attempt%len(tokens)], "wrong", "valid-new-password"); !errors.Is(err, ErrInvalidCurrentPassword) {
			t.Fatal(err)
		}
	}
	third, _ := passwordTestLogin(t, s, "admin", originalTestPassword)
	if err := s.ChangePassword(third, originalTestPassword, "valid-new-password"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("a fresh login bypassed the account limit: %v", err)
	}
	if _, ok := s.Authenticate(first); !ok {
		t.Fatal("password attempt limit invalidated an unrelated management operation")
	}
	s.loginMu.Lock()
	s.logins["password|"+user.ID].lockedUntil = time.Now().Add(-time.Second)
	s.loginMu.Unlock()
	if err := s.ChangePassword(third, originalTestPassword, "valid-new-password"); err != nil {
		t.Fatalf("account did not recover after the lock expired: %v", err)
	}
}

func TestConcurrentPasswordChangesHaveOneWinner(t *testing.T) {
	s, _ := passwordTestService(t)
	first, _ := passwordTestLogin(t, s, "admin", originalTestPassword)
	second, _ := passwordTestLogin(t, s, "admin", originalTestPassword)
	passwords := []string{"concurrent-password-one", "concurrent-password-two"}
	tokens := []string{first, second}
	errs := make([]error, len(tokens))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for index, token := range tokens {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[index] = s.ChangePassword(token, originalTestPassword, passwords[index])
		}()
	}
	close(start)
	wg.Wait()
	winners := 0
	for index, err := range errs {
		if err == nil {
			winners++
			passwordTestLogin(t, s, "admin", passwords[index])
		} else if !errors.Is(err, ErrAuthSessionExpired) && !errors.Is(err, ErrInvalidCurrentPassword) {
			t.Fatalf("unexpected concurrent failure: %v", err)
		}
		if _, ok := s.Authenticate(tokens[index]); ok {
			t.Fatal("a previous session survived concurrent password changes")
		}
	}
	if winners != 1 {
		t.Fatalf("got %d successful changes, want exactly one", winners)
	}
}

func TestDisabledAndExpiredAccountsCannotChangePassword(t *testing.T) {
	s, _ := passwordTestService(t)
	token, user := passwordTestLogin(t, s, "admin", originalTestPassword)
	s.mu.Lock()
	s.sessions[token] = SessionEntry{UserID: user.ID, ExpiresAt: time.Now().Add(-time.Second)}
	s.mu.Unlock()
	if err := s.ChangePassword(token, originalTestPassword, "valid-new-password"); !errors.Is(err, ErrAuthSessionExpired) {
		t.Fatalf("expired session changed password: %v", err)
	}
	token, _ = passwordTestLogin(t, s, "admin", originalTestPassword)
	if _, err := s.db.Exec(`UPDATE users SET status = 'disabled' WHERE id = ?`, user.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.ChangePassword(token, originalTestPassword, "valid-new-password"); !errors.Is(err, ErrAuthSessionExpired) {
		t.Fatalf("disabled account changed password: %v", err)
	}
	if token, _, err := s.Login("admin", originalTestPassword); token != "" || !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("disabled account logged in: %v", err)
	}
	if token, err := s.createSession(user); token != "" || !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("pending login restored disabled account: %v", err)
	}
}
