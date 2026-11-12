package e2ee

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

const peerPinStoreVersion = 1

var (
	ErrPeerKeyChanged = errors.New("E2EE peer public key changed; refusing silent key rotation")
	ErrPeerPinInvalid = errors.New("invalid E2EE peer key pin store")
)

type peerPinFile struct {
	Version int               `json:"version"`
	Pins    map[string]string `json:"pins"`
}

// PeerKeyPinStore implements trust-on-first-use for Exit identity keys.
// Pins are scoped to the authenticated Server instance ID and Exit Device ID.
// A changed key is rejected; an operator must verify the new key out-of-band
// and explicitly remove the pin file before trusting it.
type PeerKeyPinStore struct {
	mu   sync.Mutex
	path string
	pins map[string]string
}

func OpenPeerKeyPinStore(path string) (*PeerKeyPinStore, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("E2EE peer pin store path is required")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create E2EE peer pin directory: %w", err)
	}
	store := &PeerKeyPinStore{path: path, pins: make(map[string]string)}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect E2EE peer pin store: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, ErrPeerPinInvalid
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("E2EE peer pin store permissions must be 0600 or stricter")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read E2EE peer pin store: %w", err)
	}
	var disk peerPinFile
	if err := json.Unmarshal(data, &disk); err != nil {
		return nil, fmt.Errorf("%w: decode: %v", ErrPeerPinInvalid, err)
	}
	if disk.Version != peerPinStoreVersion || disk.Pins == nil {
		return nil, ErrPeerPinInvalid
	}
	for key, encoded := range disk.Pins {
		if len(key) != sha256.Size*2 {
			return nil, ErrPeerPinInvalid
		}
		if _, err := hex.DecodeString(key); err != nil {
			return nil, ErrPeerPinInvalid
		}
		publicKey, err := base64.RawStdEncoding.DecodeString(encoded)
		if err != nil || len(publicKey) != ed25519.PublicKeySize {
			return nil, ErrPeerPinInvalid
		}
		store.pins[key] = encoded
	}
	return store, nil
}

// VerifyOrPin records a peer key on first use and rejects every later key
// change. The caller must obtain serverInstanceID from the authenticated
// Server challenge, not from untrusted local configuration.
func (s *PeerKeyPinStore) VerifyOrPin(serverInstanceID, exitDeviceID string, publicKey ed25519.PublicKey) error {
	if s == nil {
		return errors.New("E2EE peer pin store is unavailable")
	}
	serverInstanceID = strings.TrimSpace(serverInstanceID)
	exitDeviceID = strings.TrimSpace(exitDeviceID)
	if serverInstanceID == "" || exitDeviceID == "" || strings.ContainsRune(serverInstanceID, '\x00') ||
		strings.ContainsRune(exitDeviceID, '\x00') || len(publicKey) != ed25519.PublicKeySize {
		return errors.New("E2EE peer pin requires Server instance ID, Exit Device ID and an Ed25519 public key")
	}
	scope := sha256.Sum256([]byte(serverInstanceID + "\x00" + exitDeviceID))
	key := hex.EncodeToString(scope[:])
	encoded := base64.RawStdEncoding.EncodeToString(publicKey)

	s.mu.Lock()
	defer s.mu.Unlock()
	if pinned, ok := s.pins[key]; ok {
		oldKey, err := base64.RawStdEncoding.DecodeString(pinned)
		if err != nil || subtle.ConstantTimeCompare(oldKey, publicKey) != 1 {
			oldFingerprint := fingerprint(oldKey)
			newFingerprint := fingerprint(publicKey)
			return fmt.Errorf("%w for Exit %s on Server %s (pinned=%s presented=%s)",
				ErrPeerKeyChanged, exitDeviceID, serverInstanceID, oldFingerprint, newFingerprint)
		}
		return nil
	}
	s.pins[key] = encoded
	if err := s.persistLocked(); err != nil {
		delete(s.pins, key)
		return fmt.Errorf("persist E2EE peer key pin: %w", err)
	}
	return nil
}

func (s *PeerKeyPinStore) persistLocked() error {
	disk := peerPinFile{Version: peerPinStoreVersion, Pins: s.pins}
	data, err := json.MarshalIndent(disk, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	file, err := os.CreateTemp(dir, ".peer-key-pins-*")
	if err != nil {
		return err
	}
	tempPath := file.Name()
	defer os.Remove(tempPath)
	if err := file.Chmod(0600); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, s.path)
}

func fingerprint(publicKey []byte) string {
	if len(publicKey) != ed25519.PublicKeySize {
		return "invalid"
	}
	sum := sha256.Sum256(publicKey)
	return hex.EncodeToString(sum[:8])
}
