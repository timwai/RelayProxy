package e2ee

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
)

type testStream struct {
	net.Conn
}

func (s *testStream) CloseWrite() error { return nil }

type tamperStream struct {
	*testStream
	enabled atomic.Bool
	header  atomic.Bool
}

func (s *tamperStream) Write(p []byte) (int, error) {
	if s.enabled.Load() {
		if len(p) == 4 {
			s.header.Store(true)
		} else if s.header.Swap(false) {
			copyOf := append([]byte(nil), p...)
			copyOf[0] ^= 0x80
			s.enabled.Store(false)
			p = copyOf
		}
	}
	return s.testStream.Write(p)
}

type handshakeResult struct {
	stream *SecureStream
	err    error
}

func testIdentity(t *testing.T, deviceID string) Identity {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return Identity{DeviceID: deviceID, PublicKey: publicKey, PrivateKey: privateKey}
}

func completeHandshake(t *testing.T, clientStream, serverStream *testStream, clientIdentity, exitIdentity Identity) (*SecureStream, *SecureStream) {
	t.Helper()
	serverDone := make(chan handshakeResult, 1)
	go func() {
		stream, err := ServerHandshake(serverStream, ServerConfig{
			Local: exitIdentity, ClientDeviceID: clientIdentity.DeviceID,
			ClientPublicKey: clientIdentity.PublicKey,
		})
		serverDone <- handshakeResult{stream: stream, err: err}
	}()
	client, clientErr := ClientHandshake(clientStream, ClientConfig{
		Local: clientIdentity, ExitDeviceID: exitIdentity.DeviceID,
		ExitPublicKey: exitIdentity.PublicKey,
	})
	server := <-serverDone
	if clientErr != nil || server.err != nil {
		_ = clientStream.Close()
		_ = serverStream.Close()
		if client != nil {
			_ = client.Close()
		}
		if server.stream != nil {
			_ = server.stream.Close()
		}
		t.Fatalf("handshake failed: client=%v server=%v", clientErr, server.err)
	}
	return client, server.stream
}

func TestHandshakeAndSecureStreamRoundTrip(t *testing.T) {
	clientIdentity := testIdentity(t, "client-device")
	exitIdentity := testIdentity(t, "exit-device")
	clientConn, exitConn := net.Pipe()
	clientRaw, exitRaw := &testStream{Conn: clientConn}, &testStream{Conn: exitConn}
	client, exit := completeHandshake(t, clientRaw, exitRaw, clientIdentity, exitIdentity)
	defer clientRaw.Close()
	defer exitRaw.Close()

	payload := bytes.Repeat([]byte("RelayProxy end-to-end encrypted payload/"), 1200)
	writeDone := make(chan error, 1)
	go func() {
		n, err := client.Write(payload)
		if err == nil && n != len(payload) {
			err = io.ErrShortWrite
		}
		writeDone <- err
	}()
	received := make([]byte, len(payload))
	if _, err := io.ReadFull(exit, received); err != nil {
		t.Fatalf("Exit read: %v", err)
	}
	if err := <-writeDone; err != nil {
		t.Fatalf("Client write: %v", err)
	}
	if !bytes.Equal(received, payload) {
		t.Fatal("Client-to-Exit payload changed during encryption")
	}

	reply := []byte("response from Exit")
	replyDone := make(chan error, 1)
	go func() {
		_, err := exit.Write(reply)
		replyDone <- err
	}()
	gotReply := make([]byte, len(reply))
	if _, err := io.ReadFull(client, gotReply); err != nil {
		t.Fatalf("Client read: %v", err)
	}
	if err := <-replyDone; err != nil {
		t.Fatalf("Exit write: %v", err)
	}
	if !bytes.Equal(gotReply, reply) {
		t.Fatal("Exit-to-Client payload changed during encryption")
	}
}

func TestHandshakeRejectsWrongExitKey(t *testing.T) {
	clientIdentity := testIdentity(t, "client-device")
	exitIdentity := testIdentity(t, "exit-device")
	wrongExit := testIdentity(t, "other-exit")
	clientConn, exitConn := net.Pipe()
	clientRaw, exitRaw := &testStream{Conn: clientConn}, &testStream{Conn: exitConn}
	serverDone := make(chan error, 1)
	go func() {
		_, err := ServerHandshake(exitRaw, ServerConfig{
			Local: exitIdentity, ClientDeviceID: clientIdentity.DeviceID,
			ClientPublicKey: clientIdentity.PublicKey,
		})
		serverDone <- err
	}()
	_, clientErr := ClientHandshake(clientRaw, ClientConfig{
		Local: clientIdentity, ExitDeviceID: exitIdentity.DeviceID,
		ExitPublicKey: wrongExit.PublicKey,
	})
	if clientErr == nil || !errors.Is(clientErr, ErrHandshakeIdentity) {
		t.Fatalf("wrong Exit key error=%v, want ErrHandshakeIdentity", clientErr)
	}
	_ = clientRaw.Close()
	_ = exitRaw.Close()
	<-serverDone
}

