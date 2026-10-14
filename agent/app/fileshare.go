package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"relayproxy/agent/fileshare"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

// SetFileShareStore enables the host-side scoped filesystem service. A nil
// store means even an approved files.host grant cannot serve local paths.
func (a *Agent) SetFileShareStore(store *fileshare.FileStore) {
	a.mu.Lock()
	a.fileShareStore = store
	a.mu.Unlock()
}

func (a *Agent) fileSharingSession() (tunnel.TunnelSession, string, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.readySession == nil || !a.handshakeOK.Load() {
		return nil, "", errors.New("Relay is not connected")
	}
	if !slices.Contains(a.approvedCapabilities, protocol.CapabilityFilesClient) {
		return nil, "", errors.New("files.client must be approved on Relay Server")
	}
	return a.readySession, a.cfg.DeviceID, nil
}

func (a *Agent) FileSharePeers(ctx context.Context) ([]protocol.FileSharePeer, error) {
	sess, _, err := a.fileSharingSession()
	if err != nil {
		return nil, err
	}
	stream, err := sess.OpenStream(ctx)
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	stop := tunnel.InterruptOnCancel(ctx, stream)
	defer stop()
	_ = stream.SetDeadline(time.Now().Add(15 * time.Second))
	if err := protocol.WriteStreamHeader(stream, &protocol.StreamHeader{
		Type: protocol.FrameTypeFileSharePeers, RequestID: fmt.Sprintf("files_peers_%d", time.Now().UnixNano()),
	}); err != nil {
		return nil, err
	}
	var response protocol.FileSharePeersResponse
	if err := protocol.ReadJSON(stream, &response); err != nil {
		return nil, err
	}
	if response.Error != "" {
		return nil, errors.New(response.Error)
	}
	if response.Peers == nil {
		return []protocol.FileSharePeer{}, nil
	}
	return response.Peers, nil
}

func (a *Agent) openRemoteFile(ctx context.Context, targetDeviceID string, req protocol.FileShareRequest) (tunnel.TunnelStream, protocol.FileShareResponse, error) {
	sess, _, err := a.fileSharingSession()
	if err != nil {
		return nil, protocol.FileShareResponse{}, err
	}
	targetDeviceID = strings.TrimSpace(targetDeviceID)
	if targetDeviceID == "" || len(targetDeviceID) > protocol.MaxStringLen {
		return nil, protocol.FileShareResponse{}, errors.New("invalid target device ID")
	}
	stream, err := sess.OpenStream(ctx)
	if err != nil {
		return nil, protocol.FileShareResponse{}, err
	}
	stop := tunnel.InterruptOnCancel(ctx, stream)
	success := false
	defer func() {
		stop()
		if !success {
			_ = stream.Close()
		}
	}()
	_ = stream.SetDeadline(time.Now().Add(15 * time.Second))
	if err := protocol.WriteStreamHeader(stream, &protocol.StreamHeader{
		Type: protocol.FrameTypeFileShare, ExitDeviceID: targetDeviceID,
		RequestID: fmt.Sprintf("files_%d", time.Now().UnixNano()),
	}); err != nil {
		return nil, protocol.FileShareResponse{}, err
	}
	if req.Path == "" {
		req.Path = "."
	}
	if err := protocol.WriteJSON(stream, req); err != nil {
		return nil, protocol.FileShareResponse{}, err
	}
	var response protocol.FileShareResponse
	if err := protocol.ReadJSON(stream, &response); err != nil {
		return nil, protocol.FileShareResponse{}, err
	}
	if !response.Success {
		if response.Error == "" {
			response.Error = "file share operation denied"
		}
		return nil, response, errors.New(response.Error)
	}
	// Deadline for a bulk file copy is controlled by its context, not by the
	// initial 15s handshake bound.
	_ = stream.SetDeadline(time.Time{})
	success = true
	return stream, response, nil
}

func (a *Agent) RemoteFileShares(ctx context.Context, deviceID string) ([]protocol.FileShareDescriptor, error) {
	stream, result, err := a.openRemoteFile(ctx, deviceID, protocol.FileShareRequest{Action: protocol.FileShareListShares})
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	return result.Shares, nil
}

