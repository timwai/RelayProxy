//go:build linux

package divert

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"
)

var linuxIndependentGuardCache struct {
	sync.Mutex
	checked time.Time
	status  string
	detail  string
}

// Checking once per refresh would fork nft on every UI poll. Cache for 15
// seconds, and return "unknown" rather than claiming safety on read failures.
func platformIndependentDNSGuardStatus() (string, string) {
	linuxIndependentGuardCache.Lock()
	defer linuxIndependentGuardCache.Unlock()
	if time.Since(linuxIndependentGuardCache.checked) < 15*time.Second {
		return linuxIndependentGuardCache.status, linuxIndependentGuardCache.detail
	}
	linuxIndependentGuardCache.checked = time.Now()
	bin, err := exec.LookPath("nft")
	if err != nil {
		linuxIndependentGuardCache.status = "unknown"
		linuxIndependentGuardCache.detail = "nft not installed; cannot verify independent DNS guard"
		return linuxIndependentGuardCache.status, linuxIndependentGuardCache.detail
	}
	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()
	output, err := exec.CommandContext(ctx, bin, "list", "table", "inet", "relayproxy_dns_guard").CombinedOutput()
	status, detail := interpretLinuxDNSGuardEvidence(output, err)
	if status == "rules-present" {
		// A currently installed table may disappear after reboot. The
		// standalone script installs a systemd oneshot service, which must
		// be both active now and enabled for boot. Do not mistake table
		// presence for configured restart persistence.
		unit := "relayproxy-dns-killswitch.service"
		verifyCtx, verifyCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer verifyCancel()
		enabled := exec.CommandContext(verifyCtx, "systemctl", "is-enabled", "--quiet", unit).Run() == nil
		active := exec.CommandContext(verifyCtx, "systemctl", "is-active", "--quiet", unit).Run() == nil
		if !enabled || !active {
			status, detail = "persistence-unverified", "nftables DNS rules exist, but the systemd restart guard is not confirmed enabled and active"
		} else {
			detail = "nftables DNS drops and boot-time systemd guard observed; on-wire DNS bypasses still require real-device verification"
		}
	}
	linuxIndependentGuardCache.status, linuxIndependentGuardCache.detail = status, detail
	return status, detail
}

func interpretLinuxDNSGuardEvidence(output []byte, err error) (string, string) {
	data := strings.ToLower(string(output))
	if err != nil {
		if strings.Contains(data, "no such file") || strings.Contains(data, "does not exist") {
			return "not-installed", "persistent nftables DNS guard table was not found"
		}
		return "unknown", "unable to query persistent nftables DNS guard (permissions/service state)"
	}
	// Verify the expected drop rule appears, not merely the table name.
	if !strings.Contains(data, "hook output") || !strings.Contains(data, "drop") ||
		!strings.Contains(data, "53") || !strings.Contains(data, "853") {
		return "incomplete", "persistent guard table exists, but expected output drop rule was not confirmed"
	}
	return "rules-present", "output DNS drop rules observed; startup persistence and DNS leak paths still require verification"
}
