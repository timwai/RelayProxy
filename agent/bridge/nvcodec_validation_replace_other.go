//go:build !windows

package bridge

import "os"

func replaceNVCodecValidationFile(from, to string) error {
	return os.Rename(from, to)
}
