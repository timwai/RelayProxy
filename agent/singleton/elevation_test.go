package singleton

import "testing"

func TestReplacementHelperRejectsInvalidTargets(t *testing.T) {
	for _, raw := range []string{
		`{`,
		`{"name":"OtherService","pid":42,"requester":100,"created":1234}`,
		`{"name":"RelayProxyAgent_Instance","pid":42}`,
		`{"name":"RelayProxyAgent_Instance","pid":-1,"requester":100,"created":1234}`,
		`{"name":"RelayProxyAgent_Instance","pid":4294967296,"requester":100,"created":1234}`,
	} {
		if _, err := parseReplacementRequest(raw); err == nil {
			t.Fatalf("unsafe replacement request accepted: %s", raw)
		}
	}
}

func TestReplacementHelperCannotKillAClientThatWonDuringUAC(t *testing.T) {
	for _, expectedPID := range []uint32{0, 42} {
		request := replacementRequest{Name: AgentInstanceName, PID: expectedPID, Requester: 100, Created: 1234}
		if !request.canReplace(42, 1233) {
			t.Fatal("old client could not be replaced")
		}
		for _, owner := range []struct {
			pid     uint32
			created uint64
		}{{0, 1233}, {100, 1233}, {42, 0}, {42, 1234}, {42, 1235}} {
			if request.canReplace(owner.pid, owner.created) {
				t.Fatalf("replacement could kill self, an unknown or a newer process: %+v", owner)
			}
		}
		if expectedPID != 0 && request.canReplace(43, 1233) {
			t.Fatal("UAC helper could kill a different mutex owner")
		}
	}
}
