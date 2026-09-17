//go:build linux

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
	directory := "/run"
	if os.Geteuid() != 0 {
		var err error
		directory, err = os.UserCacheDir()
		if err != nil {
			return nil, err
		}
		directory = filepath.Join(directory, "relayproxy")
		if err := os.MkdirAll(directory, 0700); err != nil {
			return nil, err
		}
	}
	clean := strings.Map(func(ch rune) rune {
		if unicode.IsLetter(ch) || unicode.IsDigit(ch) || ch == '-' || ch == '_' {
			return unicode.ToLower(ch)
		}
		return '-'
	}, name)
	path := filepath.Join(directory, clean+".lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
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
