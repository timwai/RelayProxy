//go:build linux

package gateway

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGatewayCommandDiagnostics(t *testing.T) {
	failure := errors.New("exit status 1")
	for _, tc := range []struct {
		name, command, output, want, absent string
		cause                               error
	}{
		{"missing tool", "nft", "", "安装 nftables 和 iproute2", "内核接口不可用", exec.ErrNotFound},
		{"missing capability", "nft", "netlink: Error: cache initialization failed: Operation not permitted", "权限不足", "内核接口不可用", failure},
		{"ip permission", "ip", "RTNETLINK answers: Operation not permitted", "NET_ADMIN", "内核接口不可用", failure},
		{"unsupported kernel", "nft", "netlink: Error: cache initialization failed: Invalid argument", "宿主机 nftables 内核接口不可用或与当前 nft 不兼容", "权限不足", failure},
		{"missing kernel subsystem", "nft", "netlink: Error: cache initialization failed: No such file or directory", "当前内核", "权限不足", failure},
		{"rule error", "nft", "Error: Could not process rule: No such file or directory", "Could not process rule", "内核接口不可用", failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := commandError(tc.command, []string{"list", "tables"}, []byte(tc.output), tc.cause)
			if !errors.Is(err, tc.cause) || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), tc.absent) {
				t.Fatalf("diagnostic lost cause or misclassified failure: %v", err)
			}
			if tc.output != "" && !strings.Contains(err.Error(), tc.output) {
				t.Fatalf("original diagnostic was lost: %v", err)
			}
		})
	}
}

func TestGatewayMissingTableDoesNotHideKernelFailure(t *testing.T) {
	for _, tc := range []struct {
		output string
		want   bool
	}{
		{"Error: No such file or directory\nlist table inet relayproxy_gateway\n                ^^^^^^^^^^^^^^^^^^", true},
		{"Error: Could not process rule: No such file or directory\nlist table inet relayproxy_gateway", true},
		{"netlink: Error: cache initialization failed: No such file or directory", false},
		{"netlink: Error: cache initialization failed: Invalid argument", false},
		{"netlink: Error: cache initialization failed: No such file or directory\nlist table inet relayproxy_gateway", false},
		{"Error: No such file or directory\nlist table inet unrelated_table", false},
		{"Operation not permitted", false},
	} {
		if got := missingTable([]byte(tc.output)); got != tc.want {
			t.Errorf("missingTable(%q) = %v, want %v", tc.output, got, tc.want)
		}
	}
}

func TestGatewayCommandPinsDiagnosticLocale(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "nft"), []byte("#!/bin/sh\nprintf '%s' \"$LC_ALL\"\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("LC_ALL", "fr_FR.UTF-8")
	out, err := command("", "nft", "list", "tables")
	if err == nil || string(out) != "C" {
		t.Fatalf("locale = %q, error = %v", out, err)
	}
}
