//go:build windows

package gui

import (
	"strings"
	"testing"

	agentapp "relayproxy/agent/app"
)

func TestTrayStatusLabels(t *testing.T) {
	t.Run("connected named exit", func(t *testing.T) {
		st := agentapp.AgentStatus{
			Connected:    true,
			Transport:    "quic",
			SelectedExit: "exit-tokyo",
			ProxyExits: []agentapp.ProxyExitSummary{
				{DeviceID: "exit-tokyo", Name: "Tokyo"},
			},
		}
		status, exit, tooltip := trayStatusLabels(st)
		if status != "状态：已连接 · QUIC" {
			t.Fatalf("status=%q", status)
		}
		if exit != "出口：Tokyo" {
			t.Fatalf("exit=%q", exit)
		}
		if !strings.Contains(tooltip, "Tokyo") || !strings.Contains(tooltip, "已连接") {
			t.Fatalf("tooltip=%q", tooltip)
		}
	})

	t.Run("approval pending", func(t *testing.T) {
		status, exit, tooltip := trayStatusLabels(agentapp.AgentStatus{ApprovalState: "pending"})
		if status != "状态：等待审批" || exit != "出口：自动选择" {
			t.Fatalf("unexpected labels: %q %q", status, exit)
		}
		if !strings.Contains(tooltip, "等待审批") {
			t.Fatalf("tooltip=%q", tooltip)
		}
	})

	t.Run("unknown selected exit keeps id", func(t *testing.T) {
		_, exit, tooltip := trayStatusLabels(agentapp.AgentStatus{Connected: true, SelectedExit: "exit-unknown"})
		if exit != "出口：exit-unknown" || !strings.Contains(tooltip, "exit-unknown") {
			t.Fatalf("unexpected unknown exit labels: %q %q", exit, tooltip)
		}
	})
}
