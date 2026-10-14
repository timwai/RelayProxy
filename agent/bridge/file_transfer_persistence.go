package bridge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const fileTransferTaskVersion = 2
const maxFileTransferTaskBytes = 256 << 10

type fileTransferTaskSnapshot struct {
	Version int            `json:"version"`
	Scope   string         `json:"scope"`
	Records []FileTransfer `json:"records"`
}

// initTaskPersistence is called once for each local Agent configuration. The
// sidecar contains only download metadata, never file bytes or credentials.
// It does not start network traffic; recovered work must be resumed manually.
func (m *fileTransferManager) initTaskPersistence(configPath string) error {
	if m == nil || configPath == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.taskStorePath = configPath + ".file-transfer-tasks.json"
	m.taskScope = fileTransferScope(m.agent)
	return m.restoreTasksLocked()
}

// Bind local metadata to this Agent installation and Relay identity. A device
// or identity rotation must not restore another identity's transfer paths.
func fileTransferScope(agent *app.Agent) string {
	if agent == nil {
		return ""
	}
	cfg := agent.Config()
	raw := fmt.Sprintf("%s\x00%s\x00%s\x00%d\x00%d",
		cfg.IdentityID, cfg.DeviceID, cfg.ServerAddress, cfg.TCPPort, cfg.QUICPort)
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func validDownloadTaskID(value string) bool {
	if len(value) != 32 {
		return false
	}
	data, err := hex.DecodeString(value)
	return err == nil && len(data) == 16 && hex.EncodeToString(data) == value
}

func validDownloadTask(item FileTransfer) bool {
	if !validDownloadTaskID(item.ID) || item.Direction != "download" ||
		strings.TrimSpace(item.DeviceID) == "" || strings.TrimSpace(item.ShareID) == "" ||
		item.RelativePath == "" || item.RelativePath == "." ||
		strings.ContainsAny(item.RelativePath, "\\:") || !fs.ValidPath(item.RelativePath) {
		return false
	}
	name := path.Base(item.RelativePath)
	if name == "." || name == ".." || name == "" || name != item.FileName ||
		strings.ContainsAny(name, "/\\:") {
		return false
	}
	return item.Total >= 0 && item.Done >= 0
}

func (m *fileTransferManager) restoreTasksLocked() error {
	if m.taskStorePath == "" {
		return nil
	}
	info, err := os.Lstat(m.taskStorePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > maxFileTransferTaskBytes {
		return errors.New("file transfer task index is invalid")
	}
	file, err := os.Open(m.taskStorePath)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, maxFileTransferTaskBytes+1))
	decoder.DisallowUnknownFields()
	var snapshot fileTransferTaskSnapshot
	if err := decoder.Decode(&snapshot); err != nil {
		return fmt.Errorf("read file transfer tasks: %w", err)
	}
	if snapshot.Scope != m.taskScope {
	\treturn nil
	}
	if snapshot.Version != fileTransferTaskVersion || len(snapshot.Records) > 100 {
		return errors.New("file transfer task index version or length invalid")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	downloadDir := filepath.Join(home, "Downloads", "RelayProxy")
	seen := make(map[string]bool)
	for _, item := range snapshot.Records {
		if !validDownloadTask(item) || seen[item.ID] {
			continue
		}
		seen[item.ID] = true
		// Never follow a symlink or trust an arbitrary file path from saved
		// JSON. The local .partial filename is derived only from validated ID.
		partial := filepath.Join(downloadDir, ".relayproxy-download-"+item.ID+".partial")
		info, err := os.Lstat(partial)
		if err != nil || !info.Mode().IsRegular() || info.Size() < 0 {
			continue
		}
		item.State = "paused"
		item.Done = info.Size()
		item.Speed = 0
		item.Destination = ""
		item.TransportPath = ""
		item.Error = ""
		m.records[item.ID] = &fileTransferRecord{FileTransfer: item}
		m.order = append(m.order, item.ID)
	}
	return nil
}

// persistTasksLocked is best effort when the local configuration directory is
// not writable. Never save upload staging tokens or temporary source paths.
func (m *fileTransferManager) persistTasksLocked() error {
	if m == nil || m.taskStorePath == "" {
		return nil
	}
	snapshot := fileTransferTaskSnapshot{Version: fileTransferTaskVersion, Scope: m.taskScope}
	for _, id := range m.order {
		record := m.records[id]
		if record == nil || record.Direction != "download" ||
			(record.State != "running" && record.State != "paused" && record.State != "failed") {
			continue
		}
		if !validDownloadTask(record.FileTransfer) {
			continue
		}
		item := record.FileTransfer
		item.Error, item.TransportPath, item.Destination = "", "", ""
		item.Speed = 0
		snapshot.Records = append(snapshot.Records, item)
		if len(snapshot.Records) >= 100 {
			break
		}
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	if len(data) > maxFileTransferTaskBytes {
		return errors.New("file transfer task index too large")
	}
	if stat, err := os.Lstat(m.taskStorePath); err == nil {
		if !stat.Mode().IsRegular() {
			return errors.New("file transfer task index must be a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	dir := filepath.Dir(m.taskStorePath)
	tmp, err := os.CreateTemp(dir, ".relayproxy-file-tasks-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), m.taskStorePath)
}
