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
	if len(shared.Targets) != 2 {
		t.Fatalf("shared UID targets=%+v", shared.Targets)
	}
	if missing == nil || missing.Connections != 1 {
		t.Fatalf("unknown application group=%+v", missing)
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
