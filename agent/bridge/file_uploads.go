package bridge

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"relayproxy/agent/fileshare"
)

const uploadChunkMax = 256 << 10
const uploadStageLifetime = 30 * time.Minute

// fileUploadSession stages browser/Wails File API chunks under an opaque ID.
// The browser never supplies a privileged filesystem path.
type fileUploadSession struct {
	id       string
	deviceID string
	shareID  string
	path     string
	size     int64
	done     int64
	created  time.Time
	file     *os.File
}

type FileUploadProgress struct {
	ID   string `json:"id"`
	Done int64  `json:"done"`
	Size int64  `json:"size"`
}

func (m *fileTransferManager) cleanupStagesLocked() {
	for id, stage := range m.stages {
		if time.Since(stage.created) > uploadStageLifetime {
			_ = stage.file.Close()
			_ = os.Remove(stage.file.Name())
			delete(m.stages, id)
		}
	}
}

func (m *fileTransferManager) beginUpload(deviceID, shareID, remotePath string, size int64) (FileUploadProgress, error) {
	if m == nil || m.agent == nil {
		return FileUploadProgress{}, errors.New("file transfer manager unavailable")
	}
	if strings.TrimSpace(deviceID) == "" || strings.TrimSpace(shareID) == "" ||
		remotePath == "" || remotePath == "." || strings.Contains(remotePath, "\\") ||
		strings.HasPrefix(remotePath, "/") || strings.Contains(remotePath, "..") ||
		path.Base(remotePath) == "" || path.Base(remotePath) == "." {
		return FileUploadProgress{}, errors.New("invalid file upload target")
	}
	if size < 0 || size > fileshare.MaxUploadSize {
		return FileUploadProgress{}, errors.New("invalid upload size")
	}
	id, err := transferID()
	if err != nil {
		return FileUploadProgress{}, err
	}
	file, err := os.CreateTemp("", "relayproxy-upload-*.staged")
	if err != nil {
		return FileUploadProgress{}, err
	}
	if err := file.Chmod(0600); err != nil {
		file.Close()
		os.Remove(file.Name())
		return FileUploadProgress{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupStagesLocked()
	if len(m.stages) >= 5 {
		file.Close()
		os.Remove(file.Name())
		return FileUploadProgress{}, errors.New("最多同时准备 5 个上传文件")
	}
	m.stages[id] = &fileUploadSession{
		id: id, deviceID: deviceID, shareID: shareID, path: remotePath,
		size: size, created: time.Now(), file: file,
	}
	return FileUploadProgress{ID: id, Size: size}, nil
}

func (m *fileTransferManager) appendUpload(id, base64Chunk string) (FileUploadProgress, error) {
	if len(base64Chunk) > base64.StdEncoding.EncodedLen(uploadChunkMax) {
		return FileUploadProgress{}, errors.New("upload chunk is too large")
	}
	payload, err := base64.StdEncoding.DecodeString(base64Chunk)
	if err != nil || len(payload) > uploadChunkMax {
		return FileUploadProgress{}, errors.New("invalid upload chunk")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupStagesLocked()
	stage := m.stages[id]
	if stage == nil {
		return FileUploadProgress{}, errors.New("upload session not found or expired")
	}
	if stage.done+int64(len(payload)) > stage.size {
		return FileUploadProgress{}, errors.New("upload exceeds declared size")
	}
	n, err := stage.file.Write(payload)
	stage.done += int64(n)
	stage.created = time.Now()
	if err != nil || n != len(payload) {
		return FileUploadProgress{}, fmt.Errorf("write upload staging data: %w", io.ErrShortWrite)
	}
	return FileUploadProgress{ID: id, Done: stage.done, Size: stage.size}, nil
}

func (m *fileTransferManager) commitUpload(id string) (FileTransfer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupStagesLocked()
	stage := m.stages[id]
	if stage == nil {
		return FileTransfer{}, errors.New("upload session not found or expired")
	}
	if stage.done != stage.size {
		return FileTransfer{}, errors.New("file chunks are incomplete")
	}
	running := 0
	for _, item := range m.records {
		if item.State == "running" {
			running++
		}
	}
	if running >= 3 {
		return FileTransfer{}, errors.New("最多同时执行 3 个文件传输任务")
	}
	if err := stage.file.Sync(); err != nil {
		return FileTransfer{}, err
	}
	if err := stage.file.Close(); err != nil {
		return FileTransfer{}, err
	}
	delete(m.stages, id)
	ctx, cancel := context.WithCancel(context.Background())
	rec := &fileTransferRecord{FileTransfer: FileTransfer{
		ID: id, DeviceID: stage.deviceID, ShareID: stage.shareID,
		RelativePath: stage.path, FileName: path.Base(stage.path),
		Direction: "upload", State: "running", Total: stage.size, StartedAt: time.Now().UnixMilli(),
	}, cancel: cancel}
	m.records[id] = rec
	m.order = append([]string{id}, m.order...)
	if len(m.order) > 100 {
		for _, oldID := range m.order[100:] {
			delete(m.records, oldID)
		}
		m.order = m.order[:100]
	}
	snapshot := rec.FileTransfer
	go m.upload(ctx, rec, stage.file.Name())
	return snapshot, nil
}

func (m *fileTransferManager) upload(ctx context.Context, record *fileTransferRecord, filename string) {
	defer os.Remove(filename)
	var err error
	file, openErr := os.Open(filename)
	if openErr != nil {
		err = openErr
	} else {
		started := time.Now()
		var lastDone int64
		_, err = m.agent.UploadRemoteFile(ctx, record.DeviceID, record.ShareID, record.RelativePath,
			file, record.Total, func(done, total int64) {
				m.mu.Lock()
				record.Done, record.Total = done, total
				elapsed := time.Since(started).Seconds()
				if elapsed > 0.25 {
					record.Speed = float64(done-lastDone) / elapsed
					lastDone, started = done, time.Now()
				}
				m.mu.Unlock()
			})
		_ = file.Close()
	}
	m.mu.Lock()
	switch {
	case ctx.Err() != nil:
		record.State = "cancelled"
	case err != nil:
		record.State, record.Error = "failed", err.Error()
	default:
		record.State = "completed"
	}
	record.Speed = 0
	m.mu.Unlock()
	record.cancel()
}

func (m *fileTransferManager) discardUpload(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	stage := m.stages[id]
	if stage == nil {
		return errors.New("upload staging session not found")
	}
	delete(m.stages, id)
	_ = stage.file.Close()
	return os.Remove(stage.file.Name())
}

func (b *UIBridge) BeginFileUpload(deviceID, shareID, remotePath string, size int64) (FileUploadProgress, error) {
	return b.fileTransfers.beginUpload(deviceID, shareID, remotePath, size)
}

func (b *UIBridge) AppendFileUploadChunk(id, chunk string) (FileUploadProgress, error) {
	return b.fileTransfers.appendUpload(id, chunk)
}

func (b *UIBridge) CommitFileUpload(id string) (FileTransfer, error) {
	return b.fileTransfers.commitUpload(id)
}

func (b *UIBridge) DiscardFileUpload(id string) error {
	return b.fileTransfers.discardUpload(id)
}
