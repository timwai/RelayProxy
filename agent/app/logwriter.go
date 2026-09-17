package app

import "io"

// bestEffortWriter swallows write errors from its target.
//
// This matters because the desktop build is linked with -H=windowsgui: the
// process has no console attached, so writes to os.Stdout/os.Stderr fail with
// ERROR_INVALID_HANDLE. Without this wrapper an io.MultiWriter aborts on that
// first error and the in-memory log buffer — the only log the desktop UI can
// show — would silently stay empty.
type bestEffortWriter struct{ w io.Writer }

func (w bestEffortWriter) Write(p []byte) (int, error) {
	if w.w != nil {
		_, _ = w.w.Write(p)
	}
	return len(p), nil
}

// BestEffort keeps a console writer without letting it break the log chain.
func BestEffort(w io.Writer) io.Writer { return bestEffortWriter{w: w} }
