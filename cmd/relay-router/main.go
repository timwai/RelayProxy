package main

import (
	"context"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"relayproxy/agent/router"
	"relayproxy/internal/deviceidentity"
)

// Version is stamped by the dedicated Router build.
var Version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Printf("[Router] %v", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("relay-router", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	server := flags.String("server", "", "Relay Server address (host[:port] or IP[:port])")
	serverName := flags.String("server-name", "", "TLS SNI and certificate name; required when --server is an IP")
	identityID := flags.String("identity-id", "", "Server-issued identity ID")
	deviceName := flags.String("name", "", "Router device name")
	dataDir := flags.String("data-dir", "/etc/storage/relayproxy", "Persistent identity directory")
	caFile := flags.String("ca-file", "", "Additional PEM CA bundle (system roots are retained)")
	maxConnections := flags.Int("max-connections", 64, "Maximum concurrent exit streams (1-256)")
	connectTimeout := flags.Duration("connect-timeout", 10*time.Second, "Relay connection timeout")
	showVersion := flags.Bool("version", false, "Print version and exit")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage: relay-router --server host[:port] --identity-id ID --name NAME [options]")
		fmt.Fprintln(flags.Output(), "Minimal headless exit-only client. TLS 1.3 + yamux; TCP and stream-framed UDP; no E2EE.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *showVersion {
		fmt.Printf("relay-router %s (%s/%s)\n", Version, runtime.GOOS, runtime.GOARCH)
		return nil
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	if strings.TrimSpace(*server) == "" || strings.TrimSpace(*identityID) == "" || strings.TrimSpace(*deviceName) == "" {
		flags.Usage()
		return fmt.Errorf("--server, --identity-id and --name are required")
	}
	if *maxConnections < 1 || *maxConnections > 256 {
		return fmt.Errorf("--max-connections must be between 1 and 256")
	}
	if *connectTimeout <= 0 || *connectTimeout > 2*time.Minute {
		return fmt.Errorf("--connect-timeout must be greater than 0 and no more than 2m")
	}

	host, port, err := parseServerEndpoint(*server, 443)
	if err != nil {
		return err
	}
	name, err := resolveServerName(host, *serverName)
	if err != nil {
		return err
	}
	if strings.TrimSpace(*dataDir) == "" {
		return fmt.Errorf("--data-dir cannot be empty")
	}
	if err := os.MkdirAll(*dataDir, 0700); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	roots, err := loadRootCAs(*caFile)
	if err != nil {
		return err
	}
	identity, err := deviceidentity.LoadOrCreate(filepath.Join(*dataDir, "identity.json"))
	if err != nil {
		return fmt.Errorf("load device identity: %w", err)
	}

	client, err := router.New(router.Config{
		ServerAddress: host, ServerName: name, TCPPort: port,
		IdentityID: *identityID, DeviceName: *deviceName,
		Identity: identity, TLSRootCAs: roots,
		ConnectTimeout: *connectTimeout, MaxConnections: *maxConnections,
	})
	if err != nil {
		return fmt.Errorf("initialize Router: %w", err)
	}
	defer client.Close()

	log.Printf("[Router] RelayProxy Router %s starting", Version)
	log.Printf("[Router] Device: %s; identity: %s", *deviceName, *identityID)
	log.Printf("[Router] Relay endpoint: %s:%d; TLS server name: %s", host, port, name)
	log.Printf("[Router] Features: proxy.exit, TCP, UDP-over-stream, exit system DNS; TLS 1.3; no QUIC/P2P/E2EE")
	log.Printf("[Router] Local stream limit: %d", *maxConnections)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return client.Run(ctx)
}

func parseServerEndpoint(value string, defaultPort int) (string, int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", 0, fmt.Errorf("--server is required")
	}
	if defaultPort < 1 || defaultPort > 65535 {
		return "", 0, fmt.Errorf("invalid default port %d", defaultPort)
	}

	host := value
	port := defaultPort
	if h, p, err := net.SplitHostPort(value); err == nil {
		host = h
		parsed, parseErr := strconv.Atoi(p)
		if parseErr != nil || parsed < 1 || parsed > 65535 {
			return "", 0, fmt.Errorf("invalid Relay Server port %q", p)
		}
		port = parsed
	} else if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		host = strings.TrimSuffix(strings.TrimPrefix(value, "["), "]")
		if net.ParseIP(host) == nil {
			return "", 0, fmt.Errorf("invalid bracketed Relay Server address %q", value)
		}
	} else if strings.Count(value, ":") > 1 {
		if net.ParseIP(value) == nil {
			return "", 0, fmt.Errorf("invalid IPv6 Relay Server address %q; use [IPv6]:port when specifying a port", value)
		}
	} else if strings.Contains(value, ":") {
		return "", 0, fmt.Errorf("invalid Relay Server address %q; expected host:port", value)
	}
	if host == "" {
		return "", 0, fmt.Errorf("Relay Server host cannot be empty")
	}
	return host, port, nil
}

func resolveServerName(host, configured string) (string, error) {
	configured = strings.TrimSpace(configured)
	if configured != "" {
		return configured, nil
	}
	if net.ParseIP(host) != nil {
		return "", fmt.Errorf("--server-name is required when --server uses an IP address (TLS certificate verification remains enabled)")
	}
	return host, nil
}

func loadRootCAs(caFile string) (*x509.CertPool, error) {
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		roots = x509.NewCertPool()
		for _, path := range []string{
			"/etc/ssl/certs/ca-certificates.crt",
			"/etc/ssl/cert.pem",
			"/etc/ssl/certs/ca-bundle.crt",
		} {
			pem, readErr := os.ReadFile(path)
			if readErr == nil && roots.AppendCertsFromPEM(pem) {
				break
			}
		}
	}
	if caFile != "" {
		pem, readErr := os.ReadFile(caFile)
		if readErr != nil {
			return nil, fmt.Errorf("read additional CA bundle %q: %w", caFile, readErr)
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("additional CA bundle %q contains no valid certificates", caFile)
		}
	}
	return roots, nil
}
