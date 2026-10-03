package speedtest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

const (
	MinDuration = 500 * time.Millisecond
	MaxDuration = 10 * time.Second
	bufferSize  = 32 << 10
)

type Measurement struct {
	Bytes          uint64  `json:"bytes"`
	DurationMS     float64 `json:"durationMs"`
	BytesPerSecond float64 `json:"bytesPerSecond"`
	Megabits       float64 `json:"megabitsPerSecond"`
}

func Validate(req protocol.SpeedTestRequest) (time.Duration, error) {
	if req.Direction != protocol.SpeedTestUpload && req.Direction != protocol.SpeedTestDownload {
		return 0, errors.New("invalid speed test direction")
	}
	duration := time.Duration(req.DurationMS) * time.Millisecond
	if duration < MinDuration || duration > MaxDuration {
		return 0, fmt.Errorf("speed test duration must be between %d and %d milliseconds", MinDuration.Milliseconds(), MaxDuration.Milliseconds())
	}
	return duration, nil
}

func measurement(bytes uint64, elapsed time.Duration) Measurement {
	if elapsed <= 0 {
		return Measurement{Bytes: bytes}
	}
	bps := float64(bytes) / elapsed.Seconds()
	return Measurement{
		Bytes: bytes, DurationMS: float64(elapsed) / float64(time.Millisecond),
		BytesPerSecond: bps, Megabits: bps * 8 / 1_000_000,
	}
}

func WriteError(stream io.Writer, requestID, code string, err error) error {
	message := "speed test failed"
	if err != nil {
		message = err.Error()
	}
	return protocol.WriteJSON(stream, protocol.SpeedTestResponse{
		RequestID: requestID, ErrorCode: code, ErrorMessage: message,
	})
}

// Serve runs the exit side after its request has already been decoded.
func Serve(stream tunnel.TunnelStream, req protocol.SpeedTestRequest) (Measurement, error) {
	duration, err := Validate(req)
	if err != nil {
		_ = WriteError(stream, req.RequestID, protocol.ErrCodeInvalidRequest, err)
		return Measurement{}, err
	}
	if err := protocol.WriteJSON(stream, protocol.SpeedTestResponse{RequestID: req.RequestID, Success: true}); err != nil {
		return Measurement{}, err
	}
	_ = stream.SetDeadline(time.Now().Add(duration + 15*time.Second))
	buf := make([]byte, bufferSize)

	if req.Direction == protocol.SpeedTestDownload {
		started := time.Now()
		var total uint64
		for time.Since(started) < duration {
			n, writeErr := stream.Write(buf)
			total += uint64(max(n, 0))
			if writeErr != nil {
				return measurement(total, time.Since(started)), writeErr
			}
		}
		_ = stream.CloseWrite()
		return measurement(total, time.Since(started)), nil
	}

	started := time.Now()
	n, err := io.CopyBuffer(io.Discard, stream, buf)
	result := measurement(uint64(max(n, 0)), time.Since(started))
	if err != nil {
		return result, err
	}
	if err := protocol.WriteJSON(stream, protocol.SpeedTestComplete{Bytes: result.Bytes, DurationMS: result.DurationMS}); err != nil {
		return result, err
	}
	_ = stream.CloseWrite()
	return result, nil
}

// Run executes one client-side direction on an already opened stream.
func Run(ctx context.Context, stream tunnel.TunnelStream, req protocol.SpeedTestRequest) (Measurement, error) {
	duration, err := Validate(req)
	if err != nil {
		return Measurement{}, err
	}
	if err := protocol.WriteJSON(stream, req); err != nil {
		return Measurement{}, err
	}
	var ack protocol.SpeedTestResponse
	if err := protocol.ReadJSON(stream, &ack); err != nil {
		return Measurement{}, err
	}
	if !ack.Success {
		if ack.ErrorMessage == "" {
			ack.ErrorMessage = "speed test was rejected"
		}
		return Measurement{}, errors.New(ack.ErrorMessage)
	}
	_ = stream.SetDeadline(time.Now().Add(duration + 15*time.Second))
	stop := context.AfterFunc(ctx, func() { _ = stream.Close() })
	defer stop()
	buf := make([]byte, bufferSize)

	if req.Direction == protocol.SpeedTestDownload {
		_ = stream.CloseWrite()
		started := time.Now()
		n, readErr := io.CopyBuffer(io.Discard, stream, buf)
		elapsed := time.Since(started)
		result := measurement(uint64(max(n, 0)), elapsed)
		if readErr != nil {
			return result, readErr
		}
		if elapsed < duration*8/10 {
			return result, errors.New("download speed test ended before the requested duration")
		}
		return result, nil
	}

	started := time.Now()
	for time.Since(started) < duration {
		if _, err := stream.Write(buf); err != nil {
			return Measurement{}, err
		}
	}
	if err := stream.CloseWrite(); err != nil {
		return Measurement{}, err
	}
	var complete protocol.SpeedTestComplete
	if err := protocol.ReadJSON(stream, &complete); err != nil {
		return Measurement{}, err
	}
	return measurement(complete.Bytes, time.Duration(complete.DurationMS*float64(time.Millisecond))), nil
}
