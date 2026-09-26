//go:build windows && amd64

package codec

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"
	"unsafe"
)

const nvdecMaxDisplayQueue = 32

type nvdecParserDisplayInfo struct {
	PictureIndex     int32
	ProgressiveFrame int32
	TopFieldFirst    int32
	RepeatFirstField int32
	Timestamp        int64
}

type nvdecProcParams struct {
	ProgressiveFrame int32
	SecondField      int32
	TopFieldFirst    int32
	UnpairedField    int32
	ReservedFlags    uint32
	ReservedZero     uint32
	RawInputDPtr     uint64
	RawInputPitch    uint32
	RawInputFormat   uint32
	RawOutputDPtr    uint64
	RawOutputPitch   uint32
	Reserved1        uint32
	OutputStream     uintptr
	Reserved         [46]uint32
	Reserved2        [2]uintptr
}

type nvdecMappedFrame struct {
	mu sync.Mutex

	parser       *nvdecHEVC444Parser
	session      *nvdecD3D11Session
	devicePtr    uint64
	pitch        uint32
	pictureIndex int32
	width        int
	height       int
	timestamp    time.Duration
	closed       bool
}

func nvdecTimestampDuration(timestamp int64) time.Duration {
	seconds := timestamp / int64(nvdecParserClockRate)
	ticks := timestamp % int64(nvdecParserClockRate)
	return time.Duration(seconds)*time.Second +
		time.Duration(ticks)*time.Second/time.Duration(nvdecParserClockRate)
}

