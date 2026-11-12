package e2ee

import (
	"bytes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"

	"relayproxy/internal/tunnel"
)

const (
	ProtocolVersion      = 1
	MaxHandshakeMessage  = 4 << 10
	MaxRecordPlaintext   = 16 << 10
	handshakeDomain      = "RelayProxy E2EE handshake v1\x00"
	clientHelloDomain    = "RelayProxy E2EE client hello v1\x00"
	serverHelloDomain    = "RelayProxy E2EE server hello v1\x00"
	trafficKeyInfo       = "RelayProxy E2EE traffic keys v1"
	clientFinishedDomain = "RelayProxy E2EE client finished v1\x00"
	serverFinishedDomain = "RelayProxy E2EE server finished v1\x00"
)

var (
	ErrHandshakeIdentity  = errors.New("E2EE peer identity mismatch")
	ErrHandshakeSignature = errors.New("E2EE handshake signature verification failed")
	ErrHandshakeFinished  = errors.New("E2EE key confirmation failed")
	ErrRecordSequence     = errors.New("E2EE record sequence exhausted")
	ErrRecordLength       = errors.New("invalid E2EE record length")
)

// Identity is the persistent Ed25519 identity of one RelayProxy installation.
// The signing key is never reused as an X25519 encryption key.
type Identity struct {
	DeviceID   string
	PublicKey  ed25519.PublicKey
	PrivateKey ed25519.PrivateKey
}

// ClientConfig authenticates the selected Exit. ExitPublicKey must come from
// the authenticated, authorization-filtered Exit inventory and should also
// be checked against the caller's local peer-key pin store.
type ClientConfig struct {
	Local         Identity
	ExitDeviceID  string
	ExitPublicKey ed25519.PublicKey
}

// ServerConfig authenticates the connecting Client. ClientPublicKey is
// optional for direct paths where the peer key is first learned in the signed
// handshake; Relay paths should pass the key from the authenticated session.
type ServerConfig struct {
	Local           Identity
	ClientDeviceID  string
	ClientPublicKey ed25519.PublicKey
}

type clientHello struct {
	Version      int    `json:"version"`
	ClientID     string `json:"clientId"`
	ExitID       string `json:"exitId"`
	PublicKey    []byte `json:"publicKey"`
	EphemeralKey []byte `json:"ephemeralKey"`
	Nonce        []byte `json:"nonce"`
	Signature    []byte `json:"signature"`
}

type serverHello struct {
	Version      int    `json:"version"`
	ClientID     string `json:"clientId"`
	ExitID       string `json:"exitId"`
	PublicKey    []byte `json:"publicKey"`
	EphemeralKey []byte `json:"ephemeralKey"`
	Nonce        []byte `json:"nonce"`
	Signature    []byte `json:"signature"`
}

type finishedMessage struct {
	MAC []byte `json:"mac"`
}

