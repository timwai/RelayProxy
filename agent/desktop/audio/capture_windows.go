//go:build windows

package audio

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"
	"unsafe"

	win32 "github.com/deploymenttheory/go-bindings-win32/bindings/runtime/win32"
	winaudio "github.com/deploymenttheory/go-bindings-win32/bindings/win32/media/audio"
	systemcom "github.com/deploymenttheory/go-bindings-win32/bindings/win32/system/com"
)

const (
	wasapiCaptureBufferDuration100ns = int64(1_000_000) // 100 ms
	wasapiCapturePollInterval        = 2 * time.Millisecond
)

type captureReadResult struct {
	data []byte
	err  error
}

type captureReadRequest struct {
	ctx    context.Context
	result chan captureReadResult
}

type wasapiLoopbackCapture struct {
	cfg        PCMConfig
	frameBytes int

	requests  chan captureReadRequest
	closeCh   chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

type wasapiCaptureState struct {
	enumerator    *winaudio.IMMDeviceEnumerator
	endpoint      *winaudio.IMMDevice
	client        *winaudio.IAudioClient
	captureClient *winaudio.IAudioCaptureClient
	blockAlign    int
	pending       []byte
}

var _ Capture = (*wasapiLoopbackCapture)(nil)

func OpenLoopbackCapture(ctx context.Context, cfg PCMConfig, frameDuration time.Duration) (Capture, error) {
	cfg, _, frameBytes, err := NormalizeFrameDuration(cfg, frameDuration)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	capture := &wasapiLoopbackCapture{
		cfg:        cfg,
		frameBytes: frameBytes,
		requests:   make(chan captureReadRequest),
		closeCh:    make(chan struct{}),
		done:       make(chan struct{}),
	}
	ready := make(chan error, 1)
	go capture.run(ready)

	select {
	case err := <-ready:
		if err != nil {
			return nil, err
		}
		return capture, nil
	case <-ctx.Done():
		capture.closeOnce.Do(func() { close(capture.closeCh) })
		<-capture.done
		return nil, ctx.Err()
	}
}

func (c *wasapiLoopbackCapture) Read(ctx context.Context) ([]byte, error) {
	if c == nil {
		return nil, ErrUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	req := captureReadRequest{ctx: ctx, result: make(chan captureReadResult, 1)}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.done:
		return nil, ErrClosed
	case c.requests <- req:
	}

	select {
	case result := <-req.result:
		return result.data, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.done:
		return nil, ErrClosed
	}
}

func (c *wasapiLoopbackCapture) Close() error {
	if c == nil {
		return nil
	}
	c.closeOnce.Do(func() { close(c.closeCh) })
	<-c.done
	return nil
}

func (c *wasapiLoopbackCapture) run(ready chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(c.done)

	_, err := systemcom.CoInitializeEx(uint32(systemcom.COINIT_MULTITHREADED))
	if err != nil {
		ready <- fmt.Errorf("initialize WASAPI capture COM apartment: %w", err)
		return
	}
	defer systemcom.CoUninitialize()

	state, err := openWASAPICaptureState(c.cfg)
	if err != nil {
		ready <- err
		return
	}
	defer state.close()
	ready <- nil

	for {
		select {
		case <-c.closeCh:
			return
		case req := <-c.requests:
			data, err := state.readFrame(req.ctx, c.closeCh, c.frameBytes)
			select {
			case req.result <- captureReadResult{data: data, err: err}:
			default:
			}
		}
	}
}

func loopbackStreamFlags() uint32 {
	return uint32(winaudio.AUDCLNT_STREAMFLAGS_LOOPBACK |
		winaudio.AUDCLNT_STREAMFLAGS_AUTOCONVERTPCM |
		winaudio.AUDCLNT_STREAMFLAGS_SRC_DEFAULT_QUALITY)
}

func openWASAPICaptureState(cfg PCMConfig) (*wasapiCaptureState, error) {
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
	state := &wasapiCaptureState{
		enumerator: enumerator,
		blockAlign: cfg.BlockAlign(),
	}
	fail := func(err error) (*wasapiCaptureState, error) {
		state.close()
		return nil, err
	}

	if err := enumerator.GetDefaultAudioEndpoint(winaudio.ERender, winaudio.EMultimedia, &state.endpoint); err != nil {
		return fail(fmt.Errorf("get default Windows loopback endpoint: %w", err))
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
		return fail(fmt.Errorf("activate Windows loopback audio client: %w", err))
	}
	if clientUnknown == nil {
		return fail(ErrUnavailable)
	}
	state.client = (*winaudio.IAudioClient)(unsafe.Pointer(clientUnknown))

	format := pcmWaveFormat(cfg)
	if err := state.client.Initialize(
		winaudio.AUDCLNT_SHAREMODE_SHARED,
		loopbackStreamFlags(),
		wasapiCaptureBufferDuration100ns,
		0,
		&format,
		nil,
	); err != nil {
		return fail(fmt.Errorf("initialize Windows WASAPI loopback capture: %w", err))
	}

	var captureUnknown *win32.IUnknown
	if err := state.client.GetService(&winaudio.IID_IAudioCaptureClient, &captureUnknown); err != nil {
		return fail(fmt.Errorf("open Windows audio capture service: %w", err))
	}
	if captureUnknown == nil {
		return fail(ErrUnavailable)
	}
	state.captureClient = (*winaudio.IAudioCaptureClient)(unsafe.Pointer(captureUnknown))
	if err := state.client.Start(); err != nil {
		return fail(fmt.Errorf("start Windows loopback capture: %w", err))
	}
	return state, nil
}

func (s *wasapiCaptureState) close() {
	if s == nil {
		return
	}
	if s.client != nil {
		_ = s.client.Stop()
	}
	if s.captureClient != nil {
		s.captureClient.Release()
		s.captureClient = nil
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

func (s *wasapiCaptureState) readFrame(
	ctx context.Context,
	closeCh <-chan struct{},
	frameBytes int,
) ([]byte, error) {
	if s == nil || s.captureClient == nil || s.blockAlign <= 0 || frameBytes <= 0 {
		return nil, ErrUnavailable
	}
	for len(s.pending) < frameBytes {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-closeCh:
			return nil, ErrClosed
		default:
		}

		var packetFrames uint32
		if err := s.captureClient.GetNextPacketSize(&packetFrames); err != nil {
			return nil, fmt.Errorf("query Windows loopback packet size: %w", err)
		}
		if packetFrames == 0 {
			timer := time.NewTimer(wasapiCapturePollInterval)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return nil, ctx.Err()
			case <-closeCh:
				if !timer.Stop() {
					<-timer.C
				}
				return nil, ErrClosed
			case <-timer.C:
			}
			continue
		}

		var (
			src    *byte
			frames uint32
			flags  uint32
		)
		if err := s.captureClient.GetBuffer(&src, &frames, &flags, nil, nil); err != nil {
			return nil, fmt.Errorf("acquire Windows loopback buffer: %w", err)
		}
		bytesAvailable := int(frames) * s.blockAlign
		if bytesAvailable < 0 || bytesAvailable > 16<<20 {
			_ = s.captureClient.ReleaseBuffer(frames)
			return nil, errors.New("Windows loopback packet size is invalid")
		}
		if flags&uint32(winaudio.AUDCLNT_BUFFERFLAGS_SILENT) != 0 {
			s.pending = append(s.pending, make([]byte, bytesAvailable)...)
		} else {
			if src == nil && bytesAvailable > 0 {
				_ = s.captureClient.ReleaseBuffer(frames)
				return nil, errors.New("Windows loopback capture returned a nil buffer")
			}
			if bytesAvailable > 0 {
				s.pending = append(s.pending, unsafe.Slice(src, bytesAvailable)...)
			}
		}
		if err := s.captureClient.ReleaseBuffer(frames); err != nil {
			return nil, fmt.Errorf("release Windows loopback buffer: %w", err)
		}
	}

	out := append([]byte(nil), s.pending[:frameBytes]...)
	copy(s.pending, s.pending[frameBytes:])
	clear(s.pending[len(s.pending)-frameBytes:])
	s.pending = s.pending[:len(s.pending)-frameBytes]
	return out, nil
}
