package divert

import (
	"runtime"
	"testing"
)

func TestExecutableSuffixDoesNotOverridePlatformCaseRules(t *testing.T) {
	got := matchProcess("/opt/trusted/tool.exe", "/opt/trusted/TOOL.EXE")
	if got != (runtime.GOOS == "windows") {
		t.Fatalf(".exe suffix changed native path matching on %s: %v", runtime.GOOS, got)
	}
}
