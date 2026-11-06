package protocol

import (
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

func exitDNSQuestion(t *testing.T, host string, qtype dnsmessage.Type) []byte {
	t.Helper()
	name, err := dnsmessage.NewName(host + ".")
	if err != nil {
		t.Fatal(err)
	}
	wire, err := (dnsmessage.Message{Header: dnsmessage.Header{ID: 41, RecursionDesired: true}, Questions: []dnsmessage.Question{{Name: name, Type: qtype, Class: dnsmessage.ClassINET}}}).Pack()
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

func TestExitDNSQuestionAdmitsOnlyAAndAAAA(t *testing.T) {
	for _, kind := range []dnsmessage.Type{dnsmessage.TypeA, dnsmessage.TypeAAAA} {
		_, host, err := ValidateExitDNSQuestion(exitDNSQuestion(t, "www.example.com", kind))
		if err != nil || host != "www.example.com" {
			t.Fatalf("kind %v host %q err %v", kind, host, err)
		}
	}
	for _, kind := range []dnsmessage.Type{dnsmessage.TypeTXT, dnsmessage.TypeMX, dnsmessage.Type(65)} {
		if _, _, err := ValidateExitDNSQuestion(exitDNSQuestion(t, "www.example.com", kind)); err == nil {
			t.Fatalf("accepted non-address lookup type %v", kind)
		}
	}
	if _, _, err := ValidateExitDNSQuestion(make([]byte, MaxExitDNSQuestionBytes+1)); err == nil {
		t.Fatal("accepted oversized query")
	}
	for _, host := range []string{"bad_host.example", "-bad.example"} {
		_, _, err := ValidateExitDNSQuestion(exitDNSQuestion(t, host, dnsmessage.TypeA))
		if host == "192.168.1.1" {
			continue
		} // valid DNS labels; ACL handles literal destinations
		if err == nil {
			t.Errorf("accepted forbidden host %q", host)
		}
	}
}

func TestExitDNSAnswerValidatesIDTypeAndAddressCount(t *testing.T) {
	query := exitDNSQuestion(t, "example.com", dnsmessage.TypeA)
	msg, _, err := ValidateExitDNSQuestion(query)
	if err != nil {
		t.Fatal(err)
	}
	resource := dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: msg.Questions[0].Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: ExitDNSTTLSeconds}, Body: &dnsmessage.AResource{A: [4]byte{203, 0, 113, 10}}}
	reply, err := (dnsmessage.Message{Header: dnsmessage.Header{ID: msg.ID, Response: true}, Questions: msg.Questions, Answers: []dnsmessage.Resource{resource}}).Pack()
	if err != nil {
		t.Fatal(err)
	}
	ips, err := ValidateExitDNSAnswer(query, reply)
	if err != nil || len(ips) != 1 || ips[0].String() != "203.0.113.10" {
		t.Fatalf("unexpected answer ips=%v err=%v", ips, err)
	}
	bad := append([]byte(nil), reply...)
	bad[1]++ // transaction mismatch
	if _, err := ValidateExitDNSAnswer(query, bad); err == nil {
		t.Fatal("accepted wrong DNS transaction")
	}
	if _, err := ValidateExitDNSAnswer(exitDNSQuestion(t, "example.com", dnsmessage.TypeAAAA), reply); err == nil {
		t.Fatal("accepted wrong question type")
	}
	if _, err := ValidateExitDNSAnswer(query, make([]byte, MaxExitDNSAnswerBytes+1)); err == nil {
		t.Fatal("accepted oversized DNS answer")
	}
}
