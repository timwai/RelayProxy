package desktop

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/png"
	"log"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	desktopmedia "relayproxy/internal/desktop"
	"relayproxy/internal/protocol"
)

var (
	ErrClipboardTextUnavailable  = errors.New("text clipboard unavailable")
	ErrClipboardImageUnavailable = errors.New("image clipboard unavailable")
	ErrClipboardFilesUnavailable = errors.New("file clipboard unavailable")
)

type ClipboardEndpoint interface {
	ClipboardText(context.Context) (string, error)
	SetClipboardText(context.Context, string) error
}

type ClipboardContentEndpoint interface {
	ClipboardContent(context.Context) (protocol.DesktopClipboardState, error)
	SetClipboardContent(context.Context, protocol.DesktopClipboardState) error
}

type ClipboardFileEndpoint interface {
	ClipboardFiles(context.Context) ([]string, error)
	SetClipboardFiles(context.Context, []string) error
}

type clipboardSyncState struct {
	mu          sync.Mutex
	initialized bool
	content     protocol.DesktopClipboardState
}

func validateClipboardText(text string) (string, error) {
	if index := strings.IndexByte(text, 0); index >= 0 {
		text = text[:index]
	}
	if !utf8.ValidString(text) {
		return "", errors.New("clipboard text is not valid UTF-8")
	}
	if len(text) > protocol.MaxDesktopClipboardBytes {
		return "", fmt.Errorf("clipboard text exceeds %d bytes", protocol.MaxDesktopClipboardBytes)
	}
	return text, nil
}

func validateClipboardContent(content protocol.DesktopClipboardState) (protocol.DesktopClipboardState, error) {
	content.Sequence = 0
	kind := strings.ToLower(strings.TrimSpace(content.Kind))
	if kind == "" {
		if len(content.PNG) > 0 {
			kind = protocol.DesktopClipboardKindPNG
		} else {
			kind = protocol.DesktopClipboardKindText
		}
	}
	switch kind {
	case protocol.DesktopClipboardKindText:
		text, err := validateClipboardText(content.Text)
		if err != nil {
			return protocol.DesktopClipboardState{}, err
		}
		content.Kind = protocol.DesktopClipboardKindText
		content.Text = text
		content.PNG = nil
	case protocol.DesktopClipboardKindPNG:
		if len(content.PNG) == 0 {
			return protocol.DesktopClipboardState{}, errors.New("clipboard PNG payload is empty")
		}
		if len(content.PNG) > protocol.MaxDesktopClipboardImageBytes {
			return protocol.DesktopClipboardState{}, fmt.Errorf(
				"clipboard PNG exceeds %d bytes", protocol.MaxDesktopClipboardImageBytes,
			)
		}
		if _, err := png.DecodeConfig(bytes.NewReader(content.PNG)); err != nil {
			return protocol.DesktopClipboardState{}, fmt.Errorf("invalid clipboard PNG: %w", err)
		}
		content.Kind = protocol.DesktopClipboardKindPNG
		content.Text = ""
		content.PNG = append([]byte(nil), content.PNG...)
		content.TransferID = ""
		content.Files = nil
		content.LocalPaths = nil
	case protocol.DesktopClipboardKindFiles:
		if len(content.Files) == 0 || len(content.Files) > maxDesktopClipboardFiles {
			return protocol.DesktopClipboardState{}, fmt.Errorf("clipboard file count must be 1..%d", maxDesktopClipboardFiles)
		}
		var total int64
		for i := range content.Files {
			name, err := safeClipboardFileName(content.Files[i].Name)
			if err != nil {
				return protocol.DesktopClipboardState{}, err
			}
			content.Files[i].Name = name
			if content.Files[i].Size < 0 || content.Files[i].Size > maxDesktopClipboardFileBytes {
				return protocol.DesktopClipboardState{}, fmt.Errorf("clipboard file %q exceeds size limit", name)
			}
			total += content.Files[i].Size
			if total > maxDesktopClipboardTransferBytes {
				return protocol.DesktopClipboardState{}, errors.New("clipboard transfer exceeds total size limit")
			}
		}
		content.Kind = protocol.DesktopClipboardKindFiles
		content.Text = ""
		content.PNG = nil
		content.Files = append([]protocol.DesktopClipboardFile(nil), content.Files...)
		content.LocalPaths = append([]string(nil), content.LocalPaths...)
	default:
		return protocol.DesktopClipboardState{}, fmt.Errorf("unsupported clipboard kind %q", content.Kind)
	}
	return content, nil
}

