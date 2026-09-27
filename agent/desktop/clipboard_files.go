package desktop

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"
	"time"

	desktopmedia "relayproxy/internal/desktop"
	"relayproxy/internal/protocol"
)

const (
	maxDesktopClipboardRoots         = 32
	maxDesktopClipboardFiles         = 2048
	maxDesktopClipboardFileBytes     = 256 << 20
	maxDesktopClipboardTransferBytes = 512 << 20
	desktopClipboardFileChunkBytes   = 384 << 10
)

type clipboardFileTransferPlan struct {
	offer   protocol.DesktopClipboardFileOffer
	sources []string
}

func newClipboardTransferID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func safeClipboardFileName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return "", errors.New("clipboard file name is empty")
	}
	if filepath.Base(name) != name || strings.ContainsAny(name, "/\\") {
		return "", fmt.Errorf("clipboard file name %q contains path separators", name)
	}
	if strings.ContainsRune(name, 0) {
		return "", errors.New("clipboard file name contains NUL")
	}
	return name, nil
}

func safeClipboardRelativePath(value string) (string, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" || strings.HasPrefix(value, "/") {
		return "", errors.New("clipboard relative path is empty or absolute")
	}
	parts := strings.Split(value, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("clipboard relative path %q contains an unsafe component", value)
		}
		if _, err := safeClipboardFileName(part); err != nil {
			return "", err
		}
	}
	clean := pathpkg.Clean(value)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("clipboard relative path %q escapes the transfer root", value)
	}
	return clean, nil
}

func buildClipboardFileTransfer(paths []string) (clipboardFileTransferPlan, error) {
	if len(paths) == 0 || len(paths) > maxDesktopClipboardRoots {
		return clipboardFileTransferPlan{}, fmt.Errorf("clipboard root count must be 1..%d", maxDesktopClipboardRoots)
	}
	id, err := newClipboardTransferID()
	if err != nil {
		return clipboardFileTransferPlan{}, err
	}
	plan := clipboardFileTransferPlan{
		offer: protocol.DesktopClipboardFileOffer{TransferID: id},
	}
	roots := make(map[string]struct{}, len(paths))
	files := make(map[string]struct{})
	var total int64

	addFile := func(sourcePath, relativePath string, info os.FileInfo) error {
		relativePath, err = safeClipboardRelativePath(relativePath)
		if err != nil {
			return err
		}
		key := strings.ToLower(relativePath)
		if _, exists := files[key]; exists {
			return fmt.Errorf("duplicate clipboard relative path %q", relativePath)
		}
		if len(plan.offer.Files) >= maxDesktopClipboardFiles {
			return fmt.Errorf("clipboard file count exceeds %d", maxDesktopClipboardFiles)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("clipboard path %q is not a regular file", sourcePath)
		}
		if info.Size() < 0 || info.Size() > maxDesktopClipboardFileBytes {
			return fmt.Errorf("clipboard file %q exceeds %d bytes", relativePath, maxDesktopClipboardFileBytes)
		}
		total += info.Size()
		if total > maxDesktopClipboardTransferBytes {
			return fmt.Errorf("clipboard transfer exceeds %d bytes", maxDesktopClipboardTransferBytes)
		}
		sum, err := hashClipboardFile(sourcePath)
		if err != nil {
			return err
		}
		files[key] = struct{}{}
		plan.offer.Files = append(plan.offer.Files, protocol.DesktopClipboardFile{
			Name:   filepath.Base(sourcePath),
			Path:   relativePath,
			Size:   info.Size(),
			SHA256: sum,
		})
		plan.sources = append(plan.sources, sourcePath)
		return nil
	}

	for _, inputPath := range paths {
		absolute, err := filepath.Abs(inputPath)
		if err != nil {
			return clipboardFileTransferPlan{}, err
		}
		info, err := os.Lstat(absolute)
		if err != nil {
			return clipboardFileTransferPlan{}, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return clipboardFileTransferPlan{}, fmt.Errorf("clipboard path %q is a symbolic link", inputPath)
		}
		rootName, err := safeClipboardFileName(filepath.Base(absolute))
		if err != nil {
			return clipboardFileTransferPlan{}, err
		}
		rootKey := strings.ToLower(rootName)
		if _, exists := roots[rootKey]; exists {
			return clipboardFileTransferPlan{}, fmt.Errorf("duplicate clipboard root %q", rootName)
		}
		roots[rootKey] = struct{}{}

		switch {
		case info.Mode().IsRegular():
			plan.offer.Roots = append(plan.offer.Roots, protocol.DesktopClipboardRoot{Name: rootName})
			if err := addFile(absolute, rootName, info); err != nil {
				return clipboardFileTransferPlan{}, err
			}
		case info.IsDir():
			plan.offer.Roots = append(plan.offer.Roots, protocol.DesktopClipboardRoot{Name: rootName, Directory: true})
			err := filepath.Walk(absolute, func(current string, currentInfo os.FileInfo, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if current == absolute {
					return nil
				}
				if currentInfo.Mode()&os.ModeSymlink != 0 {
					return fmt.Errorf("clipboard directory contains symbolic link %q", current)
				}
				if currentInfo.IsDir() {
					return nil
				}
				if !currentInfo.Mode().IsRegular() {
					return fmt.Errorf("clipboard directory contains unsupported entry %q", current)
				}
				relative, err := filepath.Rel(absolute, current)
				if err != nil {
					return err
				}
				return addFile(current, pathpkg.Join(rootName, filepath.ToSlash(relative)), currentInfo)
			})
			if err != nil {
				return clipboardFileTransferPlan{}, err
			}
		default:
			return clipboardFileTransferPlan{}, fmt.Errorf("clipboard path %q is neither a regular file nor directory", inputPath)
		}
	}
	return plan, nil
}