func (p *nvdecHEVC444Parser) handleDisplay(displayInfo uintptr) error {
	if p == nil || displayInfo == 0 {
		return fmt.Errorf("%w: NVDEC display callback received nil display info", ErrDecoderUnavailable)
	}
	info := *(*nvdecParserDisplayInfo)(unsafe.Pointer(displayInfo))
	runtime.KeepAlive(displayInfo)
	if info.PictureIndex < 0 {
		return fmt.Errorf("%w: NVDEC display picture index=%d", ErrDecoderUnavailable, info.PictureIndex)
	}
	if info.ProgressiveFrame == 0 {
		return fmt.Errorf("%w: NVDEC interlaced display frame is not supported", ErrDecoderUnavailable)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.session == nil || p.decoder == 0 {
		return ErrDecoderUnavailable
	}
	if len(p.displayQueue) >= nvdecMaxDisplayQueue {
		return fmt.Errorf(
			"%w: NVDEC display queue exceeds %d frames",
			ErrDecoderUnavailable,
			nvdecMaxDisplayQueue,
		)
	}
	p.displayQueue = append(p.displayQueue, info)
	p.displayCalls++
	return nil
}

func (p *nvdecHEVC444Parser) MapNextDisplay(
	ctx context.Context,
) (*nvdecMappedFrame, bool, error) {
	if p == nil {
		return nil, false, ErrDecoderUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}

	p.callMu.Lock()
	defer p.callMu.Unlock()

	p.mu.Lock()
	if p.closed || p.session == nil || p.decoder == 0 {
		p.mu.Unlock()
		return nil, false, ErrDecoderUnavailable
	}
	if len(p.displayQueue) == 0 {
		p.mu.Unlock()
		return nil, false, nil
	}
	info := p.displayQueue[0]
	copy(p.displayQueue, p.displayQueue[1:])
	p.displayQueue = p.displayQueue[:len(p.displayQueue)-1]
	session := p.session
	decoder := p.decoder
	width := p.surfaceWidth
	height := p.surfaceHeight
	p.mu.Unlock()

	if width <= 0 || height <= 0 {
		return nil, false, fmt.Errorf("%w: NVDEC output dimensions are unavailable", ErrDecoderUnavailable)
	}
	api, err := session.API()
	if err != nil {
		return nil, false, err
	}

	params := nvdecProcParams{
		ProgressiveFrame: info.ProgressiveFrame,
		SecondField:      info.RepeatFirstField + 1,
		TopFieldFirst:    info.TopFieldFirst,
	}
	if info.RepeatFirstField < 0 {
		params.UnpairedField = 1
	}
	var devicePtr uint64
	var pitch uint32
	status := cudaDriverCall(
		api.CuvidMapVideoFrame64,
		decoder,
		uintptr(uint32(info.PictureIndex)),
		uintptr(unsafe.Pointer(&devicePtr)),
		uintptr(unsafe.Pointer(&pitch)),
		uintptr(unsafe.Pointer(&params)),
	)
	runtime.KeepAlive(&params)
	runtime.KeepAlive(&devicePtr)
	runtime.KeepAlive(&pitch)
	if status != 0 {
		return nil, false, fmt.Errorf("%w: cuvidMapVideoFrame64 returned %d", ErrDecoderUnavailable, status)
	}
	if devicePtr == 0 || pitch == 0 {
		if devicePtr != 0 {
			_ = cudaDriverCall(api.CuvidUnmapVideoFrame64, decoder, uintptr(devicePtr))
		}
		return nil, false, fmt.Errorf(
			"%w: NVDEC mapped frame returned ptr=%#x pitch=%d",
			ErrDecoderUnavailable,
			devicePtr,
			pitch,
		)
	}

	frame := &nvdecMappedFrame{
		parser:       p,
		session:      session,
		devicePtr:    devicePtr,
		pitch:        pitch,
		pictureIndex: info.PictureIndex,
		width:        width,
		height:       height,
		timestamp:    nvdecTimestampDuration(info.Timestamp),
	}
	p.mu.Lock()
	if p.closed || p.decoder != decoder || p.session != session {
		p.mu.Unlock()
		_ = cudaDriverCall(api.CuvidUnmapVideoFrame64, decoder, uintptr(devicePtr))
		return nil, false, ErrDecoderUnavailable
	}
	if p.mappedFrames == nil {
		p.mappedFrames = make(map[uint64]*nvdecMappedFrame)
	}
	if _, exists := p.mappedFrames[devicePtr]; exists {
		p.mu.Unlock()
		_ = cudaDriverCall(api.CuvidUnmapVideoFrame64, decoder, uintptr(devicePtr))
		return nil, false, fmt.Errorf("%w: NVDEC mapped pointer %#x is already active", ErrDecoderUnavailable, devicePtr)
	}
	p.mappedFrames[devicePtr] = frame
	p.mu.Unlock()
	return frame, true, nil
}

func (f *nvdecMappedFrame) withMappedData(
	fn func(*nvdecD3D11Session, uint64, uint32, int, int) error,
) error {
	if f == nil || fn == nil {
		return ErrDecoderUnavailable
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed || f.session == nil || f.devicePtr == 0 || f.pitch == 0 ||
		f.width <= 0 || f.height <= 0 {
		return ErrDecoderUnavailable
	}
	return fn(f.session, f.devicePtr, f.pitch, f.width, f.height)
}

func (f *nvdecMappedFrame) DevicePointer() uint64 {
	if f == nil {
		return 0
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0
	}
	return f.devicePtr
}

func (f *nvdecMappedFrame) Pitch() uint32 {
	if f == nil {
		return 0
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0
	}
	return f.pitch
}

func (f *nvdecMappedFrame) Dimensions() (int, int) {
	if f == nil {
		return 0, 0
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, 0
	}
	return f.width, f.height
}

func (f *nvdecMappedFrame) Timestamp() time.Duration {
	if f == nil {
		return 0
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0
	}
	return f.timestamp
}

func (f *nvdecMappedFrame) detach() uint64 {
	if f == nil {
		return 0
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0
	}
	f.closed = true
	devicePtr := f.devicePtr
	f.parser = nil
	f.session = nil
	f.devicePtr = 0
	f.pitch = 0
	f.width = 0
	f.height = 0
	f.timestamp = 0
	return devicePtr
}

func (f *nvdecMappedFrame) Close() error {
	if f == nil {
		return nil
	}

	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return nil
	}
	parser := f.parser
	devicePtr := f.devicePtr
	f.mu.Unlock()
	if parser == nil || devicePtr == 0 {
		_ = f.detach()
		return nil
	}

	parser.callMu.Lock()
	defer parser.callMu.Unlock()

	parser.mu.Lock()
	if parser.closed || parser.session == nil || parser.decoder == 0 {
		delete(parser.mappedFrames, devicePtr)
		parser.mu.Unlock()
		_ = f.detach()
		return nil
	}
	session := parser.session
	decoder := parser.decoder
	delete(parser.mappedFrames, devicePtr)
	parser.mu.Unlock()

	api, err := session.API()
	if err != nil {
		_ = f.detach()
		return err
	}
	status := cudaDriverCall(api.CuvidUnmapVideoFrame64, decoder, uintptr(devicePtr))
	_ = f.detach()
	if status != 0 {
		return fmt.Errorf("cuvidUnmapVideoFrame64 returned %d", status)
	}
	return nil
}

func (p *nvdecHEVC444Parser) PendingDisplayFrames() int {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.displayQueue)
}

func (p *nvdecHEVC444Parser) ActiveMappedFrames() int {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.mappedFrames)
}

func closeNVDECMappedFrames(frames []*nvdecMappedFrame) error {
	var err error
	for _, frame := range frames {
		err = errors.Join(err, frame.Close())
	}
	return err
}
