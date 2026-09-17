//go:build windows && amd64

package divert

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"golang.org/x/sys/windows"

	"relayproxy/internal/windivert"
)

func TestEmbeddedWinDivertIsOfficialRuntime(t *testing.T) {
	files, err := bundledWinDivertFiles()
	if err != nil {
		t.Fatal(err)
	}
	for name, expected := range map[string]string{
		"WinDivert.dll":   "c1e060ee19444a259b2162f8af0f3fe8c4428a1c6f694dce20de194ac8d7d9a2",
		"WinDivert64.sys": "8da085332782708d8767bcace5327a6ec7283c17cfb85e40b03cd2323a90ddc2",
	} {
		if got := fmt.Sprintf("%x", sha256.Sum256(files[name])); got != expected {
			t.Fatalf("%s is not the pinned official binary: %s", name, got)
		}
	}
	if len(files["LICENSE"]) == 0 || !bytes.Contains(files["SOURCE.txt"], []byte(windivert.ArchiveURL)) {
		t.Fatal("license/source information missing from embedded runtime")
	}
	corrupt := bytes.Clone(embeddedWinDivertArchive)
	corrupt[len(corrupt)/2] ^= 1
	if _, err := windivert.RuntimeFiles(corrupt); err == nil {
		t.Fatal("modified driver archive accepted")
	}
}

func TestEmbeddedWinDivertReadinessNeedsNoSiblingFiles(t *testing.T) {
	directory := t.TempDir()
	if err := winDivertDependencies(filepath.Join(directory, "relay-agent.exe")); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("readiness extracted files: %v %v", entries, err)
	}
}

func TestEmbeddedWinDivertExtractsReusesAndRepairsFiles(t *testing.T) {
	files, err := bundledWinDivertFiles()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	if err := materializeWinDivert(directory, files); err != nil {
		t.Fatal(err)
	}
	for name, expected := range files {
		actual, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil || !bytes.Equal(actual, expected) {
			t.Fatalf("extracted %s differs: %v", name, err)
		}
	}
	dll := filepath.Join(directory, "WinDivert.dll")
	before, err := os.Stat(dll)
	if err != nil {
		t.Fatal(err)
	}
	if err := materializeWinDivert(directory, files); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(dll)
	if err != nil || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("matching runtime was overwritten instead of reused")
	}
	if err := os.WriteFile(dll, []byte("incomplete previous extraction"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := materializeWinDivert(directory, files); err != nil {
		t.Fatal(err)
	}
	if matches, err := winDivertFileMatches(dll, files["WinDivert.dll"]); err != nil || !matches {
		t.Fatalf("corrupted runtime not repaired: %v", err)
	}
}

func TestEmbeddedWinDivertConcurrentExtraction(t *testing.T) {
	files, err := bundledWinDivertFiles()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	var workers sync.WaitGroup
	errors := make(chan error, 8)
	for range 8 {
		workers.Go(func() { errors <- materializeWinDivert(directory, files) })
	}
	workers.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != len(files)+1 { // Includes the persistent extraction lock.
		t.Fatalf("temporary files left behind: %v %v", entries, err)
	}
}

func TestEmbeddedWinDivertRejectsNonFileDestination(t *testing.T) {
	directory := t.TempDir()
	if err := os.Mkdir(filepath.Join(directory, "WinDivert.dll"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := materializeWinDivert(directory, map[string][]byte{"WinDivert.dll": []byte("fixture")}); err == nil {
		t.Fatal("non-file DLL destination accepted")
	}
	if err := materializeWinDivert(directory, map[string][]byte{"../outside.dll": []byte("fixture")}); err == nil {
		t.Fatal("runtime path escape accepted")
	}
}

func TestWinDivertCacheRejectsUnsafePermissions(t *testing.T) {
	for _, tc := range []struct {
		name, sddl string
		valid      bool
	}{
		{"protected administrators", winDivertDirectorySDDL, true},
		{"system owner", "O:SYG:SYD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)", true},
		{"user owner", "O:BUG:BUD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)", false},
		{"user writes", "O:BAG:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;BU)", false},
		{"inherited permissions", "O:BAG:BAD:(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)", false},
		{"null permissions", "O:BAG:BAD:NO_ACCESS_CONTROL", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			descriptor, err := windows.SecurityDescriptorFromString(tc.sddl)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateWinDivertDirectorySecurity(descriptor); (err == nil) != tc.valid {
				t.Fatalf("permission validation = %v, want valid=%v", err, tc.valid)
			}
		})
	}
}