// ClientHandshake performs mutual device-identity authentication, ephemeral
// X25519 key agreement and key confirmation before returning an encrypted
// TunnelStream. The underlying stream must already be admitted to the selected
// Exit; this handshake does not replace Server authorization.
func ClientHandshake(stream tunnel.TunnelStream, cfg ClientConfig) (*SecureStream, error) {
	if stream == nil {
		return nil, errors.New("E2EE client handshake requires a stream")
	}
	if err := validateIdentity(cfg.Local); err != nil {
		return nil, fmt.Errorf("validate local E2EE identity: %w", err)
	}
	if !validDeviceID(cfg.ExitDeviceID) || len(cfg.ExitPublicKey) != ed25519.PublicKeySize {
		return nil, errors.New("E2EE client requires an Exit device ID and Ed25519 public key")
	}

	curve := ecdh.X25519()
	ephemeral, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate E2EE client ephemeral key: %w", err)
	}
	clientNonce := make([]byte, 32)
	if _, err := rand.Read(clientNonce); err != nil {
		return nil, fmt.Errorf("generate E2EE client nonce: %w", err)
	}

	hello := clientHello{
		Version: ProtocolVersion, ClientID: cfg.Local.DeviceID, ExitID: cfg.ExitDeviceID,
		PublicKey:    append([]byte(nil), cfg.Local.PublicKey...),
		EphemeralKey: ephemeral.PublicKey().Bytes(), Nonce: clientNonce,
	}
	hello.Signature = ed25519.Sign(cfg.Local.PrivateKey, clientHelloPayload(hello))
	if err := writeHandshakeMessage(stream, hello); err != nil {
		return nil, fmt.Errorf("send E2EE client hello: %w", err)
	}

	var response serverHello
	if err := readHandshakeMessage(stream, &response); err != nil {
		return nil, fmt.Errorf("read E2EE server hello: %w", err)
	}
	if response.Version != ProtocolVersion || response.ClientID != cfg.Local.DeviceID ||
		response.ExitID != cfg.ExitDeviceID || len(response.PublicKey) != ed25519.PublicKeySize ||
		len(response.EphemeralKey) != 32 || len(response.Nonce) != 32 ||
		!bytes.Equal(response.PublicKey, cfg.ExitPublicKey) {
		return nil, ErrHandshakeIdentity
	}
	if !ed25519.Verify(cfg.ExitPublicKey, serverHelloPayload(hello, response), response.Signature) {
		return nil, ErrHandshakeSignature
	}

	peerEphemeral, err := curve.NewPublicKey(response.EphemeralKey)
	if err != nil {
		return nil, fmt.Errorf("parse E2EE Exit ephemeral key: %w", err)
	}
	shared, err := ephemeral.ECDH(peerEphemeral)
	if err != nil {
		return nil, fmt.Errorf("derive E2EE shared secret: %w", err)
	}
	transcript := transcriptHash(hello, response)
	clientToExit, exitToClient, err := deriveTrafficKeys(shared, transcript)
	if err != nil {
		return nil, err
	}

	if err := writeHandshakeMessage(stream, finishedMessage{MAC: finishedMAC(clientToExit, clientFinishedDomain, transcript)}); err != nil {
		return nil, fmt.Errorf("send E2EE client key confirmation: %w", err)
	}
	var finish finishedMessage
	if err := readHandshakeMessage(stream, &finish); err != nil {
		return nil, fmt.Errorf("read E2EE Exit key confirmation: %w", err)
	}
	if !hmac.Equal(finish.MAC, finishedMAC(exitToClient, serverFinishedDomain, transcript)) {
		return nil, ErrHandshakeFinished
	}
	return NewSecureStream(stream, clientToExit, exitToClient)
}