func buildClipboardFileOffer(paths []string) (protocol.DesktopClipboardFileOffer, error) {
	plan, err := buildClipboardFileTransfer(paths)
	if err != nil {
		return protocol.DesktopClipboardFileOffer{}, err
	}
	return plan.offer, nil
}

func hashClipboardFile(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func sendClipboardFiles(ctx context.Context, conn *desktopmedia.MediaConn, paths []string) (protocol.DesktopClipboardState, error) {
	if conn == nil {
		return protocol.DesktopClipboardState{}, errors.New("desktop clipboard transport is unavailable")
	}
	plan, err := buildClipboardFileTransfer(paths)
	if err != nil {
		return protocol.DesktopClipboardState{}, err
	}
	offer := plan.offer
	if err := conn.SendSessionMessage(ctx, protocol.DesktopSessionMessage{
		Type:               protocol.DesktopSessionClipboardFileOffer,
		ClipboardFileOffer: &offer,
	}); err != nil {
		return protocol.DesktopClipboardState{}, err
	}
	buffer := make([]byte, desktopClipboardFileChunkBytes)
	for index, sourcePath := range plan.sources {
		file, err := os.Open(sourcePath)
		if err != nil {
			return protocol.DesktopClipboardState{}, err
		}
		var offset int64
		for {
			n, readErr := file.Read(buffer)
			if n > 0 {
				chunk := protocol.DesktopClipboardFileChunk{
					TransferID: offer.TransferID,
					FileIndex:  index,
					Offset:     offset,
					Data:       append([]byte(nil), buffer[:n]...),
				}
				if err := conn.SendSessionMessage(ctx, protocol.DesktopSessionMessage{
					Type:               protocol.DesktopSessionClipboardFileChunk,
					ClipboardFileChunk: &chunk,
				}); err != nil {
					file.Close()
					return protocol.DesktopClipboardState{}, err
				}
				offset += int64(n)
			}
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				file.Close()
				return protocol.DesktopClipboardState{}, readErr
			}
		}
		if err := file.Close(); err != nil {
			return protocol.DesktopClipboardState{}, err
		}
	}
	done := protocol.DesktopClipboardFileDone{TransferID: offer.TransferID}
	if err := conn.SendSessionMessage(ctx, protocol.DesktopSessionMessage{
		Type:              protocol.DesktopSessionClipboardFileDone,
		ClipboardFileDone: &done,
	}); err != nil {
		return protocol.DesktopClipboardState{}, err
	}
	return protocol.DesktopClipboardState{
		Kind:       protocol.DesktopClipboardKindFiles,
		TransferID: offer.TransferID,
		Roots:      append([]protocol.DesktopClipboardRoot(nil), offer.Roots...),
		Files:      append([]protocol.DesktopClipboardFile(nil), offer.Files...),
		LocalPaths: append([]string(nil), paths...),
	}, nil
}

type clipboardFileReceiver struct {
	transferID   string
	dir          string
	completedDir string
	roots        []protocol.DesktopClipboardRoot
	files        []protocol.DesktopClipboardFile
	handles      []*os.File
	offsets      []int64
}

func (r *clipboardFileReceiver) reset() {
	for _, handle := range r.handles {
		if handle != nil {
			_ = handle.Close()
		}
	}
	if r.dir != "" {
		_ = os.RemoveAll(r.dir)
	}
	r.transferID = ""
	r.dir = ""
	r.roots = nil
	r.files = nil
	r.handles = nil
	r.offsets = nil
}

