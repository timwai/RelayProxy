//go:build darwin

package singleton

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"golang.org/x/sys/unix"
)

type Mutex struct{ file *os.File }

func Acquire(name string) (*Mutex, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(cacheDir, "relayproxy")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	clean := strings.Map(func(ch rune) rune {
		if unicode.IsLetter(ch) || unicode.IsDigit(ch) || ch == '-' || ch == '_' {
			return unicode.ToLower(ch)
		}
		return '-'
	}, name)
	file, err := os.OpenFile(filepath.Join(dir, clean+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("another instance of %s is already running: %w", name, err)
	}
	return &Mutex{file: file}, nil
}

func (m *Mutex) Release() {
	if m == nil || m.file == nil {
		return
	}
	_ = unix.Flock(int(m.file.Fd()), unix.LOCK_UN)
	_ = m.file.Close()
	m.file = nil
}
