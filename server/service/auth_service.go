package service

import (
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"math/big"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
	"relayproxy/server/repository"
)

type SessionEntry struct {
	UserID    string
	ExpiresAt time.Time
}

type loginAttempt struct {
	failures    int
	lockedUntil time.Time
	lastFail    time.Time
}

var (
	ErrInvalidCredentials     = errors.New("invalid username or password")
	ErrAuthSessionExpired     = errors.New("session expired")
	ErrInvalidCurrentPassword = errors.New("current password is incorrect")
	ErrInvalidNewPassword     = errors.New("new password must contain 8 to 128 characters and cannot be blank")
	ErrPasswordUnchanged      = errors.New("new password must differ from current password")
	ErrRateLimited            = errors.New("RATE_LIMITED: too many failed login attempts, try again later")
)

const (
	passwordMinLength = 8
	passwordMaxLength = 128
)

type AuthService struct {
	db       *repository.DB
	sessions map[string]SessionEntry // sessionToken -> SessionEntry
	mu       sync.RWMutex
	loginMu  sync.Mutex
	logins   map[string]*loginAttempt // key: username|ip
}

func NewAuthService(db *repository.DB) *AuthService {
	s := &AuthService{
		db:       db,
		sessions: make(map[string]SessionEntry),
		logins:   make(map[string]*loginAttempt),
	}
	s.ensureAdmin()
	return s
}

func (s *AuthService) ensureAdmin() {
	_, err := s.db.GetUserByUsername("admin")
	if err != nil {
		adminPass := os.Getenv("RELAY_ADMIN_PASSWORD")
		isRandom := false
		if adminPass == "" {
			var genErr error
			adminPass, genErr = generateSecurePassword(16)
			if genErr != nil {
				log.Fatalf("[Auth] Failed to generate admin password: %v", genErr)
			}
			isRandom = true
		}
		hash, err := HashPassword(adminPass)
		if err == nil {
			_ = s.db.CreateUser(&repository.User{
				Username:     "admin",
				PasswordHash: hash,
				DisplayName:  "Administrator",
				Role:         "admin",
				Status:       "active",
			})
			if isRandom {
				log.Println("==================================================================")
				log.Println(" [SECURITY NOTICE] Initial admin account created!")
				log.Printf(" Username: admin")
				log.Printf(" Password: %s", adminPass)
				log.Println(" Please save this password! It will NOT be shown again.")
				log.Println("==================================================================")
			} else {
				log.Println("[Auth] Seeded admin user configured via RELAY_ADMIN_PASSWORD.")
			}
		}
	}
}

func (s *AuthService) Login(username, password string) (string, *repository.User, error) {
	return s.LoginFrom(username, password, "")
}

// LoginFrom authenticates with optional client IP for rate limiting.
func (s *AuthService) LoginFrom(username, password, clientIP string) (string, *repository.User, error) {
	userKey := username + "|" + clientIP
	ipKey := "ip|" + clientIP
	if err := s.checkLoginRateLimit(userKey); err != nil {
		return "", nil, err
	}
	if clientIP != "" {
		if err := s.checkLoginRateLimit(ipKey); err != nil {
			return "", nil, err
		}
	}

	u, err := s.db.GetUserByUsername(username)
	if err != nil || u.Status != "active" {
		s.recordLoginFailure(userKey)
		if clientIP != "" {
			s.recordLoginFailure(ipKey)
		}
		return "", nil, ErrInvalidCredentials
	}

	match, err := VerifyPassword(password, u.PasswordHash)
	if err != nil || !match {
		s.recordLoginFailure(userKey)
		if clientIP != "" {
			s.recordLoginFailure(ipKey)
		}
		return "", nil, ErrInvalidCredentials
	}

	token, err := s.createSession(u)
	if err != nil {
		return "", nil, err
	}
	s.clearLoginFailures(userKey)
	if clientIP != "" {
		s.clearLoginFailures(ipKey)
	}

	return token, u, nil
}

// createSession publishes a verified login only if its password is still
// current. Password updates and session revocation use the same lock, so an
// in-flight login cannot restore access with the previous password.
func (s *AuthService) createSession(u *repository.User) (string, error) {
	token, err := GenerateRandomToken(32)
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	hash, err := s.db.GetActiveUserPasswordHash(u.ID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && hash != u.PasswordHash) {
		return "", ErrInvalidCredentials
	}
	if err != nil {
		return "", fmt.Errorf("load login credentials: %w", err)
	}
	// Periodic/threshold prune
	if len(s.sessions) > 5000 {
		now := time.Now()
		for k, v := range s.sessions {
			if now.After(v.ExpiresAt) {
				delete(s.sessions, k)
			}
		}
	}
	s.sessions[token] = SessionEntry{
		UserID:    u.ID,
		ExpiresAt: time.Now().Add(7 * 24 * time.Hour), // 7-day TTL
	}
	return token, nil
}