func (r *clipboardFileReceiver) cleanupCompleted() {
	if r.completedDir != "" {
		_ = os.RemoveAll(r.completedDir)
		r.completedDir = ""
	}
}

func (r *clipboardFileReceiver) close() {
	r.reset()
	if r.completedDir == "" {
		return
	}
	dir := r.completedDir
	r.completedDir = ""
	time.AfterFunc(time.Hour, func() {
		_ = os.RemoveAll(dir)
	})
}

func normalizeClipboardFileOffer(offer protocol.DesktopClipboardFileOffer) (protocol.DesktopClipboardFileOffer, error) {
	if len(offer.TransferID) < 16 || len(offer.TransferID) > 128 {
		return protocol.DesktopClipboardFileOffer{}, errors.New("invalid clipboard transfer ID")
	}
	if len(offer.Roots) > maxDesktopClipboardRoots {
		return protocol.DesktopClipboardFileOffer{}, fmt.Errorf("clipboard root count exceeds %d", maxDesktopClipboardRoots)
	}
	if len(offer.Files) > maxDesktopClipboardFiles {
		return protocol.DesktopClipboardFileOffer{}, fmt.Errorf("clipboard file count exceeds %d", maxDesktopClipboardFiles)
	}

	rootNames := make(map[string]struct{})
	for i := range offer.Roots {
		name, err := safeClipboardFileName(offer.Roots[i].Name)
		if err != nil {
			return protocol.DesktopClipboardFileOffer{}, err
		}
		key := strings.ToLower(name)
		if _, exists := rootNames[key]; exists {
			return protocol.DesktopClipboardFileOffer{}, fmt.Errorf("duplicate clipboard root %q", name)
		}
		rootNames[key] = struct{}{}
		offer.Roots[i].Name = name
	}

	var total int64
	filePaths := make(map[string]struct{})
	for i := range offer.Files {
		relative := offer.Files[i].Path
		if relative == "" {
			relative = offer.Files[i].Name
		}
		relative, err := safeClipboardRelativePath(relative)
		if err != nil {
			return protocol.DesktopClipboardFileOffer{}, err
		}
		name, err := safeClipboardFileName(pathpkg.Base(relative))
		if err != nil {
			return protocol.DesktopClipboardFileOffer{}, err
		}
		key := strings.ToLower(relative)
		if _, exists := filePaths[key]; exists {
			return protocol.DesktopClipboardFileOffer{}, fmt.Errorf("duplicate clipboard file path %q", relative)
		}
		filePaths[key] = struct{}{}
		offer.Files[i].Name = name
		offer.Files[i].Path = relative
		if offer.Files[i].Size < 0 || offer.Files[i].Size > maxDesktopClipboardFileBytes {
			return protocol.DesktopClipboardFileOffer{}, fmt.Errorf("clipboard file %q exceeds size limit", relative)
		}
		total += offer.Files[i].Size
		if total > maxDesktopClipboardTransferBytes {
			return protocol.DesktopClipboardFileOffer{}, errors.New("clipboard transfer exceeds total size limit")
		}
		if len(offer.Files[i].SHA256) != 64 {
			return protocol.DesktopClipboardFileOffer{}, fmt.Errorf("clipboard file %q has invalid SHA-256", relative)
		}
		if _, err := hex.DecodeString(offer.Files[i].SHA256); err != nil {
			return protocol.DesktopClipboardFileOffer{}, fmt.Errorf("clipboard file %q has invalid SHA-256", relative)
		}
		if len(offer.Roots) > 0 {
			first := strings.SplitN(relative, "/", 2)[0]
			if _, ok := rootNames[strings.ToLower(first)]; !ok {
				return protocol.DesktopClipboardFileOffer{}, fmt.Errorf("clipboard file %q is outside declared roots", relative)
			}
		}
	}

	if len(offer.Roots) == 0 {
		if len(offer.Files) == 0 {
			return protocol.DesktopClipboardFileOffer{}, errors.New("clipboard transfer has no roots or files")
		}
		for _, file := range offer.Files {
			if strings.Contains(file.Path, "/") {
				return protocol.DesktopClipboardFileOffer{}, errors.New("legacy clipboard offer cannot contain nested paths without roots")
			}
			offer.Roots = append(offer.Roots, protocol.DesktopClipboardRoot{Name: file.Name})
		}
	}
	return offer, nil
}

