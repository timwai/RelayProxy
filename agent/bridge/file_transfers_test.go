package bridge

import (
	"context"
	"errors"
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

func TestCustomSaveAsPublishesWithoutOverwrite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	downloads := filepath.Join(home, "Downloads", "RelayProxy")
	if err := os.MkdirAll(downloads, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(downloads)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	const id = "0d129c2acccda18e742918f2c1c7e54d"
	if err := os.WriteFile(filepath.Join(downloads, "verified.partial"), []byte("verified payload"), 0600); err != nil {
		t.Fatal(err)
	}
	selected := filepath.Join(t.TempDir(), "chosen.txt")
	if err := publishFileDownloadTo(context.Background(), root, "verified.partial", selected, id); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(selected); err != nil || string(got) != "verified payload" {
		t.Fatalf("Save As bytes invalid: %q %v", got, err)
	}
	if err := publishFileDownloadTo(context.Background(), root, "verified.partial", selected, id); !errors.Is(err, os.ErrExist) {
		t.Fatalf("overwrite not rejected: %v", err)
	}
	if got, _ := os.ReadFile(selected); string(got) != "verified payload" {
		t.Fatal("existing file was overwritten")
	}
	if err := publishFileDownloadTo(context.Background(), root, "verified.partial", filepath.Join(t.TempDir(), "cancel.txt"), id); err != nil {
		t.Fatal(err)
	}
	if err := validateFileDownloadDestination("relative/file.txt"); err == nil {
		t.Fatal("relative save path accepted")
	}
}

func TestBrowserCanOnlyExportCompletedDefaultDownloads(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dir := filepath.Join(home, "Downloads", "RelayProxy")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	const id = "330ed2033103fa305616bac0db1c9cb9"
	name := "report.txt"
	if err := os.WriteFile(filepath.Join(dir, "report-"+id+".txt"), []byte("finished"), 0600); err != nil {
		t.Fatal(err)
	}
	manager := newFileTransferManager(nil)
	manager.records[id] = &fileTransferRecord{FileTransfer: FileTransfer{
		ID: id, Direction: "download", FileName: name, State: "completed",
	}}
	bridge := &UIBridge{fileTransfers:manager}
	f, publicName, err := bridge.OpenCompletedFileDownload(id)
	if err != nil || publicName != name {
		t.Fatalf("verified browser export: %s %v", publicName, err)
	}
	defer f.Close()
	content := make([]byte, 8)
	if _, err := f.Read(content); err != nil || string(content) != "finished" {
		t.Fatalf("export content: %q %v", content, err)
	}
	manager.records[id].State = "paused"
	if file, _, err := bridge.OpenCompletedFileDownload(id); err == nil {
		file.Close()
		t.Fatal("partial file exported via browser")
	}
	manager.records[id].State = "completed"
	manager.records[id].savePath = filepath.Join(home, "private.txt")
	if file, _, err := bridge.OpenCompletedFileDownload(id); err == nil {
		file.Close()
		t.Fatal("native private destination exported via browser")
	}
}
