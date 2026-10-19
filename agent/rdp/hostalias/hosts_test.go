package hostalias

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAliasStableDistinctAndUnicode(t *testing.T) {
	a := Alias("Office PC", "device-001")
	if a != Alias("Office PC", "device-001") {
		t.Fatal("alias changed")
	}
	if a == Alias("Office PC", "device-002") {
		t.Fatal("same-name devices must not collide")
	}
	if !strings.HasPrefix(a, "relayproxy-office-pc-") || !validAlias.MatchString(a) {
		t.Fatalf("bad alias %q", a)
	}
	for _, name := range []string{"工作电脑", "My PC!  01", "🤖", strings.Repeat("verylongname", 15)} {
		value := Alias(name, "dev-001")
		if !validAlias.MatchString(value) || len(value) > 63 || strings.Contains(value, ".test") {
			t.Fatalf("%q -> invalid alias %q", name, value)
		}
	}
}

func TestAppendHostsPreservesUserContentAndIsIdempotent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "hosts")
	prior := "# managed by system\r\n127.0.0.1 localhost\r\n192.0.2.4 internal.example\r\n"
	if err := os.WriteFile(p, []byte(prior), 0600); err != nil {
		t.Fatal(err)
	}
	alias := Alias("Work Laptop", "unique-id")
	if err := writeHosts(p, alias); err != nil {
		t.Fatal(err)
	}
	if err := writeHosts(p, alias); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(after), prior) {
		t.Fatal("preexisting hosts settings changed")
	}
	if strings.Count(string(after), "127.0.0.1 "+alias+" # RelayProxy RDP") != 1 {
		t.Fatalf("missing or duplicate alias: %q", after)
	}
	if strings.Contains(string(after), "\n127.0.0.1 "+alias) && !strings.Contains(string(after), "\r\n127.0.0.1 "+alias) {
		t.Fatal("Windows CRLF line ending was lost")
	}
	if err := verifyHosts(p, alias); err != nil {
		t.Fatal(err)
	}
}

func TestHostsAppendAddsSeparatorWhenLastLineIsUnterminated(t *testing.T) {
	p := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(p, []byte("127.0.0.1 localhost"), 0600); err != nil {
		t.Fatal(err)
	}
	alias := Alias("workstation", "device-id")
	if err := writeHosts(p, alias); err != nil {
		t.Fatal(err)
	}
	result, _ := os.ReadFile(p)
	if !strings.Contains(string(result), "localhost\n127.0.0.1 "+alias) {
		t.Fatalf("joined hosts entries: %q", result)
	}
}

func TestHostsCollisionRejectedWithoutChangingExistingContent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "hosts")
	alias := Alias("office", "device-id")
	before := "203.0.113.2 " + alias + " # existing entry\n"
	if err := os.WriteFile(p, []byte(before), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeHosts(p, alias); err == nil {
		t.Fatal("overwriting a foreign hosts entry must fail")
	}
	after, _ := os.ReadFile(p)
	if string(after) != before {
		t.Fatal("collision modified hosts")
	}
}

func TestHostAliasValidationGuardsElevatedHelper(t *testing.T) {
	for _, alias := range []string{"localhost", "127.0.0.1", "victim.test", "a.relayproxy.test", "x-01234567.relayproxy.test; rm -rf /", "../etc/hosts", "office-pc-01234567", "relayproxy-office-pc.relayproxy.test", "relayproxy-office-pc-01234567.test", "relayproxy-office-pc-abcdefghX"} {
		if validAlias.MatchString(alias) {
			t.Fatalf("unsafe helper alias accepted %q", alias)
		}
	}
	if !validAlias.MatchString(Alias("office", "dev")) {
		t.Fatal("generated alias rejected")
	}
}

func TestCommentedHostDoesNotCountAsInstalled(t *testing.T) {
	alias := Alias("office", "device-id")
	found, err := inspectHosts([]byte("# 127.0.0.1 "+alias+"\n"), alias)
	if err != nil || found {
		t.Fatalf("comment was treated as alias: %v %v", found, err)
	}
}

func TestConflictingDuplicateHostsMappingIsNeverTrusted(t *testing.T) {
	alias := Alias("office", "device-id")
	data := []byte("127.0.0.1 " + alias + "\n203.0.113.3 " + alias + "\n")
	found, err := inspectHosts(data, alias)
	if err == nil || found {
		t.Fatalf("conflicting duplicate should fail: found=%v err=%v", found, err)
	}
}

func TestMigratesOnlyOwnedLegacyRecord(t *testing.T) {
	alias := Alias("Office PC", "device-001")
	old := legacyAlias(alias)
	data := []byte("# local records\r\n127.0.0.1 localhost\r\n127.0.0.1 "+old+" # RelayProxy RDP\r\n192.0.2.2 internal.example\r\n")
	got, changed, err := updateHosts(data, alias)
	if err != nil || !changed {
		t.Fatalf("migration changed=%v err=%v", changed, err)
	}
	if strings.Contains(string(got), ".relayproxy.test") {
		t.Fatalf("old host was not removed: %s", got)
	}
	if strings.Count(string(got), "127.0.0.1 "+alias+" # RelayProxy RDP") != 1 {
		t.Fatalf("new hosts mapping was not installed: %s", got)
	}
	if !strings.Contains(string(got), "192.0.2.2 internal.example\r\n") {
		t.Fatal("third party hosts record changed")
	}
	second, changed, err := updateHosts(got, alias)
	if err != nil || changed || string(second) != string(got) {
		t.Fatalf("second migration changed=%v err=%v", changed, err)
	}
}

func TestLegacyCleanupNeverTouchesUnownedRecords(t *testing.T) {
	alias := Alias("Office PC", "device-001")
	old := legacyAlias(alias)
	data := []byte("127.0.0.1 "+old+" # personal override\n203.0.113.1 unrelated.example\n")
	result, _, err := updateHosts(data, alias)
	if err != nil || !strings.HasPrefix(string(result), string(data)) {
		t.Fatalf("personal hosts record was modified: %q %v", result, err)
	}
}

func TestLegacyCleanupIfNewMappingAlreadyExists(t *testing.T) {
	alias := Alias("Office PC", "device-001")
	old := legacyAlias(alias)
	data := []byte("127.0.0.1 "+alias+" # RelayProxy RDP\n127.0.0.1 "+old+" # RelayProxy RDP\n")
	result, changed, err := updateHosts(data, alias)
	if err != nil || !changed || strings.Contains(string(result), old) {
		t.Fatalf("old alias still present: %q, changed=%v err=%v", result, changed, err)
	}
	if strings.Count(string(result), alias) != 1 {
		t.Fatal("new alias duplicated")
	}
}

func TestRejectConflictingNewAliasDuringMigration(t *testing.T) {
	alias := Alias("Office PC", "device-001")
	old := legacyAlias(alias)
	data := []byte("203.0.113.12 "+alias+"\n127.0.0.1 "+old+" # RelayProxy RDP\n")
	if _, changed, err := updateHosts(data, alias); err == nil || changed {
		t.Fatal("conflicting hostname should block migration")
	}
}