// ServerHandshake authenticates the connecting Client and completes the same
// transcript-bound key agreement. A Relay-routed caller should provide the
// Client public key from its authenticated session. A direct-path caller may
// pass nil and must apply local TOFU pinning before accepting the peer.
func ServerHandshake(stream tunnel.TunnelStream, cfg ServerConfig) (*SecureStream, error) {
	if stream == nil {
		return nil, errors.New("E2EE server handshake requires a stream")
	}
	if err := validateIdentity(cfg.Local); err != nil {
		return nil, fmt.Errorf("validate local E2EE identity: %w", err)
	}
	if !validDeviceID(cfg.ClientDeviceID) {
		return nil, errors.New("E2EE server requires the authenticated Client device ID")
	}
	if len(cfg.ClientPublicKey) != 0 && len(cfg.ClientPublicKey) != ed25519.PublicKeySize {
		return nil, errors.New("E2EE expected Client public key has an invalid length")
	}

	var hello clientHello
	if err := readHandshakeMessage(stream, &hello); err != nil {
		return nil, fmt.Errorf("read E2EE client hello: %w", err)
	}
	if hello.Version != ProtocolVersion || hello.ClientID != cfg.ClientDeviceID ||
		hello.ExitID != cfg.Local.DeviceID || len(hello.PublicKey) != ed25519.PublicKeySize ||
		len(hello.EphemeralKey) != 32 || len(hello.Nonce) != 32 ||
		(len(cfg.ClientPublicKey) != 0 && !bytes.Equal(hello.PublicKey, cfg.ClientPublicKey)) {
		return nil, ErrHandshakeIdentity
	}
	if !ed25519.Verify(ed25519.PublicKey(hello.PublicKey), clientHelloPayload(hello), hello.Signature) {
		return nil, ErrHandshakeSignature
	}

	curve := ecdh.X25519()
	ephemeral, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate E2EE Exit ephemeral key: %w", err)
	}
	serverNonce := make([]byte, 32)
	if _, err := rand.Read(serverNonce); err != nil {
		return nil, fmt.Errorf("generate E2EE Exit nonce: %w", err)
	}
	response := serverHello{
		Version: ProtocolVersion, ClientID: hello.ClientID, ExitID: cfg.Local.DeviceID,
		PublicKey:    append([]byte(nil), cfg.Local.PublicKey...),
		EphemeralKey: ephemeral.PublicKey().Bytes(), Nonce: serverNonce,
	}
	response.Signature = ed25519.Sign(cfg.Local.PrivateKey, serverHelloPayload(hello, response))
	if err := writeHandshakeMessage(stream, response); err != nil {
		return nil, fmt.Errorf("send E2EE server hello: %w", err)
	}

	peerEphemeral, err := curve.NewPublicKey(hello.EphemeralKey)
	if err != nil {
		return nil, fmt.Errorf("parse E2EE Client ephemeral key: %w", err)
	}
	shared, err := ephemeral.ECDH(peerEphemeral)
	if err != nil {
		return nil, fmt.Errorf("derive E2EE shared secret: %w", err)
	}
	transcript := transcriptHash(hello, response)
	clientToExit, exitToClient, err := deriveTrafficKeys(shared, transcript)
	if err != nil {
		return nil, err
	}

	var finish finishedMessage
	if err := readHandshakeMessage(stream, &finish); err != nil {
		return nil, fmt.Errorf("read E2EE Client key confirmation: %w", err)
	}
	if !hmac.Equal(finish.MAC, finishedMAC(clientToExit, clientFinishedDomain, transcript)) {
		return nil, ErrHandshakeFinished
	}
	if err := writeHandshakeMessage(stream, finishedMessage{MAC: finishedMAC(exitToClient, serverFinishedDomain, transcript)}); err != nil {
		return nil, fmt.Errorf("send E2EE Exit key confirmation: %w", err)
	}
	return NewSecureStream(stream, exitToClient, clientToExit)
}

// SecureStream encrypts each record independently and preserves TunnelStream
// deadlines and half-close behavior. Separate directional keys permit both
// directions to start their nonce sequence at zero safely.
type SecureStream struct {
	stream tunnel.TunnelStream
	tx     cipher.AEAD
	rx     cipher.AEAD

	readMu   sync.Mutex
	writeMu  sync.Mutex
	readBuf  []byte
	readEnd  bool
	sendSeq  uint64
	recvSeq  uint64
	writeEnd bool

	closeOnce sync.Once
	closeErr  error
}

func NewSecureStream(stream tunnel.TunnelStream, sendKey, receiveKey []byte) (*SecureStream, error) {
	if stream == nil || len(sendKey) != chacha20poly1305.KeySize || len(receiveKey) != chacha20poly1305.KeySize {
		return nil, errors.New("E2EE stream requires a transport stream and two 32-byte directional keys")
	}
	tx, err := chacha20poly1305.New(sendKey)
	if err != nil {
		return nil, fmt.Errorf("create E2EE send cipher: %w", err)
	}
	rx, err := chacha20poly1305.New(receiveKey)
	if err != nil {
		return nil, fmt.Errorf("create E2EE receive cipher: %w", err)
	}
	return &SecureStream{stream: stream, tx: tx, rx: rx}, nil
}

