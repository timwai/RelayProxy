package desktop

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	desktopmedia "relayproxy/internal/desktop"
	"relayproxy/internal/protocol"
)

var ErrClipboardTextUnavailable = errors.New("text clipboard unavailable")

type ClipboardEndpoint interface {
	ClipboardText(context.Context) (string, error)
	SetClipboardText(context.Context, string) error
}

type clipboardSyncState struct {
	mu          sync.Mutex
	initialized bool
	text        string
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

func (s *clipboardSyncState) Seed(text string) {
	s.mu.Lock()
	s.initialized = true
	s.text = text
	s.mu.Unlock()
}

func (s *clipboardSyncState) Changed(text string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.initialized && s.text == text {
		return false
	}
	s.initialized = true
	s.text = text
	return true
}

func (s *clipboardSyncState) IsCurrent(text string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.initialized && s.text == text
}

func clipboardEnabled(options protocol.RemoteDesktopConnectOptions) bool {
	return options.Clipboard == nil || *options.Clipboard
}

func (h *Host) streamClipboard(ctx context.Context, conn *desktopmedia.MediaConn, endpoint ClipboardEndpoint, state *clipboardSyncState) error {
	if endpoint == nil || state == nil {
		return errors.New("desktop clipboard endpoint is unavailable")
	}

	seed, err := endpoint.ClipboardText(ctx)
	if err == nil {
		if seed, err = validateClipboardText(seed); err == nil {
			state.Seed(seed)
		}
	} else if !errors.Is(err, ErrClipboardTextUnavailable) && ctx.Err() == nil {
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
			text, err := endpoint.ClipboardText(ctx)
			if err != nil {
				if errors.Is(err, ErrClipboardTextUnavailable) {
					continue
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
				log.Printf("[Desktop] clipboard read failed: %v", err)
				continue
			}
			text, err = validateClipboardText(text)
			if err != nil {
				log.Printf("[Desktop] clipboard text ignored: %v", err)
				continue
			}
			if !state.Changed(text) {
				continue
			}
			sequence++
			if err := conn.SendSessionMessage(ctx, protocol.DesktopSessionMessage{
				Type: protocol.DesktopSessionClipboard,
				Clipboard: &protocol.DesktopClipboardState{
					Sequence: sequence,
					Text:     text,
				},
			}); err != nil {
				return err
			}
		}
	}
}
