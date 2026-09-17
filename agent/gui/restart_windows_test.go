//go:build windows

package gui

import (
	"strings"
	"testing"
)

func TestRestartEnvironmentReplacesParentPID(t *testing.T) {
	environment := []string{"RELAYPROXY_TEST=1", restartParentEnv + "=old", "PATH=C:\\RelayProxy"}
	got := restartEnvironment(environment, 1234)
	seen := 0
	for _, item := range got {
		if strings.HasPrefix(item, restartParentEnv+"=") {
			seen++
			if item != restartParentEnv+"=1234" {
				t.Fatalf("restart parent marker = %q", item)
			}
		}
	}
	if seen != 1 {
		t.Fatalf("restart parent marker count = %d, want 1", seen)
	}
}
