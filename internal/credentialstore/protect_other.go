//go:build !windows

package credentialstore

// Unix desktop builds rely on the per-user RelayProxy directory (0700) and
// credential file permissions (0600). The credential is still kept separate
// from ordinary configuration, status, diagnostics and exports.
func protect(plain []byte) ([]byte, error) {
	return append([]byte(nil), plain...), nil
}

func unprotect(protected []byte) ([]byte, error) {
	return append([]byte(nil), protected...), nil
}
