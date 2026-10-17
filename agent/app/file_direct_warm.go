package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"relayproxy/internal/protocol"
)

// FileDirectWarmStatus describes only connection readiness, not a credential.
// Every subsequent file request still needs a fresh one-time Server ticket,
// host authorization and TLS fingerprint validation.
type FileDirectWarmStatus struct {
	State string `json:"state"`
	Path  string `json:"path,omitempty"`
}

type fileDirectWarmHint struct {
	state string
	path  string
	until time.Time
	epoch uint64
}

const (
	fileDirectWarmReadyTTL   = 45 * time.Second
	fileDirectWarmFailureTTL = 12 * time.Second
)

// PrewarmRemoteFileDirect is intentionally limited to a verified share root.
// It proves reachability with a cheap authorized shared-inventory request,
// exercises P2P punch/direct TLS/QUIC without transferring file contents,
// and caches only a short-lived outcome. A failed warmup is not an ACL grant.
func (a *Agent) PrewarmRemoteFileDirect(ctx context.Context, deviceID, shareID string) (FileDirectWarmStatus, error) {
	deviceID = strings.TrimSpace(deviceID)
	shareID = strings.TrimSpace(shareID)
	if deviceID == "" || shareID == "" || len(deviceID) > protocol.MaxStringLen || len(shareID) > protocol.MaxStringLen {
		return FileDirectWarmStatus{}, errors.New("invalid file direct warmup target")
	}
	if _, _, err := a.fileSharingSession(); err != nil {
		return FileDirectWarmStatus{}, err
	}
	a.mu.RLock()
	epoch := a.epoch
	a.mu.RUnlock()
	key := deviceID + "\x00" + shareID
	now := time.Now()
	a.fileWarmMu.Lock()
	if hint, ok := a.fileWarmHints[key]; ok && hint.epoch == epoch && now.Before(hint.until) {
		a.fileWarmMu.Unlock()
		return FileDirectWarmStatus{State: hint.state, Path: hint.path}, nil
	}
	if a.fileWarmSlots == nil {
		a.fileWarmSlots = make(chan struct{}, 2)
	}
	slots := a.fileWarmSlots
	// Prevent the UI from dispatching duplicate offers on every folder click.
	if len(a.fileWarmHints) > 32 {
		a.fileWarmHints = map[string]fileDirectWarmHint{}
	}
	if a.fileWarmHints == nil {
		a.fileWarmHints = make(map[string]fileDirectWarmHint)
	}
	a.fileWarmHints[key] = fileDirectWarmHint{state: "warming", epoch: epoch, until: now.Add(15 * time.Second)}
	a.fileWarmMu.Unlock()
	select {
	case slots <- struct{}{}:
	case <-ctx.Done():
		a.forgetFileWarm(key, epoch)
		return FileDirectWarmStatus{}, ctx.Err()
	}
	defer func() { <-slots }()
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	stream, response, err := a.tryFileDirectMode(probeCtx, deviceID, protocol.FileShareRequest{
		Action: protocol.FileShareListShares,
	}, true)
	// Verify that the requested share is still visible under the authenticated
	// peer ACL; never infer permission from a successful transport handshake.
	authorized := false
	if err == nil {
		for _, share := range response.Shares {
			if share.ID == shareID {
				authorized = true
				break
			}
		}
	}
	result := FileDirectWarmStatus{State: "relay"}
	ttl := fileDirectWarmFailureTTL
	if err == nil && authorized {
		result.State = "ready"
		result.Path = fileTransferPath(stream)
		ttl = fileDirectWarmReadyTTL
	}
	if stream != nil {
		_ = stream.Close()
	}
	if probeCtx.Err() != nil && ctx.Err() != nil {
		a.forgetFileWarm(key, epoch)
		return FileDirectWarmStatus{}, ctx.Err()
	}
	a.fileWarmMu.Lock()
	if current, ok := a.fileWarmHints[key]; ok && current.epoch == epoch {
		a.fileWarmHints[key] = fileDirectWarmHint{state: result.State, path: result.Path, epoch: epoch, until: time.Now().Add(ttl)}
	}
	a.fileWarmMu.Unlock()
	return result, nil
}

func (a *Agent) forgetFileWarm(key string, epoch uint64) {
	a.fileWarmMu.Lock()
	if item, ok := a.fileWarmHints[key]; ok && item.epoch == epoch {
		delete(a.fileWarmHints, key)
	}
	a.fileWarmMu.Unlock()
}

// Skip a costly failed NAT probe for a few seconds, never skip fresh Relay
// authorization or any direct path that has been confirmed reachable.
func (a *Agent) fileDirectRecentlyUnreachable(deviceID, shareID string) bool {
	a.mu.RLock()
	epoch := a.epoch
	a.mu.RUnlock()
	a.fileWarmMu.Lock()
	hint, ok := a.fileWarmHints[deviceID+"\x00"+shareID]
	a.fileWarmMu.Unlock()
	return ok && hint.epoch == epoch && hint.state == "relay" && time.Now().Before(hint.until)
}

// A successful preflight is only a transport-order hint. The next request
// always performs independent, Server-issued one-time ticket authentication.
func (a *Agent) fileDirectWarmPath(deviceID, shareID string) string {
	a.mu.RLock()
	epoch := a.epoch
	a.mu.RUnlock()
	a.fileWarmMu.Lock()
	hint, ok := a.fileWarmHints[deviceID+"\x00"+shareID]
	a.fileWarmMu.Unlock()
	if !ok || hint.epoch != epoch || hint.state != "ready" || time.Now().After(hint.until) {
		return ""
	}
	return hint.path
}
