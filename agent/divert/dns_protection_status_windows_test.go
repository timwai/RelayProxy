//go:build windows

package divert

import (
	"context"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsDNSGuardEvidenceRequiresActivePersistentAndCorrectPorts(t *testing.T) {
	good := []byte(`{"profilesEnabled":true,"entries":[
	{"name":"RelayProxy-DNS-KillSwitch-TCP","active":true,"persistent":true,"enabled":"True","direction":"Outbound","action":"Block","protocol":"TCP","ports":["53","853","784","8853"]},
	{"name":"RelayProxy-DNS-KillSwitch-UDP","active":true,"persistent":true,"enabled":"True","direction":"Outbound","action":"Block","protocol":"UDP","ports":["53","853","784","8853"]}
	]}`)
	if state, msg := interpretWindowsDNSGuardEvidence(good); state != "rules-present" || msg == "" {
		t.Fatalf("expected verified rule presence; got %q %q", state, msg)
	}
	cases := []struct {
		name string
		raw  []byte
		want string
	}{
		{"empty", []byte(`{"profilesEnabled":true,"entries":[]}`), "not-installed"},
		{"invalid", []byte("not JSON"), "unknown"},
		{"missing_udp", []byte(`{"profilesEnabled":true,"entries":[{"name":"RelayProxy-DNS-KillSwitch-TCP","active":true,"persistent":true,"enabled":"True","direction":"Outbound","action":"Block","protocol":"TCP","ports":["53","853","784","8853"]}]}`), "incomplete"},
		{"disabled", []byte(`{"profilesEnabled":true,"entries":[
		{"name":"RelayProxy-DNS-KillSwitch-TCP","active":true,"persistent":true,"enabled":"False","direction":"Outbound","action":"Block","protocol":"TCP","ports":["53","853","784","8853"]},
		{"name":"RelayProxy-DNS-KillSwitch-UDP","active":true,"persistent":true,"enabled":"True","direction":"Outbound","action":"Block","protocol":"UDP","ports":["53","853","784","8853"]}]}`), "incomplete"},
		{"firewall_profile_off", []byte(`{"profilesEnabled":false,"entries":[
		{"name":"RelayProxy-DNS-KillSwitch-TCP","active":true,"persistent":true,"enabled":"True","direction":"Outbound","action":"Block","protocol":"TCP","ports":["53","853","784","8853"]},
		{"name":"RelayProxy-DNS-KillSwitch-UDP","active":true,"persistent":true,"enabled":"True","direction":"Outbound","action":"Block","protocol":"UDP","ports":["53","853","784","8853"]}]}`), "incomplete"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := interpretWindowsDNSGuardEvidence(tc.raw)
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWindowsDNSGuardPowerShellProbeNeverShowsConsole(t *testing.T) {
	cmd := windowsDNSGuardCommand(context.Background())
	if cmd.Path == "" || len(cmd.Args) < 4 {
		t.Fatalf("invalid PowerShell DNS guard command: %q %q", cmd.Path, cmd.Args)
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.HideWindow {
		t.Fatal("periodic DNS firewall PowerShell query would show a console window")
	}
	if cmd.SysProcAttr.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
		t.Fatal("DNS firewall query must use CREATE_NO_WINDOW to prevent terminal flashes")
	}
}
