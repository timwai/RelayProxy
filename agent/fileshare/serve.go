package fileshare

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

// Serve reads one request from a trusted Relay stream. Caller identity MUST
// come from the Server-rewritten StreamHeader, never from the request payload.
func (s *FileStore) Serve(ctx context.Context, stream tunnel.TunnelStream, authenticatedDeviceID string) {
	defer stream.Close()
	stop := tunnel.InterruptOnCancel(ctx, stream)
	defer stop()
	_ = stream.SetDeadline(time.Now().Add(15 * time.Second))
	var req protocol.FileShareRequest
	if err := protocol.ReadJSON(stream, &req); err != nil {
		return
	}
	fail := func(message string) {
		_ = protocol.WriteJSON(stream, protocol.FileShareResponse{Success: false, Error: message})
	}
	if authenticatedDeviceID == "" {
		fail("unauthenticated device")
		return
	}
	state, err := s.Load()
	if err != nil || !state.Enabled {
		fail("file sharing is disabled")
		return
	}
	if req.Action == protocol.FileShareListShares {
		result := make([]protocol.FileShareDescriptor, 0)
		for _, share := range state.Shares {
			perm := share.Permissions[authenticatedDeviceID]
			if share.Enabled && (perm == PermissionRead || perm == PermissionReadWrite) {
				result = append(result, protocol.FileShareDescriptor{
					ID: share.ID, Name: share.Name, Permission: string(perm),
				})
			}
		}
		_ = protocol.WriteJSON(stream, protocol.FileShareResponse{Success: true, Shares: result})
		return
	}
	var selected *Share
	for i := range state.Shares {
		if state.Shares[i].ID == req.ShareID && state.Shares[i].Enabled {
			selected = &state.Shares[i]
			break
		}
	}
	if selected == nil {
		fail("shared folder is unavailable")
		return
	}
	permission := selected.Permissions[authenticatedDeviceID]
	if permission != PermissionRead && permission != PermissionReadWrite {
		fail("device has no access to this shared folder")
		return
	}
	if !fs.ValidPath(req.Path) || !filepath.IsLocal(filepath.FromSlash(req.Path)) || strings.Contains(req.Path, "\\") {
		fail("invalid relative path")
		return
	}
	// Private staging files are not part of the shared namespace. Other
	// readers cannot list or guess them while an upload is incomplete.
	for _, component := range strings.Split(req.Path, "/") {
		if strings.HasPrefix(component, ".relayproxy-upload-") {
			fail("private upload staging path")
			return
		}
	}
	if req.Action == protocol.FileShareUpload {
		s.serveUpload(ctx, stream, req, *selected, authenticatedDeviceID)
		return
	}
	if req.Action != protocol.FileShareListDir && req.Action != protocol.FileShareDownload {
		fail("unsupported file sharing operation")
		return
	}
	// os.Root prevents escape via symlinks, races and platform-specific path
	// normalization. Never concatenate untrusted input with RootPath.
	root, err := os.OpenRoot(selected.RootPath)
	if err != nil {
		fail("shared folder is unavailable")
		return
	}
	defer root.Close()
	file, err := root.Open(filepath.FromSlash(req.Path))
	if err != nil {
		fail("file not available")
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		fail("file not available")
		return
	}
	if req.Action == protocol.FileShareListDir {
		if !info.IsDir() {
			fail("not a directory")
			return
		}
		infos, err := file.Readdir(5001)
		if err != nil && !errors.Is(err, io.EOF) {
			fail("unable to list directory")
			return
		}
		if len(infos) > 5000 {
			fail("directory contains more than 5000 entries")
			return
		}
		entries := make([]protocol.FileShareEntry, 0, len(infos))
		for _, item := range infos {
			// Do not advertise filesystem links (their target may change after
			// listing, and the UI cannot reliably distinguish them).
			if item.Mode()&os.ModeSymlink != 0 || strings.HasPrefix(item.Name(), ".relayproxy-upload-") {
				continue
			}
			entries = append(entries, protocol.FileShareEntry{
				Name: item.Name(), IsDir: item.IsDir(),
				Size: item.Size(), ModTime: item.ModTime().UnixMilli(),
			})
		}
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].IsDir != entries[j].IsDir {
				return entries[i].IsDir
			}
			return entries[i].Name < entries[j].Name
		})
		_ = protocol.WriteJSON(stream, protocol.FileShareResponse{Success: true, Entries: entries})
		return
	}
	if !info.Mode().IsRegular() {
		fail("not a regular file")
		return
	}
	if err := protocol.WriteJSON(stream, protocol.FileShareResponse{Success: true, Size: info.Size()}); err != nil {
		return
	}
	_ = stream.SetDeadline(time.Time{})
	writer := &sharePermissionWriter{
		stream: stream, store: s, shareID: selected.ID,
		rootPath: selected.RootPath, deviceID: authenticatedDeviceID,
	}
	_, _ = io.CopyN(writer, file, info.Size())
}

// sharePermissionWriter re-reads the local ACL at most once per second during
// a long transfer. Removing a share, disabling it, changing its root or
// revoking the peer's read access terminates the stream immediately.
type sharePermissionWriter struct {
	stream   io.Writer
	store    *FileStore
	shareID  string
	rootPath string
	deviceID string
	lastCheck time.Time
}

func (w *sharePermissionWriter) Write(p []byte) (int, error) {
	if w.lastCheck.IsZero() || time.Since(w.lastCheck) >= time.Second {
		state, err := w.store.Load()
		if err != nil || !state.Enabled {
			return 0, errors.New("file sharing is disabled")
		}
		valid := false
		for _, share := range state.Shares {
			perm := share.Permissions[w.deviceID]
			if share.ID == w.shareID && share.Enabled &&
				share.RootPath == w.rootPath &&
				(perm == PermissionRead || perm == PermissionReadWrite) {
				valid = true
				break
			}
		}
		if !valid {
			return 0, errors.New("file sharing permission revoked")
		}
		w.lastCheck = time.Now()
	}
	return w.stream.Write(p)
}
