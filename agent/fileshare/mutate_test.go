package fileshare

import (
	"os"
	"path/filepath"
	"testing"

	"relayproxy/internal/protocol"
)

func TestMutateFileCreateMoveAndDelete(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	store := &FileStore{}
	share := Share{RootPath: dir}
	run := func(action, p, d string) {
		t.Helper()
		if err := store.mutateFile(share, protocol.FileShareRequest{Action: action, Path: p, Destination: d}); err != nil {
			t.Fatal(err)
		}
	}
	run(protocol.FileShareMkdir, "folder", "")
	run(protocol.FileShareMove, "note.txt", "folder/note.txt")
	if _, err := os.Stat(filepath.Join(dir, "note.txt")); !os.IsNotExist(err) {
		t.Fatalf("old path exists: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "folder", "note.txt"))
	if err != nil || string(raw) != "hello" {
		t.Fatalf("move lost data: %v %q", err, raw)
	}
	run(protocol.FileShareRemove, "folder/note.txt", "")
	run(protocol.FileShareRemove, "folder", "")
	if _, err := os.Stat(filepath.Join(dir, "folder")); !os.IsNotExist(err) {
		t.Fatalf("folder still exists: %v", err)
	}
}

func TestMutateFileRejectsUnsafePathsAndConflicts(t *testing.T) {
	dir := t.TempDir()
	store := &FileStore{}
	share := Share{RootPath: dir}
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("a"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b"), []byte("b"), 0600); err != nil {
		t.Fatal(err)
	}
	invalid := []protocol.FileShareRequest{
		{Action: protocol.FileShareMkdir, Path: "."},
		{Action: protocol.FileShareMkdir, Path: "../out"},
		{Action: protocol.FileShareMkdir, Path: "sub\\unsafe"},
		{Action: protocol.FileShareMkdir, Path: ".relayproxy-upload-private"},
		{Action: protocol.FileShareMove, Path: "a", Destination: "../outside"},
		{Action: protocol.FileShareMove, Path: "a", Destination: "b"},
		{Action: protocol.FileShareMove, Path: "a", Destination: "a"},
		{Action: protocol.FileShareRemove, Path: "."},
	}
	for _, req := range invalid {
		if err := store.mutateFile(share, req); err == nil {
			t.Errorf("accepted unsafe request: %#v", req)
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "b"))
	if err != nil || string(raw) != "b" {
		t.Fatalf("conflict overwrote destination: %v %q", err, raw)
	}
	if err := store.mutateFile(share, protocol.FileShareRequest{Action: protocol.FileShareMkdir, Path: "dir"}); err != nil {
		t.Fatal(err)
	}
	if err := store.mutateFile(share, protocol.FileShareRequest{Action: protocol.FileShareMove, Path: "dir", Destination: "dir/child"}); err == nil {
		t.Fatal("moved directory inside itself")
	}
	if err := os.WriteFile(filepath.Join(dir, "dir", "nested.txt"), []byte("safe"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.mutateFile(share, protocol.FileShareRequest{Action: protocol.FileShareRemove, Path: "dir"}); err == nil {
		t.Fatal("recursively deleted nonempty directory")
	}
}

func TestMutateFileRejectsSymlinkOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "out")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	store := &FileStore{}
	share := Share{RootPath: root}
	if err := store.mutateFile(share, protocol.FileShareRequest{Action: protocol.FileShareRemove, Path: "out"}); err == nil {
		t.Fatal("deleted symlink")
	}
	if err := store.mutateFile(share, protocol.FileShareRequest{Action: protocol.FileShareMove, Path: "out/secret", Destination: "copy"}); err == nil {
		t.Fatal("read file outside root")
	}
	raw, err := os.ReadFile(filepath.Join(outside, "secret"))
	if err != nil || string(raw) != "secret" {
		t.Fatalf("outside altered: %v", err)
	}
}