// ChangePassword changes only the account associated with the supplied
// session. All of that account's management sessions expire after persistence
// succeeds; device credentials and tunnel sessions are independent.
func (s *AuthService) ChangePassword(token, currentPassword, newPassword string) error {
	userID, ok := s.Authenticate(token)
	if !ok {
		return ErrAuthSessionExpired
	}
	// Scope attempts to the account, including its other browsers and IPs.
	rateKey := "password|" + userID
	if err := s.checkLoginRateLimit(rateKey); err != nil {
		return err
	}
	length := utf8.RuneCountInString(newPassword)
	if !utf8.ValidString(newPassword) || length < passwordMinLength || length > passwordMaxLength || strings.TrimSpace(newPassword) == "" {
		return ErrInvalidNewPassword
	}
	hash, err := s.db.GetActiveUserPasswordHash(userID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAuthSessionExpired
	}
	if err != nil {
		return fmt.Errorf("load password: %w", err)
	}
	match, err := VerifyPassword(currentPassword, hash)
	if err != nil {
		return fmt.Errorf("verify current password: %w", err)
	}
	if !match {
		s.recordLoginFailure(rateKey)
		return ErrInvalidCurrentPassword
	}
	if newPassword == currentPassword {
		return ErrPasswordUnchanged
	}
	newHash, err := HashPassword(newPassword)
	if err != nil {
		return fmt.Errorf("hash new password: %w", err)
	}

	// Keep expensive password hashing outside the session lock. Recheck the
	// session before writing in case it was revoked while hashing.
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.sessions[token]
	if !ok || entry.UserID != userID || !time.Now().Before(entry.ExpiresAt) {
		return ErrAuthSessionExpired
	}
	updated, err := s.db.UpdateUserPassword(userID, hash, newHash)
	if err != nil {
		return fmt.Errorf("save password: %w", err)
	}
	if !updated {
		return ErrAuthSessionExpired
	}
	for sessionToken, session := range s.sessions {
		if session.UserID == userID {
			delete(s.sessions, sessionToken)
		}
	}
	s.clearLoginFailures(rateKey)
	return nil
}

const (
	loginMaxFailures   = 5
	loginLockDuration  = 5 * time.Minute
	loginMaxMapEntries = 10000
)

func (s *AuthService) checkLoginRateLimit(key string) error {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	s.pruneLoginsLocked()
	entry, ok := s.logins[key]
	if !ok {
		return nil
	}
	if time.Now().Before(entry.lockedUntil) {
		return ErrRateLimited
	}
	return nil
}

func (s *AuthService) recordLoginFailure(key string) {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	s.pruneLoginsLocked()
	entry, ok := s.logins[key]
	if !ok {
		entry = &loginAttempt{}
		s.logins[key] = entry
	}
	entry.failures++
	entry.lastFail = time.Now()
	if entry.failures >= loginMaxFailures {
		entry.lockedUntil = time.Now().Add(loginLockDuration)
		entry.failures = 0
	}
}

func (s *AuthService) clearLoginFailures(key string) {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	delete(s.logins, key)
}

func (s *AuthService) pruneLoginsLocked() {
	now := time.Now()
	for k, v := range s.logins {
		stale := now.After(v.lockedUntil) && now.Sub(v.lastFail) > loginLockDuration*2
		if stale {
			delete(s.logins, k)
		}
	}
	if len(s.logins) <= loginMaxMapEntries {
		return
	}
	// Drop arbitrary excess entries when under flood (capacity hard cap).
	excess := len(s.logins) - loginMaxMapEntries
	for k := range s.logins {
		if excess <= 0 {
			break
		}
		delete(s.logins, k)
		excess--
	}
}

func (s *AuthService) Authenticate(token string) (string, bool) {
	if token == "" {
		return "", false
	}
	s.mu.RLock()
	entry, ok := s.sessions[token]
	s.mu.RUnlock()
	if !ok {
		return "", false
	}
	if time.Now().After(entry.ExpiresAt) {
		s.mu.Lock()
		delete(s.sessions, token)
		s.mu.Unlock()
		return "", false
	}
	return entry.UserID, true
}

func (s *AuthService) Logout(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, token)
}

// Argon2id parameters
const (
	argonMemory      = 64 * 1024 // 64 MB
	argonIterations  = 3
	argonParallelism = 2
	argonSaltLen     = 16
	argonKeyLen      = 32
)

func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("failed to generate salt: %w", err)
	}

	hash := argon2.IDKey([]byte(password), salt, argonIterations, argonMemory, argonParallelism, argonKeyLen)

	b64Salt := base64.RawStdEncoding.EncodeToString(salt)
	b64Hash := base64.RawStdEncoding.EncodeToString(hash)

	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		argonMemory, argonIterations, argonParallelism, b64Salt, b64Hash), nil
}

func VerifyPassword(password, encodedHash string) (bool, error) {
	parts := strings.Split(encodedHash, "$")
	if len(parts) != 6 {
		return false, errors.New("invalid hash format")
	}

	var mem uint32
	var iter uint32
	var par uint8
	_, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &iter, &par)
	if err != nil {
		return false, err
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, err
	}

	expectedHash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, err
	}

	calculatedHash := argon2.IDKey([]byte(password), salt, iter, mem, par, uint32(len(expectedHash)))
	if subtle.ConstantTimeCompare(calculatedHash, expectedHash) == 1 {
		return true, nil
	}
	return false, nil
}

func GenerateRandomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to read random bytes: %w", err)
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

func generateSecurePassword(length int) (string, error) {
	const charset = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789!@#$%^&*"
	numChars := big.NewInt(int64(len(charset)))
	res := make([]byte, length)
	for i := 0; i < length; i++ {
		idx, err := rand.Int(rand.Reader, numChars)
		if err != nil {
			return "", fmt.Errorf("crypto/rand failed while generating password: %w", err)
		}
		res[i] = charset[idx.Int64()]
	}
	return string(res), nil
}
