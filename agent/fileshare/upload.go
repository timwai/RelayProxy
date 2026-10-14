package fileshare

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

// MaxUploadSize bounds how many bytes one peer may write in a single stream.
// This limit is enforced at both endpoints, prior to creating any local file.
const MaxUploadSize int64 = 20 << 30

// serveUpload creates a private sibling staging file in the same root-bound
// directory as the requested destination. A hard link publishes the completed
// file atomically and fails if the destination already exists. No existing
// user file is truncated, including under concurrent uploads.
func (s *FileStore) serveUpload(ctx context.Context, stream tunnel.TunnelStream, req protocol.FileShareRequest, selected Share, authenticatedDeviceID string) {
	fail := func(message string) {
		_ = protocol.WriteJSON(stream, protocol.FileShareResponse{Success: false, Error: message})
	}
	if selected.Permissions[authenticatedDeviceID] != PermissionReadWrite {
		fail("device has no write access to this shared folder")
		return
	}
	if req.Size < 0 || req.Size > MaxUploadSize {
		fail("upload size exceeds maximum allowed size")
		return
	}
	if req.Path == "" || req.Path == "." || !fs.ValidPath(req.Path) ||
		!filepath.IsLocal(filepath.FromSlash(req.Path)) || strings.Contains(req.Path, "\\") {
		fail("invalid relative upload path")
		return
	}
	filename := path.Base(req.Path)
	if filename == "." || filename == "" || strings.HasPrefix(filename, ".relayproxy-upload-") {
		fail("invalid upload filename")
		return
	}
	root, err := os.OpenRoot(selected.RootPath)
	if err != nil {
		fail("shared folder unavailable")
		return
	}
	defer root.Close()
	// Target folder must exist. os.Root confines any symlink traversals.
	parent := path.Dir(req.Path)
	parentInfo, err := root.Stat(filepath.FromSlash(parent))
	if err != nil || !parentInfo.IsDir() {
		fail("destination folder unavailable")
		return
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		fail("unable to allocate upload")
		return
	}
	tempRel := path.Join(parent, ".relayproxy-upload-"+hex.EncodeToString(nonce[:])+".partial")
	tempLocal := filepath.FromSlash(tempRel)
	targetLocal := filepath.FromSlash(req.Path)
	temp, err := root.OpenFile(tempLocal, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		fail("unable to prepare destination file")
		return
	}
	defer root.Remove(tempLocal)
	defer temp.Close()
	// Initial response acknowledges the upload but does not promise that the
	// destination file exists. A final response follows all bytes and sync.
	if err := protocol.WriteJSON(stream, protocol.FileShareResponse{Success: true}); err != nil {
		return
	}
	_ = stream.SetDeadline(time.Time{})
	src := &sharePermissionReader{
		input: stream, store: s, shareID: selected.ID, rootPath: selected.RootPath,
		deviceID: authenticatedDeviceID,
	}
	n, err := io.CopyN(temp, src, req.Size)
	if err != nil || n != req.Size || ctx.Err() != nil {
		// A canceled or truncated transfer cannot leave visible partial data.
		if ctx.Err() == nil {
			fail("upload incomplete or permission revoked")
		}
		return
	}
	if err := src.authorize(); err != nil {
		fail("file sharing permission revoked")
		return
	}
	if err := temp.Sync(); err != nil {
		fail("unable to sync uploaded data")
		return
	}
	if err := temp.Close(); err != nil {
		fail("unable to finalize uploaded data")
		return
	}
	// Recheck immediately before publishing; a permission change during
	// fsync must not turn an incomplete authorization into a visible file.
	if err := src.authorize(); err != nil {
		fail("file sharing permission revoked")
		return
	}
	// Root.Link has NO overwrite semantics; it fails with os.ErrExist when
	// another file appears at the same destination concurrently.
	if err := root.Link(tempLocal, targetLocal); err != nil {
		if errors.Is(err, os.ErrExist) {
			fail("destination already exists")
		} else {
			fail("unable to publish uploaded file")
		}
		return
	}
	_ = protocol.WriteJSON(stream, protocol.FileShareResponse{Success: true, Size: req.Size})
}

// sharePermissionReader handles mid-upload revocation as well as the initial
// grant. A blocked network read is also bounded by the enclosing context.
type sharePermissionReader struct {
	input    io.Reader
	store    *FileStore
	shareID  string
	rootPath string
	deviceID string
	lastCheck time.Time
}

func (r *sharePermissionReader) authorize() error {
	state, err := r.store.Load()
	if err != nil || !state.Enabled {
		return errors.New("file sharing disabled")
	}
	for _, share := range state.Shares {
		if share.ID == r.shareID && share.Enabled && share.RootPath == r.rootPath &&
			share.Permissions[r.deviceID] == PermissionReadWrite {
			r.lastCheck = time.Now()
			return nil
		}
	}
	return fmt.Errorf("write permission revoked")
}

func (r *sharePermissionReader) Read(p []byte) (int, error) {
	if r.lastCheck.IsZero() || time.Since(r.lastCheck) >= time.Second {
		if err := r.authorize(); err != nil {
			return 0, err
		}
	}
	return r.input.Read(p)
}
