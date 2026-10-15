package bridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"relayproxy/agent/app"
)

type FileTransfer struct {
	ID            string  `json:"id"`
	DeviceID      string  `json:"deviceId"`
	ShareID       string  `json:"shareId"`
	RelativePath  string  `json:"relativePath"`
	FileName      string  `json:"fileName"`
	Direction     string  `json:"direction"`
	TransportPath string  `json:"transportPath,omitempty"`
	Destination   string  `json:"destination,omitempty"`
	RequiresSaveAs bool  `json:"requiresSaveAs,omitempty"`
	State         string  `json:"state"`
	Total         int64   `json:"total"`
	Done          int64   `json:"done"`
	Speed         float64 `json:"speed"`
	Error         string  `json:"error,omitempty"`
	StartedAt     int64   `json:"startedAt"`
}

type fileTransferRecord struct {
	FileTransfer
	cancel     context.CancelFunc
	lastSample time.Time
	lastBytes  int64
	savePath   string // from local OS picker only; never accepted from Web HTTP
}

type fileTransferManager struct {
	mu            sync.Mutex
	agent         *app.Agent
	records       map[string]*fileTransferRecord
	stages        map[string]*fileUploadSession
	order         []string
	taskStorePath string
	taskScope     string
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
	return m.startTo(deviceID, shareID, relativePath, "")
}

