package bridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"relayproxy/agent/app"
)

type FileTransfer struct {
	ID           string  `json:"id"`
	DeviceID     string  `json:"deviceId"`
	ShareID      string  `json:"shareId"`
	RelativePath string  `json:"relativePath"`
	FileName     string  `json:"fileName"`
	Direction    string  `json:"direction"`
	Destination  string  `json:"destination,omitempty"`
	State        string  `json:"state"`
	Total        int64   `json:"total"`
	Done         int64   `json:"done"`
	Speed        float64 `json:"speed"`
	Error        string  `json:"error,omitempty"`
	StartedAt    int64   `json:"startedAt"`
}

type fileTransferRecord struct {
	FileTransfer
	cancel     context.CancelFunc
	lastSample time.Time
	lastBytes  int64
}

type fileTransferManager struct {
	mu      sync.Mutex
	agent   *app.Agent
	records map[string]*fileTransferRecord
	stages  map[string]*fileUploadSession
	order   []string
}

func newFileTransferManager(agent *app.Agent) *fileTransferManager {
	return &fileTransferManager{agent: agent, records: map[string]*fileTransferRecord{}, stages: map[string]*fileUploadSession{}}
}

func transferID() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(random[:]), nil
}

func (m *fileTransferManager) start(deviceID, shareID, relativePath string) (FileTransfer, error) {
	if m == nil || m.agent == nil {
		return FileTransfer{}, errors.New("file transfer manager unavailable")
	}
	if deviceID == "" || shareID == "" || relativePath == "" || relativePath == "." ||
		strings.Contains(relativePath, "\\") || strings.HasPrefix(relativePath, "/") ||
		!fs.ValidPath(relativePath) {
		return FileTransfer{}, errors.New("invalid remote file selection")
	}
	name := path.Base(relativePath)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\:") {
		return FileTransfer{}, errors.New("invalid file name")
	}
	id, err := transferID()
	if err != nil {
		return FileTransfer{}, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	record := &fileTransferRecord{FileTransfer: FileTransfer{
		ID: id, DeviceID: deviceID, ShareID: shareID, RelativePath: relativePath,
		FileName: name, Direction: "download", State: "running", StartedAt: time.Now().UnixMilli(),
	}, cancel: cancel, lastSample: time.Now()}
	m.mu.Lock()
	running := 0
	for _, item := range m.records {
		if item.State == "running" {
			running++
		}
	}
	if running >= 3 {
		m.mu.Unlock()
		cancel()
		return FileTransfer{}, errors.New("最多同时进行 3 个文件下载")
	}
	m.records[id] = record
	m.order = append([]string{id}, m.order...)
	if len(m.order) > 100 {
		for _, oldID := range m.order[100:] {
			delete(m.records, oldID)
		}
		m.order = m.order[:100]
	}
	snapshot := record.FileTransfer
	m.mu.Unlock()
	go m.download(ctx, record)
	return snapshot, nil
}

func (m *fileTransferManager) download(ctx context.Context, record *fileTransferRecord) {
	err := m.downloadToDisk(ctx, record)
	m.mu.Lock()
	defer m.mu.Unlock()
	if ctx.Err() != nil {
		record.State = "cancelled"
	} else if err != nil {
		record.State, record.Error = "failed", err.Error()
	} else {
		record.State, record.Speed = "completed", 0
	}
	record.cancel()
}

func (m *fileTransferManager) downloadToDisk(ctx context.Context, record *fileTransferRecord) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, "Downloads", "RelayProxy")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".relayproxy-download-*")
	if err != nil {
		return err
	}
	tempPath := file.Name()
	defer os.Remove(tempPath)
	defer file.Close()
	if err := file.Chmod(0600); err != nil {
		return err
	}
	lastTime := time.Now()
	lastDone := int64(0)
	_, err = m.agent.DownloadRemoteFile(ctx, record.DeviceID, record.ShareID, record.RelativePath, file, func(done, total int64) {
		m.mu.Lock()
		record.Done, record.Total = done, total
		elapsed := time.Since(lastTime)
		if elapsed >= 250*time.Millisecond {
			rate := float64(done-lastDone) / elapsed.Seconds()
			if record.Speed > 0 {
				record.Speed = 0.6*record.Speed + 0.4*rate
			} else {
				record.Speed = rate
			}
			lastDone, lastTime = done, time.Now()
		}
		m.mu.Unlock()
	})
	if err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	ext := filepath.Ext(record.FileName)
	base := strings.TrimSuffix(record.FileName, ext)
	// Randomized suffix avoids overwriting a pre-existing download.
	destination := filepath.Join(dir, fmt.Sprintf("%s-%s%s", base, record.ID[:8], ext))
	// Publish with no-replace semantics. Rename on Windows/Unix can overwrite
	// a destination that appeared while another download was running.
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Link(tempPath, destination); err != nil {
		return fmt.Errorf("publish download without overwriting: %w", err)
	}
	_ = os.Remove(tempPath)
	m.mu.Lock()
	record.Destination = destination
	m.mu.Unlock()
	return nil
}

func (m *fileTransferManager) list() []FileTransfer {
	if m == nil {
		return []FileTransfer{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]FileTransfer, 0, len(m.order))
	for _, id := range m.order {
		if item := m.records[id]; item != nil {
			out = append(out, item.FileTransfer)
		}
	}
	return out
}

func (m *fileTransferManager) cancelTransfer(id string) error {
	if m == nil {
		return errors.New("file transfer manager unavailable")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	item := m.records[id]
	if item == nil {
		return errors.New("download task not found")
	}
	if item.State != "running" {
		return errors.New("download task is not active")
	}
	item.cancel()
	return nil
}

func (b *UIBridge) StartFileDownload(deviceID, shareID, relativePath string) (FileTransfer, error) {
	return b.fileTransfers.start(deviceID, shareID, relativePath)
}

func (b *UIBridge) GetFileTransfers() []FileTransfer {
	return b.fileTransfers.list()
}

func (b *UIBridge) CancelFileDownload(id string) error {
	return b.fileTransfers.cancelTransfer(id)
}
