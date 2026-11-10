//go:build windows && amd64

package divert

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unsafe"

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
		{"duplicate trusted grants", "O:BAG:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)", true},
		{"reordered trusted grants", "O:BAG:BAD:P(A;OICI;FA;;;BA)(A;OICI;FA;;;SY)", true},
		{"user owner", "O:BUG:BUD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)", false},
		{"user writes", "O:BAG:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;BU)", false},
		{"user replaces system grant", "O:BAG:BAD:P(A;OICI;FA;;;BA)(A;OICI;FA;;;BU)", false},
		{"duplicate administrators without system", "O:BAG:BAD:P(A;OICI;FA;;;BA)(A;OICI;FA;;;BA)", false},
		{"inherit only system", "O:BAG:BAD:P(A;OICIIO;FA;;;SY)(A;OICI;FA;;;BA)", false},
		{"inherited flag", "O:BAG:BAD:P(A;OICIID;FA;;;SY)(A;OICI;FA;;;BA)", false},
		{"read only system", "O:BAG:BAD:P(A;OICI;FR;;;SY)(A;OICI;FA;;;BA)", false},
		{"deny entry", "O:BAG:BAD:P(D;OICI;FW;;;BU)(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)", false},
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

func TestWinDivertDirectoryRepairRebuildsUnsafeTrustedDirectory(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("repairing administrator-owned directory ACLs requires elevation")
	}
	directory := filepath.Join(t.TempDir(), "运行库")
	if err := ensureWinDivertDirectory(directory); err != nil {
		t.Fatal(err)
	}
	oldHandle, err := os.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	// Stat by handle captures the Windows file ID now; os.Stat may load it
	// lazily by path after that path has already been replaced.
	oldDirectory, err := oldHandle.Stat()
	_ = oldHandle.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "untrusted.dll"), []byte("old cache"), 0600); err != nil {
		t.Fatal(err)
	}
	// Simulate an old cache with an extra user grant. Normal capture must
	// reject it without changing permissions; explicit repair must replace it.
	descriptor, err := windows.SecurityDescriptorFromString("O:BAG:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;BU)")
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(directory, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	readSecurity := func() *windows.SECURITY_DESCRIPTOR {
		t.Helper()
		descriptor, err := windows.GetNamedSecurityInfo(directory, windows.SE_FILE_OBJECT,
			windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			t.Fatal(err)
		}
		return descriptor
	}
	before := readSecurity().String()
	if err := ensureWinDivertDirectory(directory); err == nil || !strings.Contains(err.Error(), "SDDL:") {
		t.Fatalf("unsafe runtime accepted or ACL diagnostics missing: %v", err)
	}
	if after := readSecurity().String(); after != before {
		t.Fatal("ordinary capture changed directory permissions")
	}
	if err := repairWinDivertDirectory(directory); err != nil {
		t.Fatal(err)
	}
	if err := validateWinDivertDirectorySecurity(readSecurity()); err != nil {
		t.Fatalf("repair left unsafe permissions: %v", err)
	}
	if err := ensureWinDivertDirectory(directory); err != nil {
		t.Fatalf("repaired runtime failed normal capture validation: %v", err)
	}
	newDirectory, err := os.Stat(directory)
	if err != nil || os.SameFile(oldDirectory, newDirectory) {
		t.Fatalf("repair reused the unsafe directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, "untrusted.dll")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repair reused files from the unsafe cache: %v", err)
	}
	backups, err := filepath.Glob(directory + ".unsafe-*")
	if err != nil || len(backups) != 1 {
		t.Fatalf("unsafe cache not retained for inspection: %v %v", backups, err)
	}
	if err := repairWinDivertDirectory(directory); err != nil {
		t.Fatal(err)
	}
	if backups, err = filepath.Glob(directory + ".unsafe-*"); err != nil || len(backups) != 1 {
		t.Fatalf("healthy directory replaced on repeat repair: %v %v", backups, err)
	}
}

func TestWinDivertDirectoryRepairRejectsUserOwner(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("testing administrator-only cache repair requires elevation")
	}
	sid, err := currentWindowsUserSID()
	if err != nil {
		t.Fatal(err)
	}
	if sid == "S-1-5-18" {
		t.Skip("SYSTEM is a trusted cache owner")
	}
	descriptor, err := windows.SecurityDescriptorFromString("O:" + sid + "G:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(t.TempDir(), "user-owned")
	path, err := windows.UTF16PtrFromString(directory)
	if err != nil {
		t.Fatal(err)
	}
	attributes := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}
	if err := windows.CreateDirectory(path, &attributes); err != nil {
		t.Fatal(err)
	}
	if err := repairWinDivertDirectory(directory); err == nil || !strings.Contains(err.Error(), "owned by Administrators or SYSTEM") {
		t.Fatalf("repair accepted a user-owned directory: %v", err)
	}
}

func TestWinDivertDirectoryRepairRejectsReparsePoint(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("testing directory reparse points requires elevation")
	}
	root := t.TempDir()
	target, link := filepath.Join(root, "target"), filepath.Join(root, "link")
	if err := ensureWinDivertDirectory(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		if errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) {
			t.Skipf("symbolic links unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if err := repairWinDivertDirectory(link); err == nil {
		t.Fatal("repair followed a directory reparse point")
	}
	if err := ensureWinDivertDirectory(target); err != nil {
		t.Fatalf("link target changed during rejected repair: %v", err)
	}
}
