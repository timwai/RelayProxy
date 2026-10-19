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
	if !strings.HasPrefix(a, "office-pc-") || !validAlias.MatchString(a) {
		t.Fatalf("bad alias %q", a)
	}
	for _, name := range []string{"工作电脑", "My PC!  01", "🤖", strings.Repeat("verylongname", 15)} {
		value := Alias(name, "dev-001")
		if !validAlias.MatchString(value) {
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
	for _, alias := range []string{"localhost", "127.0.0.1", "victim.test", "a.relayproxy.test", "x-01234567.relayproxy.test; rm -rf /", "../etc/hosts"} {
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
