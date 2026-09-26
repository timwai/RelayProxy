//go:build windows && amd64

package codec

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

const nvdecH265D3D11Backend = "nvdec-hevc444-d3d11-zero-copy"

type nvdecD3D11OutputOwner struct {
	mu sync.Mutex

	surface *nvdecD3D11AYUVInteropSurface
	refs    int
}

func newNVDECD3D11OutputOwner(surface *nvdecD3D11AYUVInteropSurface) *nvdecD3D11OutputOwner {
	if surface == nil {
		return nil
	}
	return &nvdecD3D11OutputOwner{
		surface: surface,
		refs:    1,
	}
}

func (o *nvdecD3D11OutputOwner) retain() error {
	if o == nil {
		return ErrDecoderUnavailable
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.surface == nil || o.refs <= 0 {
		return ErrDecoderUnavailable
	}
	o.refs++
	return nil
}

func (o *nvdecD3D11OutputOwner) release() {
	if o == nil {
		return
	}
	o.mu.Lock()
	if o.surface == nil || o.refs <= 0 {
		o.mu.Unlock()
		return
	}
	o.refs--
	if o.refs > 0 {
		o.mu.Unlock()
		return
	}
	surface := o.surface
	o.surface = nil
	o.mu.Unlock()
	_ = surface.Close()
}

type nvdecH265Decoder struct {
	mu sync.Mutex

	cfg    VideoConfig
	device uintptr

	parser *nvdecHEVC444Parser
	packer *nvdecAYUVPacker
	closed bool
}

func nvdecParserSession(parser *nvdecHEVC444Parser) (*nvdecD3D11Session, error) {
	if parser == nil {
		return nil, ErrDecoderUnavailable
	}
	parser.mu.Lock()
	defer parser.mu.Unlock()
	if parser.closed || parser.session == nil {
		return nil, ErrDecoderUnavailable
	}
	return parser.session, nil
}

func openNVDECH265DecoderComponents(
	ctx context.Context,
	cfg VideoConfig,
	device uintptr,
) (*nvdecHEVC444Parser, *nvdecAYUVPacker, error) {
	parser, err := openNVDECHEVC444Parser(ctx, cfg, device)
	if err != nil {
		return nil, nil, err
	}
	session, err := nvdecParserSession(parser)
	if err != nil {
		_ = parser.Close()
		return nil, nil, err
	}
	packer, err := newNVDECAYUVPacker(session)
	if err != nil {
		_ = parser.Close()
		return nil, nil, err
	}
	return parser, packer, nil
}

func OpenNVDECH265DecoderWithD3D11(
	ctx context.Context,
	cfg VideoConfig,
	device uintptr,
) (Decoder, error) {
	if device == 0 {
		return nil, fmt.Errorf("%w: NVDEC D3D11 device is nil", ErrDecoderUnavailable)
	}
	normalized, err := NormalizeVideoConfig(cfg)
	if err != nil {
		return nil, err
	}
	if normalized.Chroma != Chroma444 || normalized.BitDepth != 8 {
		return nil, fmt.Errorf("%w: NVDEC HEVC requires 8-bit 4:4:4 video", ErrInvalidVideoConfig)
	}
	parser, packer, err := openNVDECH265DecoderComponents(ctx, normalized, device)
	if err != nil {
		return nil, err
	}
	return &nvdecH265Decoder{
		cfg:    normalized,
		device: device,
		parser: parser,
		packer: packer,
	}, nil
}

func closeNVDECDecodedFrames(frames []DecodedFrame) {
	for i := range frames {
		frames[i].Close()
	}
}

func (d *nvdecH265Decoder) drainDisplayLocked(
	ctx context.Context,
) ([]DecodedFrame, error) {
	if d.parser == nil || d.packer == nil {
		return nil, ErrDecoderUnavailable
	}
	session, err := nvdecParserSession(d.parser)
	if err != nil {
		return nil, err
	}

	frames := make([]DecodedFrame, 0, d.parser.PendingDisplayFrames())
	fail := func(err error) ([]DecodedFrame, error) {
		closeNVDECDecodedFrames(frames)
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		mapped, ok, err := d.parser.MapNextDisplay(ctx)
		if err != nil {
			return fail(err)
		}
		if !ok {
			return frames, nil
		}
		timestamp := mapped.Timestamp()

		surface, err := createNVDECD3D11AYUVInteropSurface(
			session,
			d.cfg.Width,
			d.cfg.Height,
		)
		if err != nil {
			_ = mapped.Close()
			return fail(err)
		}

		packErr := surface.Pack(mapped, d.packer)
		unmapErr := mapped.Close()
		if packErr != nil || unmapErr != nil {
			_ = surface.Close()
			return fail(errors.Join(packErr, unmapErr))
		}
		if err := surface.FinalizeForD3D11(); err != nil {
			_ = surface.Close()
			return fail(err)
		}
		resource := surface.Texture()
		if resource == 0 {
			_ = surface.Close()
			return fail(fmt.Errorf("%w: NVDEC packed AYUV texture is nil", ErrDecoderUnavailable))
		}

		owner := newNVDECD3D11OutputOwner(surface)
		frames = append(frames, DecodedFrame{
			Format: PixelFormatAYUV,
			D3D11: &D3D11Surface{
				Device:      d.device,
				Resource:    resource,
				Subresource: 0,
				Format:      PixelFormatAYUV,
				release:     owner.release,
				gpuRetain:   owner.retain,
				gpuRelease:  owner.release,
			},
			Width:     d.cfg.Width,
			Height:    d.cfg.Height,
			Timestamp: timestamp,
			Hardware:  true,
		})
	}
}

func (d *nvdecH265Decoder) Decode(
	ctx context.Context,
	data []byte,
	timestamp time.Duration,
) ([]DecodedFrame, error) {
	if d == nil {
		return nil, ErrDecoderUnavailable
	}
	if len(data) == 0 {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || d.parser == nil || d.packer == nil {
		return nil, ErrDecoderUnavailable
	}
	if err := d.parser.Parse(ctx, data, timestamp); err != nil {
		return nil, err
	}
	return d.drainDisplayLocked(ctx)
}

func (d *nvdecH265Decoder) Flush(ctx context.Context) error {
	if d == nil {
		return ErrDecoderUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || d.parser == nil || d.packer == nil {
		return ErrDecoderUnavailable
	}

	oldParser := d.parser
	oldPacker := d.packer
	d.parser = nil
	d.packer = nil

	eosErr := oldParser.EndOfStream(ctx)
	packerCloseErr := oldPacker.Close()
	parserCloseErr := oldParser.Close()

	if err := ctx.Err(); err != nil {
		return errors.Join(eosErr, packerCloseErr, parserCloseErr, err)
	}
	parser, packer, openErr := openNVDECH265DecoderComponents(ctx, d.cfg, d.device)
	if openErr == nil {
		d.parser = parser
		d.packer = packer
	}
	return errors.Join(eosErr, packerCloseErr, parserCloseErr, openErr)
}

func (d *nvdecH265Decoder) Hardware() bool {
	return d != nil
}

func (d *nvdecH265Decoder) Backend() string {
	if d == nil {
		return ""
	}
	return nvdecH265D3D11Backend
}

func (d *nvdecH265Decoder) Close() error {
	if d == nil {
		return nil
	}

	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	d.closed = true
	parser := d.parser
	packer := d.packer
	d.parser = nil
	d.packer = nil
	d.mu.Unlock()

	var closeErr error
	if packer != nil {
		closeErr = errors.Join(closeErr, packer.Close())
	}
	if parser != nil {
		closeErr = errors.Join(closeErr, parser.Close())
	}
	return closeErr
}
