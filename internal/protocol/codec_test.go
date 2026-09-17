package protocol

import (
	"bytes"
	"testing"
)

func TestStreamHeaderCodec(t *testing.T) {
	orig := &StreamHeader{
		Magic:          MagicHeader,
		Version:        CurrentVersion,
		Type:           FrameTypeOpenTCP,
		RequestID:      "req-12345",
		ClientDeviceID: "dev-client-abc",
		ExitDeviceID:   "dev-exit-xyz",
	}

	var buf bytes.Buffer
	if err := WriteStreamHeader(&buf, orig); err != nil {
		t.Fatalf("WriteStreamHeader failed: %v", err)
	}

	decoded, err := ReadStreamHeader(&buf)
	if err != nil {
		t.Fatalf("ReadStreamHeader failed: %v", err)
	}

	if decoded.Magic != orig.Magic {
		t.Errorf("expected magic 0x%x, got 0x%x", orig.Magic, decoded.Magic)
	}
	if decoded.Version != orig.Version {
		t.Errorf("expected version %d, got %d", orig.Version, decoded.Version)
	}
	if decoded.Type != orig.Type {
		t.Errorf("expected type %d, got %d", orig.Type, decoded.Type)
	}
	if decoded.RequestID != orig.RequestID {
		t.Errorf("expected requestID %s, got %s", orig.RequestID, decoded.RequestID)
	}
	if decoded.ClientDeviceID != orig.ClientDeviceID {
		t.Errorf("expected clientID %s, got %s", orig.ClientDeviceID, decoded.ClientDeviceID)
	}
	if decoded.ExitDeviceID != orig.ExitDeviceID {
		t.Errorf("expected exitID %s, got %s", orig.ExitDeviceID, decoded.ExitDeviceID)
	}
}

func TestJSONCodec(t *testing.T) {
	req := OpenTCPRequest{
		RequestID: "req-999",
		Host:      "10.20.30.40",
		Port:      8080,
		TimeoutMs: 5000,
	}

	var buf bytes.Buffer
	if err := WriteJSON(&buf, req); err != nil {
		t.Fatalf("WriteJSON failed: %v", err)
	}

	var decoded OpenTCPRequest
	if err := ReadJSON(&buf, &decoded); err != nil {
		t.Fatalf("ReadJSON failed: %v", err)
	}

	if decoded.RequestID != req.RequestID || decoded.Host != req.Host || decoded.Port != req.Port || decoded.TimeoutMs != req.TimeoutMs {
		t.Errorf("decoded struct %+v does not match original %+v", decoded, req)
	}
}
