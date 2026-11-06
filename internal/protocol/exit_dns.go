package protocol

import (
	"fmt"
	"net"
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

// ValidateExitDNSAnswer is used by the Relay and the requester. An exit may
// return only the original A/AAAA question with up to 16 matching addresses.
func ValidateExitDNSAnswer(question, answer []byte) ([]net.IP, error) {
	request, _, err := ValidateExitDNSQuestion(question)
	if err != nil {
		return nil, err
	}
	if len(answer) < 12 || len(answer) > MaxExitDNSAnswerBytes {
		return nil, fmt.Errorf("exit DNS answer exceeds size limit")
	}
	var response dnsmessage.Message
	if err := response.Unpack(answer); err != nil || !response.Response ||
		response.ID != request.ID || response.OpCode != 0 ||
		len(response.Questions) != 1 || response.Questions[0] != request.Questions[0] ||
		response.RCode != dnsmessage.RCodeSuccess || len(response.Answers) > MaxExitDNSAnswers {
		return nil, fmt.Errorf("invalid exit DNS answer")
	}
	ips := make([]net.IP, 0, len(response.Answers))
	for _, resource := range response.Answers {
		if resource.Header.Name != request.Questions[0].Name || resource.Header.Type != request.Questions[0].Type ||
			resource.Header.Class != dnsmessage.ClassINET || resource.Header.TTL > 300 {
			return nil, fmt.Errorf("unexpected exit DNS resource")
		}
		switch v := resource.Body.(type) {
		case *dnsmessage.AResource:
			ips = append(ips, net.IP(append([]byte(nil), v.A[:]...)))
		case *dnsmessage.AAAAResource:
			ips = append(ips, net.IP(append([]byte(nil), v.AAAA[:]...)))
		default:
			return nil, fmt.Errorf("unsupported exit DNS resource")
		}
	}
	return ips, nil
}
