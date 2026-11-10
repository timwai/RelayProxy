package androidcore

import (
	"encoding/json"
	"errors"
	"strings"
)

// ProbeExitJSON is a bounded, UI-initiated response-latency measurement.
// Inventory and approval are checked again here; a stale Android button cannot
// test an offline, removed, or unauthorized exit through a cached direct path.
func (c *Client) ProbeExitJSON(exitID string) (string, error) {
	if c == nil {
		return "", errors.New("服务未运行")
	}
	if !c.probeMu.TryLock() {
		return "", errors.New("出口检测正在进行")
	}
	defer c.probeMu.Unlock()
	exitID = strings.TrimSpace(exitID)
	token, err := c.exitProbeAllowed(exitID)
	if err != nil {
		return "", err
	}
	result, err := c.proxyDialer.ProbeExit(c.ctx, exitID)
	if err != nil {
		return "", err
	}
	current, err := c.exitProbeAllowed(exitID)
	if err != nil {
		return "", err
	}
	if current != token {
		return "", errors.New("出口列表或连接已更新，请重新检测")
	}
	raw, err := json.Marshal(result)
	return string(raw), err
}

type exitProbeToken struct{ epoch, revision uint64 }

func (c *Client) exitProbeAllowed(exitID string) (exitProbeToken, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.closed || c.ctx == nil || c.ctx.Err() != nil || c.proxyDialer == nil || c.status.ConnectionState != "CONNECTED" {
		return exitProbeToken{}, errors.New("请先连接 Relay Server")
	}
	if !c.clientApproved.Load() {
		return exitProbeToken{}, errors.New("代理客户端未启用或尚未授权")
	}
	for _, exit := range c.status.ProxyExits {
		if exit.DeviceID == exitID && exitID != "" {
			if !exit.Online {
				return exitProbeToken{}, errors.New("出口已离线")
			}
			return exitProbeToken{c.probeEpoch, c.proxyExitRevision}, nil
		}
	}
	return exitProbeToken{}, errors.New("出口不在已授权列表中")
}
