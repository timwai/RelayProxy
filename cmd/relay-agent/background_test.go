package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBackgroundChildArgs(t *testing.T) {
	args := backgroundChildArgs([]string{"--background", "--config", "./custom config.yaml", "--server", "127.0.0.1"}, "/tmp/agent.log")
	want := []string{"--background", "--config", "./custom config.yaml", "--server", "127.0.0.1", "--no-gui", "--log-file=/tmp/agent.log", "--_background-child"}
	if strings.Join(args, "\n") != strings.Join(want, "\n") {
		t.Fatalf("backgroundChildArgs = %q, want %q", args, want)
	}
}

func TestResolveBackgroundLogFile(t *testing.T) {
	got, err := resolveBackgroundLogFile("")
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(got) || filepath.Base(got) != "relay-agent.log" {
		t.Fatalf("unexpected background log path: %q", got)
	}
	custom := filepath.Join(t.TempDir(), "custom.log")
	got, err = resolveBackgroundLogFile(custom)
	if err != nil || got != custom {
		t.Fatalf("custom log path = %q, %v", got, err)
	}
}

// This subprocess test exercises the actual detach and readiness handshake
// without opening sockets or requiring a configured RelayProxy server.
func TestBackgroundCommandHandshake(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "agent.log")
	for _, tc := range []struct {
		name string
		mode string
		ok   bool
	}{
		{name: "ready", mode: "ready", ok: true},
		{name: "startup failure", mode: "failed", ok: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestBackgroundHelperProcess$")
			cmd.Env = append(os.Environ(), "RELAYPROXY_TEST_BACKGROUND_HELPER="+tc.mode)
			pid, err := startBackgroundCommand(cmd, logPath)
			if (err == nil) != tc.ok {
				t.Fatalf("pid=%d err=%v, want success=%t", pid, err, tc.ok)
			}
			if tc.ok && pid <= 0 {
				t.Fatalf("invalid background PID %d", pid)
			}
			if !tc.ok && !strings.Contains(err.Error(), logPath) {
				t.Fatalf("startup failure should point to log file: %v", err)
			}
		})
	}
}

func TestBackgroundHelperProcess(t *testing.T) {
	mode := os.Getenv("RELAYPROXY_TEST_BACKGROUND_HELPER")
	if mode == "" {
		return
	}
	if mode == "failed" {
		_, _ = os.Stderr.WriteString("simulated startup failure\n")
		os.Exit(3)
	}
	notifyBackgroundReady()
	time.Sleep(200 * time.Millisecond)
}
