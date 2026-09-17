package protocol

import "fmt"

// Standard Error Codes defined in Section 47 of RelayProxy Design Document
const (
	ErrCodeSuccess           = "OK"
	ErrCodeAuthFailed        = "AUTH_FAILED"
	ErrCodeDeviceNotFound    = "DEVICE_NOT_FOUND"
	ErrCodeExitOffline       = "EXIT_OFFLINE"
	ErrCodeAccessDenied      = "ACCESS_DENIED"
	ErrCodeACLDenied         = "ACL_DENIED"
	ErrCodeDNSFailed         = "DNS_FAILED"
	ErrCodeConnectTimeout    = "CONNECT_TIMEOUT"
	ErrCodeConnectionRefused = "CONNECTION_REFUSED"
	ErrCodeNetworkUnreach    = "NETWORK_UNREACHABLE"
	ErrCodeHostUnreach       = "HOST_UNREACHABLE"
	ErrCodeStreamOpenFailed  = "STREAM_OPEN_FAILED"
	ErrCodeDatagramRequired  = "DATAGRAM_REQUIRED"
	ErrCodeTunnelClosed      = "TUNNEL_CLOSED"
	ErrCodeRateLimited       = "RATE_LIMITED"
	ErrCodeConnectionLimit   = "CONNECTION_LIMIT"
	ErrCodeInvalidRequest    = "INVALID_REQUEST"
	ErrCodeInternalError     = "INTERNAL_ERROR"
)

// RelayError represents an application-level protocol error
type RelayError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *RelayError) Error() string {
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

func NewRelayError(code, message string) *RelayError {
	return &RelayError{
		Code:    code,
		Message: message,
	}
}
