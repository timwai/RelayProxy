//go:build windows

package desktop

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func testWindowsVirtualDescriptor(
	name string,
	directory bool,
	hasSize bool,
	size uint64,
) []byte {
	raw := make([]byte, windowsVirtualDescriptorBytes)
	var flags uint32
	if directory {
		flags |= windowsFDAttributes
		binary.LittleEndian.PutUint32(raw[36:40], windowsFileAttributeDirectory)
	}
	if hasSize {
		flags |= windowsFDFileSize
		binary.LittleEndian.PutUint32(raw[64:68], uint32(size>>32))
		binary.LittleEndian.PutUint32(raw[68:72], uint32(size))
	}
	binary.LittleEndian.PutUint32(raw[0:4], flags)
	units, err := windows.UTF16FromString(name)
	if err != nil {
		panic(err)
	}
	if len(units) > windowsVirtualDescriptorNameUTF16 {
		panic("test descriptor name is too long")
	}
	for index, value := range units {
		binary.LittleEndian.PutUint16(raw[72+index*2:74+index*2], value)
	}
	return raw
}

func testWindowsVirtualDescriptorGroup(items ...[]byte) []byte {
	raw := make([]byte, 4+len(items)*windowsVirtualDescriptorBytes)
	binary.LittleEndian.PutUint32(raw[:4], uint32(len(items)))
	for index, item := range items {
		copy(raw[4+index*windowsVirtualDescriptorBytes:], item)
	}
	return raw
}

func TestParseWindowsVirtualClipboardDescriptors(t *testing.T) {
	raw := testWindowsVirtualDescriptorGroup(
		testWindowsVirtualDescriptor("docs", true, false, 0),
		testWindowsVirtualDescriptor("docs\\readme.txt", false, true, 123),
		testWindowsVirtualDescriptor("root.bin", false, true, 456),
	)
	got, err := parseWindowsVirtualClipboardDescriptors(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("descriptor count=%d want=3", len(got))
	}
	if got[0].RelativePath != "docs" || !got[0].Directory {
		t.Fatalf("directory descriptor=%+v", got[0])
	}
	if got[1].RelativePath != "docs/readme.txt" || got[1].Directory ||
		!got[1].HasSize || got[1].DeclaredSize != 123 {
		t.Fatalf("nested file descriptor=%+v", got[1])
	}
	if got[2].RelativePath != "root.bin" || got[2].DeclaredSize != 456 {
		t.Fatalf("root file descriptor=%+v", got[2])
	}
}

func TestParseWindowsVirtualClipboardDescriptorsRejectsTraversal(t *testing.T) {
	raw := testWindowsVirtualDescriptorGroup(
		testWindowsVirtualDescriptor("..\\escape.txt", false, true, 1),
	)
	if _, err := parseWindowsVirtualClipboardDescriptors(raw); err == nil {
		t.Fatal("virtual clipboard traversal path was accepted")
	}
}

func TestParseWindowsVirtualClipboardDescriptorsRejectsDuplicatePathsCaseInsensitive(t *testing.T) {
	raw := testWindowsVirtualDescriptorGroup(
		testWindowsVirtualDescriptor("Report.txt", false, true, 1),
		testWindowsVirtualDescriptor("report.txt", false, true, 1),
	)
	if _, err := parseWindowsVirtualClipboardDescriptors(raw); err == nil {
		t.Fatal("case-insensitive duplicate virtual clipboard path was accepted")
	}
}

func TestParseWindowsVirtualClipboardDescriptorsRejectsOversizedFile(t *testing.T) {
	raw := testWindowsVirtualDescriptorGroup(
		testWindowsVirtualDescriptor(
			"huge.bin",
			false,
			true,
			uint64(maxDesktopClipboardFileBytes)+1,
		),
	)
	if _, err := parseWindowsVirtualClipboardDescriptors(raw); err == nil {
		t.Fatal("oversized virtual clipboard file was accepted")
	}
}

func TestMaterializeWindowsVirtualClipboardZeroLengthFiles(t *testing.T) {
	descriptors := []windowsVirtualClipboardDescriptor{
		{RelativePath: "folder", Directory: true},
		{RelativePath: "folder/empty.txt", HasSize: true, DeclaredSize: 0},
		{RelativePath: "root.txt", HasSize: true, DeclaredSize: 0},
	}
	paths, dir, err := materializeWindowsVirtualClipboard(
		t.Context(),
		0,
		0,
		descriptors,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if len(paths) != 2 {
		t.Fatalf("root paths=%v want 2 roots", paths)
	}
	if filepath.Base(paths[0]) != "folder" || filepath.Base(paths[1]) != "root.txt" {
		t.Fatalf("unexpected root order=%v", paths)
	}
	info, err := os.Stat(paths[0])
	if err != nil || !info.IsDir() {
		t.Fatalf("folder root stat=%v info=%v", err, info)
	}
	nested := filepath.Join(paths[0], "empty.txt")
	if info, err := os.Stat(nested); err != nil || !info.Mode().IsRegular() || info.Size() != 0 {
		t.Fatalf("nested zero-length file stat=%v info=%v", err, info)
	}
	if info, err := os.Stat(paths[1]); err != nil || !info.Mode().IsRegular() || info.Size() != 0 {
		t.Fatalf("root zero-length file stat=%v info=%v", err, info)
	}
}

func TestMaterializeWindowsVirtualClipboardRejectsRootFileDirectoryConflict(t *testing.T) {
	descriptors := []windowsVirtualClipboardDescriptor{
		{RelativePath: "same", HasSize: true, DeclaredSize: 0},
		{RelativePath: "same/child.txt", HasSize: true, DeclaredSize: 0},
	}
	_, _, err := materializeWindowsVirtualClipboard(t.Context(), 0, 0, descriptors)
	if err == nil || !strings.Contains(err.Error(), "both a file and directory") {
		t.Fatalf("root conflict error=%v", err)
	}
}
