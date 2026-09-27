//go:build !windows

package desktop

import "errors"

func NewSystemHost() (*Host, error) {
	return nil, errors.New("Relay Desktop host capture is currently available on Windows only")
}
