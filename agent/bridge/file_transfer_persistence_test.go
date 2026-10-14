package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"relayproxy/agent/app"
)

func TestDownloadTasksSurviveAgentUIRestartWithoutAutoStarting(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	configPath := filepath.Join(t.TempDir(), "agent.yaml")
	dir := filepath.Join(home, "Downloads", "RelayProxy")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	const id = "b07527163286e21e14cd3e8d05439720"
	data := []byte("resume content must survive restart")
	if err := os.WriteFile(filepath.Join(dir, ".relayproxy-download-"+id+".partial"), data, 0600); err != nil {
		t.Fatal(err)
	}
	initial := newFileTransferManager(&app.Agent{})
	if err := initial.initTaskPersistence(configPath); err != nil {
		t.Fatal(err)
	}
	initial.mu.Lock()
	initial.records[id] = &fileTransferRecord{FileTransfer: FileTransfer{
		ID: id, Direction: "download", State: "running",
		DeviceID: "device-alpha", ShareID: "share-1", RelativePath: "data/report..txt",
		FileName: "report..txt", Total: 123456, StartedAt: 1200,
	}}
	initial.order = append(initial.order, id)
	if err := initial.persistTasksLocked(); err != nil {
		initial.mu.Unlock()
		t.Fatal(err)
	}
	initial.mu.Unlock()

	restored := newFileTransferManager(&app.Agent{})
	if err := restored.initTaskPersistence(configPath); err != nil {
		t.Fatal(err)
	}
	items := restored.list()
	if len(items) != 1 || items[0].State != "paused" || items[0].Done != int64(len(data)) ||
		items[0].DeviceID != "device-alpha" || items[0].RelativePath != "data/report..txt" {
		t.Fatalf("unexpected restored task (should be paused): %+v", items)
	}
	info, err := os.Stat(configPath + ".file-transfer-tasks.json")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0077 != 0 {
		t.Fatalf("transfer task index must remain private: %v", info.Mode())
	}
}

func TestDownloadTaskRecoveryRejectsUntrustedMetadataAndSymlinks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	configPath := filepath.Join(t.TempDir(), "agent.yaml")
	dir := filepath.Join(home, "Downloads", "RelayProxy")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	const id = "c8b3f0a13289d709a1b3ef96803488cb"
	secret := filepath.Join(home, "secret")
	if err := os.WriteFile(secret, []byte("leave alone"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, ".relayproxy-download-"+id+".partial")); err != nil {
		t.Skipf("symbolic links not available: %v", err)
	}
	base := FileTransfer{
		ID: id, Direction: "download", State: "running", DeviceID: "client",
		ShareID: "share", RelativePath: "folder/file.txt", FileName: "file.txt",
	}
	malformed := base
	malformed.ID = "../../unauthorized"
	escaped := base
	escaped.RelativePath = "../secret"
	snapshot := fileTransferTaskSnapshot{Version: fileTransferTaskVersion,
		Records: []FileTransfer{base, malformed, escaped},
	}
	bytes, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath+".file-transfer-tasks.json", bytes, 0600); err != nil {
		t.Fatal(err)
	}
	manager := newFileTransferManager(&app.Agent{})
	if err := manager.initTaskPersistence(configPath); err != nil {
		t.Fatal(err)
	}
	if len(manager.list()) != 0 {
		t.Fatal("untrusted transfer task or symlink was restored")
	}
	if data, err := os.ReadFile(secret); err != nil || string(data) != "leave alone" {
		t.Fatalf("untrusted task touched secret: %q %v", data, err)
	}
}

func TestDownloadTaskCannotCrossIdentityScope(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	configPath := filepath.Join(t.TempDir(), "agent.yaml")
	dir := filepath.Join(home, "Downloads", "RelayProxy")
	if err := os.MkdirAll(dir, 0700); err != nil { t.Fatal(err) }
	const id = "ab83fc12a9fc203b7710350192f22ad0"
	if err := os.WriteFile(filepath.Join(dir, ".relayproxy-download-"+id+".partial"), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	initial := newFileTransferManager(&app.Agent{})
	if err := initial.initTaskPersistence(configPath); err != nil { t.Fatal(err) }
	initial.records[id] = &fileTransferRecord{FileTransfer: FileTransfer{
		ID: id, Direction: "download", State: "paused", DeviceID: "client",
		ShareID: "share", RelativePath: "report.txt", FileName: "report.txt",
	}}
	initial.order = []string{id}
	if err := initial.persistTasksLocked(); err != nil { t.Fatal(err) }
	index := configPath + ".file-transfer-tasks.json"
	raw, err := os.ReadFile(index)
	if err != nil { t.Fatal(err) }
	var snapshot fileTransferTaskSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil { t.Fatal(err) }
	snapshot.Scope = "some-other-relay-identity"
	raw, err = json.Marshal(snapshot)
	if err != nil { t.Fatal(err) }
	if err := os.WriteFile(index, raw, 0600); err != nil { t.Fatal(err) }
	newManager := newFileTransferManager(&app.Agent{})
	if err := newManager.initTaskPersistence(configPath); err != nil { t.Fatal(err) }
	if len(newManager.list()) != 0 {
		t.Fatal("stale transfer metadata leaked across Relay identity rotation")
	}
}

func TestCompletedDownloadTaskIsNotRestored(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	config := filepath.Join(t.TempDir(), "agent.yaml")
	manager := newFileTransferManager(&app.Agent{})
	if err := manager.initTaskPersistence(config); err != nil {
		t.Fatal(err)
	}
	const id = "bca8f4ea511723abb533682404d02a8f"
	manager.records[id] = &fileTransferRecord{FileTransfer: FileTransfer{
		ID: id, Direction: "download", State: "completed",
		DeviceID: "client", ShareID: "share", RelativePath: "file.bin", FileName: "file.bin",
	}}
	manager.order = append(manager.order, id)
	if err := manager.persistTasksLocked(); err != nil {
		t.Fatal(err)
	}
	restored := newFileTransferManager(&app.Agent{})
	if err := restored.initTaskPersistence(config); err != nil {
		t.Fatal(err)
	}
	if len(restored.list()) != 0 {
		t.Fatal("completed transfer should not be requeued")
	}
}
