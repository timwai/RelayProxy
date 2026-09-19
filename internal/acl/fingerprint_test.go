package acl

import "testing"

func TestPolicyFingerprintComesFromCompiledContent(t *testing.T) {
	p := Policy{
		ID:            "relay",
		Fingerprint:   "caller-controlled",
		AllowInternet: true,
		Rules:         []Rule{{Priority: 10, Action: ActionAllow, Protocol: "TCP", TargetType: TargetDomain, TargetValue: "example.com"}},
	}
	checker, err := NewChecker(p)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := checker.Policy()
	if snapshot.Fingerprint == "" || snapshot.Fingerprint == "caller-controlled" {
		t.Fatalf("fingerprint was not recomputed: %q", snapshot.Fingerprint)
	}
	checker2, err := NewChecker(snapshot)
	if err != nil { t.Fatal(err) }
	if got := checker2.Policy().Fingerprint; got != snapshot.Fingerprint {
		t.Fatalf("fingerprint not stable: %q != %q", got, snapshot.Fingerprint)
	}
}
