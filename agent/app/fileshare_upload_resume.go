package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"relayproxy/agent/fileshare"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

// UploadRemoteFileResuming preserves the server-provided random upload token
// only for the lifetime of this task. Each retry opens a freshly authenticated
// Relay stream. The remote offset is checked against SHA-256 of the same local
// file before sending the suffix; the final digest covers the whole file.
func (a *Agent) UploadRemoteFileResuming(ctx context.Context, deviceID, shareID, relativePath string,
	src io.ReadSeeker, size int64, progress func(int64, int64)) (int64, error) {
	if src == nil || size < 0 || size > fileshare.MaxUploadSize {
		return 0, errors.New("invalid resumable upload source or size")
	}
	var uploadID string
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if err := ctx.Err(); err != nil { return 0, err }
		stream, ready, err := a.openRemoteFile(ctx, deviceID, protocol.FileShareRequest{
			Action: protocol.FileShareUpload, ShareID: shareID, Path: relativePath,
			Size: size, Resume: true, UploadID: uploadID,
		})
		if err != nil {
			lastErr = err
			// A missing host state on retry cannot be silently replaced with a
			// new upload; this preserves explicit revocation semantics.
			if uploadID != "" && strings.Contains(err.Error(), "resume token") { return 0, err }
		} else {
			if ready.UploadID == "" || len(ready.UploadID) != 48 ||
				(uploadID != "" && ready.UploadID != uploadID) ||
				ready.Size != size || ready.Offset < 0 || ready.Offset > size {
				_ = stream.Close()
				return 0, errors.New("invalid remote upload resume acknowledgement")
			}
			uploadID = ready.UploadID
			hash := sha256.New()
			if _, err = src.Seek(0, io.SeekStart); err != nil {
				_ = stream.Close()
				return 0, err
			}
			if _, err = io.CopyN(hash, src, ready.Offset); err != nil {
				_ = stream.Close()
				return 0, fmt.Errorf("cannot hash upload prefix: %w", err)
			}
			if ready.PrefixSHA256 != hex.EncodeToString(hash.Sum(nil)) {
				_ = stream.Close()
				return 0, errors.New("remote upload prefix differs from selected local file")
			}
			if _, err = src.Seek(ready.Offset, io.SeekStart); err != nil {
				_ = stream.Close()
				return 0, err
			}
			if progress != nil { progress(ready.Offset, size) }
			stop := tunnel.InterruptOnCancel(ctx, stream)
			writer := &fileProgressWriter{w: io.MultiWriter(stream, hash), n: ready.Offset, size: size, progress: progress}
			n, copyErr := io.CopyN(writer, src, size-ready.Offset)
			if copyErr == nil && n == size-ready.Offset {
				copyErr = stream.CloseWrite()
			}
			if copyErr == nil {
				var final protocol.FileShareResponse
				copyErr = protocol.ReadJSON(stream, &final)
				if copyErr == nil && !final.Success {
					if final.Error == "" { final.Error = "remote upload rejected" }
					copyErr = errors.New(final.Error)
				}
				if copyErr == nil && (final.Size != size || final.SHA256 != hex.EncodeToString(hash.Sum(nil))) {
					copyErr = errors.New("resumable upload final SHA-256 mismatch")
				}
			}
			stop()
			_ = stream.Close()
			if copyErr == nil {
				if err := ctx.Err(); err != nil { return 0, err }
				return size, nil
			}
			lastErr = copyErr
		}
		if ctx.Err() != nil { return 0, ctx.Err() }
		if !isRetryableFileUploadError(lastErr) || attempt == 3 { break }
		delay := time.NewTimer(time.Duration(attempt+1)*350*time.Millisecond)
		select {
		case <-ctx.Done(): delay.Stop(); return 0, ctx.Err()
		case <-delay.C:
		}
	}
	return 0, fmt.Errorf("file upload did not finish: %w", lastErr)
}

func isRetryableFileUploadError(err error) bool {
	if err == nil {return false}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {return true}
	msg:=strings.ToLower(err.Error())
	return strings.Contains(msg,"connection reset") ||
		strings.Contains(msg,"stream reset") ||
		strings.Contains(msg,"transport closed") ||
		strings.Contains(msg,"connection lost") ||
		strings.Contains(msg,"timeout")
}
