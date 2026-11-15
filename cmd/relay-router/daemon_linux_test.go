//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const daemonTestChildEnv = "RELAY_ROUTER_DAEMON_TEST_CHILD"
const daemonTestMarkerEnv = "RELAY_ROUTER_DAEMON_TEST_MARKER"

func TestDaemonChildHelper(t *testing.T) {
	if os.Getenv(daemonTestChildEnv) != "1" {
		return
	}
	marker := os.Getenv(daemonTestMarkerEnv)
	if marker == "" {
		t.Fatal("missing daemon test marker path")
	}
	if err := os.WriteFile(marker, []byte("started"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLaunchDaemonDetachesAndRedirectsOutput(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "started")
	logFile := filepath.Join(dir, "daemon.log")

	t.Setenv(daemonTestChildEnv, "1")
	t.Setenv(daemonTestMarkerEnv, marker)

	pid, err := launchDaemon(executable, []string{"-test.run=^TestDaemonChildHelper$"}, logFile)
	if err != nil {
		t.Fatalf("launchDaemon() error = %v", err)
	}
	if pid <= 0 {
		t.Fatalf("launchDaemon() returned invalid pid %d", pid)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			if _, err := os.Stat(logFile); err != nil {
				t.Fatalf("daemon log was not created: %v", err)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("detached child did not create its startup marker")
}

func TestDaemonChildEnvironmentReplacesMarker(t *testing.T) {
	got := daemonChildEnvironment([]string{"PATH=/bin", daemonChildEnv + "=0", "HOME=/root"})
	if len(got) != 3 || got[1] != "HOME=/root" || got[2] != daemonChildEnv+"=1" {
		t.Fatalf("unexpected daemon child environment: %#v", got)
	}
}
