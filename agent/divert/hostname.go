package divert

import (
	"bytes"
	"encoding/binary"
	"net"
	"strconv"
	"strings"
)

const maxHostnameProbe = 8192

// parseApplicationHostname reads only the initial, unencrypted request
// metadata: TLS ClientHello SNI or HTTP/1.x Host. These fields are declared
// by the application and are useful for telemetry, not an authenticated
// network destination or an authorization/routing decision.
//
// needMore is true only while a bounded first request may still be incomplete.
func parseApplicationHostname(data []byte) (host, source string, needMore bool) {
	if len(data) == 0 {
		return "", "", true
	}
	if data[0] == 0x16 {
		return parseTLSClientHelloSNI(data)
	}
	return parseHTTPHostname(data)
}

func parseTLSClientHelloSNI(data []byte) (string, string, bool) {
	if len(data) < 5 {
		return "", "", true
	}
	if data[1] != 3 || data[2] > 4 {
		return "", "", false
	}
	size := int(binary.BigEndian.Uint16(data[3:5]))
	if size < 4 || size+5 > maxHostnameProbe {
		return "", "", false
	}
	if len(data) < 5+size {
		return "", "", true
	}
	b := data[5 : 5+size]
	if len(b) < 4 || b[0] != 1 {
		return "", "", false
	}
	handshakeLen := int(b[1])<<16 | int(b[2])<<8 | int(b[3])
	if handshakeLen > len(b)-4 || handshakeLen < 34 {
		return "", "", false
	}
	b = b[4 : 4+handshakeLen]
	if len(b) < 35 {
		return "", "", false
	}
	// legacy_version (2), random (32), session ID vector (1+N).
	pos := 34
	sessionLen := int(b[pos])
	pos++
	if sessionLen > 32 || pos+sessionLen+2 > len(b) {
		return "", "", false
	}
	pos += sessionLen
	cipherLen := int(binary.BigEndian.Uint16(b[pos:]))
	pos += 2
	if cipherLen < 2 || cipherLen%2 != 0 || pos+cipherLen+1 > len(b) {
		return "", "", false
	}
	pos += cipherLen
	compressionLen := int(b[pos])
	pos++
	if compressionLen < 1 || pos+compressionLen > len(b) {
		return "", "", false
	}
	pos += compressionLen
	if pos == len(b) {
		return "", "", false
	}
	if pos+2 > len(b) {
		return "", "", false
	}
	extensionLen := int(binary.BigEndian.Uint16(b[pos:]))
	pos += 2
	if pos+extensionLen != len(b) {
		return "", "", false
	}
	end := pos + extensionLen
	for pos+4 <= end {
		typ := binary.BigEndian.Uint16(b[pos:])
		size := int(binary.BigEndian.Uint16(b[pos+2:]))
		pos += 4
		if pos+size > end {
			return "", "", false
		}
		if typ == 0 { // RFC 6066 server_name extension
			names := b[pos : pos+size]
			if len(names) < 5 || int(binary.BigEndian.Uint16(names[:2])) != len(names)-2 {
				return "", "", false
			}
			for idx := 2; idx+3 <= len(names); {
				nameType := names[idx]
				nameLen := int(binary.BigEndian.Uint16(names[idx+1:]))
				idx += 3
				if nameLen == 0 || idx+nameLen > len(names) {
					return "", "", false
				}
				if nameType == 0 {
					if h := safeObservedHostname(string(names[idx : idx+nameLen])); h != "" {
						return h, "tls-sni", false
					}
				}
				idx += nameLen
			}
			return "", "", false
		}
		pos += size
	}
	return "", "", false
}

func parseHTTPHostname(data []byte) (string, string, bool) {
	// Only HTTP request methods with a host field are supported. Server-first
	// protocols, HTTP/2 prefaces and arbitrary binary payloads are ignored.
	methods := []string{"GET ", "POST ", "HEAD ", "PUT ", "PATCH ", "DELETE ", "OPTIONS ", "CONNECT ", "TRACE "}
	matchesPrefix := false
	methodSeen := false
	for _, method := range methods {
		if bytes.HasPrefix(data, []byte(method)) {
			methodSeen = true
			break
		}
		if len(data) < len(method) && bytes.Equal(data, []byte(method[:len(data)])) {
			matchesPrefix = true
		}
	}
	if !methodSeen {
		return "", "", matchesPrefix
	}
	if len(data) >= maxHostnameProbe {
		return "", "", false
	}
	end := bytes.Index(data, []byte("\r\n\r\n"))
	if end < 0 {
		return "", "", true
	}
	lines := bytes.Split(data[:end], []byte("\r\n"))
	if len(lines) < 2 {
		return "", "", false
	}
	for _, line := range lines[1:] {
		pair := bytes.SplitN(line, []byte(":"), 2)
		if len(pair) == 2 && strings.EqualFold(string(pair[0]), "host") {
			host := strings.TrimSpace(string(pair[1]))
			if h, _, err := net.SplitHostPort(host); err == nil {
				host = h
			} else if strings.Count(host, ":") == 1 {
				name, port, ok := strings.Cut(host, ":")
				if ok {
					if _, err := strconv.Atoi(port); err != nil {
						return "", "", false
					}
					host = name
				}
			}
			if valid := safeObservedHostname(host); valid != "" {
				return valid, "http-host", false
			}
			return "", "", false
		}
	}
	return "", "", false
}

func safeObservedHostname(input string) string {
	host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(input), "."))
	if len(host) < 3 || len(host) > 253 || !strings.Contains(host, ".") {
		return ""
	}
	if net.ParseIP(host) != nil {
		return ""
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return ""
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9') && c != '-' {
				return ""
			}
		}
	}
	return host
}
