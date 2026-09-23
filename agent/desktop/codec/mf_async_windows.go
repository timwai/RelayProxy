//go:build windows

package codec

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	imfMediaEventGeneratorGetEvent   = 3
	imfMediaEventGeneratorQueueEvent = 6
	imfMediaEventGetType             = 33
	imfMediaEventGetStatus           = 35

	meUnknown             = 0
	meTransformNeedInput  = 601
	meTransformHaveOutput = 602
)

var iidIMFMediaEventGenerator = windows.GUID{
	Data1: 0x2cd0bd52, Data2: 0xbcd5, Data3: 0x4b89,
	Data4: [8]byte{0xb6, 0x2c, 0xea, 0xdc, 0x0c, 0x03, 0x1e, 0x7d},
}

type mfAsyncEvent struct {
	kind uint32
	err  error
}

type mfEventPump struct {
	generator unsafe.Pointer
	events    chan mfAsyncEvent
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

func comQueryInterface(object unsafe.Pointer, iid *windows.GUID) (unsafe.Pointer, error) {
	if object == nil {
		return nil, errors.New("QueryInterface on nil COM object")
	}
	var result unsafe.Pointer
	hr := comCall(object, 0, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&result)))
	if hresultFailed(hr) {
		return nil, hresultError("IUnknown.QueryInterface", hr)
	}
	if result == nil {
		return nil, errors.New("QueryInterface returned nil")
	}
	return result, nil
}

func startMFEventPump(transform unsafe.Pointer) (*mfEventPump, error) {
	generator, err := comQueryInterface(transform, &iidIMFMediaEventGenerator)
	if err != nil {
		return nil, fmt.Errorf("query IMFMediaEventGenerator: %w", err)
	}
	pump := &mfEventPump{
		generator: generator,
		events:    make(chan mfAsyncEvent, 16),
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}
	initCh := make(chan error, 1)
	go pump.run(initCh)
	if err := <-initCh; err != nil {
		releaseIUnknown(generator)
		return nil, err
	}
	return pump, nil
}

func (p *mfEventPump) run(initCh chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(p.done)
	defer close(p.events)

	uninitCOM, err := initializeCOM()
	if err != nil {
		initCh <- err
		return
	}
	defer uninitCOM()
	initCh <- nil

	for {
		var event unsafe.Pointer
		hr := comCall(
			p.generator,
			imfMediaEventGeneratorGetEvent,
			0,
			uintptr(unsafe.Pointer(&event)),
		)
		if hresultFailed(hr) {
			select {
			case <-p.stop:
				return
			case p.events <- mfAsyncEvent{err: hresultError("IMFMediaEventGenerator.GetEvent", hr)}:
				return
			}
		}
		if event == nil {
			select {
			case <-p.stop:
				return
			case p.events <- mfAsyncEvent{err: errors.New("Media Foundation event generator returned nil event")}:
				return
			}
		}

		select {
		case <-p.stop:
			releaseIUnknown(event)
			return
		default:
		}

		var kind uint32
		typeHR := comCall(event, imfMediaEventGetType, uintptr(unsafe.Pointer(&kind)))
		if hresultFailed(typeHR) {
			releaseIUnknown(event)
			select {
			case <-p.stop:
				return
			case p.events <- mfAsyncEvent{err: hresultError("IMFMediaEvent.GetType", typeHR)}:
				return
			}
		}
		var status int32
		statusHR := comCall(event, imfMediaEventGetStatus, uintptr(unsafe.Pointer(&status)))
		releaseIUnknown(event)
		if hresultFailed(statusHR) {
			select {
			case <-p.stop:
				return
			case p.events <- mfAsyncEvent{err: hresultError("IMFMediaEvent.GetStatus", statusHR)}:
				return
			}
		}
		if status < 0 {
			select {
			case <-p.stop:
				return
			case p.events <- mfAsyncEvent{err: fmt.Errorf("Media Foundation async event %d failed with HRESULT 0x%08x", kind, uint32(status))}:
				return
			}
		}
		select {
		case <-p.stop:
			return
		case p.events <- mfAsyncEvent{kind: kind}:
		}
	}
}

func (p *mfEventPump) wake() error {
	var zero windows.GUID
	hr := comCall(
		p.generator,
		imfMediaEventGeneratorQueueEvent,
		meUnknown,
		uintptr(unsafe.Pointer(&zero)),
		0,
		0,
	)
	if hresultFailed(hr) {
		return hresultError("IMFMediaEventGenerator.QueueEvent", hr)
	}
	return nil
}

