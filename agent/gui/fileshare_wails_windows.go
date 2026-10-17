//go:build windows

package gui

import (
	"os"
	"path"
	"path/filepath"

	"relayproxy/agent/app"
	"relayproxy/agent/bridge"
	"relayproxy/agent/fileshare"
	"relayproxy/internal/protocol"
)

// Native Wails callers use exactly the same bridge validation as local Web.
// ChooseShareFolder uses the OS directory picker; the returned absolute
// path is shown for confirmation and still validated by FileStore.Save.
func (s *WailsService) ChooseShareFolder() (string, error) {
	return s.owner.app.Dialog.OpenFile().
		SetTitle("选择要共享的文件夹").
		CanChooseDirectories(true).CanChooseFiles(false).
		PromptForSingleSelection()
}

// ChooseAndStartFileDownload avoids giving the webpage a writable local
// filesystem API. The native save dialog supplies the destination to Go.
func (s *WailsService) ChooseAndStartFileDownload(deviceID, shareID, relativePath string) (bridge.FileTransfer, error) {
	name := path.Base(relativePath)
	if name == "" || name == "." || name == ".." {
		return bridge.FileTransfer{}, os.ErrInvalid
	}
	dialog := s.owner.app.Dialog.SaveFile().SetMessage("保存远程文件").SetFilename(name)
	if home, err := os.UserHomeDir(); err == nil {
		dialog.SetDirectory(filepath.Join(home, "Downloads"))
	}
	destination, err := dialog.PromptForSingleSelection()
	if err != nil || destination == "" {
		return bridge.FileTransfer{}, err
	}
	return s.owner.bridge.StartFileDownloadTo(deviceID, shareID, relativePath, destination)
}

// Restore an explicit Save As target after pause or Agent restart. The file
// is still downloaded to the private partial before publication.
func (s *WailsService) ChooseAndResumeFileDownload(id string) (bool, error) {
	var name string
	for _, item := range s.owner.bridge.GetFileTransfers() {
		if item.ID == id && item.Direction == "download" {
			name = item.FileName
			break
		}
	}
	if name == "" {
		return false, os.ErrNotExist
	}
	dialog := s.owner.app.Dialog.SaveFile().SetMessage("选择继续下载的保存位置").SetFilename(name)
	if home, err := os.UserHomeDir(); err == nil {
		dialog.SetDirectory(filepath.Join(home, "Downloads"))
	}
	destination, err := dialog.PromptForSingleSelection()
	if err != nil || destination == "" {
		return false, err
	}
	if err := s.owner.bridge.ResumeFileDownloadTo(id, destination); err != nil {
		return false, err
	}
	return true, nil
}

func (s *WailsService) GetFileShares() (fileshare.State, error) {
	return s.owner.bridge.GetFileShares()
}
func (s *WailsService) SaveFileShares(state fileshare.State) (fileshare.State, error) {
	return s.owner.bridge.SaveFileShares(state)
}

func (s *WailsService) GetFileSharePeers() ([]protocol.FileSharePeer, error) {
	return s.owner.bridge.GetFileSharePeers()
}
func (s *WailsService) GetRemoteFileShares(deviceID string) ([]protocol.FileShareDescriptor, error) {
	return s.owner.bridge.GetRemoteFileShares(deviceID)
}
func (s *WailsService) GetRemoteFileEntries(deviceID, shareID, relativePath string) ([]protocol.FileShareEntry, error) {
	return s.owner.bridge.GetRemoteFileEntries(deviceID, shareID, relativePath)
}
func (s *WailsService) OpenFileBrowserWindow(deviceID, shareID, relativePath string) string {
	if s == nil || s.owner == nil {
		return "unavailable"
	}
	s.owner.openFileBrowser(deviceID, shareID, relativePath)
	return "ok"
}

func (s *WailsService) MutateRemoteFile(deviceID, shareID, action, relativePath, destination string) error {
	return s.owner.bridge.MutateRemoteFile(deviceID, shareID, action, relativePath, destination)
}

func (s *WailsService) StartFileDownload(deviceID, shareID, relativePath string) (bridge.FileTransfer, error) {
	return s.owner.bridge.StartFileDownload(deviceID, shareID, relativePath)
}
func (s *WailsService) GetFileTransfers() []bridge.FileTransfer {
	return s.owner.bridge.GetFileTransfers()
}
func (s *WailsService) ClearFileTransfer(id string) error {
 return s.owner.bridge.ClearFileTransfer(id)
}
func (s *WailsService) ClearInactiveFileTransfers() (int,error) {
 return s.owner.bridge.ClearInactiveFileTransfers()
}
func (s *WailsService) CancelFileDownload(id string) error {
	return s.owner.bridge.CancelFileDownload(id)
}

func (s *WailsService) BeginFileUpload(deviceID, shareID, relativePath string, size int64) (bridge.FileUploadProgress, error) {
	return s.owner.bridge.BeginFileUpload(deviceID, shareID, relativePath, size)
}
func (s *WailsService) AppendFileUploadChunk(id, base64Chunk string) (bridge.FileUploadProgress, error) {
	return s.owner.bridge.AppendFileUploadChunk(id, base64Chunk)
}
func (s *WailsService) CommitFileUpload(id string) (bridge.FileTransfer, error) {
	return s.owner.bridge.CommitFileUpload(id)
}
func (s *WailsService) DiscardFileUpload(id string) error {
	return s.owner.bridge.DiscardFileUpload(id)
}

func (s *WailsService) GetFileShareCandidates() ([]protocol.FileShareCandidate, error) {
	return s.owner.bridge.GetFileShareCandidates()
}

func (s *WailsService) ResumeFileDownload(id string) error {
	return s.owner.bridge.ResumeFileDownload(id)
}

func (s *WailsService) GetFileDirectStatus() app.FileDirectStatus {
	return s.owner.bridge.GetFileDirectStatus()
}
