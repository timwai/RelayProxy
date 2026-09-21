package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"relayproxy/internal/acl"
	"relayproxy/internal/config"
	"relayproxy/internal/protocol"
	"relayproxy/server/api"
	serverdesktop "relayproxy/server/desktop"
	"relayproxy/server/gateway"
	serverrdp "relayproxy/server/rdp"
	"relayproxy/server/repository"
	"relayproxy/server/service"
	"relayproxy/server/session"
)

func main() {
	configPath := flag.String("config", "configs/relay-server.yaml", "Path to configuration file")
	flag.Parse()
	startOptionalPprof()

	log.Println("==================================================")
	log.Println("      RelayProxy Server v1.0.0 Starting...        ")
	log.Println("==================================================")

	// 1. Load configuration
	cfg, err := config.LoadServerConfig(*configPath)
	if err != nil {
		log.Fatalf("[Config] Failed to load config from %s: %v", *configPath, err)
	}
	resolvedConfigPath, err := filepath.Abs(*configPath)
	if err != nil {
		log.Fatalf("[Config] Failed to resolve config path: %v", err)
	}
	log.Printf("[Config] Loaded %s", resolvedConfigPath)

	// Validate the selected certificate before creating database or listeners.
	tlsConfig, err := loadServerTLS(cfg)
	if err != nil {
		log.Fatalf("[Cert] Failed to load TLS certificate: %v", err)
	}

	// 2. Initialize Database (V1: SQLite)
	if cfg.Database.Driver != "sqlite" && cfg.Database.Driver != "sqlite3" {
		log.Fatalf("[DB] V1 only supports sqlite, got driver=%q", cfg.Database.Driver)
	}
	db, err := repository.OpenDB(cfg.Database.Driver, cfg.Database.DSN)
	if err != nil {
		log.Fatalf("[DB] Failed to connect database: %v", err)
	}
	defer db.Close()
	log.Println("[DB] SQLite connected with the server-approval schema.")

	// Keep audit batches on their own single-connection SQLite handle. WAL
	// allows reads on the primary handle to continue while an audit transaction
	// commits, without weakening the per-handle PRAGMA guarantees in OpenDB.
	auditDB := db
	if !sqliteDSNIsMemory(cfg.Database.DSN) {
		separateAuditDB, auditErr := repository.OpenDB(cfg.Database.Driver, cfg.Database.DSN)
		if auditErr != nil {
			log.Printf("[Audit] Dedicated SQLite handle unavailable, sharing primary DB: %v", auditErr)
		} else {
			auditDB = separateAuditDB
			defer separateAuditDB.Close()
			log.Println("[Audit] Dedicated SQLite WAL handle enabled.")
		}
	}

	serverInstanceID, err := db.ServerInstanceID()
	if err != nil {
		log.Fatalf("[DB] Failed to load server instance identity: %v", err)
	}

	// 4. Initialize Services & Session Manager
	authService := service.NewAuthService(db)
	deviceService := service.NewDeviceService(db)
	sessionMgr := session.NewManager()

	relayACL, err := acl.NewChecker(cfg.RelayPolicy())
	if err != nil {
		log.Fatalf("[ACL] Invalid relay policy: %v", err)
	}
	rendezvous, err := serverrdp.StartRendezvous(context.Background(), cfg.RDP.RendezvousListen, cfg.RDP.Ingress.RateLimitPerMin)
	if err != nil {
		log.Fatalf("[RDP] Failed to start rendezvous: %v", err)
	}
	if rendezvous != nil {
		defer rendezvous.Close()
	}
	rendezvousAddress := cfg.RDP.RendezvousAdvertise
	if rendezvousAddress == "" && cfg.RDP.RendezvousListen != "" {
		if host, port, splitErr := net.SplitHostPort(cfg.RDP.RendezvousListen); splitErr == nil && host != "" && host != "0.0.0.0" && host != "::" {
			rendezvousAddress = net.JoinHostPort(host, port)
		}
	}
	coordinator := serverrdp.NewCoordinator(sessionMgr, db, time.Duration(cfg.RDP.LeaseSec)*time.Second, rendezvousAddress)
	coordinator.Start(context.Background())
	defer coordinator.Close()
	desktopCoordinator := serverdesktop.NewCoordinator(sessionMgr, db.AuthorizeRDP, time.Duration(cfg.RDP.LeaseSec)*time.Second)
	desktopCoordinator.Start(context.Background())
	defer desktopCoordinator.Close()

	// Async audit writer (N5): bounded channel + background insert
	auditCh := make(chan *repository.ConnectionAudit, 2048)
	auditDone := make(chan struct{})
	var lastAuditErrorLog, lastAuditDropLog atomic.Int64
	logAuditRateLimited := func(last *atomic.Int64, format string, args ...any) {
		now := time.Now().UnixNano()
		previous := last.Load()
		if now-previous >= int64(time.Second) && last.CompareAndSwap(previous, now) {
			log.Printf(format, args...)
		}
	}
	go func() {
		defer close(auditDone)
		const batchSize = 100
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		batch := make([]*repository.ConnectionAudit, 0, batchSize)
		flush := func() {
			if len(batch) == 0 {
				return
			}
			if err := auditDB.InsertConnectionAudits(batch); err != nil {
				logAuditRateLimited(&lastAuditErrorLog, "[Audit] Failed to log connection batch: %v", err)
			}
			batch = batch[:0]
		}
		for {
			select {
			case a, ok := <-auditCh:
				if !ok {
					flush()
					return
				}
				if a != nil {
					batch = append(batch, a)
				}
				if len(batch) >= batchSize {
					flush()
				}
			case <-ticker.C:
				flush()
			}
		}
	}()

	router := gateway.NewStreamRouter(
		sessionMgr,
		relayACL,
		func(clientDeviceID, exitDeviceID string) (bool, error) {
			return db.AuthorizeClientExit(clientDeviceID, exitDeviceID)
		},
		func(audit *repository.ConnectionAudit) {
			select {
			case auditCh <- audit:
			default:
				logAuditRateLimited(&lastAuditDropLog, "[Audit] queue full, dropping audit records")
			}
		},
	)
	router.SetRDPChecker(func(controllerDeviceID, targetDeviceID string) (bool, error) {
		return db.AuthorizeRDP(controllerDeviceID, targetDeviceID)
	})
	router.SetRDPControlHandler(coordinator.HandleControl)
	router.SetDesktopControlHandler(desktopCoordinator.HandleControl)
	router.SetDesktopMediaChecker(desktopCoordinator.ValidateMedia)

	// 5. Start Tunnel Gateway (QUIC + TLS; QUIC requires TLS)
	quicAddr := cfg.Server.QUIC.Listen
	if !cfg.IsTLSEnabled() {
		quicAddr = "" // QUIC requires TLS 1.3, disable when plaintext
	}
	gw := gateway.NewGateway(gateway.GatewayConfig{
		TCPAddr:          cfg.Server.TLS.Listen,
		QUICAddr:         quicAddr,
		TLSConfig:        tunnelTLSConfig(cfg, tlsConfig),
		ServerInstanceID: serverInstanceID,
		AuthorizeDevice: func(fingerprint string, hello protocol.DeviceHello) (gateway.DeviceAuthorization, error) {
			decision, err := db.ObserveDeviceIdentity(repository.DeviceIdentityObservation{
				Fingerprint: fingerprint, InstallationID: hello.InstallationID, PublicKey: hello.PublicKey,
				DeviceName: hello.DeviceName, Platform: hello.Platform, Arch: hello.Arch,
				ClientVersion: hello.ClientVersion, RequestedCapabilities: hello.RequestedCapabilities,
			})
			if err != nil {
				return gateway.DeviceAuthorization{}, err
			}
			authorized := gateway.DeviceAuthorization{State: decision.State, DeviceID: decision.DeviceID,
				OwnerUserID: decision.OwnerUserID, ApprovedCapabilities: decision.ApprovedCapabilities}
			if decision.State == "approved" && decision.DeviceID != "" {
				targets, listErr := db.ListRDPTargetsForController(decision.DeviceID)
				if listErr != nil {
					return gateway.DeviceAuthorization{}, listErr
				}
				authorized.RDPTargets = make([]protocol.RDPTarget, 0, len(targets))
				for _, target := range targets {
					if target == nil {
						continue
					}
					_, online := sessionMgr.Get(target.DeviceID)
					item := protocol.RDPTarget{DeviceID: target.DeviceID, Name: target.Name, Online: online}
					if capabilities, ok := desktopCoordinator.Capabilities(target.DeviceID); ok {
						item.DesktopCapabilities = &capabilities
					}
					authorized.RDPTargets = append(authorized.RDPTargets, item)
				}
			}
			return authorized, nil
		},
		RecheckDevice: func(fingerprint, deviceID string) bool {
			return db.IsDeviceIdentityApproved(fingerprint, deviceID)
		},
		OnDeviceConnected: func(deviceID string) {
			_ = db.UpdateDeviceLastSeen(deviceID)
		},
		OnDeviceDisconnected: func(deviceID string) {
			_ = db.UpdateDeviceLastSeen(deviceID)
			coordinator.CloseDevice(deviceID)
			desktopCoordinator.CloseDevice(deviceID)
		},
		MaxConnections:          cfg.Tunnel.MaxConnections,
		MaxConnectionsPerDevice: cfg.Tunnel.MaxConnectionsPerDevice,
		HeartbeatSec:            cfg.Tunnel.HeartbeatSec,
		RendezvousAddress:       rendezvousAddress,
		RDPLeaseSec:             coordinator.LeaseSeconds(),
	}, sessionMgr, router)

	if err := gw.Start(); err != nil {
		log.Fatalf("[Gateway] Failed to start tunnel gateway: %v", err)
	}
	defer gw.Close()

	ingress := serverrdp.NewIngressManager(context.Background(), db, sessionMgr, serverrdp.IngressConfig{
		Enabled: cfg.RDP.Ingress.Enabled != nil && *cfg.RDP.Ingress.Enabled,
		Listen:  cfg.RDP.Ingress.Listen, RateLimit: cfg.RDP.Ingress.RateLimitPerMin,
		SourceCIDRs: cfg.RDP.Ingress.SourceCIDRs, PortStart: cfg.RDP.Ingress.PortStart, PortEnd: cfg.RDP.Ingress.PortEnd,
		Audit: func(audit *repository.ConnectionAudit) {
			select {
			case auditCh <- audit:
			default:
				logAuditRateLimited(&lastAuditDropLog, "[Audit] queue full, dropping public RDP audit records")
			}
		},
	})
	if err := ingress.Start(); err != nil {
		log.Fatalf("[RDP] Failed to start public ingress: %v", err)
	}
	defer ingress.Close()

	// The gateway derives control read deadlines from the negotiated heartbeat
	// interval and owns session cleanup; there is no independent fixed reaper.

	// 6. Start Admin Web & API Server
	settings, err := config.NewServerSettings(resolvedConfigPath, cfg)
	if err != nil {
		log.Fatalf("[Admin] Failed to initialize settings: %v", err)
	}
	apiRouter := api.NewRouter(authService, deviceService, sessionMgr, db,
		api.WithServerSettings(settings, tlsConfig),
		api.WithDeviceAuthorizationChanged(func(deviceID string) { coordinator.CloseDevice(deviceID); desktopCoordinator.CloseDevice(deviceID); ingress.Reload() }),
		api.WithDeviceRevoked(func(deviceID string) { coordinator.CloseDevice(deviceID); desktopCoordinator.CloseDevice(deviceID); ingress.CloseDevice(deviceID) }),
		api.WithRDPIngressEnabled(ingress.Enabled),
		api.WithRDPIngressReload(func(string) error { return ingress.Reload() }),
		api.WithRDPIngressStatus(func(id string) api.RDPIngressRuntimeStatus {
			status := ingress.EndpointStatus(id)
			return api.RDPIngressRuntimeStatus{TCPListening: status.TCPListening, UDPListening: status.UDPListening, ActiveUDP: status.ActiveUDP}
		}),
		api.WithRDPIngressPortRange(cfg.RDP.Ingress.PortStart, cfg.RDP.Ingress.PortEnd))
	adminServer := &http.Server{
		Addr:         cfg.Server.Admin.Listen,
		Handler:      apiRouter,
		TLSConfig:    adminTLSConfig(cfg, tlsConfig),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	go func() {
		ln, err := net.Listen("tcp", cfg.Server.Admin.Listen)
		if err != nil {
			log.Fatalf("[Admin] Listen failed: %v", err)
		}
		if adminServer.TLSConfig != nil {
			ln = tls.NewListener(ln, adminServer.TLSConfig)
			log.Printf("[Admin] Web Console & API starting on https://%s", cfg.Server.Admin.Listen)
		} else {
			log.Printf("[Admin] Web Console & API starting on http://%s (plaintext)", cfg.Server.Admin.Listen)
		}
		if err := adminServer.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[Admin] Server error: %v", err)
		}
	}()

	// 7. Wait for termination signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	sig := <-sigChan
	log.Printf("[Server] Received signal %v, shutting down gracefully...", sig)

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	_ = adminServer.Shutdown(shutdownCtx)
	_ = gw.Close()
	close(auditCh)
	select {
	case <-auditDone:
	case <-shutdownCtx.Done():
	}

	log.Println("[Server] RelayProxy Server stopped.")
	fmt.Println("Bye!")
}

func sqliteDSNIsMemory(dsn string) bool {
	value := strings.ToLower(strings.TrimSpace(dsn))
	return value == ":memory:" || strings.Contains(value, "file::memory:") || strings.Contains(value, "mode=memory")
}
