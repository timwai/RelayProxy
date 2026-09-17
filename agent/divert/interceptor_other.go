//go:build !windows && !linux && !darwin

package divert

func startPlatformInterceptor(s *Server) (systemInterceptor, error) {
	return nil, ErrUnsupportedPlatform
}
