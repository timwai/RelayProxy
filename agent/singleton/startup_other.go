//go:build !windows

package singleton

import "errors"

// AcquireForStartup retains the platform's existing exclusive Agent lock.
func AcquireForStartup(name string, prepare func(replacing bool) error) (*Mutex, error) {
	mutex, err := Acquire(name)
	if err != nil {
		return nil, err
	}
	if prepare != nil {
		if err := prepare(false); err != nil {
			mutex.Release()
			return nil, err
		}
	}
	return mutex, nil
}

func RunReplacementHelper(string) error { return errors.New("客户端替换助手仅支持 Windows") }
