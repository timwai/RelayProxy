//go:build windows

package main

import "testing"

func TestConsumeElevationParentArgument(t *testing.T) {
	parent, args := consumeElevationParentArgument([]string{
		"relay-agent.exe",
		"--minimized",
		elevationParentArgument + "=1234",
		"--config",
		`C:\\Relay Proxy\\agent.yaml`,
	})
	if parent != 1234 {
		t.Fatalf("parent = %d, want 1234", parent)
	}
	if len(args) != 4 || args[1] != "--minimized" || args[2] != "--config" {
		t.Fatalf("filtered arguments = %#v", args)
	}
}

func TestConsumeElevationParentArgumentIgnoresInvalidPID(t *testing.T) {
	parent, args := consumeElevationParentArgument([]string{
		"relay-agent.exe",
		elevationParentArgument + "=not-a-pid",
		"--no-gui",
	})
	if parent != 0 {
		t.Fatalf("parent = %d, want 0", parent)
	}
	if len(args) != 2 || args[1] != "--no-gui" {
		t.Fatalf("filtered arguments = %#v", args)
	}
}
