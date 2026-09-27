//go:build !windows

package viewer

func Open(Config) (Native, error) {
	return nil, ErrUnavailable
}