func TestHandshakeRejectsWrongClientKeyAtExit(t *testing.T) {
	clientIdentity := testIdentity(t, "client-device")
	exitIdentity := testIdentity(t, "exit-device")
	wrongClient := testIdentity(t, "client-device")
	clientConn, exitConn := net.Pipe()
	clientRaw, exitRaw := &testStream{Conn: clientConn}, &testStream{Conn: exitConn}
	serverDone := make(chan error, 1)
	go func() {
		_, err := ServerHandshake(exitRaw, ServerConfig{
			Local: exitIdentity, ClientDeviceID: clientIdentity.DeviceID,
			ClientPublicKey: wrongClient.PublicKey,
		})
		serverDone <- err
	}()
	clientDone := make(chan error, 1)
	go func() {
		_, err := ClientHandshake(clientRaw, ClientConfig{
			Local: clientIdentity, ExitDeviceID: exitIdentity.DeviceID,
			ExitPublicKey: exitIdentity.PublicKey,
		})
		clientDone <- err
	}()
	serverErr := <-serverDone
	if !errors.Is(serverErr, ErrHandshakeIdentity) {
		t.Fatalf("Exit error=%v, want ErrHandshakeIdentity", serverErr)
	}
	_ = exitRaw.Close()
	clientErr := <-clientDone
	if clientErr == nil {
		t.Fatal("Client unexpectedly completed a handshake rejected by the Exit")
	}
	_ = clientRaw.Close()
}

func TestSecureStreamRejectsTamperedRecord(t *testing.T) {
	clientIdentity := testIdentity(t, "client-device")
	exitIdentity := testIdentity(t, "exit-device")
	clientConn, exitConn := net.Pipe()
	clientRaw := &tamperStream{testStream: &testStream{Conn: clientConn}}
	exitRaw := &testStream{Conn: exitConn}
	serverDone := make(chan handshakeResult, 1)
	go func() {
		stream, err := ServerHandshake(exitRaw, ServerConfig{
			Local: exitIdentity, ClientDeviceID: clientIdentity.DeviceID,
			ClientPublicKey: clientIdentity.PublicKey,
		})
		serverDone <- handshakeResult{stream: stream, err: err}
	}()
	client, err := ClientHandshake(clientRaw, ClientConfig{
		Local: clientIdentity, ExitDeviceID: exitIdentity.DeviceID,
		ExitPublicKey: exitIdentity.PublicKey,
	})
	server := <-serverDone
	if err != nil || server.err != nil {
		t.Fatalf("handshake failed: client=%v server=%v", err, server.err)
	}
	defer clientRaw.Close()
	defer exitRaw.Close()

	clientRaw.enabled.Store(true)
	writeDone := make(chan error, 1)
	go func() {
		_, writeErr := client.Write([]byte("must not be accepted"))
		writeDone <- writeErr
	}()
	_, readErr := server.stream.Read(make([]byte, 64))
	if readErr == nil {
		t.Fatal("tampered E2EE record was accepted")
	}
	<-writeDone
}

func TestSecureStreamAuthenticatedHalfCloseIsStickyEOF(t *testing.T) {
	clientConn, exitConn := net.Pipe()
	keyA, keyB := bytes.Repeat([]byte{0x11}, 32), bytes.Repeat([]byte{0x22}, 32)
	client, err := NewSecureStream(&testStream{Conn: clientConn}, keyA, keyB)
	if err != nil {
		t.Fatal(err)
	}
	exit, err := NewSecureStream(&testStream{Conn: exitConn}, keyB, keyA)
	if err != nil {
		t.Fatal(err)
	}
	defer clientConn.Close()
	defer exitConn.Close()

	// net.Pipe has no buffering: a synchronous CloseWrite cannot complete
	// until the peer has read the authenticated close record.
	closeDone := make(chan error, 1)
	go func() { closeDone <- client.CloseWrite() }()
	if _, err := exit.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("first read after authenticated close=%v, want EOF", err)
	}
	if _, err := exit.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("subsequent read after authenticated close=%v, want sticky EOF", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("write authenticated close record: %v", err)
	}
}

