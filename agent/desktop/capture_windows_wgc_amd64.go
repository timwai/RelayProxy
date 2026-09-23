//go:build windows && amd64

package desktop

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"time"
	"unsafe"

	"github.com/go-mswin/screencapture"

	win32 "github.com/deploymenttheory/go-bindings-win32/bindings/runtime/win32"
	graphicsdirect3d "github.com/deploymenttheory/go-bindings-win32/bindings/win32/graphics/direct3d"
	graphicsdirect3d11 "github.com/deploymenttheory/go-bindings-win32/bindings/win32/graphics/direct3d11"
	graphicsdxgi "github.com/deploymenttheory/go-bindings-win32/bindings/win32/graphics/dxgi"
	graphicsdxgicommon "github.com/deploymenttheory/go-bindings-win32/bindings/win32/graphics/dxgi/common"
	graphicsgdi "github.com/deploymenttheory/go-bindings-win32/bindings/win32/graphics/gdi"
	systemwinrt "github.com/deploymenttheory/go-bindings-win32/bindings/win32/system/winrt"
	win32direct3d11 "github.com/deploymenttheory/go-bindings-win32/bindings/win32/system/winrt/direct3d11"
	win32capture "github.com/deploymenttheory/go-bindings-win32/bindings/win32/system/winrt/graphics/capture"

	winrtruntime "github.com/deploymenttheory/go-bindings-winrt/bindings/runtime/winrt"
	winrtfoundation "github.com/deploymenttheory/go-bindings-winrt/bindings/winrt/foundation"
	winrtgraphics "github.com/deploymenttheory/go-bindings-winrt/bindings/winrt/graphics"
	winrtcapture "github.com/deploymenttheory/go-bindings-winrt/bindings/winrt/graphics/capture"
	winrtdirectx "github.com/deploymenttheory/go-bindings-winrt/bindings/winrt/graphics/directx"
	winrtdirect3d11 "github.com/deploymenttheory/go-bindings-winrt/bindings/winrt/graphics/directx/direct3d11"

	"relayproxy/internal/protocol"
)

const wgcFramePoolBuffers int32 = 2

type wgcFrameStream struct {
	device      *graphicsdirect3d11.ID3D11Device
	context     *graphicsdirect3d11.ID3D11DeviceContext
	winrtDevice *winrtdirect3d11.IDirect3DDevice
	item        *winrtcapture.IGraphicsCaptureItem
	pool        *winrtcapture.IDirect3D11CaptureFramePool
	session     *winrtcapture.IGraphicsCaptureSession

	frameReady             chan struct{}
	closedReady            chan struct{}
	frameArrivedHandler    *winrtcapture.TypedEventHandlerOfDirect3D11CaptureFramePoolAndObject
	frameArrivedToken      systemwinrt.EventRegistrationToken
	frameArrivedRegistered bool

	staging       *graphicsdirect3d11.ID3D11Texture2D
	stagingSource graphicsdirect3d11.D3D11_TEXTURE2D_DESC
	poolSize      winrtgraphics.SizeInt32

	pix      []byte
	width    int
	height   int
	stride   int
	sequence uint64
	at       time.Time
	lastErr  error
	closed   bool
}

func windowsWGCAvailable() bool {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := winrtruntime.Initialize(); err != nil {
		return false
	}
	statics, err := winrtcapture.GraphicsCaptureSessionStatics()
	if err != nil {
		return false
	}
	defer statics.Release()
	supported, err := statics.IsSupported()
	return err == nil && supported
}

