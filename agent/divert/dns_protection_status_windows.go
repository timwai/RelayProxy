//go:build windows

package divert

import (
	"context"
	"encoding/json"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"
)

var windowsDNSGuardCache struct {
	sync.Mutex
	checked time.Time
	status  string
	detail  string
}

type windowsDNSGuardEvidence struct {
	ProfilesEnabled bool `json:"profilesEnabled"`
	Entries         []struct {
		Name       string   `json:"name"`
		Active     bool     `json:"active"`
		Persistent bool     `json:"persistent"`
		Enabled    string   `json:"enabled"`
		Direction  string   `json:"direction"`
		Action     string   `json:"action"`
		Protocol   string   `json:"protocol"`
		Ports      []string `json:"ports"`
	} `json:"entries"`
}

// Check both the ACTIVE and PERSISTENT rule stores. Firewall rule names alone
// are not evidence: inspect protocol, remote ports, direction, action and state.
// Fail conservatively if group policy or missing privileges prevent querying.
const windowsDNSGuardProbe = `$ErrorActionPreference='Stop'
$profilesEnabled = @((Get-NetFirewallProfile | Where-Object { $_.Enabled -eq $false })).Count -eq 0
$items = @()
foreach ($proto in @('TCP','UDP')) {
  $name = 'RelayProxy-DNS-KillSwitch-' + $proto
  $active = Get-NetFirewallRule -Name $name -PolicyStore ActiveStore -ErrorAction SilentlyContinue
  $persistent = Get-NetFirewallRule -Name $name -PolicyStore PersistentStore -ErrorAction SilentlyContinue
  if ($null -eq $active) { continue }
  $filter = $active | Get-NetFirewallPortFilter
  $items += [pscustomobject]@{
    name=$name; active=$true; persistent=($null -ne $persistent)
    enabled=[string]$active.Enabled; direction=[string]$active.Direction
    action=[string]$active.Action; protocol=[string]$filter.Protocol
    ports=@($filter.RemotePort | ForEach-Object { [string]$_ })
  }
}
@{ profilesEnabled=$profilesEnabled; entries=@($items) } | ConvertTo-Json -Compress -Depth 5`

func platformIndependentDNSGuardStatus() (string, string) {
	windowsDNSGuardCache.Lock()
	defer windowsDNSGuardCache.Unlock()
	if time.Since(windowsDNSGuardCache.checked) < 20*time.Second {
		return windowsDNSGuardCache.status, windowsDNSGuardCache.detail
	}
	windowsDNSGuardCache.checked = time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", windowsDNSGuardProbe).Output()
	if err != nil {
		windowsDNSGuardCache.status = "unknown"
		windowsDNSGuardCache.detail = "Cannot inspect Windows Firewall ActiveStore and PersistentStore"
		return windowsDNSGuardCache.status, windowsDNSGuardCache.detail
	}
	windowsDNSGuardCache.status, windowsDNSGuardCache.detail = interpretWindowsDNSGuardEvidence(output)
	return windowsDNSGuardCache.status, windowsDNSGuardCache.detail
}

func interpretWindowsDNSGuardEvidence(raw []byte) (string, string) {
	var evidence windowsDNSGuardEvidence
	if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(string(raw), "\ufeff"))), &evidence); err != nil {
		return "unknown", "Windows Firewall returned invalid rule information"
	}
	if len(evidence.Entries) == 0 {
		return "not-installed", "No RelayProxy DNS Firewall rules were found in ActiveStore"
	}
	for _, protocol := range []string{"TCP", "UDP"} {
		found := false
		for _, entry := range evidence.Entries {
			if entry.Name != "RelayProxy-DNS-KillSwitch-"+protocol {
				continue
			}
			found = true
			if !entry.Active || !entry.Persistent || entry.Enabled != "True" ||
				entry.Direction != "Outbound" || entry.Action != "Block" ||
				!strings.EqualFold(entry.Protocol, protocol) {
				return "incomplete", "Windows DNS rule exists but is disabled, non-persistent or incorrectly configured"
			}
			for _, port := range []string{"53", "853", "784", "8853"} {
				if !slices.Contains(entry.Ports, port) {
					return "incomplete", "Windows DNS rule does not block all expected remote DNS ports"
				}
			}
		}
		if !found {
			return "incomplete", "Missing RelayProxy persistent Windows Firewall rule for " + protocol
		}
	}
	if !evidence.ProfilesEnabled {
		return "incomplete", "At least one Windows Firewall profile is disabled"
	}
	return "rules-present", "Both DNS port block rules observed in ActiveStore and PersistentStore; WFP/WinDivert ordering and bypass remain unverified"
}
