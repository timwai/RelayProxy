//go:build darwin

package gui

import (
	"testing"
)

func TestManagementURLPrefersEffectiveRuntimeAddress(t *testing.T) {
	got := managementURL(nil, Options{WebURL: " http://127.0.0.1:18765 "})
	if got != "http://127.0.0.1:18765/" {
		t.Fatalf("managementURL() = %q", got)
	}
}
