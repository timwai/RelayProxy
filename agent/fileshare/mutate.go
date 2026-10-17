package fileshare

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"relayproxy/internal/protocol"
)

// mutateFile is called only after Serve authenticated the device and checked
// the current share's read_write ACL. os.Root keeps every operation beneath the
// configured share even when intermediate directories are symlinks.
func (s *FileStore) mutateFile(share Share, req protocol.FileShareRequest) error {
	if !safeMutationPath(req.Path) {
		return errors.New("invalid source path")
	}
	if req.Action == protocol.FileShareMove {
		if !safeMutationPath(req.Destination) {
			return errors.New("invalid destination path")
		}
		if req.Path == req.Destination {
			return errors.New("source and destination are identical")
		}
		if strings.HasPrefix(req.Destination, req.Path+"/") {
			return errors.New("cannot move directory inside itself")
		}
	}
	if req.Action != protocol.FileShareMkdir && req.Action != protocol.FileShareMove && req.Action != protocol.FileShareRemove {
		return errors.New("unsupported mutation")
	}
	s.mutateMu.Lock()
	defer s.mutateMu.Unlock()
	root, err := os.OpenRoot(share.RootPath)
	if err != nil {
		return errors.New("shared folder unavailable")
	}
	defer root.Close()
	source := filepath.FromSlash(req.Path)
	switch req.Action {
	case protocol.FileShareMkdir:
		if _, err := root.Lstat(source); err == nil {
			return errors.New("file or folder already exists")
		} else if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("cannot create folder: %w", err)
		}
		return root.Mkdir(source, 0750)
	case protocol.FileShareRemove:
		info, err := root.Lstat(source)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("symbolic links are not supported")
		}
		// Remove is deliberately non-recursive. Never erase entire trees by accident.
		return root.Remove(source)
	case protocol.FileShareMove:
		info, err := root.Lstat(source)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("symbolic links are not supported")
		}
		target := filepath.FromSlash(req.Destination)
		if _, err := root.Lstat(target); err == nil {
			return errors.New("destination already exists")
		} else if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("destination unavailable: %w", err)
		}
		if info.Mode().IsRegular() {
			// A hard link never replaces an existing destination, even when two
			// uploads or other writers race with this move.
			if err := root.Link(source, target); err != nil {
				return err
			}
			if err := root.Remove(source); err != nil {
				_ = root.Remove(target)
				return err
			}
			return nil
		}
		if !info.IsDir() {
			return errors.New("unsupported file type")
		}
		// Rename is required for directories; serialization guards simultaneous
		// requests to this FileStore, while Lstat rejects ordinary name conflicts.
		return root.Rename(source, target)
	}
	return errors.New("unsupported mutation")
}

func safeMutationPath(name string) bool {
	if name == "" || name == "." || !fs.ValidPath(name) ||
		!filepath.IsLocal(filepath.FromSlash(name)) || strings.Contains(name, "\\") {
		return false
	}
	for _, component := range strings.Split(name, "/") {
		if strings.HasPrefix(component, ".relayproxy-upload-") {
			return false
		}
	}
	return true
}
