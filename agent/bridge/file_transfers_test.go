package bridge

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"relayproxy/agent/app"
)

func TestResumableDownloadRetainsVerifiedPartialAcrossPauseAndRetry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	downloads := filepath.Join(home, "Downloads", "RelayProxy")
	if err := os.MkdirAll(downloads, 0700); err != nil {
		t.Fatal(err)
	}
	const id = "a5f30599065fc95dc1cdece0349d21d3"
	partial := filepath.Join(downloads, ".relayproxy-download-"+id+".partial")
	if err := os.WriteFile(partial, []byte("incomplete-file-prefix"), 0600); err != nil {
		t.Fatal(err)
	}
	manager := newFileTransferManager(&app.Agent{})
	record := &fileTransferRecord{FileTransfer: FileTransfer{
		ID: id, DeviceID: "device", ShareID: "share", RelativePath: "data.txt",
		FileName: "data.txt", Direction: "download", State: "paused", Done: 22,
	}, cancel: func() {}}
	manager.records[id] = record
	manager.order = append(manager.order, id)
	if err := manager.resumeTransfer(id); err != nil {
		t.Fatalf("a valid local partial must permit resume: %v", err)
	}
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if current := manager.list(); len(current) == 1 && current[0].State != "running" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	items := manager.list()
	if len(items) != 1 || items[0].State != "failed" {
		t.Fatalf("offline Agent should fail but keep resumable state: %+v", items)
	}
	data, err := os.ReadFile(partial)
	if err != nil || string(data) != "incomplete-file-prefix" {
		t.Fatalf("failed transfer lost its resumable partial: %q %v", data, err)
	}
	if err := manager.resumeTransfer("unrecognized"); err == nil {
		t.Fatal("resumed unknown transfer")
	}
	if err := manager.resumeTransfer(id); err != nil {
		t.Fatalf("failed task must be manually retryable: %v", err)
	}
	record.cancel()
}

func TestDownloadDoesNotFollowPartialSymlink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	downloads := filepath.Join(home, "Downloads", "RelayProxy")
	if err := os.MkdirAll(downloads, 0700); err != nil {
		t.Fatal(err)
	}
	const id = "10a251fc2d3339098b0dbf1fe1e3d84f"
	secret := filepath.Join(home, "do-not-modify.txt")
	if err := os.WriteFile(secret, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	partial := filepath.Join(downloads, ".relayproxy-download-"+id+".partial")
	if err := os.Symlink(secret, partial); err != nil {
		t.Skipf("test filesystem does not allow symlinks: %v", err)
	}
	m := newFileTransferManager(&app.Agent{})
	err := m.downloadToDisk(context.Background(), &fileTransferRecord{FileTransfer: FileTransfer{
		ID: id, DeviceID: "device", ShareID: "share", RelativePath: "data.txt", FileName: "data.txt",
	}})
	if err == nil {
		t.Fatal("accepted a symlink in place of private partial download")
	}
	if data, err := os.ReadFile(secret); err != nil || string(data) != "unchanged" {
		t.Fatalf("symlink destination was modified: %q %v", data, err)
	}
}