func (r *clipboardFileReceiver) offer(offer protocol.DesktopClipboardFileOffer) error {
	r.reset()
	normalized, err := normalizeClipboardFileOffer(offer)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "relayproxy-clipboard-*")
	if err != nil {
		return err
	}
	r.transferID = normalized.TransferID
	r.dir = dir
	r.roots = append([]protocol.DesktopClipboardRoot(nil), normalized.Roots...)
	r.files = append([]protocol.DesktopClipboardFile(nil), normalized.Files...)
	r.handles = make([]*os.File, len(normalized.Files))
	r.offsets = make([]int64, len(normalized.Files))

	for _, root := range r.roots {
		if root.Directory {
			if err := os.MkdirAll(filepath.Join(dir, root.Name), 0o700); err != nil {
				r.reset()
				return err
			}
		}
	}
	for i, meta := range r.files {
		destination := filepath.Join(dir, filepath.FromSlash(meta.Path))
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			r.reset()
			return err
		}
		handle, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			r.reset()
			return err
		}
		r.handles[i] = handle
	}
	return nil
}

func (r *clipboardFileReceiver) chunk(chunk protocol.DesktopClipboardFileChunk) error {
	if r.transferID == "" || chunk.TransferID != r.transferID {
		return errors.New("clipboard file chunk has no matching offer")
	}
	if chunk.FileIndex < 0 || chunk.FileIndex >= len(r.files) {
		return errors.New("clipboard file chunk has invalid file index")
	}
	if len(chunk.Data) == 0 || len(chunk.Data) > desktopClipboardFileChunkBytes {
		return errors.New("clipboard file chunk has invalid size")
	}
	if chunk.Offset != r.offsets[chunk.FileIndex] {
		return errors.New("clipboard file chunk offset is not sequential")
	}
	if chunk.Offset+int64(len(chunk.Data)) > r.files[chunk.FileIndex].Size {
		return errors.New("clipboard file chunk exceeds declared file size")
	}
	n, err := r.handles[chunk.FileIndex].Write(chunk.Data)
	if err != nil {
		return err
	}
	if n != len(chunk.Data) {
		return io.ErrShortWrite
	}
	r.offsets[chunk.FileIndex] += int64(n)
	return nil
}

func (r *clipboardFileReceiver) done(done protocol.DesktopClipboardFileDone) (protocol.DesktopClipboardState, error) {
	if r.transferID == "" || done.TransferID != r.transferID {
		return protocol.DesktopClipboardState{}, errors.New("clipboard file completion has no matching offer")
	}
	for i, meta := range r.files {
		if r.offsets[i] != meta.Size {
			return protocol.DesktopClipboardState{}, fmt.Errorf("clipboard file %q incomplete: %d/%d", meta.Path, r.offsets[i], meta.Size)
		}
		if r.handles[i] != nil {
			if err := r.handles[i].Close(); err != nil {
				return protocol.DesktopClipboardState{}, err
			}
			r.handles[i] = nil
		}
		filePath := filepath.Join(r.dir, filepath.FromSlash(meta.Path))
		sum, err := hashClipboardFile(filePath)
		if err != nil {
			return protocol.DesktopClipboardState{}, err
		}
		if !strings.EqualFold(sum, meta.SHA256) {
			return protocol.DesktopClipboardState{}, fmt.Errorf("clipboard file %q SHA-256 mismatch", meta.Path)
		}
	}
	rootPaths := make([]string, 0, len(r.roots))
	for _, root := range r.roots {
		rootPath := filepath.Join(r.dir, root.Name)
		if _, err := os.Stat(rootPath); err != nil {
			return protocol.DesktopClipboardState{}, fmt.Errorf("clipboard root %q is missing: %w", root.Name, err)
		}
		rootPaths = append(rootPaths, rootPath)
	}
	state := protocol.DesktopClipboardState{
		Kind:       protocol.DesktopClipboardKindFiles,
		TransferID: r.transferID,
		Roots:      append([]protocol.DesktopClipboardRoot(nil), r.roots...),
		Files:      append([]protocol.DesktopClipboardFile(nil), r.files...),
		LocalPaths: rootPaths,
	}
	oldCompleted := r.completedDir
	r.completedDir = r.dir
	r.transferID = ""
	r.dir = ""
	r.roots = nil
	r.files = nil
	r.handles = nil
	r.offsets = nil
	if oldCompleted != "" && oldCompleted != r.completedDir {
		_ = os.RemoveAll(oldCompleted)
	}
	return state, nil
}

func clipboardFilePathsKey(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	normalized := make([]string, 0, len(paths))
	for _, filePath := range paths {
		absolute, err := filepath.Abs(filePath)
		if err != nil {
			absolute = filePath
		}
		normalized = append(normalized, filepath.Clean(absolute))
	}
	return strings.Join(normalized, "\x00")
}
