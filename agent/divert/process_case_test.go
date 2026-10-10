package divert

import "testing"

func TestProcessMatchingIgnoresCaseOnEveryPlatform(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		process string
		want    bool
	}{
		{"basename", "chrome.exe", "/opt/apps/CHROME.EXE", true},
		{"unix full path", "/opt/Trusted/*.exe", "/OPT/TRUSTED/Tool.EXE", true},
		{"windows full path", `C:\Apps\Chrome.EXE`, `c:\APPS\chrome.exe`, true},
		{"wildcard", "FireFox*", "/usr/bin/FIREFOX-ESR", true},
		{"service alias", "service:Dnscache", "SERVICE:DNSCACHE", true},
		{"unrelated process", "chrome.exe", "/opt/apps/firefox.exe", false},
		{"different directory", "/opt/trusted/*.exe", "/opt/other/TOOL.EXE", false},
		{"unknown process", "chrome.exe", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchProcess(tc.pattern, tc.process); got != tc.want {
				t.Fatalf("matchProcess(%q, %q) = %v, want %v", tc.pattern, tc.process, got, tc.want)
			}
		})
	}
}

func TestProcessExclusionsIgnoreCase(t *testing.T) {
	engine := requireEngine(t, Config{
		DefaultAction:    ActionProxy,
		ExcludeProcesses: []string{"relayproxy.exe"},
		Rules:            []Rule{{Name: "all", Enabled: true, Process: "*", Action: ActionProxy}},
	})
	for _, name := range []string{"RelayProxy.EXE", "/usr/bin/RELAYPROXY.exe"} {
		if got := engine.Match(Flow{Process: name, Protocol: ProtoTCP}); got.Action != ActionDirect {
			t.Fatalf("case-insensitive exclusion missed %q: %+v", name, got)
		}
	}
}
