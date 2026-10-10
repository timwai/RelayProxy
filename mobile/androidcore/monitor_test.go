package androidcore

import (
	"encoding/json"
	"testing"

	"relayproxy/internal/traffic"
)

func TestActiveApplicationsJSONGroupsLiveAndroidPackages(t *testing.T) {
	client := &Client{traffic: traffic.NewRegistry(16, 4)}

	first := client.traffic.Start(traffic.Metadata{
		Process:        "com.example.alpha",
		ProcessAliases: []string{"com.example.shared"},
		Host:           "api.example.com",
		Port:           443,
		Protocol:       "tcp",
		Action:         "PROXY",
		Rule:           "视频优先",
		ExitID:         "exit-a",
	})
	first.Activate()
	first.AddUpload(2048)
	first.AddDownload(8192)

	second := client.traffic.Start(traffic.Metadata{
		Process:        "com.example.shared",
		ProcessAliases: []string{"com.example.alpha"},
		Host:           "dns.example.com",
		Port:           53,
		Protocol:       "udp",
		Action:         "PROXY",
		Rule:           "默认规则",
		ExitID:         "exit-a",
	})
	second.Activate()
	second.AddUpload(512)
	second.AddDownload(1024)

	unknown := client.traffic.Start(traffic.Metadata{
		Process:  androidUnknownProcess,
		IP:       "203.0.113.8",
		Port:     443,
		Protocol: "tcp",
		Action:   "PROXY",
	})
	unknown.Activate()

	var snapshot activeApplicationsSnapshot
	if err := json.Unmarshal([]byte(client.ActiveApplicationsJSON()), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.ActiveConnections != 3 || snapshot.TrackedConnections != 3 {
		t.Fatalf("connection counts=%d/%d", snapshot.ActiveConnections, snapshot.TrackedConnections)
	}
	if len(snapshot.Applications) != 2 {
		t.Fatalf("applications=%+v", snapshot.Applications)
	}

	var shared *activeApplicationSnapshot
	var missing *activeApplicationSnapshot
	for index := range snapshot.Applications {
		app := &snapshot.Applications[index]
		switch app.PackageName {
		case "com.example.alpha":
			shared = app
		case androidUnknownProcess:
			missing = app
		}
	}
	if shared == nil {
		t.Fatalf("shared UID application group missing: %+v", snapshot.Applications)
	}
	if !shared.SharedUID || len(shared.PackageAliases) != 1 ||
		shared.PackageAliases[0] != "com.example.shared" {
		t.Fatalf("shared UID identity=%+v", shared)
	}
	if shared.Connections != 2 || shared.TCP != 1 || shared.UDP != 1 {
		t.Fatalf("shared UID connections=%+v", shared)
	}
	if shared.Upload != 2560 || shared.Download != 9216 {
		t.Fatalf("shared UID traffic=%+v", shared)
	}
	if len(shared.Exits) != 1 || shared.Exits[0] != "exit-a" {
		t.Fatalf("shared UID exits=%+v", shared.Exits)
	}
	if len(shared.Rules) != 2 || shared.Rules[0] != "视频优先" && shared.Rules[1] != "视频优先" {
		t.Fatalf("shared UID rules=%+v", shared.Rules)
	}
	if len(shared.Targets) != 2 {
		t.Fatalf("shared UID targets=%+v", shared.Targets)
	}
	if missing == nil || missing.Connections != 1 {
		t.Fatalf("unknown application group=%+v", missing)
	}
}

func TestActiveApplicationsJSONDeduplicatesMatchedRulesAndOmitsClosedRules(t *testing.T) {
	client := &Client{traffic: traffic.NewRegistry(16, 4)}
	for _, rule := range []string{"直连规则", "代理规则", "代理规则", ""} {
		record := client.traffic.Start(traffic.Metadata{
			Process: "com.example.alpha", Host: "api.example.com",
			Port: 443, Protocol: "tcp", Rule: rule,
		})
		record.Activate()
	}
	closed := client.traffic.Start(traffic.Metadata{
		Process: "com.example.alpha", Host: "old.example.com",
		Port: 443, Protocol: "tcp", Rule: "旧规则",
	})
	closed.Activate()
	closed.Finish("closed", nil)

	var snapshot activeApplicationsSnapshot
	if err := json.Unmarshal([]byte(client.ActiveApplicationsJSON()), &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Applications) != 1 {
		t.Fatalf("applications: %+v", snapshot.Applications)
	}
	app := snapshot.Applications[0]
	if app.Connections != 4 || len(app.Rules) != 2 ||
		app.Rules[0] != "代理规则" || app.Rules[1] != "直连规则" {
		t.Fatalf("rule aggregation: %+v", app)
	}
}

func TestActiveApplicationsJSONOmitsClosedConnections(t *testing.T) {
	client := &Client{traffic: traffic.NewRegistry(16, 4)}
	record := client.traffic.Start(traffic.Metadata{
		Process: "com.example.closed", Host: "example.com", Port: 443, Protocol: "tcp",
	})
	record.Activate()
	record.Finish("closed", nil)

	var snapshot activeApplicationsSnapshot
	if err := json.Unmarshal([]byte(client.ActiveApplicationsJSON()), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.ActiveConnections != 0 || snapshot.TrackedConnections != 0 || len(snapshot.Applications) != 0 {
		t.Fatalf("closed connection leaked into live monitor: %+v", snapshot)
	}
}
