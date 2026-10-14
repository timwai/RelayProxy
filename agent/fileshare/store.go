// Package fileshare owns the Agent-local share registry and its device ACLs.
// It deliberately does not expose file contents over the local Web API.
package fileshare

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

type Permission string

const (
	PermissionNone      Permission = "none"
	PermissionRead      Permission = "read"
	PermissionReadWrite Permission = "read_write"
)

var ErrConflict = errors.New("file sharing configuration changed; reload before saving")

type Share struct {
	ID          string                `json:"id"`
	Name        string                `json:"name"`
	RootPath    string                `json:"rootPath"`
	Enabled     bool                  `json:"enabled"`
	Permissions map[string]Permission `json:"permissions"`
}

type State struct {
	Revision uint64  `json:"revision"`
	Enabled  bool    `json:"enabled"`
	Shares   []Share `json:"shares"`
}

type Descriptor struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Permission Permission `json:"permission"`
}

// FileStore uses a local sidecar adjacent to the Agent YAML so file shares are
// never transmitted in the public device inventory or overwritten by YAML edits.
// Hosting is intentionally bounded even when a peer opens many concurrent
// authenticated streams. The local configuration API is not subject to this limit.
const MaxConcurrentHostedFileStreams int32 = 6

type FileStore struct {
	Path string
	activeFileStreams atomic.Int32
}

func (s *FileStore) acquireFileStream() bool {
	for {
		active := s.activeFileStreams.Load()
		if active >= MaxConcurrentHostedFileStreams {
			return false
		}
		if s.activeFileStreams.CompareAndSwap(active, active+1) {
			return true
		}
	}
}

// All FileStore instances inside a running Agent share this lock. This also
// covers the Web and Wails management surfaces using the same sidecar.
var fileMu sync.Mutex

func New(configPath string) (*FileStore, error) {
	if strings.TrimSpace(configPath) == "" {
		return nil, errors.New("agent config path required")
	}
	abs, err := filepath.Abs(configPath)
	if err != nil {
		return nil, err
	}
	return &FileStore{Path: abs + ".file-shares.json"}, nil
}

func (s *FileStore) Load() (State, error) {
	fileMu.Lock()
	defer fileMu.Unlock()
	return s.load()
}

func (s *FileStore) load() (State, error) {
	state := State{Shares: []Share{}}
	raw, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return State{}, fmt.Errorf("read file shares: %w", err)
	}
	if state.Shares == nil {
		state.Shares = []Share{}
	}
	return state, nil
}

func newShareID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return "share_" + hex.EncodeToString(value[:]), nil
}

// Save atomically replaces the registry if its revision still matches.
// No file is copied, moved, removed or chmod'd by this method.
func (s *FileStore) Save(next State) (State, error) {
	fileMu.Lock()
	defer fileMu.Unlock()
	old, err := s.load()
	if err != nil {
		return State{}, err
	}
	if old.Revision != next.Revision {
		return State{}, ErrConflict
	}
	if len(next.Shares) > 128 {
		return State{}, errors.New("too many shared folders")
	}
	ids := map[string]bool{}
	paths := make([]string, 0, len(next.Shares))
	for i := range next.Shares {
		share := &next.Shares[i]
		share.Name = strings.TrimSpace(share.Name)
		if share.Name == "" || len([]rune(share.Name)) > 100 {
			return State{}, fmt.Errorf("invalid share name at index %d", i)
		}
		if share.ID == "" {
			share.ID, err = newShareID()
			if err != nil {
				return State{}, err
			}
		}
		if !strings.HasPrefix(share.ID, "share_") || ids[share.ID] {
			return State{}, errors.New("duplicate or invalid share ID")
		}
		ids[share.ID] = true
		if !filepath.IsAbs(share.RootPath) {
			return State{}, fmt.Errorf("%s: absolute root path required", share.Name)
		}
		resolved, e := filepath.EvalSymlinks(filepath.Clean(share.RootPath))
		if e != nil {
			return State{}, fmt.Errorf("%s: %w", share.Name, e)
		}
		info, e := os.Stat(resolved)
		if e != nil {
			return State{}, fmt.Errorf("%s: %w", share.Name, e)
		}
		if !info.IsDir() {
			return State{}, fmt.Errorf("%s: root must be a directory", share.Name)
		}
		// Keep old ACLs from silently granting access to a different tree.
		// Changing a previously authorized share root requires explicit ACL
		// removal; the GUI can then re-authorize devices against the new root.
		for _, previous := range old.Shares {
			if previous.ID == share.ID && previous.RootPath != resolved && len(share.Permissions) != 0 {
				return State{}, fmt.Errorf("%s: clear device permissions before changing the shared folder", share.Name)
			}
		}
		share.RootPath = resolved
		for _, p := range paths {
			relativeFromExisting, e1 := filepath.Rel(p, resolved)
			relativeFromNew, e2 := filepath.Rel(resolved, p)
			if e1 != nil || e2 != nil {
				continue
			} // Different volumes cannot overlap.
			within := func(relative string) bool {
				return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
			}
			if within(relativeFromExisting) || within(relativeFromNew) {
				return State{}, fmt.Errorf("overlapping shared folders: %s", share.Name)
			}
		}
		paths = append(paths, resolved)
		if share.Permissions == nil {
			share.Permissions = map[string]Permission{}
		}
		for device, perm := range share.Permissions {
			if strings.TrimSpace(device) != device || device == "" || len(device) > 256 {
				return State{}, errors.New("invalid device ID")
			}
			switch perm {
			case PermissionNone:
				delete(share.Permissions, device)
			case PermissionRead, PermissionReadWrite:
			default:
				return State{}, fmt.Errorf("invalid permission for device %s", device)
			}
		}
	}
	next.Revision++
	if err := s.persist(next); err != nil {
		return State{}, err
	}
	return next, nil
}

func (s *FileStore) persist(state State) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.Path)
	tmp, err := os.CreateTemp(dir, ".file-shares-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err = tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, s.Path)
}

// Visible returns metadata for one authenticated peer. Never return RootPath
// or ACLs to remote peers. Callers must pass the peer ID derived from a
// verified server session, NOT an ID supplied in a request body.
func (s *FileStore) Visible(authenticatedDeviceID string) ([]Descriptor, error) {
	state, err := s.Load()
	if err != nil {
		return nil, err
	}
	result := []Descriptor{}
	if !state.Enabled || authenticatedDeviceID == "" {
		return result, nil
	}
	for _, share := range state.Shares {
		if !share.Enabled {
			continue
		}
		perm := share.Permissions[authenticatedDeviceID]
		if perm == PermissionRead || perm == PermissionReadWrite {
			result = append(result, Descriptor{ID: share.ID, Name: share.Name, Permission: perm})
		}
	}
	return result, nil
}