func cloneClipboardContent(content protocol.DesktopClipboardState) protocol.DesktopClipboardState {
	content.PNG = append([]byte(nil), content.PNG...)
	content.Files = append([]protocol.DesktopClipboardFile(nil), content.Files...)
	content.LocalPaths = append([]string(nil), content.LocalPaths...)
	return content
}

func clipboardContentEqual(a, b protocol.DesktopClipboardState) bool {
	if a.Kind != b.Kind || a.Text != b.Text || !bytes.Equal(a.PNG, b.PNG) ||
		a.TransferID != b.TransferID || len(a.Files) != len(b.Files) {
		return false
	}
	for i := range a.Files {
		if a.Files[i] != b.Files[i] {
			return false
		}
	}
	return true
}

func readClipboardContent(ctx context.Context, endpoint ClipboardEndpoint) (protocol.DesktopClipboardState, error) {
	if rich, ok := endpoint.(ClipboardContentEndpoint); ok {
		content, err := rich.ClipboardContent(ctx)
		if err != nil {
			return protocol.DesktopClipboardState{}, err
		}
		return validateClipboardContent(content)
	}
	text, err := endpoint.ClipboardText(ctx)
	if err != nil {
		return protocol.DesktopClipboardState{}, err
	}
	return validateClipboardContent(protocol.DesktopClipboardState{
		Kind: protocol.DesktopClipboardKindText,
		Text: text,
	})
}

func writeClipboardContent(ctx context.Context, endpoint ClipboardEndpoint, content protocol.DesktopClipboardState) error {
	content, err := validateClipboardContent(content)
	if err != nil {
		return err
	}
	if rich, ok := endpoint.(ClipboardContentEndpoint); ok {
		return rich.SetClipboardContent(ctx, content)
	}
	if content.Kind != protocol.DesktopClipboardKindText {
		return ErrClipboardImageUnavailable
	}
	return endpoint.SetClipboardText(ctx, content.Text)
}

func (s *clipboardSyncState) SeedContent(content protocol.DesktopClipboardState) {
	s.mu.Lock()
	s.initialized = true
	s.content = cloneClipboardContent(content)
	s.mu.Unlock()
}

func (s *clipboardSyncState) ChangedContent(content protocol.DesktopClipboardState) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.initialized && clipboardContentEqual(s.content, content) {
		return false
	}
	s.initialized = true
	s.content = cloneClipboardContent(content)
	return true
}

func (s *clipboardSyncState) IsCurrentContent(content protocol.DesktopClipboardState) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.initialized && clipboardContentEqual(s.content, content)
}

func (s *clipboardSyncState) Seed(text string) {
	content, _ := validateClipboardContent(protocol.DesktopClipboardState{
		Kind: protocol.DesktopClipboardKindText, Text: text,
	})
	s.SeedContent(content)
}

func (s *clipboardSyncState) Changed(text string) bool {
	content, _ := validateClipboardContent(protocol.DesktopClipboardState{
		Kind: protocol.DesktopClipboardKindText, Text: text,
	})
	return s.ChangedContent(content)
}

func (s *clipboardSyncState) IsCurrent(text string) bool {
	content, _ := validateClipboardContent(protocol.DesktopClipboardState{
		Kind: protocol.DesktopClipboardKindText, Text: text,
	})
	return s.IsCurrentContent(content)
}

func clipboardEnabled(options protocol.RemoteDesktopConnectOptions) bool {
	return options.Clipboard == nil || *options.Clipboard
}

func (h *Host) streamClipboard(ctx context.Context, conn *desktopmedia.MediaConn, endpoint ClipboardEndpoint, state *clipboardSyncState) error {
	if endpoint == nil || state == nil {
		return errors.New("desktop clipboard endpoint is unavailable")
	}

	seed, err := readClipboardContent(ctx, endpoint)
	if err == nil {
		state.SeedContent(seed)
	} else if !errors.Is(err, ErrClipboardTextUnavailable) &&
		!errors.Is(err, ErrClipboardImageUnavailable) && ctx.Err() == nil {
		log.Printf("[Desktop] initial clipboard read failed: %v", err)
	}

	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var sequence uint64
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			content, err := readClipboardContent(ctx, endpoint)
			if err != nil {
				if errors.Is(err, ErrClipboardTextUnavailable) || errors.Is(err, ErrClipboardImageUnavailable) {
					continue
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
				log.Printf("[Desktop] clipboard read failed: %v", err)
				continue
			}
			if !state.ChangedContent(content) {
				continue
			}
			sequence++
			content.Sequence = sequence
			if err := conn.SendSessionMessage(ctx, protocol.DesktopSessionMessage{
				Type:      protocol.DesktopSessionClipboard,
				Clipboard: &content,
			}); err != nil {
				return err
			}
		}
	}
}