func (p *mfEventPump) Close() error {
	if p == nil {
		return nil
	}
	var closeErr error
	p.closeOnce.Do(func() {
		close(p.stop)
		closeErr = p.wake()
		if closeErr == nil {
			<-p.done
			releaseIUnknown(p.generator)
			p.generator = nil
		}
	})
	return closeErr
}

type mfAsyncState struct {
	transform unsafe.Pointer
	cfg       VideoConfig
	codec     string

	needInput int
	pending   []mfTransformCommand
	inFlight  []mfTransformCommand
	output    []EncodedPacket
	fatal     error
}

func newMFAsyncState(transform unsafe.Pointer, cfg VideoConfig, codec string) *mfAsyncState {
	return &mfAsyncState{transform: transform, cfg: cfg, codec: codec}
}

func (s *mfAsyncState) reply(command mfTransformCommand, packets []EncodedPacket, err error) {
	command.reply <- mfTransformResult{packets: packets, err: err}
}

func (s *mfAsyncState) fail(err error) {
	if err == nil {
		err = ErrEncoderUnavailable
	}
	if s.fatal == nil {
		s.fatal = err
	}
	for _, command := range s.pending {
		s.reply(command, nil, s.fatal)
	}
	for _, command := range s.inFlight {
		s.reply(command, nil, s.fatal)
	}
	s.pending = nil
	s.inFlight = nil
	s.output = nil
	s.needInput = 0
}

func (s *mfAsyncState) enqueue(command mfTransformCommand) {
	if s.fatal != nil {
		s.reply(command, nil, s.fatal)
		return
	}
	s.pending = append(s.pending, command)
	s.submitAvailable()
}

func (s *mfAsyncState) submitAvailable() {
	for s.fatal == nil && s.needInput > 0 && len(s.pending) > 0 {
		command := s.pending[0]
		s.pending = s.pending[1:]
		if err := s.submit(*command.input); err != nil {
			s.reply(command, nil, err)
			s.fail(err)
			return
		}
		s.needInput--
		if len(s.output) > 0 {
			packets := append([]EncodedPacket(nil), s.output...)
			s.output = nil
			s.reply(command, packets, nil)
		} else {
			s.inFlight = append(s.inFlight, command)
		}
	}
}

func (s *mfAsyncState) submit(input mfEncodeInput) error {
	sample, err := createEncodeInputSample(input)
	if err != nil {
		return err
	}
	defer releaseIUnknown(sample)
	hr := processTransformInputSample(s.transform, sample)
	if hresultFailed(hr) {
		return hresultError("IMFTransform.ProcessInput async", hr)
	}
	return nil
}

func (s *mfAsyncState) finishOldest(packets []EncodedPacket) {
	if len(s.inFlight) == 0 {
		return
	}
	command := s.inFlight[0]
	s.inFlight = s.inFlight[1:]
	s.reply(command, packets, nil)
}

func (s *mfAsyncState) handle(event mfAsyncEvent) {
	if s.fatal != nil {
		return
	}
	if event.err != nil {
		s.fail(event.err)
		return
	}
	switch event.kind {
	case meTransformNeedInput:
		if len(s.inFlight) > 0 {
			packets := append([]EncodedPacket(nil), s.output...)
			s.output = nil
			s.finishOldest(packets)
		}
		s.needInput++
		s.submitAvailable()
	case meTransformHaveOutput:
		fallback := time.Duration(0)
		if len(s.inFlight) > 0 && s.inFlight[0].input != nil {
			fallback = s.inFlight[0].input.timestamp
		}
		packet, hr, err := processTransformOutputOnce(s.transform, s.codec, fallback)
		if err != nil {
			s.fail(err)
			return
		}
		if hresultFailed(hr) {
			if uint32(hr) == mfETransformNeedMoreInput {
				return
			}
			s.fail(hresultError("IMFTransform.ProcessOutput async", hr))
			return
		}
		if packet != nil {
			s.output = append(s.output, *packet)
			if len(s.inFlight) > 0 {
				packets := append([]EncodedPacket(nil), s.output...)
				s.output = nil
				s.finishOldest(packets)
			}
		}
	}
}
