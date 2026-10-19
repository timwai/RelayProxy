//go:build !windows

package hostalias

import "errors"

// The Windows elevation implementation is compiled only for Windows targets.
func elevateWindows(_, _ string) error {
	return errors.New("Windows UAC is not available")
}
