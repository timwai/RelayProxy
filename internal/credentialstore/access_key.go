package credentialstore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const accessKeyFileName = "access-key.cred"

// PathForConfig keeps credentials next to the per-user agent configuration
// without ever serializing them into relay-agent.yaml.
func PathForConfig(configPath string) string {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(configPath), accessKeyFileName)
}

func LoadAccessKey(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read access-key credential: %w", err)
	}
	plain, err := unprotect(data)
	if err != nil {
		return "", fmt.Errorf("unprotect access-key credential: %w", err)
	}
	value := strings.TrimSpace(string(plain))
	if value != "" && !strings.HasPrefix(value, "rpk_") {
		return "", errors.New("stored access-key credential has an invalid format")
	}
	return value, nil
}

func SaveAccessKey(path, accessKey string) error {
	path = strings.TrimSpace(path)
	accessKey = strings.TrimSpace(accessKey)
	if path == "" {
		return errors.New("credential path is required")
	}
	if accessKey == "" {
		return ClearAccessKey(path)
	}
	if !strings.HasPrefix(accessKey, "rpk_") || len(accessKey) < 16 {
		return errors.New("identity access key must start with rpk_")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create credential directory: %w", err)
	}
	protected, err := protect([]byte(accessKey))
	if err != nil {
		return fmt.Errorf("protect access-key credential: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, protected, 0o600); err != nil {
		return fmt.Errorf("write temporary access-key credential: %w", err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("secure access-key credential permissions: %w", err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace access-key credential: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("activate access-key credential: %w", err)
	}
	return nil
}

func ClearAccessKey(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove access-key credential: %w", err)
	}
	return nil
}
