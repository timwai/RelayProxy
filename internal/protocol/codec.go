package protocol

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

const (
	MaxStringLen      = 255
	MaxJSONPayloadLen = 1024 * 1024 // 1 MB limit for control frames
)

// WriteStreamHeader encodes and writes the StreamHeader to w
func WriteStreamHeader(w io.Writer, h *StreamHeader) error {
	if h.Magic == 0 {
		h.Magic = MagicHeader
	}
	if h.Version == 0 {
		h.Version = CurrentVersion
	}

	if len(h.RequestID) > MaxStringLen || len(h.ClientDeviceID) > MaxStringLen || len(h.ExitDeviceID) > MaxStringLen {
		return fmt.Errorf("string length exceeds %d bytes in StreamHeader", MaxStringLen)
	}

	// 2 bytes magic + 1 byte version + 1 byte type + 1 + len(reqID) + 1 + len(clientID) + 1 + len(exitID)
	totalLen := 2 + 1 + 1 + 1 + len(h.RequestID) + 1 + len(h.ClientDeviceID) + 1 + len(h.ExitDeviceID)
	buf := make([]byte, totalLen)

	binary.BigEndian.PutUint16(buf[0:2], h.Magic)
	buf[2] = h.Version
	buf[3] = byte(h.Type)

	offset := 4
	buf[offset] = byte(len(h.RequestID))
	offset++
	copy(buf[offset:], h.RequestID)
	offset += len(h.RequestID)

	buf[offset] = byte(len(h.ClientDeviceID))
	offset++
	copy(buf[offset:], h.ClientDeviceID)
	offset += len(h.ClientDeviceID)

	buf[offset] = byte(len(h.ExitDeviceID))
	offset++
	copy(buf[offset:], h.ExitDeviceID)

	n, err := w.Write(buf)
	if err != nil {
		return err
	}
	if n < len(buf) {
		return io.ErrShortWrite
	}
	return nil
}

// ReadStreamHeader reads and decodes the StreamHeader from r
func ReadStreamHeader(r io.Reader) (*StreamHeader, error) {
	// Read fixed 4-byte prefix: Magic (2), Version (1), Type (1).
	var fixedBuf [4]byte
	if _, err := io.ReadFull(r, fixedBuf[:]); err != nil {
		return nil, err
	}

	magic := binary.BigEndian.Uint16(fixedBuf[0:2])
	if magic != MagicHeader {
		return nil, fmt.Errorf("invalid magic header: 0x%04x, expected 0x%04x", magic, MagicHeader)
	}

	version := fixedBuf[2]
	if version != CurrentVersion {
		return nil, fmt.Errorf("unsupported protocol version: %d, expected %d", version, CurrentVersion)
	}

	frameType := FrameType(fixedBuf[3])

	var lenBuf [1]byte
	var strBuf [MaxStringLen]byte
	readString := func() (string, error) {
		if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
			return "", err
		}
		strLen := int(lenBuf[0])
		if strLen == 0 {
			return "", nil
		}
		if _, err := io.ReadFull(r, strBuf[:strLen]); err != nil {
			return "", err
		}
		return string(strBuf[:strLen]), nil
	}

	reqID, err := readString()
	if err != nil {
		return nil, fmt.Errorf("failed to read RequestID: %w", err)
	}
	clientID, err := readString()
	if err != nil {
		return nil, fmt.Errorf("failed to read ClientDeviceID: %w", err)
	}
	exitID, err := readString()
	if err != nil {
		return nil, fmt.Errorf("failed to read ExitDeviceID: %w", err)
	}

	return &StreamHeader{
		Magic:          magic,
		Version:        version,
		Type:           frameType,
		RequestID:      reqID,
		ClientDeviceID: clientID,
		ExitDeviceID:   exitID,
	}, nil
}

// WriteJSON writes a uint32 length-prefixed JSON encoded payload to w atomically
func WriteJSON(w io.Writer, val any) error {
	data, err := json.Marshal(val)
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}
	if len(data) > MaxJSONPayloadLen {
		return fmt.Errorf("payload size %d exceeds max %d", len(data), MaxJSONPayloadLen)
	}

	buf := make([]byte, 4+len(data))
	binary.BigEndian.PutUint32(buf[0:4], uint32(len(data)))
	copy(buf[4:], data)

	n, err := w.Write(buf)
	if err != nil {
		return err
	}
	if n < len(buf) {
		return io.ErrShortWrite
	}
	return nil
}

// ReadJSON reads a uint32 length-prefixed JSON payload from r and unmarshals into target
func ReadJSON(r io.Reader, target any) error {
	var lenBuf [4]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return err
	}
	payloadLen := binary.BigEndian.Uint32(lenBuf[:])
	if payloadLen > MaxJSONPayloadLen {
		return fmt.Errorf("payload size %d exceeds max %d", payloadLen, MaxJSONPayloadLen)
	}

	data := make([]byte, payloadLen)
	if _, err := io.ReadFull(r, data); err != nil {
		return err
	}

	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("failed to unmarshal JSON: %w", err)
	}
	return nil
}