func (s *SecureStream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	s.readMu.Lock()
	defer s.readMu.Unlock()
	if s.readEnd {
		return 0, io.EOF
	}
	if len(s.readBuf) == 0 {
		plain, err := s.readRecord()
		if err != nil {
			return 0, err
		}
		if len(plain) == 0 {
			s.readEnd = true
			return 0, io.EOF
		}
		s.readBuf = plain
	}
	n := copy(p, s.readBuf)
	s.readBuf = s.readBuf[n:]
	return n, nil
}

func (s *SecureStream) readRecord() ([]byte, error) {
	var header [4]byte
	n, err := io.ReadFull(s.stream, header[:])
	if err != nil {
		if err == io.EOF && n == 0 {
			return nil, io.ErrUnexpectedEOF
		}
		return nil, fmt.Errorf("read E2EE record header: %w", err)
	}
	length := uint64(binary.BigEndian.Uint32(header[:]))
	minimum := uint64(s.rx.Overhead())
	maximum := uint64(MaxRecordPlaintext + s.rx.Overhead())
	if length < minimum || length > maximum {
		return nil, ErrRecordLength
	}
	ciphertext := make([]byte, int(length))
	if _, err := io.ReadFull(s.stream, ciphertext); err != nil {
		return nil, fmt.Errorf("read E2EE record body: %w", err)
	}
	if s.recvSeq == ^uint64(0) {
		return nil, ErrRecordSequence
	}
	plain, err := s.rx.Open(nil, recordNonce(s.recvSeq, s.rx.NonceSize()), ciphertext, header[:])
	if err != nil {
		return nil, fmt.Errorf("authenticate E2EE record: %w", err)
	}
	s.recvSeq++
	return plain, nil
}

func (s *SecureStream) Write(p []byte) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.writeEnd {
		return 0, errors.New("write on closed E2EE stream")
	}
	total := 0
	for len(p) > 0 {
		size := min(len(p), MaxRecordPlaintext)
		if err := s.writeRecord(p[:size]); err != nil {
			return total, err
		}
		total += size
		p = p[size:]
	}
	return total, nil
}

func (s *SecureStream) writeRecord(plain []byte) error {
	if s.sendSeq == ^uint64(0) {
		return ErrRecordSequence
	}
	length := uint32(len(plain) + s.tx.Overhead())
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], length)
	ciphertext := s.tx.Seal(nil, recordNonce(s.sendSeq, s.tx.NonceSize()), plain, header[:])
	if err := writeAll(s.stream, header[:]); err != nil {
		return fmt.Errorf("write E2EE record header: %w", err)
	}
	if err := writeAll(s.stream, ciphertext); err != nil {
		return fmt.Errorf("write E2EE record body: %w", err)
	}
	s.sendSeq++
	return nil
}

func (s *SecureStream) CloseWrite() error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.writeEnd {
		return nil
	}
	if err := s.writeRecord(nil); err != nil {
		return err
	}
	s.writeEnd = true
	return s.stream.CloseWrite()
}

func (s *SecureStream) Close() error {
	s.closeOnce.Do(func() {
		if err := s.CloseWrite(); err != nil {
			s.closeErr = err
		}
		if err := s.stream.Close(); s.closeErr == nil {
			s.closeErr = err
		}
	})
	return s.closeErr
}

func (s *SecureStream) SetDeadline(t time.Time) error      { return s.stream.SetDeadline(t) }
func (s *SecureStream) SetReadDeadline(t time.Time) error  { return s.stream.SetReadDeadline(t) }
func (s *SecureStream) SetWriteDeadline(t time.Time) error { return s.stream.SetWriteDeadline(t) }

func validateIdentity(identity Identity) error {
	if !validDeviceID(identity.DeviceID) || len(identity.PublicKey) != ed25519.PublicKeySize ||
		len(identity.PrivateKey) != ed25519.PrivateKeySize {
		return errors.New("incomplete Ed25519 device identity")
	}
	if !bytes.Equal(identity.PrivateKey[ed25519.SeedSize:], identity.PublicKey) {
		return errors.New("Ed25519 public and private keys do not match")
	}
	return nil
}

