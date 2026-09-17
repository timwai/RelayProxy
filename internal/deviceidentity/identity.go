package deviceidentity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const identityVersion = 1

// Identity is an installation-scoped signing identity. It is deliberately
// stored outside relay-agent.yaml so configuration never contains credentials.
type Identity struct {
	InstallationID string
	PublicKey      ed25519.PublicKey
	PrivateKey     ed25519.PrivateKey
}

type diskIdentity struct {
	Version        int    `json:"version"`
	InstallationID string `json:"installationId"`
	PrivateKey     string `json:"privateKey"`
	Protection     string `json:"protection"`
}

func (i *Identity) Fingerprint() string {
	sum := sha256.Sum256(i.PublicKey)
	return hex.EncodeToString(sum[:])
}

func (i *Identity) Sign(payload []byte) []byte {
	return ed25519.Sign(i.PrivateKey, payload)
}

// LoadOrCreate loads an existing installation identity or atomically creates
// one. Newly created files use mode 0600 as a safe default on Unix-like
// systems; existing file permission bits are not enforced. Windows additionally
// wraps the private key with DPAPI for the current user.
func LoadOrCreate(path string) (*Identity, error) {
	if path == "" {
		return nil, errors.New("identity path is required")
	}
	identity, err := load(path)
	if err == nil {
		return identity, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("create identity directory: %w", err)
	}
	identity, err = Generate()
	if err != nil {
		return nil, err
	}
	if err := saveExclusive(path, identity); err != nil {
		if errors.Is(err, os.ErrExist) {
			return load(path)
		}
		return nil, err
	}
	return identity, nil
}

// Generate creates an in-memory identity. Long-running applications should use
// LoadOrCreate; this helper is useful to embedders that provide their own store.
func Generate() (*Identity, error) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate device identity: %w", err)
	}
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return nil, fmt.Errorf("generate installation id: %w", err)
	}
	return &Identity{InstallationID: hex.EncodeToString(idBytes), PublicKey: publicKey, PrivateKey: privateKey}, nil
}

func load(path string) (*Identity, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("identity path must be a regular file")
	}
	if err := validateIdentityFileMode(info.Mode()); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read device identity: %w", err)
	}
	var disk diskIdentity
	if err := json.Unmarshal(data, &disk); err != nil {
		return nil, fmt.Errorf("decode device identity: %w", err)
	}
	if disk.Version != identityVersion || disk.InstallationID == "" {
		return nil, errors.New("unsupported or incomplete device identity")
	}
	protected, err := base64.RawStdEncoding.DecodeString(disk.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("decode private key: %w", err)
	}
	privateKey, protection, err := unprotectPrivateKey(protected, disk.Protection)
	if err != nil {
		return nil, err
	}
	if protection != disk.Protection || len(privateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("invalid device private key")
	}
	key := ed25519.PrivateKey(append([]byte(nil), privateKey...))
	publicKey := append(ed25519.PublicKey(nil), key.Public().(ed25519.PublicKey)...)
	return &Identity{InstallationID: disk.InstallationID, PublicKey: publicKey, PrivateKey: key}, nil
}

func saveExclusive(path string, identity *Identity) error {
	protected, protection, err := protectPrivateKey(identity.PrivateKey)
	if err != nil {
		return fmt.Errorf("protect device identity: %w", err)
	}
	disk := diskIdentity{
		Version: identityVersion, InstallationID: identity.InstallationID,
		PrivateKey: base64.RawStdEncoding.EncodeToString(protected), Protection: protection,
	}
	data, err := json.MarshalIndent(disk, "", "  ")
	if err != nil {
		return fmt.Errorf("encode device identity: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	writeErr := error(nil)
	if _, err := file.Write(append(data, '\n')); err != nil {
		writeErr = err
	} else if err := file.Sync(); err != nil {
		writeErr = err
	}
	if closeErr := file.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		_ = os.Remove(path)
		return fmt.Errorf("write device identity: %w", writeErr)
	}
	return nil
}
