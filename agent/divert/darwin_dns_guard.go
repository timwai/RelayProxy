//go:build darwin

package divert

import (
	"errors"
	"os"
	"path/filepath"
	"time"
)

// The protection marker survives Agent crashes. Its lifecycle follows the
// user's FakeIP policy rather than the IPC socket: disabling FakeIP removes
// the marker, but terminating the Agent while enabled leaves it armed.
func writeDarwinGuardMarker(tokenPath string, armed bool) error {
	path := filepath.Join(filepath.Dir(tokenPath), "dns-guard.enabled")
	if !armed {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, ".dns-guard-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.WriteString("enabled\n"); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func (i *darwinInterceptor) watchGuardMarker(tokenPath string) {
	defer i.wg.Done()
	last := i.server.fakeIPEnabled()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-i.ctx.Done():
			return
		case <-ticker.C:
			current := i.server.fakeIPEnabled()
			if current != last {
				if err := writeDarwinGuardMarker(tokenPath, current); err != nil {
					// Do not publish an unverified security state.
					i.server.cancel()
					return
				}
				last = current
			}
		}
	}
}
