package main

// Test-only fixture for real packaged Agent/Server process pairing.
// Never use on real credentials or a production database. This creates one
// normal approved, identity-bound Agent with real ed25519 installation keys.
import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"relayproxy/internal/deviceidentity"
	"relayproxy/server/repository"
)

func main() {
	dbPath := flag.String("db", "", "isolated test SQLite file")
	agentDir := flag.String("agent-dir", "", "isolated agent config and identity directory")
	quicPort := flag.Int("quic-port", 0, "loopback server UDP port")
	tcpPort := flag.Int("tcp-port", 0, "loopback server TLS port")
	mode := flag.String("mode", "quic_only", "quic_only or tcp_only")
	flag.Parse()
	if *dbPath == "" || *agentDir == "" || *quicPort < 1024 || *tcpPort < 1024 ||
		(*mode != "quic_only" && *mode != "tcp_only") {
		fmt.Fprintln(os.Stderr, "usage: compat_seed --db DB --agent-dir DIR --quic-port PORT --tcp-port PORT --mode MODE")
		os.Exit(2)
	}
	if err := run(*dbPath, *agentDir, *quicPort, *tcpPort, *mode); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(dbPath, dir string, quicPort, tcpPort int, mode string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	db, err := repository.OpenDB("sqlite", dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	identity, err := db.CreateIdentityWithLogin("compat-owner", "Compatibility CI", "compat-ci-admin", "disabled-login-hash")
	if err != nil {
		return err
	}
	installation, err := deviceidentity.LoadOrCreate(filepath.Join(dir, "device-identity.json"))
	if err != nil {
		return err
	}
	authorized, err := db.ResolveIdentity(identity.ShortID)
	if err != nil {
		return err
	}
	observed, err := db.ObserveIdentityDevice(*authorized, repository.DeviceIdentityObservation{
		Fingerprint: installation.Fingerprint(), InstallationID: installation.InstallationID,
		PublicKey: installation.PublicKey, DeviceName: "compat-full-agent", Platform: "linux",
		Arch: "amd64", ClientVersion: "compat-binary-test",
		RequestedCapabilities: []string{"proxy.client"},
	})
	if err != nil {
		return err
	}
	if observed.RequestID == "" {
		return fmt.Errorf("no pending enrollment request; refusing to fake approval")
	}
	approved, err := db.ApproveEnrollmentForIdentity(observed.RequestID, "compat-ci-admin", identity.ID, []string{"proxy.client"})
	if err != nil {
		return err
	}
	if approved.ID == "" {
		return fmt.Errorf("approved device has no ID")
	}
	config := fmt.Sprintf("server:\n  address: 127.0.0.1\n  quic_port: %d\n  tcp_port: %d\n  insecure_tls: true\n"+
		"device:\n  name: compat-full-agent\n  identity_id: %s\n"+
		"transport:\n  mode: %s\n  bandwidth:\n    up_mbps: 0\n    down_mbps: 0\n"+
		"p2p:\n  enabled: false\n  mode: relay_only\n"+
		"direct:\n  public:\n    advertise: \"\"\n"+
		"proxy:\n  socks5:\n    enabled: true\n    listen: 127.0.0.1\n    port: 0\n  http:\n    enabled: false\n", quicPort, tcpPort, identity.ShortID, mode)
	if err := os.WriteFile(filepath.Join(dir, "relay-agent.yaml"), []byte(config), 0600); err != nil {
		return err
	}
	fmt.Printf("seeded identity=%s device=%s transport=%s\n", identity.ShortID, approved.ID, mode)
	return nil
}
