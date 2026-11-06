package protocol

import (
	"fmt"
	"strings"

	"golang.org/x/net/dns/dnsmessage"

	"relayproxy/internal/acl"
)

const (
	MaxExitDNSQuestionBytes = 4096
	MaxExitDNSAnswerBytes   = 4096
	MaxExitDNSAnswers       = 16
	ExitDNSTTLSeconds       = 30
)

// ExitDNSRequest carries one bounded A/AAAA query. The Relay overwrites
// RelayPolicy with its own authenticated policy before forwarding.
type ExitDNSRequest struct {
	RequestID   string      `json:"requestId"`
	Question    []byte      `json:"question"`
	RelayPolicy *acl.Policy `json:"relayPolicy,omitempty"`
}

type ExitDNSResponse struct {
	RequestID    string `json:"requestId"`
	Success      bool   `json:"success"`
	Answer       []byte `json:"answer,omitempty"`
	ErrorCode    string `json:"errorCode,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`
}

// ValidateExitDNSQuestion rejects arbitrary DNS transactions, excessive wire
// sizes and unsupported record types before any system resolver is called.
func ValidateExitDNSQuestion(raw []byte) (dnsmessage.Message, string, error) {
	var msg dnsmessage.Message
	if len(raw) < 12 || len(raw) > MaxExitDNSQuestionBytes {
		return msg, "", fmt.Errorf("invalid exit DNS query size")
	}
	if err := msg.Unpack(raw); err != nil || msg.Response || msg.Truncated ||
		msg.OpCode != 0 || len(msg.Questions) != 1 {
		return msg, "", fmt.Errorf("invalid exit DNS question")
	}
	q := msg.Questions[0]
	if q.Class != dnsmessage.ClassINET || (q.Type != dnsmessage.TypeA && q.Type != dnsmessage.TypeAAAA) {
		return msg, "", fmt.Errorf("exit system DNS supports only A and AAAA")
	}
	host := strings.ToLower(strings.TrimSuffix(q.Name.String(), "."))
	if len(host) == 0 || len(host) > 253 {
		return msg, "", fmt.Errorf("invalid DNS hostname")
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return msg, "", fmt.Errorf("invalid DNS hostname label")
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return msg, "", fmt.Errorf("invalid DNS hostname label")
			}
		}
	}
	return msg, host, nil
}
