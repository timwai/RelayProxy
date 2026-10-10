package main

// Compatibility probe built once against baseline main and once against the
// Brutal branch. It exercises the production gateway, transport and signed
// v5 identity control handshake without requiring an administrator database.
// No capability is granted beyond the isolated proxy.client test identity.
import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"flag"
	"fmt"
	"os"
	"reflect"
	"time"

	"relayproxy/internal/cert"
	"relayproxy/internal/deviceidentity"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
	"relayproxy/server/gateway"
	"relayproxy/server/session"
)

const testIdentityID = "a1b2c3d4e5f6g7h8"

func main() {
	role := flag.String("role", "", "server or client")
	transport := flag.String("transport", "quic", "quic or tls")
	addr := flag.String("addr", "", "server address for client")
	ready := flag.String("ready-file", "", "ready file for server")
	flag.Parse()

	var err error
	switch {
	case *role == "server" && *ready != "" && (*transport == "quic" || *transport == "tls"):
		err = runServer(*transport, *ready)
	case *role == "client" && *addr != "" && (*transport == "quic" || *transport == "tls"):
		err = runClient(*transport, *addr)
	default:
		err = fmt.Errorf("invalid role, transport or missing address/ready-file")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "compatibility probe failed:", err)
		os.Exit(1)
	}
}

func setOptionalUint(target any, name string, value uint64) {
	field := reflect.ValueOf(target).Elem().FieldByName(name)
	if field.IsValid() && field.CanSet() && field.Kind() == reflect.Uint64 {
		field.SetUint(value)
	}
}

func runServer(transport, ready string) error {
	certificate, err := cert.EnsureCertificate("", "", "localhost")
	if err != nil {
		return err
	}
	config := gateway.GatewayConfig{
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{certificate}},
		ServerInstanceID: "mixed-binary-probe",
		HandshakeTimeout: 5 * time.Second,
		ResolveIdentity: func(shortID string) (gateway.IdentityAuthorization, error) {
			if shortID != testIdentityID {
				return gateway.IdentityAuthorization{}, fmt.Errorf("unrecognized identity")
			}
			return gateway.IdentityAuthorization{IdentityID: testIdentityID, PolicyRevision: 1}, nil
		},
		AuthorizeIdentityDevice: func(_ string, _ protocol.DeviceHello, _ gateway.IdentityAuthorization) (gateway.DeviceAuthorization, error) {
			return gateway.DeviceAuthorization{
				State: "approved", DeviceID: "compat-client", IdentityID: testIdentityID,
				ApprovedCapabilities: []string{protocol.CapabilityProxyClient},
			}, nil
		},
		RecheckIdentityDevice: func(_, _, _ string) bool { return true },
	}
	if transport == "quic" {
		config.QUICAddr = "127.0.0.1:0"
	} else {
		config.TCPAddr = "127.0.0.1:0"
	}
	// Old baseline has no Brutal fields; reflection keeps exactly the same
	// probe source compilable against either dependency version.
	setOptionalUint(&config, "BrutalMaxUploadBPS", 10_000_000)
	setOptionalUint(&config, "BrutalMaxDownloadBPS", 10_000_000)
	sessions := session.NewManager()
	server := gateway.NewGateway(config, sessions, gateway.NewStreamRouter(sessions, nil, nil, nil))
	if err := server.Start(); err != nil {
		return err
	}
	defer server.Close()
	listen := server.QUICAddr()
	if transport == "tls" {
		listen = server.TCPAddr()
	}
	if listen == nil {
		return fmt.Errorf("no %s listener", transport)
	}
	if err := os.WriteFile(ready, []byte(listen.String()), 0600); err != nil {
		return err
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if registered, ok := sessions.Get("compat-client"); ok && registered != nil {
			fmt.Printf("server %s authenticated compat-client\n", transport)
			return nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for authenticated session")
}

func runClient(transport, addr string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 12 * time.Second)
	defer cancel()
	tlsConfig := &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13}
	var sess tunnel.TunnelSession
	var err error
	if transport == "quic" {
		sess, err = tunnel.DialQUIC(ctx, addr, tlsConfig, nil)
	} else {
		sess, err = tunnel.DialTLS(ctx, addr, tlsConfig, nil)
	}
	if err != nil {
		return err
	}
	defer sess.Close()
	control, err := sess.OpenStream(ctx)
	if err != nil {
		return err
	}
	defer control.Close()
	_ = control.SetDeadline(time.Now().Add(10 * time.Second))
	if err := protocol.WriteStreamHeader(control, &protocol.StreamHeader{
		Magic: protocol.MagicHeader, Version: protocol.CurrentVersion, Type: protocol.FrameTypeControl,
	}); err != nil {
		return err
	}
	identity, err := deviceidentity.Generate()
	if err != nil {
		return err
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	hello := protocol.DeviceHello{
		ProtocolVersion: protocol.DeviceProtocolVersion,
		IdentityID: testIdentityID, InstallationID: identity.InstallationID,
		PublicKey: identity.PublicKey, ClientNonce: nonce,
		DeviceName: "compat-probe", RequestedCapabilities: []string{protocol.CapabilityProxyClient},
	}
	setOptionalUint(&hello, "BrutalUploadBPS", 5_000_000)
	setOptionalUint(&hello, "BrutalDownloadBPS", 7_000_000)
	if err := protocol.WriteJSON(control, hello); err != nil {
		return err
	}
	var challenge protocol.AuthChallenge
	if err := protocol.ReadJSON(control, &challenge); err != nil {
		return err
	}
	if err := protocol.WriteJSON(control, protocol.AuthProof{
		ChallengeID: challenge.ChallengeID,
		Signature: identity.Sign(protocol.DeviceAuthPayload(hello, challenge)),
	}); err != nil {
		return err
	}
	var accepted protocol.DeviceAccepted
	if err := protocol.ReadJSON(control, &accepted); err != nil {
		return err
	}
	if !accepted.Success || accepted.DeviceID != "compat-client" {
		return fmt.Errorf("authorization failed: success=%v device=%q code=%q", accepted.Success, accepted.DeviceID, accepted.ErrorCode)
	}
	fmt.Printf("client %s authenticated by server\n", transport)
	// Give the gateway time to observe successful registration before
	// closing the transport. No traffic path or published product settings
	// are modified by this probe.
	time.Sleep(250 * time.Millisecond)
	return nil
}
