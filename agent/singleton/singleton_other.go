//go:build !windows && !linux && !darwin

package singleton

type Mutex struct{}

func Acquire(name string) (*Mutex, error) {
	return &Mutex{}, nil
}

func (m *Mutex) Release() {}
