package desktop

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"image"
	"image/jpeg"
	"sync"
	"time"

	desktopmedia "relayproxy/internal/desktop"
)

type CaptureSource interface {
	Capture(context.Context) (*image.RGBA, error)
	Close() error
}

type HostConfig struct {
	MaxFPS      int
	MaxWidth    int
	MaxHeight   int
	JPEGQuality int
	PacketSize  int
}

func DefaultHostConfig() HostConfig {
	return HostConfig{
		MaxFPS:      10,
		MaxWidth:    1280,
		MaxHeight:   720,
		JPEGQuality: 68,
		PacketSize:  1150,
	}
}

type Host struct {
	source CaptureSource
	cfg    HostConfig

	sessionMu sync.Mutex
	closeOnce sync.Once
}

func NewHost(source CaptureSource, cfg HostConfig) (*Host, error) {
	if source == nil {
		return nil, errors.New("desktop capture source is required")
	}
	defaults := DefaultHostConfig()
	if cfg.MaxFPS <= 0 {
		cfg.MaxFPS = defaults.MaxFPS
	}
	if cfg.MaxFPS > 30 {
		cfg.MaxFPS = 30
	}
	if cfg.MaxWidth <= 0 {
		cfg.MaxWidth = defaults.MaxWidth
	}
	if cfg.MaxHeight <= 0 {
		cfg.MaxHeight = defaults.MaxHeight
	}
	if cfg.JPEGQuality <= 0 {
		cfg.JPEGQuality = defaults.JPEGQuality
	}
	if cfg.JPEGQuality < 25 {
		cfg.JPEGQuality = 25
	}
	if cfg.JPEGQuality > 95 {
		cfg.JPEGQuality = 95
	}
	if cfg.PacketSize <= desktopmedia.MediaHeaderSize {
		cfg.PacketSize = defaults.PacketSize
	}
	return &Host{source: source, cfg: cfg}, nil
}

func newMediaSessionID() (uint64, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return 0, err
	}
	id := binary.BigEndian.Uint64(raw[:])
	if id == 0 {
		id = 1
	}
	return id, nil
}

// HandleDesktopMedia owns one host-side media session. The MVP intentionally
// uses independent JPEG frames; later DXGI + H.264 replaces capture/encoding
// behind this method without changing the RD/1 transport or session API.
func (h *Host) HandleDesktopMedia(ctx context.Context, conn *desktopmedia.MediaConn) error {
	if h == nil || conn == nil {
		return errors.New("desktop media connection is unavailable")
	}
	h.sessionMu.Lock()
	defer h.sessionMu.Unlock()

	sessionID, err := newMediaSessionID()
	if err != nil {
		return err
	}
	var frameID uint32 = 1
	var sequence uint32 = 1
	frameInterval := time.Second / time.Duration(h.cfg.MaxFPS)
	ticker := time.NewTicker(frameInterval)
	defer ticker.Stop()

	sendFrame := func() error {
		encoded, err := h.captureJPEG(ctx)
		if err != nil {
			return err
		}
		frame := desktopmedia.EncodedFrame{
			SessionID:  sessionID,
			StreamID:   1,
			Generation: 1,
			FrameID:    frameID,
			Timestamp:  uint64(time.Now().UnixMicro()),
			KeyFrame:   true,
			Data:       encoded,
		}
		packets, next, err := desktopmedia.PacketizeFrame(frame, h.cfg.PacketSize, sequence)
		if err != nil {
			return err
		}
		for _, packet := range packets {
			if err := conn.Send(ctx, packet); err != nil {
				return err
			}
		}
		frameID++
		sequence = next
		return nil
	}

	if err := sendFrame(); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := sendFrame(); err != nil {
				return err
			}
		}
	}
}

func (h *Host) captureJPEG(ctx context.Context) ([]byte, error) {
	frame, err := h.source.Capture(ctx)
	if err != nil {
		return nil, err
	}
	frame = fitRGBA(frame, h.cfg.MaxWidth, h.cfg.MaxHeight)
	var out bytes.Buffer
	if err := jpeg.Encode(&out, frame, &jpeg.Options{Quality: h.cfg.JPEGQuality}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func fitRGBA(src *image.RGBA, maxWidth, maxHeight int) *image.RGBA {
	if src == nil {
		return nil
	}
	bounds := src.Bounds()
	sw, sh := bounds.Dx(), bounds.Dy()
	if sw <= 0 || sh <= 0 || maxWidth <= 0 || maxHeight <= 0 || (sw <= maxWidth && sh <= maxHeight) {
		return src
	}
	dw, dh := maxWidth, sh*maxWidth/sw
	if dh > maxHeight {
		dh = maxHeight
		dw = sw * maxHeight / sh
	}
	if dw < 1 {
		dw = 1
	}
	if dh < 1 {
		dh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < dh; y++ {
		sy := bounds.Min.Y + y*sh/dh
		for x := 0; x < dw; x++ {
			sx := bounds.Min.X + x*sw/dw
			si := src.PixOffset(sx, sy)
			di := dst.PixOffset(x, y)
			copy(dst.Pix[di:di+4], src.Pix[si:si+4])
		}
	}
	return dst
}

func (h *Host) Close() error {
	if h == nil {
		return nil
	}
	var err error
	h.closeOnce.Do(func() {
		if h.source != nil {
			err = h.source.Close()
		}
	})
	return err
}
