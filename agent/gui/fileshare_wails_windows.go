//go:build windows

package gui

import (
	"relayproxy/agent/bridge"
	"relayproxy/agent/fileshare"
	"relayproxy/internal/protocol"
)

// Native Wails callers use exactly the same bridge validation as local Web.
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
func (s *WailsService) StartFileDownload(deviceID, shareID, relativePath string) (bridge.FileTransfer, error) {
	return s.owner.bridge.StartFileDownload(deviceID, shareID, relativePath)
}
func (s *WailsService) GetFileTransfers() []bridge.FileTransfer {
	return s.owner.bridge.GetFileTransfers()
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
