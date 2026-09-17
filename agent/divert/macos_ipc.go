package divert

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
)

const (
	darwinIPCVersion  = 1
	darwinIPCMaxFrame = 2 << 20

	darwinFrameHello       = 1
	darwinFrameOpen        = 2
	darwinFrameDecision    = 3
	darwinFrameError       = 4
	darwinFrameUDPDatagram = 10
	darwinFrameUDPDirect   = 11
	darwinFrameUDPReply    = 12
	darwinFrameUDPReject   = 13
)

type darwinOpenFlow struct {
	Version         int      `json:"version"`
	Protocol        Protocol `json:"protocol"`
	SourceIP        string   `json:"source_ip"`
	SourcePort      uint16   `json:"source_port"`
	DestinationIP   string   `json:"destination_ip,omitempty"`
	DestinationPort uint16   `json:"destination_port,omitempty"`
	ProcessID       uint32   `json:"process_id,omitempty"`
	Process         string   `json:"process"`
	Hostname        string   `json:"hostname,omitempty"`
}

func (open darwinOpenFlow) flow(destination netip.AddrPort) Flow {
	return Flow{
		Process: open.Process, ProcessID: open.ProcessID, Protocol: open.Protocol,
		SourceIP: open.SourceIP, SourcePort: open.SourcePort,
		IP: destination.Addr().String(), Port: destination.Port(), Host: open.Hostname, DomainSource: "network-extension",
	}
}

func (open darwinOpenFlow) destination() (netip.AddrPort, error) {
	address, err := netip.ParseAddr(open.DestinationIP)
	if err != nil || open.DestinationPort == 0 {
		return netip.AddrPort{}, errors.New("macOS IPC 缺少有效的目标地址")
	}
	return netip.AddrPortFrom(address.Unmap(), open.DestinationPort), nil
}

type darwinFlowDecision struct {
	Action Action `json:"action"`
	ExitID string `json:"exit_id,omitempty"`
	Reason string `json:"reason,omitempty"`
}

func readDarwinFrame(reader io.Reader) (byte, []byte, error) {
	var header [5]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return 0, nil, err
	}
	length := binary.BigEndian.Uint32(header[1:])
	if length > darwinIPCMaxFrame {
		return 0, nil, fmt.Errorf("macOS IPC frame 太大: %d", length)
	}
	payload := make([]byte, int(length))
	if _, err := io.ReadFull(reader, payload); err != nil {
		return 0, nil, err
	}
	return header[0], payload, nil
}

// writeDarwinFrame uses writev through net.Buffers, so the payload is not
// copied into a header-sized staging allocation before entering the kernel.
func writeDarwinFrame(writer io.Writer, kind byte, payload []byte) error {
	if len(payload) > darwinIPCMaxFrame {
		return fmt.Errorf("macOS IPC frame 太大: %d", len(payload))
	}
	var header [5]byte
	header[0] = kind
	binary.BigEndian.PutUint32(header[1:], uint32(len(payload)))
	buffers := net.Buffers{header[:], payload}
	_, err := buffers.WriteTo(writer)
	return err
}

func writeDarwinJSON(writer io.Writer, kind byte, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return writeDarwinFrame(writer, kind, payload)
}

func encodeDarwinEndpoint(endpoint netip.AddrPort) ([19]byte, int, error) {
	var encoded [19]byte
	if !endpoint.IsValid() || endpoint.Port() == 0 || endpoint.Addr().Zone() != "" {
		return encoded, 0, errors.New("macOS IPC datagram endpoint 无效")
	}
	endpoint = netip.AddrPortFrom(endpoint.Addr().Unmap(), endpoint.Port())
	addressLength := 4
	if endpoint.Addr().Is6() {
		addressLength = 16
	}
	encoded[0] = byte(addressLength)
	binary.BigEndian.PutUint16(encoded[1:3], endpoint.Port())
	if addressLength == 4 {
		address := endpoint.Addr().As4()
		copy(encoded[3:7], address[:])
	} else {
		address := endpoint.Addr().As16()
		copy(encoded[3:19], address[:])
	}
	return encoded, 3 + addressLength, nil
}

func encodeDarwinDatagram(endpoint netip.AddrPort, payload []byte) ([]byte, error) {
	header, headerLength, err := encodeDarwinEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	encoded := make([]byte, headerLength+len(payload))
	copy(encoded, header[:headerLength])
	copy(encoded[headerLength:], payload)
	return encoded, nil
}

// writeDarwinDatagramFrame emits the frame header, endpoint and payload with
// writev. Reply payloads therefore do not need to be copied into a contiguous
// endpoint-prefixed allocation before entering the Unix socket.
func writeDarwinDatagramFrame(writer io.Writer, kind byte, endpoint netip.AddrPort, payload []byte) error {
	endpointHeader, endpointLength, err := encodeDarwinEndpoint(endpoint)
	if err != nil {
		return err
	}
	payloadLength := endpointLength + len(payload)
	if payloadLength > darwinIPCMaxFrame {
		return fmt.Errorf("macOS IPC frame 太大: %d", payloadLength)
	}
	var frameHeader [5]byte
	frameHeader[0] = kind
	binary.BigEndian.PutUint32(frameHeader[1:], uint32(payloadLength))
	buffers := net.Buffers{frameHeader[:], endpointHeader[:endpointLength], payload}
	_, err = buffers.WriteTo(writer)
	return err
}

func decodeDarwinDatagram(encoded []byte) (netip.AddrPort, []byte, error) {
	if len(encoded) < 7 || (encoded[0] != 4 && encoded[0] != 16) {
		return netip.AddrPort{}, nil, errors.New("macOS IPC datagram header 无效")
	}
	addressLength := int(encoded[0])
	if len(encoded) < 3+addressLength {
		return netip.AddrPort{}, nil, errors.New("macOS IPC datagram 被截断")
	}
	port := binary.BigEndian.Uint16(encoded[1:3])
	if port == 0 {
		return netip.AddrPort{}, nil, errors.New("macOS IPC datagram port 无效")
	}
	var address netip.Addr
	if addressLength == 4 {
		var value [4]byte
		copy(value[:], encoded[3:7])
		address = netip.AddrFrom4(value)
	} else {
		var value [16]byte
		copy(value[:], encoded[3:19])
		address = netip.AddrFrom16(value)
	}
	return netip.AddrPortFrom(address, port), encoded[3+addressLength:], nil
}
