package androidcore

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"relayproxy/internal/traffic"
)

const (
	maxMonitorApplications = 64
	maxMonitorTargetsPerApp = 6
)

type activeApplicationTarget struct {
	Host     string `json:"host"`
	Port     uint16 `json:"port,omitempty"`
	Protocol string `json:"protocol,omitempty"`
}

type activeApplicationSnapshot struct {
	PackageName    string                    `json:"packageName"`
	PackageAliases []string                  `json:"packageAliases,omitempty"`
	SharedUID      bool                      `json:"sharedUid,omitempty"`
	Connections    int                       `json:"connections"`
	TCP            int                       `json:"tcp"`
	UDP            int                       `json:"udp"`
	Upload         uint64                    `json:"upload"`
	Download       uint64                    `json:"download"`
	UploadRate     uint64                    `json:"uploadRate"`
	DownloadRate   uint64                    `json:"downloadRate"`
	Exits          []string                  `json:"exits,omitempty"`
	Paths          []string                  `json:"paths,omitempty"`
	Targets        []activeApplicationTarget `json:"targets,omitempty"`
}

type activeApplicationsSnapshot struct {
	Applications      []activeApplicationSnapshot `json:"applications"`
	ActiveConnections int                         `json:"activeConnections"`
	TrackedConnections int                        `json:"trackedConnections"`
	OmittedConnections uint64                     `json:"omittedConnections,omitempty"`
	SampledAt          time.Time                   `json:"sampledAt"`
}

type activeApplicationAccumulator struct {
	snapshot activeApplicationSnapshot
	exits    map[string]struct{}
	paths    map[string]struct{}
	targets  map[string]struct{}
}

func (c *Client) ActiveApplicationsJSON() string {
	snapshot := activeApplicationsSnapshot{
		Applications: []activeApplicationSnapshot{},
		SampledAt:    time.Now(),
	}
	if c == nil || c.traffic == nil {
		data, _ := json.Marshal(snapshot)
		return string(data)
	}
	trafficSnapshot := c.traffic.Snapshot()
	snapshot.ActiveConnections = trafficSnapshot.Active
	snapshot.OmittedConnections = trafficSnapshot.Omitted
	snapshot.SampledAt = trafficSnapshot.SampledAt

	groups := make(map[string]*activeApplicationAccumulator)
	for _, connection := range trafficSnapshot.Connections {
		if connection.EndedAt != nil || (connection.State != "active" && connection.State != "connecting") {
			continue
		}
		snapshot.TrackedConnections++
		packages := monitorPackages(connection)
		key := strings.Join(packages, "\x00")
		group := groups[key]
		if group == nil {
			primary := packages[0]
			aliases := append([]string(nil), packages[1:]...)
			group = &activeApplicationAccumulator{
				snapshot: activeApplicationSnapshot{
					PackageName:    primary,
					PackageAliases: aliases,
					SharedUID:      len(packages) > 1,
				},
				exits:   make(map[string]struct{}),
				paths:   make(map[string]struct{}),
				targets: make(map[string]struct{}),
			}
			groups[key] = group
		}
		group.snapshot.Connections++
		switch strings.ToLower(connection.Protocol) {
		case "tcp":
			group.snapshot.TCP++
		case "udp":
			group.snapshot.UDP++
		}
		group.snapshot.Upload += connection.Upload
		group.snapshot.Download += connection.Download
		group.snapshot.UploadRate += monitorRate(connection.UploadRate)
		group.snapshot.DownloadRate += monitorRate(connection.DownloadRate)
		if exitID := strings.TrimSpace(connection.ExitID); exitID != "" {
			group.exits[exitID] = struct{}{}
		}
		if path := strings.TrimSpace(connection.Path); path != "" {
			group.paths[path] = struct{}{}
		}
		if len(group.snapshot.Targets) < maxMonitorTargetsPerApp {
			target := monitorTarget(connection)
			if target.Host != "" {
				targetKey := target.Protocol + "\x00" + target.Host + "\x00" + string(rune(target.Port))
				if _, exists := group.targets[targetKey]; !exists {
					group.targets[targetKey] = struct{}{}
					group.snapshot.Targets = append(group.snapshot.Targets, target)
				}
			}
		}
	}

	for _, group := range groups {
		group.snapshot.Exits = sortedMonitorKeys(group.exits)
		group.snapshot.Paths = sortedMonitorKeys(group.paths)
		snapshot.Applications = append(snapshot.Applications, group.snapshot)
	}
	sort.Slice(snapshot.Applications, func(i, j int) bool {
		left := snapshot.Applications[i]
		right := snapshot.Applications[j]
		leftRate := left.UploadRate + left.DownloadRate
		rightRate := right.UploadRate + right.DownloadRate
		if leftRate != rightRate {
			return leftRate > rightRate
		}
		if left.Connections != right.Connections {
			return left.Connections > right.Connections
		}
		return left.PackageName < right.PackageName
	})
	if len(snapshot.Applications) > maxMonitorApplications {
		snapshot.Applications = snapshot.Applications[:maxMonitorApplications]
	}

	data, err := json.Marshal(snapshot)
	if err != nil {
		return `{"applications":[],"activeConnections":0,"trackedConnections":0}`
	}
	return string(data)
}

func monitorPackages(connection traffic.Connection) []string {
	values := make([]string, 0, 1+len(connection.ProcessAliases))
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" || value == androidUnknownProcess {
			return
		}
		for _, existing := range values {
			if existing == value {
				return
			}
		}
		values = append(values, value)
	}
	add(connection.Process)
	for _, alias := range connection.ProcessAliases {
		add(alias)
	}
	sort.Strings(values)
	if len(values) == 0 {
		return []string{androidUnknownProcess}
	}
	return values
}

func monitorTarget(connection traffic.Connection) activeApplicationTarget {
	host := strings.TrimSpace(connection.Host)
	if host == "" {
		host = strings.TrimSpace(connection.IP)
	}
	return activeApplicationTarget{Host: host, Port: connection.Port, Protocol: strings.ToLower(connection.Protocol)}
}

func monitorRate(rate float64) uint64 {
	if rate <= 0 {
		return 0
	}
	return uint64(rate + 0.5)
}

func sortedMonitorKeys(values map[string]struct{}) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
