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
	"path/filepath"
	"strings"

	desktopmedia "relayproxy/internal/desktop"
	"relayproxy/internal/protocol"
)

const (
	maxDesktopClipboardFiles         = 32
	maxDesktopClipboardFileBytes     = 256 << 20
	maxDesktopClipboardTransferBytes = 512 << 20
	desktopClipboardFileChunkBytes   = 384 << 10
)

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

func buildClipboardFileOffer(paths []string) (protocol.DesktopClipboardFileOffer, error) {
	if len(paths) == 0 || len(paths) > maxDesktopClipboardFiles {
		return protocol.DesktopClipboardFileOffer{}, fmt.Errorf("clipboard file count must be 1..%d", maxDesktopClipboardFiles)
	}
	id, err := newClipboardTransferID()
	if err != nil {
		return protocol.DesktopClipboardFileOffer{}, err
	}
	offer := protocol.DesktopClipboardFileOffer{TransferID: id}
	var total int64
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return protocol.DesktopClipboardFileOffer{}, err
		}
		if !info.Mode().IsRegular() {
			return protocol.DesktopClipboardFileOffer{}, fmt.Errorf("clipboard path %q is not a regular file", path)
		}
		name, err := safeClipboardFileName(filepath.Base(path))
		if err != nil {
			return protocol.DesktopClipboardFileOffer{}, err
		}
		if info.Size() < 0 || info.Size() > maxDesktopClipboardFileBytes {
			return protocol.DesktopClipboardFileOffer{}, fmt.Errorf("clipboard file %q exceeds %d bytes", name, maxDesktopClipboardFileBytes)
		}
		total += info.Size()
		if total > maxDesktopClipboardTransferBytes {
			return protocol.DesktopClipboardFileOffer{}, fmt.Errorf("clipboard transfer exceeds %d bytes", maxDesktopClipboardTransferBytes)
		}
		sum, err := hashClipboardFile(path)
		if err != nil {
			return protocol.DesktopClipboardFileOffer{}, err
		}
		offer.Files = append(offer.Files, protocol.DesktopClipboardFile{Name: name, Size: info.Size(), SHA256: sum})
	}
	return offer, nil
}

func hashClipboardFile(path string) (string, error) {
	file, err := os.Open(path)
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
	offer, err := buildClipboardFileOffer(paths)
	if err != nil {
		return protocol.DesktopClipboardState{}, err
	}
	if err := conn.SendSessionMessage(ctx, protocol.DesktopSessionMessage{
		Type:               protocol.DesktopSessionClipboardFileOffer,
		ClipboardFileOffer: &offer,
	}); err != nil {
		return protocol.DesktopClipboardState{}, err
	}
	buffer := make([]byte, desktopClipboardFileChunkBytes)
	for index, path := range paths {
		file, err := os.Open(path)
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
		Files:      append([]protocol.DesktopClipboardFile(nil), offer.Files...),
		LocalPaths: append([]string(nil), paths...),
	}, nil
}

type clipboardFileReceiver struct {
	transferID string
	dir        string
	files      []protocol.DesktopClipboardFile
	handles    []*os.File
	offsets    []int64
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
	*r = clipboardFileReceiver{}
}

func (r *clipboardFileReceiver) offer(offer protocol.DesktopClipboardFileOffer) error {
	r.reset()
	if len(offer.TransferID) < 16 || len(offer.TransferID) > 128 {
		return errors.New("invalid clipboard transfer ID")
	}
	if len(offer.Files) == 0 || len(offer.Files) > maxDesktopClipboardFiles {
		return fmt.Errorf("clipboard file count must be 1..%d", maxDesktopClipboardFiles)
	}
	var total int64
	for i := range offer.Files {
		name, err := safeClipboardFileName(offer.Files[i].Name)
		if err != nil {
			return err
		}
		offer.Files[i].Name = name
		if offer.Files[i].Size < 0 || offer.Files[i].Size > maxDesktopClipboardFileBytes {
			return fmt.Errorf("clipboard file %q exceeds size limit", name)
		}
		total += offer.Files[i].Size
		if total > maxDesktopClipboardTransferBytes {
			return errors.New("clipboard transfer exceeds total size limit")
		}
		if len(offer.Files[i].SHA256) != 64 {
			return fmt.Errorf("clipboard file %q has invalid SHA-256", name)
		}
		if _, err := hex.DecodeString(offer.Files[i].SHA256); err != nil {
			return fmt.Errorf("clipboard file %q has invalid SHA-256", name)
		}
	}
	dir, err := os.MkdirTemp("", "relayproxy-clipboard-*")
	if err != nil {
		return err
	}
	r.transferID = offer.TransferID
	r.dir = dir
	r.files = append([]protocol.DesktopClipboardFile(nil), offer.Files...)
	r.handles = make([]*os.File, len(offer.Files))
	r.offsets = make([]int64, len(offer.Files))
	for i, meta := range offer.Files {
		handle, err := os.OpenFile(filepath.Join(dir, meta.Name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
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
	paths := make([]string, len(r.files))
	for i, meta := range r.files {
		if r.offsets[i] != meta.Size {
			return protocol.DesktopClipboardState{}, fmt.Errorf("clipboard file %q incomplete: %d/%d", meta.Name, r.offsets[i], meta.Size)
		}
		if r.handles[i] != nil {
			if err := r.handles[i].Close(); err != nil {
				return protocol.DesktopClipboardState{}, err
			}
			r.handles[i] = nil
		}
		path := filepath.Join(r.dir, meta.Name)
		sum, err := hashClipboardFile(path)
		if err != nil {
			return protocol.DesktopClipboardState{}, err
		}
		if !strings.EqualFold(sum, meta.SHA256) {
			return protocol.DesktopClipboardState{}, fmt.Errorf("clipboard file %q SHA-256 mismatch", meta.Name)
		}
		paths[i] = path
	}
	state := protocol.DesktopClipboardState{
		Kind:       protocol.DesktopClipboardKindFiles,
		TransferID: r.transferID,
		Files:      append([]protocol.DesktopClipboardFile(nil), r.files...),
		LocalPaths: paths,
	}
	r.transferID = ""
	r.dir = ""
	r.files = nil
	r.handles = nil
	r.offsets = nil
	return state, nil
}

func clipboardFilePathsKey(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	normalized := make([]string, 0, len(paths))
	for _, path := range paths {
		absolute, err := filepath.Abs(path)
		if err != nil {
			absolute = path
		}
		normalized = append(normalized, filepath.Clean(absolute))
	}
	return strings.Join(normalized, "\x00")
}