func TestSecureStreamFullCloseDoesNotWaitForPeerRead(t *testing.T) {
	clientConn, exitConn := net.Pipe()
	defer exitConn.Close()
	client, err := NewSecureStream(&testStream{Conn: clientConn},
		bytes.Repeat([]byte{0x11}, 32), bytes.Repeat([]byte{0x22}, 32))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- client.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("close aborted E2EE stream: %v", err)
		}
	case <-time.After(2 * time.Second):
		_ = exitConn.Close()
		t.Fatal("full Close blocked waiting for unread authenticated close record")
	}
}

func TestSecureStreamRejectsOversizedAndTruncatedRecords(t *testing.T) {
	t.Run("oversized", func(t *testing.T) {
		clientConn, exitConn := net.Pipe()
		exit, err := NewSecureStream(&testStream{Conn: exitConn}, bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32))
		if err != nil {
			t.Fatal(err)
		}
		defer exitConn.Close()
		go func() {
			var header [4]byte
			binary.BigEndian.PutUint32(header[:], uint32(MaxRecordPlaintext+chachaOverhead()+1))
			_, _ = clientConn.Write(header[:])
		}()
		if _, err := exit.Read(make([]byte, 1)); !errors.Is(err, ErrRecordLength) {
			t.Fatalf("oversized record error=%v, want ErrRecordLength", err)
		}
		_ = clientConn.Close()
	})
	t.Run("truncated", func(t *testing.T) {
		clientConn, exitConn := net.Pipe()
		exit, err := NewSecureStream(&testStream{Conn: exitConn}, bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32))
		if err != nil {
			t.Fatal(err)
		}
		defer exitConn.Close()
		go func() {
			var header [4]byte
			binary.BigEndian.PutUint32(header[:], uint32(chachaOverhead()+8))
			_, _ = clientConn.Write(header[:])
			_, _ = clientConn.Write([]byte{1, 2})
			_ = clientConn.Close()
		}()
		if _, err := exit.Read(make([]byte, 1)); err == nil {
			t.Fatal("truncated record was accepted")
		}
	})
}

func TestSecureStreamRejectsReplayedAndOutOfOrderRecords(t *testing.T) {
	key := bytes.Repeat([]byte{0x51}, chacha20poly1305.KeySize)
	makeRecord := func(sequence uint64, plaintext []byte) []byte {
		aead, err := chacha20poly1305.New(key)
		if err != nil {
			t.Fatal(err)
		}
		var header [4]byte
		binary.BigEndian.PutUint32(header[:], uint32(len(plaintext)+aead.Overhead()))
		ciphertext := aead.Seal(nil, recordNonce(sequence, aead.NonceSize()), plaintext, header[:])
		return append(append([]byte(nil), header[:]...), ciphertext...)
	}

	for _, tc := range []struct {
		name  string
		wire  []byte
		first []byte
	}{
		{name: "out-of-order", wire: makeRecord(1, []byte("sequence one"))},
		{name: "replay", wire: append(makeRecord(0, []byte("first")), makeRecord(0, []byte("replayed"))...), first: []byte("first")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clientConn, exitConn := net.Pipe()
			defer clientConn.Close()
			defer exitConn.Close()
			secure, err := NewSecureStream(&testStream{Conn: exitConn}, key, key)
			if err != nil {
				t.Fatal(err)
			}
			writeDone := make(chan error, 1)
			go func() { writeDone <- writeAll(clientConn, tc.wire) }()

			if len(tc.first) > 0 {
				got := make([]byte, len(tc.first))
				if _, err := io.ReadFull(secure, got); err != nil {
					t.Fatalf("read first valid record: %v", err)
				}
				if !bytes.Equal(got, tc.first) {
					t.Fatalf("first record = %q, want %q", got, tc.first)
				}
			}
			if _, err := secure.Read(make([]byte, 32)); err == nil {
				t.Fatal("replayed or out-of-order E2EE record was accepted")
			}
			if err := <-writeDone; err != nil {
				t.Fatalf("write crafted E2EE records: %v", err)
			}
		})
	}
}

func FuzzReadHandshakeMessage(f *testing.F) {
	f.Add([]byte{0, 0, 0, 0})
	f.Add([]byte{0, 0, 0, 1, '{'})
	f.Add([]byte{0, 0, 0, 2, '{', '}'})
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > MaxHandshakeMessage+4 {
			return
		}
		var message clientHello
		_ = readHandshakeMessage(bytes.NewReader(raw), &message)
	})
}

func chachaOverhead() int { return 16 }
