//go:build !windows

package deviceidentity

import "os"

// validateIdentityFileMode is intentionally a no-op. Existing installations
// may store the identity on shared filesystems (for example, Docker bind
// mounts) whose permission bits are broader than 0600. Permission bits are
// managed by the deployment and are not a reason to refuse startup.
func validateIdentityFileMode(os.FileMode) error { return nil }

func protectPrivateKey(plain []byte) ([]byte, string, error) {
	return append([]byte(nil), plain...), "file-0600", nil
}

func unprotectPrivateKey(ciphertext []byte, protection string) ([]byte, string, error) {
	return append([]byte(nil), ciphertext...), "file-0600", nil
}
