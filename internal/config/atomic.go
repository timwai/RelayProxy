package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// Write beside the destination, then replace it. A failed write or replacement
// leaves the original file intact and never publishes a truncated credential file.
func atomicWriteConfig(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	defer f.Close()
	if err := f.Chmod(0600); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := replaceConfigFile(tmp, path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}
