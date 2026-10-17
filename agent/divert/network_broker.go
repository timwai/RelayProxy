package divert

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

const (
	networkPipeMagic   = 0x31504e52 // "RNP1" little-endian
	networkPipeVersion = 5

	networkFrameHello    = 1
	networkFrameReady    = 2
	networkFrameError    = 3
	networkFrameCapture  = 4
	networkFrameInject   = 5
	networkFrameComplete = 6

	networkFrameFlagOutbound = 1 << 17
	networkFrameHeaderBytes  = 24
	networkFrameMaxPayload   = 1 << 20

	// Idle capture is harmless. Only an outstanding packet may time out: the
	// kernel is diverting other host traffic while the Agent owns this packet.
	networkCaptureTimeout = 5 * time.Second
)

var errNetworkCaptureTimeout = errors.New("packet handling timed out; capture released")

type networkFrame struct {
	kind       uint16
	flags      uint32
	ifIndex    uint32
	subIfIndex uint32
	payload    []byte
}

func writeNetworkFrame(writer io.Writer, mu *sync.Mutex, frame networkFrame) error {
	if len(frame.payload) > networkFrameMaxPayload {
		return fmt.Errorf("RelayProxy Network Service frame payload too large: %d", len(frame.payload))
	}
	if mu != nil {
		mu.Lock()
		defer mu.Unlock()
	}
	header := make([]byte, networkFrameHeaderBytes)
	binary.LittleEndian.PutUint32(header[0:4], networkPipeMagic)
	binary.LittleEndian.PutUint16(header[4:6], networkPipeVersion)
	binary.LittleEndian.PutUint16(header[6:8], frame.kind)
	binary.LittleEndian.PutUint32(header[8:12], frame.flags)
	binary.LittleEndian.PutUint32(header[12:16], frame.ifIndex)
	binary.LittleEndian.PutUint32(header[16:20], frame.subIfIndex)
	binary.LittleEndian.PutUint32(header[20:24], uint32(len(frame.payload)))
	if err := writeNetworkBytes(writer, header); err != nil {
		return err
	}
	return writeNetworkBytes(writer, frame.payload)
}

func writeNetworkBytes(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := writer.Write(data)
		if err != nil {
			return err
		}
		if n <= 0 || n > len(data) {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func readNetworkFrame(reader io.Reader) (networkFrame, error) {
	header := make([]byte, networkFrameHeaderBytes)
	if _, err := io.ReadFull(reader, header); err != nil {
		return networkFrame{}, err
	}
	if binary.LittleEndian.Uint32(header[0:4]) != networkPipeMagic {
		return networkFrame{}, errors.New("RelayProxy Network Service frame magic mismatch")
	}
	if binary.LittleEndian.Uint16(header[4:6]) != networkPipeVersion {
		return networkFrame{}, errors.New("RelayProxy Network Service protocol version mismatch")
	}
	length := binary.LittleEndian.Uint32(header[20:24])
	if length > networkFrameMaxPayload {
		return networkFrame{}, fmt.Errorf("RelayProxy Network Service frame payload exceeds limit: %d", length)
	}
	frame := networkFrame{
		kind:       binary.LittleEndian.Uint16(header[6:8]),
		flags:      binary.LittleEndian.Uint32(header[8:12]),
		ifIndex:    binary.LittleEndian.Uint32(header[12:16]),
		subIfIndex: binary.LittleEndian.Uint32(header[16:20]),
	}
	if length != 0 {
		frame.payload = make([]byte, int(length))
		if _, err := io.ReadFull(reader, frame.payload); err != nil {
			return networkFrame{}, err
		}
	}
	return frame, nil
}

// serveNetworkPackets requires a full-duplex stream. Capture backpressure is
// bounded to one outstanding packet, while injections are accepted even when
// the kernel has no new packet to capture (notably asynchronous UDP replies).
func serveNetworkPackets(pipe io.ReadWriteCloser, device packetDevice) error {
	return serveNetworkPacketsWithTimeout(pipe, device, networkCaptureTimeout)
}

func serveNetworkPacketsWithTimeout(pipe io.ReadWriteCloser, device packetDevice, timeout time.Duration) error {
	stop := make(chan struct{})
	var stopOnce sync.Once
	var stopErr error
	shutdown := func(err error) {
		stopOnce.Do(func() {
			stopErr = err
			close(stop)
			// Release machine-wide capture before interrupting pipe IO. This
			// also unblocks Receive/Send when the Agent is alive but unresponsive.
			_ = device.Shutdown()
			_ = pipe.Close()
		})
	}
	complete := make(chan struct{}, 1)
	captureDone := make(chan error, 1)
	go func() {
		var timer *time.Timer
		defer func() {
			if timer != nil {
				timer.Stop()
			}
		}()
		err := func() error {
			buffer := make([]byte, 40+65535)
			for {
				n, meta, err := device.Receive(buffer)
				if err != nil {
					return err
				}
				flags := uint32(0)
				if meta.outbound {
					flags = networkFrameFlagOutbound
				}
				if timer == nil {
					timer = time.AfterFunc(timeout, func() { shutdown(errNetworkCaptureTimeout) })
				} else {
					timer.Reset(timeout)
				}
				if err := writeNetworkFrame(pipe, nil, networkFrame{kind: networkFrameCapture,
					flags: flags, ifIndex: meta.ifIndex, subIfIndex: meta.subIfIndex, payload: buffer[:n]}); err != nil {
					timer.Stop()
					return err
				}
				select {
				case <-complete:
				case <-stop:
					timer.Stop()
					return nil
				}
				timer.Stop()
			}
		}()
		shutdown(err)
		captureDone <- err
	}()
	var readErr error
	for readErr == nil {
		frame, err := readNetworkFrame(pipe)
		if err != nil {
			readErr = err
			break
		}
		switch frame.kind {
		case networkFrameInject:
			readErr = device.Send(frame.payload, packetMetadata{
				outbound: frame.flags&networkFrameFlagOutbound != 0,
				ifIndex:  frame.ifIndex, subIfIndex: frame.subIfIndex,
			})
		case networkFrameComplete:
			select {
			case complete <- struct{}{}:
			default:
				readErr = errors.New("duplicate capture completion")
			}
		default:
			readErr = fmt.Errorf("unknown Network Service packet frame %d", frame.kind)
		}
	}
	shutdown(readErr)
	return errors.Join(stopErr, readErr, <-captureDone)
}