func validDeviceID(value string) bool {
	return value != "" && len(value) <= 128 && bytes.IndexByte([]byte(value), 0) < 0
}

func clientHelloPayload(hello clientHello) []byte {
	var out bytes.Buffer
	out.WriteString(clientHelloDomain)
	_ = binary.Write(&out, binary.BigEndian, uint32(hello.Version))
	writeField(&out, []byte(hello.ClientID))
	writeField(&out, []byte(hello.ExitID))
	writeField(&out, hello.PublicKey)
	writeField(&out, hello.EphemeralKey)
	writeField(&out, hello.Nonce)
	return out.Bytes()
}

func serverHelloFields(hello serverHello) []byte {
	var out bytes.Buffer
	out.WriteString(serverHelloDomain)
	_ = binary.Write(&out, binary.BigEndian, uint32(hello.Version))
	writeField(&out, []byte(hello.ClientID))
	writeField(&out, []byte(hello.ExitID))
	writeField(&out, hello.PublicKey)
	writeField(&out, hello.EphemeralKey)
	writeField(&out, hello.Nonce)
	return out.Bytes()
}

func serverHelloPayload(client clientHello, server serverHello) []byte {
	var out bytes.Buffer
	out.WriteString(handshakeDomain)
	writeField(&out, clientHelloPayload(client))
	writeField(&out, client.Signature)
	writeField(&out, serverHelloFields(server))
	return out.Bytes()
}

func transcriptHash(client clientHello, server serverHello) []byte {
	var out bytes.Buffer
	writeField(&out, clientHelloPayload(client))
	writeField(&out, client.Signature)
	writeField(&out, serverHelloFields(server))
	writeField(&out, server.Signature)
	sum := sha256.Sum256(out.Bytes())
	return sum[:]
}

func deriveTrafficKeys(shared, transcript []byte) ([]byte, []byte, error) {
	reader := hkdf.New(sha256.New, shared, transcript, []byte(trafficKeyInfo))
	material := make([]byte, chacha20poly1305.KeySize*2)
	if _, err := io.ReadFull(reader, material); err != nil {
		return nil, nil, fmt.Errorf("derive E2EE traffic keys: %w", err)
	}
	return material[:chacha20poly1305.KeySize], material[chacha20poly1305.KeySize:], nil
}

func finishedMAC(key []byte, domain string, transcript []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(domain))
	_, _ = mac.Write(transcript)
	return mac.Sum(nil)
}

func writeField(out *bytes.Buffer, value []byte) {
	_ = binary.Write(out, binary.BigEndian, uint32(len(value)))
	_, _ = out.Write(value)
}

func writeHandshakeMessage(stream io.Writer, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(payload) == 0 || len(payload) > MaxHandshakeMessage {
		return errors.New("E2EE handshake message exceeds size limit")
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	if err := writeAll(stream, header[:]); err != nil {
		return err
	}
	return writeAll(stream, payload)
}

func readHandshakeMessage(stream io.Reader, value any) error {
	var header [4]byte
	if _, err := io.ReadFull(stream, header[:]); err != nil {
		return err
	}
	length := binary.BigEndian.Uint32(header[:])
	if length == 0 || length > MaxHandshakeMessage {
		return errors.New("invalid E2EE handshake message length")
	}
	payload := make([]byte, int(length))
	if _, err := io.ReadFull(stream, payload); err != nil {
		return err
	}
	if err := json.Unmarshal(payload, value); err != nil {
		return fmt.Errorf("decode E2EE handshake message: %w", err)
	}
	return nil
}

func recordNonce(sequence uint64, size int) []byte {
	nonce := make([]byte, size)
	copy(nonce[:4], []byte("RPX1"))
	binary.BigEndian.PutUint64(nonce[size-8:], sequence)
	return nonce
}

func writeAll(writer io.Writer, value []byte) error {
	for len(value) > 0 {
		n, err := writer.Write(value)
		if n > 0 {
			value = value[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrNoProgress
		}
	}
	return nil
}