func (a *Agent) RemoteFileList(ctx context.Context, deviceID, shareID, relativePath string) ([]protocol.FileShareEntry, error) {
	stream, result, err := a.openRemoteFile(ctx, deviceID, protocol.FileShareRequest{
		Action: protocol.FileShareListDir, ShareID: shareID, Path: relativePath,
	})
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	return result.Entries, nil
}

// DownloadRemoteFile streams raw file bytes into dst. The caller owns the
// destination and supplies a bounded context. No untrusted path ever becomes
// a local filesystem path in the Agent transport code.
func (a *Agent) DownloadRemoteFile(ctx context.Context, deviceID, shareID, relativePath string, dst io.Writer, progress func(int64, int64)) (int64, error) {
	stream, result, err := a.openRemoteFile(ctx, deviceID, protocol.FileShareRequest{
		Action: protocol.FileShareDownload, ShareID: shareID, Path: relativePath,
	})
	if err != nil {
		return 0, err
	}
	defer stream.Close()
	stop := tunnel.InterruptOnCancel(ctx, stream)
	defer stop()
	if result.Size < 0 {
		return 0, errors.New("invalid remote file size")
	}
	if progress != nil {
		progress(0, result.Size)
	}
	digest := sha256.New()
	writer := &fileProgressWriter{w: io.MultiWriter(dst, digest), size: result.Size, progress: progress}
	n, err := io.CopyN(writer, stream, result.Size)
	if err != nil {
		return n, err
	}
	if err := ctx.Err(); err != nil {
		return n, err
	}
	var receipt protocol.FileShareResponse
	if err := protocol.ReadJSON(stream, &receipt); err != nil {
		return n, fmt.Errorf("missing download completion receipt: %w", err)
	}
	if !receipt.Success {
		if receipt.Error == "" {
			receipt.Error = "remote file download failed"
		}
		return n, errors.New(receipt.Error)
	}
	if receipt.Size != result.Size || receipt.SHA256 == "" ||
		receipt.SHA256 != hex.EncodeToString(digest.Sum(nil)) {
		return n, errors.New("remote file download failed integrity verification")
	}
	if err := ctx.Err(); err != nil {
		return n, err
	}
	return n, nil
}

type fileProgressWriter struct {
	w        io.Writer
	n        int64
	size     int64
	progress func(int64, int64)
}

func (w *fileProgressWriter) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	w.n += int64(n)
	if w.progress != nil {
		w.progress(w.n, w.size)
	}
	return n, err
}

// FileShareCandidates returns only Server-confirmed files.client devices that
// are eligible to receive a per-directory ACL. The host never trusts an
// arbitrary device ID typed into the browser as a source of authorization.
func (a *Agent) FileShareCandidates(ctx context.Context) ([]protocol.FileShareCandidate, error) {
	a.mu.RLock()
	sess := a.readySession
	approved := slices.Contains(a.approvedCapabilities, protocol.CapabilityFilesHost)
	connected := a.handshakeOK.Load()
	a.mu.RUnlock()
	if sess == nil || !connected {
		return nil, errors.New("Relay is not connected")
	}
	if !approved {
		return nil, errors.New("files.host must be approved on Relay Server")
	}
	stream, err := sess.OpenStream(ctx)
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	stop := tunnel.InterruptOnCancel(ctx, stream)
	defer stop()
	_ = stream.SetDeadline(time.Now().Add(15 * time.Second))
	if err := protocol.WriteStreamHeader(stream, &protocol.StreamHeader{
		Type:      protocol.FrameTypeFileShareCandidates,
		RequestID: fmt.Sprintf("files_candidates_%d", time.Now().UnixNano()),
	}); err != nil {
		return nil, err
	}
	var response protocol.FileShareCandidatesResponse
	if err := protocol.ReadJSON(stream, &response); err != nil {
		return nil, err
	}
	if response.Error != "" {
		return nil, errors.New(response.Error)
	}
	if response.Devices == nil {
		return []protocol.FileShareCandidate{}, nil
	}
	return response.Devices, nil
}
