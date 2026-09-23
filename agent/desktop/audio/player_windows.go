//go:build windows

package audio

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"
	"unsafe"

	win32 "github.com/deploymenttheory/go-bindings-win32/bindings/win32"
	winaudio "github.com/deploymenttheory/go-bindings-win32/bindings/win32/media/audio"
	systemcom "github.com/deploymenttheory/go-bindings-win32/bindings/win32/system/com"
)

const (
	wasapiRenderBufferDuration100ns = int64(400_000) // 40 ms
	wasapiCapacityPollInterval      = 2 * time.Millisecond
)

var clsidMMDeviceEnumerator = win32.GUID{
	Data1: 0xbcde0395,
	Data2: 0xe52f,
	Data3: 0x467c,
	Data4: [8]byte{0x8e, 0x3d, 0xc4, 0x57, 0x92, 0x91, 0x69, 0x2e},
}

type playerWriteRequest struct {
	ctx    context.Context
	data   []byte
	result chan error
}

type wasapiPlayer struct {
	cfg PCMConfig

	requests  chan playerWriteRequest
	closeCh   chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

type wasapiRenderState struct {
	enumerator   *winaudio.IMMDeviceEnumerator
	endpoint     *winaudio.IMMDevice
	client       *winaudio.IAudioClient
	renderClient *winaudio.IAudioRenderClient
	bufferFrames uint32
	blockAlign   int
}

var _ Player = (*wasapiPlayer)(nil)

func OpenPCMPlayer(ctx context.Context, cfg PCMConfig) (Player, error) {
	cfg, err := NormalizePCMConfig(cfg)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	player := &wasapiPlayer{
		cfg:      cfg,
		requests: make(chan playerWriteRequest),
		closeCh:  make(chan struct{}),
		done:     make(chan struct{}),
	}
	ready := make(chan error, 1)
	go player.run(ready)

	select {
	case err := <-ready:
		if err != nil {
			return nil, err
		}
		return player, nil
	case <-ctx.Done():
		player.closeOnce.Do(func() { close(player.closeCh) })
		<-player.done
		return nil, ctx.Err()
	}
}

func (p *wasapiPlayer) Write(ctx context.Context, data []byte) error {
	if p == nil {
		return ErrUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if len(data) == 0 {
		return nil
	}
	if err := p.cfg.ValidatePayload(data); err != nil {
		return err
	}

	// The worker owns COM/WASAPI on another OS thread. Copy across that
	// boundary so callers may immediately reuse their receive buffer.
	payload := append([]byte(nil), data...)
	req := playerWriteRequest{
		ctx:    ctx,
		data:   payload,
		result: make(chan error, 1),
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		return ErrClosed
	case p.requests <- req:
	}

	select {
	case err := <-req.result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		return ErrClosed
	}
}

func (p *wasapiPlayer) Close() error {
	if p == nil {
		return nil
	}
	p.closeOnce.Do(func() { close(p.closeCh) })
	<-p.done
	return nil
}

func (p *wasapiPlayer) run(ready chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(p.done)

	_, err := systemcom.CoInitializeEx(uint32(systemcom.COINIT_MULTITHREADED))
	if err != nil {
		ready <- fmt.Errorf("initialize WASAPI COM apartment: %w", err)
		return
	}
	defer systemcom.CoUninitialize()

	state, err := openWASAPIRenderState(p.cfg)
	if err != nil {
		ready <- err
		return
	}
	defer state.close()
	ready <- nil

	for {
		select {
		case <-p.closeCh:
			return
		case req := <-p.requests:
			err := state.write(req.ctx, p.closeCh, req.data)
			select {
			case req.result <- err:
			default:
			}
		}
	}
}

func openWASAPIRenderState(cfg PCMConfig) (*wasapiRenderState, error) {
	var enumeratorUnknown *win32.IUnknown
	if err := systemcom.CoCreateInstance(
		&clsidMMDeviceEnumerator,
		nil,
		systemcom.CLSCTX_ALL,
		&winaudio.IID_IMMDeviceEnumerator,
		&enumeratorUnknown,
	); err != nil {
		return nil, fmt.Errorf("create Windows audio device enumerator: %w", err)
	}
	if enumeratorUnknown == nil {
		return nil, ErrUnavailable
	}
	enumerator := (*winaudio.IMMDeviceEnumerator)(unsafe.Pointer(enumeratorUnknown))

	state := &wasapiRenderState{
		enumerator: enumerator,
		blockAlign: cfg.BlockAlign(),
	}
	fail := func(err error) (*wasapiRenderState, error) {
		state.close()
		return nil, err
	}

	if err := enumerator.GetDefaultAudioEndpoint(
		winaudio.ERender,
		winaudio.EMultimedia,
		&state.endpoint,
	); err != nil {
		return fail(fmt.Errorf("get default Windows render endpoint: %w", err))
	}
	if state.endpoint == nil {
		return fail(ErrUnavailable)
	}

	var clientUnknown *win32.IUnknown
	if err := state.endpoint.Activate(
		&winaudio.IID_IAudioClient,
		systemcom.CLSCTX_ALL,
		nil,
		&clientUnknown,
	); err != nil {
		return fail(fmt.Errorf("activate Windows audio client: %w", err))
	}
	if clientUnknown == nil {
		return fail(ErrUnavailable)
	}
	state.client = (*winaudio.IAudioClient)(unsafe.Pointer(clientUnknown))

	format := pcmWaveFormat(cfg)
	flags := uint32(winaudio.AUDCLNT_STREAMFLAGS_AUTOCONVERTPCM |
		winaudio.AUDCLNT_STREAMFLAGS_SRC_DEFAULT_QUALITY)
	if err := state.client.Initialize(
		winaudio.AUDCLNT_SHAREMODE_SHARED,
		flags,
		wasapiRenderBufferDuration100ns,
		0,
		&format,
		nil,
	); err != nil {
		return fail(fmt.Errorf("initialize Windows shared audio renderer: %w", err))
	}
	if err := state.client.GetBufferSize(&state.bufferFrames); err != nil {
		return fail(fmt.Errorf("get Windows audio buffer size: %w", err))
	}
	if state.bufferFrames == 0 {
		return fail(errors.New("Windows audio renderer returned an empty buffer"))
	}

	var renderUnknown *win32.IUnknown
	if err := state.client.GetService(
		&winaudio.IID_IAudioRenderClient,
		&renderUnknown,
	); err != nil {
		return fail(fmt.Errorf("open Windows audio render service: %w", err))
	}
	if renderUnknown == nil {
		return fail(ErrUnavailable)
	}
	state.renderClient = (*winaudio.IAudioRenderClient)(unsafe.Pointer(renderUnknown))

	if err := state.client.Start(); err != nil {
		return fail(fmt.Errorf("start Windows audio renderer: %w", err))
	}
	return state, nil
}

func (s *wasapiRenderState) close() {
	if s == nil {
		return
	}
	if s.client != nil {
		_ = s.client.Stop()
	}
	if s.renderClient != nil {
		s.renderClient.Release()
		s.renderClient = nil
	}
	if s.client != nil {
		s.client.Release()
		s.client = nil
	}
	if s.endpoint != nil {
		s.endpoint.Release()
		s.endpoint = nil
	}
	if s.enumerator != nil {
		s.enumerator.Release()
		s.enumerator = nil
	}
}

func (s *wasapiRenderState) write(
	ctx context.Context,
	closeCh <-chan struct{},
	data []byte,
) error {
	if s == nil || s.client == nil || s.renderClient == nil || s.blockAlign <= 0 {
		return ErrUnavailable
	}
	if len(data)%s.blockAlign != 0 {
		return errors.New("unaligned PCM payload")
	}
	offset := 0
	for offset < len(data) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-closeCh:
			return ErrClosed
		default:
		}

		var padding uint32
		if err := s.client.GetCurrentPadding(&padding); err != nil {
			return fmt.Errorf("query Windows audio padding: %w", err)
		}
		if padding > s.bufferFrames {
			return fmt.Errorf("Windows audio padding %d exceeds buffer %d", padding, s.bufferFrames)
		}
		available := s.bufferFrames - padding
		if available == 0 {
			timer := time.NewTimer(wasapiCapacityPollInterval)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return ctx.Err()
			case <-closeCh:
				if !timer.Stop() {
					<-timer.C
				}
				return ErrClosed
			case <-timer.C:
			}
			continue
		}

		remainingFrames := uint32((len(data) - offset) / s.blockAlign)
		frames := available
		if remainingFrames < frames {
			frames = remainingFrames
		}
		if frames == 0 {
			return errors.New("Windows audio writer made no progress")
		}
		bytesToWrite := int(frames) * s.blockAlign

		var dst *byte
		if err := s.renderClient.GetBuffer(frames, &dst); err != nil {
			return fmt.Errorf("acquire Windows audio render buffer: %w", err)
		}
		if dst == nil {
			_ = s.renderClient.ReleaseBuffer(frames, uint32(winaudio.AUDCLNT_BUFFERFLAGS_SILENT))
			return errors.New("Windows audio renderer returned a nil buffer")
		}
		copy(unsafe.Slice(dst, bytesToWrite), data[offset:offset+bytesToWrite])
		if err := s.renderClient.ReleaseBuffer(frames, 0); err != nil {
			return fmt.Errorf("release Windows audio render buffer: %w", err)
		}
		offset += bytesToWrite
	}
	return nil
}

func pcmWaveFormat(cfg PCMConfig) winaudio.WAVEFORMATEX {
	var format winaudio.WAVEFORMATEX
	blockAlign := cfg.BlockAlign()
	binary.LittleEndian.PutUint16(format.Data[0:2], uint16(winaudio.WAVE_FORMAT_PCM))
	binary.LittleEndian.PutUint16(format.Data[2:4], uint16(cfg.Channels))
	binary.LittleEndian.PutUint32(format.Data[4:8], uint32(cfg.SampleRate))
	binary.LittleEndian.PutUint32(format.Data[8:12], uint32(cfg.SampleRate*blockAlign))
	binary.LittleEndian.PutUint16(format.Data[12:14], uint16(blockAlign))
	binary.LittleEndian.PutUint16(format.Data[14:16], uint16(cfg.BitsPerSample))
	binary.LittleEndian.PutUint16(format.Data[16:18], 0)
	return format
}