func openWGCFrameStream(
	ctx context.Context,
	display screencapture.Display,
	maxFPS int,
) (windowsFrameStream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if display.ID == 0 {
		return nil, errors.New("WGC capture requires a concrete Windows display")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := winrtruntime.Initialize(); err != nil {
		return nil, fmt.Errorf("%w: initialize WinRT: %v", errWindowsGraphicsCaptureUnavailable, err)
	}

	stream := &wgcFrameStream{
		frameReady:  make(chan struct{}, 1),
		closedReady: make(chan struct{}),
	}
	if err := stream.initialize(display, maxFPS); err != nil {
		_ = stream.closeLocked()
		return nil, err
	}
	return stream, nil
}

type wgcUnknown interface {
	QueryInterface(*win32.GUID, **win32.IUnknown) error
}

func wgcQueryInterface[T any](obj wgcUnknown, iid *win32.GUID) (*T, error) {
	var out *win32.IUnknown
	if err := obj.QueryInterface(iid, &out); err != nil {
		return nil, err
	}
	if out == nil {
		return nil, errors.New("COM QueryInterface returned nil")
	}
	return (*T)(unsafe.Pointer(out)), nil
}

func wgcCast[T any](obj *win32.IUnknown) *T {
	return (*T)(unsafe.Pointer(obj))
}

func (s *wgcFrameStream) initialize(display screencapture.Display, maxFPS int) error {
	var selectedLevel graphicsdirect3d.D3D_FEATURE_LEVEL
	featureLevels := []graphicsdirect3d.D3D_FEATURE_LEVEL{
		graphicsdirect3d.D3D_FEATURE_LEVEL_11_1,
		graphicsdirect3d.D3D_FEATURE_LEVEL_11_0,
		graphicsdirect3d.D3D_FEATURE_LEVEL_10_1,
		graphicsdirect3d.D3D_FEATURE_LEVEL_10_0,
	}
	flags := []graphicsdirect3d11.D3D11_CREATE_DEVICE_FLAG{
		graphicsdirect3d11.D3D11_CREATE_DEVICE_BGRA_SUPPORT |
			graphicsdirect3d11.D3D11_CREATE_DEVICE_VIDEO_SUPPORT,
		graphicsdirect3d11.D3D11_CREATE_DEVICE_BGRA_SUPPORT,
	}
	var createErr error
	for _, flag := range flags {
		createErr = graphicsdirect3d11.D3D11CreateDevice(
			nil,
			graphicsdirect3d.D3D_DRIVER_TYPE_HARDWARE,
			0,
			flag,
			featureLevels,
			graphicsdirect3d11.D3D11_SDK_VERSION,
			&s.device,
			&selectedLevel,
			&s.context,
		)
		if createErr == nil && s.device != nil && s.context != nil {
			break
		}
		if s.context != nil {
			s.context.Release()
			s.context = nil
		}
		if s.device != nil {
			s.device.Release()
			s.device = nil
		}
	}
	if createErr != nil {
		return fmt.Errorf("%w: create D3D11 device: %v", errWindowsGraphicsCaptureUnavailable, createErr)
	}
	if s.device == nil || s.context == nil {
		return fmt.Errorf("%w: D3D11 returned a nil device/context", errWindowsGraphicsCaptureUnavailable)
	}

	dxgiDevice, err := wgcQueryInterface[graphicsdxgi.IDXGIDevice](s.device, &graphicsdxgi.IID_IDXGIDevice)
	if err != nil {
		return fmt.Errorf("WGC query IDXGIDevice: %w", err)
	}
	defer dxgiDevice.Release()

	var inspectable *systemwinrt.IInspectable
	if err := win32direct3d11.CreateDirect3D11DeviceFromDXGIDevice(dxgiDevice, &inspectable); err != nil {
		return fmt.Errorf("WGC wrap D3D11 device for WinRT: %w", err)
	}
	if inspectable == nil {
		return errors.New("WGC WinRT D3D11 device is nil")
	}
	s.winrtDevice, err = wgcQueryInterface[winrtdirect3d11.IDirect3DDevice](
		inspectable,
		&winrtdirect3d11.IID_IDirect3DDevice,
	)
	inspectable.Release()
	if err != nil {
		return fmt.Errorf("WGC query IDirect3DDevice: %w", err)
	}

	factoryUnknown, err := winrtruntime.GetActivationFactory(
		"Windows.Graphics.Capture.GraphicsCaptureItem",
		&win32capture.IID_IGraphicsCaptureItemInterop,
	)
	if err != nil {
		return fmt.Errorf("%w: get GraphicsCaptureItem interop: %v", errWindowsGraphicsCaptureUnavailable, err)
	}
	interop := wgcCast[win32capture.IGraphicsCaptureItemInterop](factoryUnknown)
	defer interop.Release()

	var itemUnknown *win32.IUnknown
	if err := interop.CreateForMonitor(
		graphicsgdi.HMONITOR(display.ID),
		&winrtcapture.IID_IGraphicsCaptureItem,
		&itemUnknown,
	); err != nil {
		return fmt.Errorf("%w: create capture item for monitor %#x: %v",
			errWindowsGraphicsCaptureUnavailable, display.ID, err)
	}
	if itemUnknown == nil {
		return errors.New("WGC monitor capture item is nil")
	}
	s.item = wgcCast[winrtcapture.IGraphicsCaptureItem](itemUnknown)

	size, err := s.item.Size()
	if err != nil {
		return fmt.Errorf("WGC read capture item size: %w", err)
	}
	if size.Width <= 0 || size.Height <= 0 {
		return fmt.Errorf("WGC capture item has invalid size %dx%d", size.Width, size.Height)
	}
	s.poolSize = size

	statics, err := winrtcapture.Direct3D11CaptureFramePoolStatics2()
	if err != nil {
		return fmt.Errorf("%w: get free-threaded frame pool factory: %v",
			errWindowsGraphicsCaptureUnavailable, err)
	}
	defer statics.Release()
	s.pool, err = statics.CreateFreeThreaded(
		s.winrtDevice,
		winrtdirectx.DirectXPixelFormatB8G8R8A8UIntNormalized,
		wgcFramePoolBuffers,
		size,
	)
	if err != nil {
		return fmt.Errorf("%w: create free-threaded WGC frame pool: %v",
			errWindowsGraphicsCaptureUnavailable, err)
	}
	s.session, err = s.pool.CreateCaptureSession(s.item)
	if err != nil {
		return fmt.Errorf("WGC create capture session: %w", err)
	}
	if err := s.registerFrameArrived(); err != nil {
		return err
	}

	session2, err := wgcQueryInterface[winrtcapture.IGraphicsCaptureSession2](
		s.session,
		&winrtcapture.IID_IGraphicsCaptureSession2,
	)
	if err != nil {
		return fmt.Errorf("%w: cursor-exclusion API unavailable: %v",
			errWindowsGraphicsCaptureUnavailable, err)
	}
	if err := session2.SetIsCursorCaptureEnabled(false); err != nil {
		session2.Release()
		return fmt.Errorf("WGC disable captured cursor: %w", err)
	}
	session2.Release()

	s.configureFrameRateLimit(maxFPS)

	if err := s.session.StartCapture(); err != nil {
		return fmt.Errorf("WGC start capture: %w", err)
	}
	return nil
}

func signalWGCFrameReady(ready chan struct{}) {
	if ready == nil {
		return
	}
	select {
	case ready <- struct{}{}:
	default:
	}
}

func (s *wgcFrameStream) registerFrameArrived() error {
	if s == nil || s.pool == nil {
		return screencapture.ErrBackendUnavailable
	}
	handler, err := winrtcapture.NewTypedEventHandlerOfDirect3D11CaptureFramePoolAndObject(
		func(_ *winrtcapture.IDirect3D11CaptureFramePool, _ *systemwinrt.IInspectable) {
			signalWGCFrameReady(s.frameReady)
		},
	)
	if err != nil {
		return fmt.Errorf("WGC create FrameArrived handler: %w", err)
	}
	token, err := s.pool.AddFrameArrived(handler)
	if err != nil {
		handler.Close()
		return fmt.Errorf("WGC register FrameArrived handler: %w", err)
	}
	s.frameArrivedHandler = handler
	s.frameArrivedToken = token
	s.frameArrivedRegistered = true
	return nil
}

func wgcMinUpdateInterval(maxFPS int) winrtfoundation.TimeSpan {
	if maxFPS <= 0 {
		return winrtfoundation.TimeSpan{}
	}
	interval := time.Second / time.Duration(maxFPS)
	ticks := interval / (100 * time.Nanosecond)
	if ticks < 1 {
		ticks = 1
	}
	return winrtfoundation.TimeSpan{Duration: int64(ticks)}
}

func (s *wgcFrameStream) configureFrameRateLimit(maxFPS int) {
	interval := wgcMinUpdateInterval(maxFPS)
	if s == nil || s.session == nil || interval.Duration <= 0 {
		return
	}
	session5, err := wgcQueryInterface[winrtcapture.IGraphicsCaptureSession5](
		s.session,
		&winrtcapture.IID_IGraphicsCaptureSession5,
	)
	if err != nil {
		return
	}
	defer session5.Release()
	// Session5 is optional on older Windows builds. Treat the frame-rate cap
	// as an efficiency hint: WGC remains usable even when this property is
	// unavailable or rejected by the runtime.
	_ = session5.SetMinUpdateInterval(interval)
}

func (s *wgcFrameStream) SetFrameRateLimit(maxFPS int) error {
	if maxFPS <= 0 {
		return fmt.Errorf("invalid WGC frame rate %d", maxFPS)
	}
	if s == nil {
		return screencapture.ErrBackendUnavailable
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := winrtruntime.Initialize(); err != nil {
		return err
	}
	if s.closed || s.session == nil {
		return screencapture.ErrBackendUnavailable
	}
	// Keep the same compatibility semantics as session initialization:
	// IGraphicsCaptureSession5 is optional and SetMinUpdateInterval is only an
	// efficiency hint. Unsupported/rejected updates must not tear down capture.
	s.configureFrameRateLimit(maxFPS)
	return nil
}

func (s *wgcFrameStream) Backend() protocol.DesktopCaptureBackend {
	return protocol.DesktopCaptureWGC
}

func (s *wgcFrameStream) refreshFrame() (windowsCaptureFrame, bool, error) {
	if s == nil || s.closed {
		return windowsCaptureFrame{}, false, screencapture.ErrBackendUnavailable
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := winrtruntime.Initialize(); err != nil {
		s.fail(err)
		return windowsCaptureFrame{}, false, err
	}
	fresh, err := s.refreshLocked()
	if err != nil {
		s.fail(err)
		return windowsCaptureFrame{}, false, err
	}
	return s.snapshot(), fresh, nil
}

func (s *wgcFrameStream) Frame() (windowsCaptureFrame, bool) {
	frame, fresh, err := s.refreshFrame()
	if err != nil {
		return windowsCaptureFrame{}, false
	}
	return frame, fresh
}

func (s *wgcFrameStream) WaitFrame(ctx context.Context) (windowsCaptureFrame, error) {
	if s == nil || s.closed {
		return windowsCaptureFrame{}, screencapture.ErrBackendUnavailable
	}
	for {
		if err := ctx.Err(); err != nil {
			return windowsCaptureFrame{}, err
		}
		frame, fresh, err := s.refreshFrame()
		if err != nil {
			return windowsCaptureFrame{}, err
		}
		if fresh {
			return frame, nil
		}
		if s.lastErr != nil {
			return windowsCaptureFrame{}, s.lastErr
		}
		select {
		case <-ctx.Done():
			return windowsCaptureFrame{}, ctx.Err()
		case <-s.closedReady:
			return windowsCaptureFrame{}, screencapture.ErrBackendUnavailable
		case <-s.frameReady:
		}
	}
}

func (s *wgcFrameStream) refreshD3D11Frame() (*D3D11CaptureFrame, bool, error) {
	if s == nil || s.closed {
		return nil, false, screencapture.ErrBackendUnavailable
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := winrtruntime.Initialize(); err != nil {
		s.fail(err)
		return nil, false, err
	}
	frame, fresh, err := s.captureD3D11Locked()
	if err != nil {
		s.fail(err)
		return nil, false, err
	}
	return frame, fresh, nil
}

func (s *wgcFrameStream) WaitD3D11Frame(ctx context.Context) (*D3D11CaptureFrame, error) {
	if s == nil || s.closed {
		return nil, screencapture.ErrBackendUnavailable
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		frame, fresh, err := s.refreshD3D11Frame()
		if err != nil {
			return nil, err
		}
		if fresh && frame != nil {
			return frame, nil
		}
		if s.lastErr != nil {
			return nil, s.lastErr
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-s.closedReady:
			return nil, screencapture.ErrBackendUnavailable
		case <-s.frameReady:
		}
	}
}

func (s *wgcFrameStream) captureD3D11Locked() (*D3D11CaptureFrame, bool, error) {
	if s.pool == nil || s.device == nil {
		return nil, false, screencapture.ErrBackendUnavailable
	}
	var latest *winrtcapture.IDirect3D11CaptureFrame
	for {
		frame, err := s.pool.TryGetNextFrame()
		if err != nil {
			if latest != nil {
				latest.Release()
			}
			return nil, false, fmt.Errorf("WGC get next D3D11 frame: %w", err)
		}
		if frame == nil {
			break
		}
		if latest != nil {
			latest.Release()
		}
		latest = frame
	}
	if latest == nil {
		return nil, false, nil
	}
	defer latest.Release()

	size, err := latest.ContentSize()
	if err != nil {
		return nil, false, fmt.Errorf("WGC read D3D11 frame content size: %w", err)
	}
	if size.Width <= 0 || size.Height <= 0 {
		return nil, false, fmt.Errorf("WGC D3D11 frame has invalid size %dx%d", size.Width, size.Height)
	}

	surface, err := latest.Surface()
	if err != nil {
		return nil, false, fmt.Errorf("WGC get D3D11 surface: %w", err)
	}
	if surface == nil {
		return nil, false, errors.New("WGC D3D11 frame surface is nil")
	}
	defer surface.Release()

	access, err := wgcQueryInterface[win32direct3d11.IDirect3DDxgiInterfaceAccess](
		surface,
		&win32direct3d11.IID_IDirect3DDxgiInterfaceAccess,
	)
	if err != nil {
		return nil, false, fmt.Errorf("WGC D3D11 surface query DXGI access: %w", err)
	}
	defer access.Release()

	var textureUnknown *win32.IUnknown
	if err := access.GetInterface(&graphicsdirect3d11.IID_ID3D11Texture2D, &textureUnknown); err != nil {
		return nil, false, fmt.Errorf("WGC get D3D11 texture: %w", err)
	}
	if textureUnknown == nil {
		return nil, false, errors.New("WGC D3D11 texture is nil")
	}
	texture := wgcCast[graphicsdirect3d11.ID3D11Texture2D](textureUnknown)

	var desc graphicsdirect3d11.D3D11_TEXTURE2D_DESC
	texture.GetDesc(&desc)
	if desc.Format != graphicsdxgicommon.DXGI_FORMAT_B8G8R8A8_UNORM {
		texture.Release()
		return nil, false, fmt.Errorf("WGC D3D11 texture format=%v want BGRA8", desc.Format)
	}
	if size.Width > int32(desc.Width) || size.Height > int32(desc.Height) {
		texture.Release()
		return nil, false, fmt.Errorf(
			"WGC D3D11 content %dx%d exceeds texture %dx%d",
			size.Width, size.Height, desc.Width, desc.Height,
		)
	}

	if size != s.poolSize {
		if err := s.pool.Recreate(
			s.winrtDevice,
			winrtdirectx.DirectXPixelFormatB8G8R8A8UIntNormalized,
			wgcFramePoolBuffers,
			size,
		); err != nil {
			texture.Release()
			return nil, false, fmt.Errorf(
				"WGC recreate D3D11 frame pool for %dx%d: %w", size.Width, size.Height, err,
			)
		}
		s.poolSize = size
	}

	now := time.Now()
	s.sequence++
	s.at = now
	s.lastErr = nil
	return &D3D11CaptureFrame{
		Device:      uintptr(unsafe.Pointer(s.device)),
		Resource:    uintptr(unsafe.Pointer(texture)),
		Subresource: 0,
		Width:       int(size.Width),
		Height:      int(size.Height),
		At:          now,
		release: func() {
			texture.Release()
		},
	}, true, nil
}

func (s *wgcFrameStream) refreshLocked() (bool, error) {
	if s.pool == nil {
		return false, screencapture.ErrBackendUnavailable
	}
	var latest *winrtcapture.IDirect3D11CaptureFrame
	for {
		frame, err := s.pool.TryGetNextFrame()
		if err != nil {
			if latest != nil {
				latest.Release()
			}
			return false, fmt.Errorf("WGC get next frame: %w", err)
		}
		if frame == nil {
			break
		}
		if latest != nil {
			latest.Release()
		}
		latest = frame
	}
	if latest == nil {
		return false, nil
	}

	size, sizeErr := latest.ContentSize()
	if sizeErr != nil {
		latest.Release()
		return false, fmt.Errorf("WGC read frame content size: %w", sizeErr)
	}
	if size.Width <= 0 || size.Height <= 0 {
		latest.Release()
		return false, fmt.Errorf("WGC frame has invalid size %dx%d", size.Width, size.Height)
	}
	if err := s.readbackLocked(latest, size); err != nil {
		latest.Release()
		return false, err
	}
	latest.Release()

	if size != s.poolSize {
		if err := s.pool.Recreate(
			s.winrtDevice,
			winrtdirectx.DirectXPixelFormatB8G8R8A8UIntNormalized,
			wgcFramePoolBuffers,
			size,
		); err != nil {
			return false, fmt.Errorf("WGC recreate frame pool for %dx%d: %w", size.Width, size.Height, err)
		}
		s.poolSize = size
	}

	s.sequence++
	s.at = time.Now()
	s.lastErr = nil
	return true, nil
}

func (s *wgcFrameStream) readbackLocked(
	frame *winrtcapture.IDirect3D11CaptureFrame,
	size winrtgraphics.SizeInt32,
) error {
	surface, err := frame.Surface()
	if err != nil {
		return fmt.Errorf("WGC get Direct3D surface: %w", err)
	}
	if surface == nil {
		return errors.New("WGC frame surface is nil")
	}
	defer surface.Release()

	access, err := wgcQueryInterface[win32direct3d11.IDirect3DDxgiInterfaceAccess](
		surface,
		&win32direct3d11.IID_IDirect3DDxgiInterfaceAccess,
	)
	if err != nil {
		return fmt.Errorf("WGC surface query DXGI access: %w", err)
	}
	defer access.Release()

	var textureUnknown *win32.IUnknown
	if err := access.GetInterface(&graphicsdirect3d11.IID_ID3D11Texture2D, &textureUnknown); err != nil {
		return fmt.Errorf("WGC get ID3D11Texture2D: %w", err)
	}
	if textureUnknown == nil {
		return errors.New("WGC frame texture is nil")
	}
	texture := wgcCast[graphicsdirect3d11.ID3D11Texture2D](textureUnknown)
	defer texture.Release()

	var desc graphicsdirect3d11.D3D11_TEXTURE2D_DESC
	texture.GetDesc(&desc)
	if desc.Format != graphicsdxgicommon.DXGI_FORMAT_B8G8R8A8_UNORM {
		return fmt.Errorf("WGC frame format=%v want BGRA8", desc.Format)
	}
	if size.Width > int32(desc.Width) || size.Height > int32(desc.Height) {
		return fmt.Errorf("WGC content %dx%d exceeds texture %dx%d",
			size.Width, size.Height, desc.Width, desc.Height)
	}
	if err := s.ensureStagingLocked(desc); err != nil {
		return err
	}

	s.context.CopyResource(&s.staging.ID3D11Resource, &texture.ID3D11Resource)
	var mapped graphicsdirect3d11.D3D11_MAPPED_SUBRESOURCE
	if err := s.context.Map(
		&s.staging.ID3D11Resource,
		0,
		graphicsdirect3d11.D3D11_MAP_READ,
		0,
		&mapped,
	); err != nil {
		return fmt.Errorf("WGC map staging texture: %w", err)
	}
	defer s.context.Unmap(&s.staging.ID3D11Resource, 0)
	if mapped.PData == nil {
		return errors.New("WGC mapped staging texture has nil data")
	}
	width := int(size.Width)
	height := int(size.Height)
	stride := int(mapped.RowPitch)
	if stride < width*4 {
		return fmt.Errorf("WGC staging row pitch=%d smaller than BGRA row=%d", stride, width*4)
	}
	required := stride * height
	if cap(s.pix) < required {
		s.pix = make([]byte, required)
	} else {
		s.pix = s.pix[:required]
	}
	source := unsafe.Slice((*byte)(mapped.PData), required)
	copy(s.pix, source)
	s.width = width
	s.height = height
	s.stride = stride
	return nil
}

func (s *wgcFrameStream) ensureStagingLocked(desc graphicsdirect3d11.D3D11_TEXTURE2D_DESC) error {
	if s.staging != nil && desc == s.stagingSource {
		return nil
	}
	if s.staging != nil {
		s.staging.Release()
		s.staging = nil
	}
	stagingDesc := desc
	stagingDesc.Usage = graphicsdirect3d11.D3D11_USAGE_STAGING
	stagingDesc.BindFlags = 0
	stagingDesc.CPUAccessFlags = uint32(graphicsdirect3d11.D3D11_CPU_ACCESS_READ)
	stagingDesc.MiscFlags = 0
	if err := s.device.CreateTexture2D(&stagingDesc, nil, &s.staging); err != nil {
		return fmt.Errorf("WGC create staging texture: %w", err)
	}
	if s.staging == nil {
		return errors.New("WGC staging texture is nil")
	}
	s.stagingSource = desc
	return nil
}

func (s *wgcFrameStream) snapshot() windowsCaptureFrame {
	if s.width <= 0 || s.height <= 0 || s.stride <= 0 || len(s.pix) == 0 {
		return windowsCaptureFrame{}
	}
	return windowsCaptureFrame{
		Pix:      s.pix,
		Width:    s.width,
		Height:   s.height,
		Stride:   s.stride,
		Sequence: s.sequence,
		At:       s.at,
	}
}

func (s *wgcFrameStream) fail(err error) {
	s.lastErr = err
	s.width = 0
	s.height = 0
	s.stride = 0
	s.pix = s.pix[:0]
}

func (s *wgcFrameStream) Close() error {
	if s == nil || s.closed {
		return nil
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	_ = winrtruntime.Initialize()
	return s.closeLocked()
}

func (s *wgcFrameStream) closeLocked() error {
	if s == nil || s.closed {
		return nil
	}
	s.closed = true
	if s.closedReady != nil {
		close(s.closedReady)
	}
	var firstErr error
	closeWinRT := func(obj wgcUnknown) {
		if obj == nil {
			return
		}
		closable, err := wgcQueryInterface[winrtfoundation.IClosable](obj, &winrtfoundation.IID_IClosable)
		if err != nil {
			return
		}
		if err := closable.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		closable.Release()
	}
	if s.session != nil {
		closeWinRT(s.session)
		s.session.Release()
		s.session = nil
	}
	if s.pool != nil && s.frameArrivedRegistered {
		if err := s.pool.RemoveFrameArrived(s.frameArrivedToken); err != nil && firstErr == nil {
			firstErr = err
		}
		s.frameArrivedRegistered = false
	}
	if s.pool != nil {
		closeWinRT(s.pool)
		s.pool.Release()
		s.pool = nil
	}
	if s.frameArrivedHandler != nil {
		s.frameArrivedHandler.Close()
		s.frameArrivedHandler = nil
	}
	if s.staging != nil {
		s.staging.Release()
		s.staging = nil
	}
	if s.item != nil {
		s.item.Release()
		s.item = nil
	}
	if s.winrtDevice != nil {
		closeWinRT(s.winrtDevice)
		s.winrtDevice.Release()
		s.winrtDevice = nil
	}
	if s.context != nil {
		s.context.Release()
		s.context = nil
	}
	if s.device != nil {
		s.device.Release()
		s.device = nil
	}
	s.pix = nil
	return firstErr
}