// startTo is available to a trusted native UI only. Browser endpoints never
// accept an arbitrary Agent-host filesystem destination from HTTP requests.
func (m *fileTransferManager) startTo(deviceID, shareID, relativePath, savePath string) (FileTransfer, error) {
	if savePath != "" {
		if err := validateFileDownloadDestination(savePath); err != nil {
			return FileTransfer{}, err
		}
	}
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
		RequiresSaveAs: savePath != "",
	}, cancel: cancel, lastSample: time.Now(), savePath: savePath}
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
	_ = m.persistTasksLocked()
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
		record.State = "paused"
	} else if err != nil {
		record.State, record.Error = "failed", err.Error()
	} else {
		record.State, record.Speed = "completed", 0
	}
	record.cancel()
	_ = m.persistTasksLocked()
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
	// Stable, unguessable partial name permits a paused/failed download to
	// reuse its verified prefix without ever exposing incomplete data as a
	// normal download. The root confines all file operations to the download
	// folder even when an existing partial path is a symlink.
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	tempName := ".relayproxy-download-" + record.ID + ".partial"
	if info, statErr := root.Lstat(tempName); statErr == nil {
		if !info.Mode().IsRegular() {
			return errors.New("download partial file must be a regular file")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	file, err := root.OpenFile(tempName, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := file.Chmod(0600); err != nil {
		return err
	}
	lastTime := time.Now()
	lastDone := int64(0)
	for attempt := 0; attempt < 4; attempt++ {
		_, err = m.agent.DownloadRemoteFileResumingWithPath(ctx, record.DeviceID, record.ShareID, record.RelativePath, file, func(done, total int64) {
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
		}, func(path string) {
			m.mu.Lock()
			record.TransportPath = path
			m.mu.Unlock()
		})
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if strings.Contains(err.Error(), "resume prefix mismatch") ||
			strings.Contains(err.Error(), "invalid resume offset") ||
			strings.Contains(err.Error(), "invalid remote resume response") {
			// The remote file changed. Do not splice mismatched versions;
			// discard the local partial and attempt a fresh snapshot.
			if truncateErr := file.Truncate(0); truncateErr != nil {
				return truncateErr
			}
			if _, seekErr := file.Seek(0, 0); seekErr != nil {
				return seekErr
			}
		} else if !isRetryableFileDownloadError(err) {
			break
		}
		if attempt == 3 {
			break
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 350 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
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
	// Full random task ID avoids collisions with another prior download.
	destinationName := fmt.Sprintf("%s-%s%s", base, record.ID, ext)
	destination := filepath.Join(dir, destinationName)
	if record.savePath != "" {
		// Save-dialog destinations may live on a different filesystem, so a
		// hard link from the private partial folder would fail with EXDEV.
		// Copy to a private file in the chosen directory, fsync, then
		// publish without replacement, never leaving a partial visible.
		if err := publishFileDownloadTo(ctx, root, tempName, record.savePath, record.ID); err != nil {
			return err
		}
		destination = record.savePath
	} else {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := root.Link(tempName, destinationName); err != nil {
			return fmt.Errorf("publish download without overwriting: %w", err)
		}
	}
	_ = root.Remove(tempName)
	m.mu.Lock()
	record.Destination = destination
	m.mu.Unlock()
	return nil
}

// validateFileDownloadDestination prevents a UI dialog result from becoming
// an implicit create-directory or symlink-following operation. os.Root below
// resolves files only inside the explicitly selected existing directory.
func validateFileDownloadDestination(destination string) error {
	if !filepath.IsAbs(destination) || filepath.Clean(destination) != destination {
		return errors.New("download save path must be absolute and canonical")
	}
	name := filepath.Base(destination)
	if name == "." || name == ".." || name == "" || name == string(filepath.Separator) {
		return errors.New("invalid download save file name")
	}
	if len(name) > 255 {
		return errors.New("download save file name too long")
	}
	return nil
}

func publishFileDownloadTo(ctx context.Context, sourceRoot *os.Root, sourceName, destination, id string) error {
	if err := validateFileDownloadDestination(destination); err != nil {
		return err
	}
	parent := filepath.Dir(destination)
	root, err := os.OpenRoot(parent)
	if err != nil {
		return err
	}
	defer root.Close()
	name := filepath.Base(destination)
	if _, err := root.Lstat(name); err == nil {
		return os.ErrExist // The save dialog must not silently overwrite files.
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmp := ".relayproxy-save-" + id + ".partial"
	out, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(tmp)
	defer out.Close()
	in, err := sourceRoot.Open(sourceName)
	if err != nil {
		return err
	}
	defer in.Close()
	if _, err := io.Copy(out, &fileContextReader{ctx: ctx, r: in}); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := root.Link(tmp, name); err != nil {
		return fmt.Errorf("publish chosen save file without overwriting: %w", err)
	}
	return nil
}

type fileContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *fileContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

// resumeTransfer deliberately supports downloads only. Upload source files
// are deleted once their background task exits, so a cancelled upload must
// be selected again instead of pretending its source remains available.
func (m *fileTransferManager) resumeTransfer(id string) error {
	if m == nil || m.agent == nil {
		return errors.New("file transfer manager unavailable")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	item := m.records[id]
	if item == nil || item.Direction != "download" ||
		(item.State != "paused" && item.State != "failed") {
		return errors.New("download task cannot be resumed")
	}
	if item.RequiresSaveAs && item.savePath == "" {
		return errors.New("请重新选择下载保存位置，然后继续")
	}
	active := 0
	for _, record := range m.records {
		if record.State == "running" {
			active++
		}
	}
	if active >= 3 {
		return errors.New("最多同时进行 3 个文件传输任务")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	partial := filepath.Join(home, "Downloads", "RelayProxy", ".relayproxy-download-"+id+".partial")
	info, err := os.Lstat(partial)
	if err != nil {
		return fmt.Errorf("cannot resume without local partial file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("download partial file is not a regular file")
	}
	ctx, cancel := context.WithCancel(context.Background())
	item.cancel = cancel
	item.State, item.Error, item.Speed = "running", "", 0
	_ = m.persistTasksLocked()
	go m.download(ctx, item)
	return nil
}

// Resume custom Save As tasks only after an explicit local OS picker result.
// A restarted Agent must never silently redirect a previously chosen
// destination into Downloads/RelayProxy.
func (m *fileTransferManager) resumeTransferTo(id, destination string) error {
	if err := validateFileDownloadDestination(destination); err != nil {
		return err
	}
	m.mu.Lock()
	record := m.records[id]
	if record == nil || record.Direction != "download" ||
		(record.State != "paused" && record.State != "failed") {
		m.mu.Unlock()
		return errors.New("download task cannot be resumed")
	}
	record.savePath = destination
	record.RequiresSaveAs = true
	m.mu.Unlock()
	return m.resumeTransfer(id)
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

// StartFileDownloadTo is for a local OS Save dialog (Wails). Do not expose
// this method as an Agent Web HTTP endpoint accepting caller-supplied paths.
func (b *UIBridge) StartFileDownloadTo(deviceID, shareID, relativePath, savePath string) (FileTransfer, error) {
	if savePath == "" {
		return FileTransfer{}, errors.New("save dialog was cancelled")
	}
	return b.fileTransfers.startTo(deviceID, shareID, relativePath, savePath)
}

// OpenCompletedFileDownload exposes only successfully verified, default-root
// downloads to the local authenticated browser. Native Save As paths are
// deliberately NOT readable through this Web endpoint.
func (b *UIBridge) OpenCompletedFileDownload(id string) (*os.File, string, error) {
	m := b.fileTransfers
	if m == nil || !validDownloadTaskID(id) {
		return nil, "", os.ErrNotExist
	}
	m.mu.Lock()
	record := m.records[id]
	if record == nil || record.Direction != "download" || record.State != "completed" ||
		record.savePath != "" {
		m.mu.Unlock()
		return nil, "", os.ErrNotExist
	}
	name := record.FileName
	if name == "" || path.Base(name) != name {
		m.mu.Unlock()
		return nil, "", os.ErrInvalid
	}
	m.mu.Unlock()
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, "", err
	}
	root, err := os.OpenRoot(filepath.Join(home, "Downloads", "RelayProxy"))
	if err != nil {
		return nil, "", err
	}
	defer root.Close()
	ext := filepath.Ext(name)
	diskName := fmt.Sprintf("%s-%s%s", strings.TrimSuffix(name, ext), id, ext)
	info, err := root.Lstat(diskName)
	if err != nil || !info.Mode().IsRegular() {
		return nil, "", os.ErrNotExist
	}
	f, err := root.Open(diskName)
	return f, name, err
}

func (b *UIBridge) GetFileTransfers() []FileTransfer {
	return b.fileTransfers.list()
}

func (b *UIBridge) CancelFileDownload(id string) error {
	return b.fileTransfers.cancelTransfer(id)
}

func (b *UIBridge) ResumeFileDownload(id string) error {
	return b.fileTransfers.resumeTransfer(id)
}

func (b *UIBridge) ResumeFileDownloadTo(id, savePath string) error {
	return b.fileTransfers.resumeTransferTo(id, savePath)
}

func isRetryableFileDownloadError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "connection reset") || strings.Contains(msg, "stream reset") ||
		strings.Contains(msg, "transport closed") || strings.Contains(msg, "connection closed") ||
		strings.Contains(msg, "connection lost")
}
