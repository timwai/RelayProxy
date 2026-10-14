package app

import (
	"context"
	"errors"
	"fmt"
	"io"

	"relayproxy/agent/fileshare"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

// UploadRemoteFile streams exactly size bytes into one authorized remote
// shared directory. Success is returned ONLY after the remote host has
// committed the complete file; existing files are never overwritten.
func (a *Agent) UploadRemoteFile(ctx context.Context, deviceID, shareID, relativePath string, src io.Reader, size int64, progress func(int64, int64)) (int64, error) {
	if src == nil || size < 0 || size > fileshare.MaxUploadSize {
		return 0, fmt.Errorf("invalid file upload size or source")
	}
	stream, _, err := a.openRemoteFile(ctx, deviceID, protocol.FileShareRequest{
		Action: protocol.FileShareUpload, ShareID: shareID, Path: relativePath, Size: size,
	})
	if err != nil {
		return 0, err
	}
	defer stream.Close()
	stop := tunnel.InterruptOnCancel(ctx, stream)
	defer stop()
	if progress != nil {
		progress(0, size)
	}
	writer := &fileProgressWriter{w: stream, size: size, progress: progress}
	n, err := io.CopyN(writer, src, size)
	if err != nil {
		return n, err
	}
	if n != size {
		return n, io.ErrShortWrite
	}
	if err := ctx.Err(); err != nil {
		return n, err
	}
	// Half-close is essential: Server must continue forwarding the final
	// success/error acknowledgement in the opposite direction.
	if err := stream.CloseWrite(); err != nil {
		return n, err
	}
	var final protocol.FileShareResponse
	if err := protocol.ReadJSON(stream, &final); err != nil {
		return n, fmt.Errorf("remote upload final acknowledgement failed: %w", err)
	}
	if !final.Success {
		if final.Error == "" {
			final.Error = "remote file upload was rejected"
		}
		return n, errors.New(final.Error)
	}
	if final.Size != size {
		return n, errors.New("remote file upload size mismatch")
	}
	if err := ctx.Err(); err != nil {
		return n, err
	}
	return n, nil
}
