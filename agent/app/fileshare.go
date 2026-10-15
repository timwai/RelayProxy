package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"log"
	"io"
	"slices"
	"strings"
	"time"

	"relayproxy/agent/fileshare"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

type fileTransportStream struct {
	tunnel.TunnelStream
	path string
}

func (s *fileTransportStream) FileTransferPath() string { return s.path }

func fileTransferPath(stream tunnel.TunnelStream) string {
	if stream != nil {
		if reported, ok := stream.(interface{ FileTransferPath() string }); ok {
			return reported.FileTransferPath()
		}
	}
	return ""
}

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

// File metadata uses the existing, authenticated Relay session immediately.
// Negotiating a fresh file-only QUIC/TLS ticket for every directory made
// browsing wait for unreachable NAT candidates (up to several seconds).
// Actual file bytes still prefer P2P/direct with Relay fallback.
func fileSharePrefersDirect(action string) bool {
	return action == protocol.FileShareDownload || action == protocol.FileShareUpload
}

func (a *Agent) openRemoteFile(ctx context.Context, targetDeviceID string, req protocol.FileShareRequest) (tunnel.TunnelStream, protocol.FileShareResponse, error) {
	metadata := req.Action == protocol.FileShareListShares || req.Action == protocol.FileShareListDir
	start := time.Now()
	defer func() {
		if metadata && time.Since(start) > 250*time.Millisecond {
			// Never log share names, remote paths, directory entries or device IDs.
			log.Printf("[FileBrowse] action=%s relay_request_ms=%d", req.Action, time.Since(start).Milliseconds())
		}
	}()
	if fileSharePrefersDirect(req.Action) {
		if direct, response, directErr := a.tryFileDirect(ctx, targetDeviceID, req); directErr == nil {
			return &fileTransportStream{TunnelStream: direct, path: fileTransferPath(direct)}, response, nil
		}
	}
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
	pathName := "relay_tls"
	if sess.Transport() == tunnel.TransportQUIC {
		pathName = "relay_quic"
	}
	return &fileTransportStream{TunnelStream: stream, path: pathName}, response, nil
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

// DownloadRemoteFile preserves the original, zero-offset streaming API.
func (a *Agent) DownloadRemoteFile(ctx context.Context, deviceID, shareID, relativePath string, dst io.Writer, progress func(int64, int64)) (int64, error) {
	return a.downloadRemoteFileAt(ctx, deviceID, shareID, relativePath, dst, 0, "", sha256.New(), progress, nil)
}

// DownloadRemoteFileResuming authenticates the whole local prefix before asking
// the host for the remaining bytes. A resumed response is accepted only if the
// host verifies the prefix against its current scoped file. No bytes are
// appended if the remote file changed before resuming.
func (a *Agent) DownloadRemoteFileResuming(ctx context.Context, deviceID, shareID, relativePath string, dst io.ReadWriteSeeker, progress func(int64, int64)) (int64, error) {
	return a.DownloadRemoteFileResumingWithPath(ctx, deviceID, shareID, relativePath, dst, progress, nil)
}

// DownloadRemoteFileResumingWithPath reports the authenticated transport
// actually used for this attempt, not just the route preference.
func (a *Agent) DownloadRemoteFileResumingWithPath(ctx context.Context, deviceID, shareID, relativePath string,
	dst io.ReadWriteSeeker, progress func(int64, int64), pathChanged func(string)) (int64, error) {
	offset, err := dst.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, err
	}
	if offset < 0 {
		return 0, errors.New("invalid local partial file length")
	}
	digest := sha256.New()
	if offset > 0 {
		if _, err := dst.Seek(0, io.SeekStart); err != nil {
			return 0, err
		}
		if _, err := io.CopyN(digest, dst, offset); err != nil {
			return 0, fmt.Errorf("unable to hash local download prefix: %w", err)
		}
	}
	if _, err := dst.Seek(offset, io.SeekStart); err != nil {
		return 0, err
	}
	prefix := ""
	if offset > 0 {
		prefix = hex.EncodeToString(digest.Sum(nil))
	}
	return a.downloadRemoteFileAt(ctx, deviceID, shareID, relativePath, dst, offset, prefix, digest, progress, pathChanged)
}

func (a *Agent) downloadRemoteFileAt(ctx context.Context, deviceID, shareID, relativePath string,
	dst io.Writer, offset int64, prefix string, digest hash.Hash, progress func(int64, int64), pathChanged func(string)) (int64, error) {
	stream, result, err := a.openRemoteFile(ctx, deviceID, protocol.FileShareRequest{
		Action: protocol.FileShareDownload, ShareID: shareID, Path: relativePath,
		Offset: offset, PrefixSHA256: prefix,
	})
	if err != nil {
		return 0, err
	}
	defer stream.Close()
	stop := tunnel.InterruptOnCancel(ctx, stream)
	defer stop()
	if pathChanged != nil {
		pathChanged(fileTransferPath(stream))
	}
	if result.Size < offset || result.Offset != offset {
		return 0, errors.New("invalid remote resume response")
	}
	if progress != nil {
		progress(offset, result.Size)
	}
	writer := &fileProgressWriter{w: io.MultiWriter(dst, digest), n: offset, size: result.Size, progress: progress}
	n, err := io.CopyN(writer, stream, result.Size-offset)
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
