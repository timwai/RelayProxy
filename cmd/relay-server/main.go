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
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	agentexit "relayproxy/agent/exit"
	"relayproxy/internal/acl"
	"relayproxy/internal/config"
	"relayproxy/internal/protocol"
	"relayproxy/server/api"
	"relayproxy/server/gateway"
	serverp2p "relayproxy/server/p2p"
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

	var serverExit *agentexit.Handler
	if cfg.Exit.Enabled != nil && *cfg.Exit.Enabled {
		serverExitACL, err := acl.NewChecker(cfg.ServerExitPolicy())
		if err != nil {
			log.Fatalf("[Exit] Invalid server exit policy: %v", err)
		}
		upstream := agentexit.UpstreamConfig{
			Mode: cfg.Exit.Upstream.Mode, Address: cfg.Exit.Upstream.Address,
			Username: cfg.Exit.Upstream.Username, Password: cfg.Exit.Upstream.Password,
		}
		if err := agentexit.ValidateUpstreamConfig(upstream); err != nil {
			log.Fatalf("[Exit] Invalid server exit upstream: %v", err)
		}
		serverExit = agentexit.NewHandler(agentexit.HandlerConfig{
			ACLChecker: serverExitACL, ConnectTimeout: 10 * time.Second, Upstream: upstream,
		})
		defer serverExit.Close()
		log.Printf("[Exit] Server network exit enabled: id=%s upstream=%s", protocol.ServerExitDeviceID, cfg.Exit.Upstream.Mode)
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
	rdpCoordinator := serverrdp.NewCoordinator(sessionMgr, db, time.Duration(cfg.RDP.LeaseSec)*time.Second, rendezvousAddress)
	rdpCoordinator.Start(context.Background())
	defer rdpCoordinator.Close()

	var proxyP2PCoordinator *serverp2p.Coordinator
	p2pRendezvousAddress := ""
	if cfg.P2P.Enabled != nil && *cfg.P2P.Enabled {
		proxyRendezvous, startErr := serverp2p.StartRendezvous(context.Background(), cfg.P2P.RendezvousListen, 120)
		if startErr != nil {
			log.Fatalf("[P2P] Failed to start rendezvous: %v", startErr)
		}
		if proxyRendezvous != nil {
			defer proxyRendezvous.Close()
		}
		p2pRendezvousAddress = cfg.P2P.RendezvousAdvertise
		if p2pRendezvousAddress == "" && cfg.P2P.RendezvousListen != "" {
			if host, port, splitErr := net.SplitHostPort(cfg.P2P.RendezvousListen); splitErr == nil && host != "" && host != "0.0.0.0" && host != "::" {
				p2pRendezvousAddress = net.JoinHostPort(host, port)
			}
		}
		proxyP2PCoordinator = serverp2p.NewCoordinator(
			sessionMgr,
			db.AuthorizeClientExit,
			time.Duration(cfg.P2P.LeaseSec)*time.Second,
			p2pRendezvousAddress,
			cfg.P2P.MaxSessionsPerDevice,
			relayACL.Policy(),
		)
		proxyP2PCoordinator.Start(context.Background())
		defer proxyP2PCoordinator.Close()
	}

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
	if serverExit != nil {
		router.SetLocalExit(serverExit)
	}
	router.SetRDPChecker(func(controllerDeviceID, targetDeviceID string) (bool, error) {
		return db.AuthorizeRDP(controllerDeviceID, targetDeviceID)
	})
	router.SetRDPControlHandler(rdpCoordinator.HandleControl)
	if proxyP2PCoordinator != nil {
		router.SetP2PControlHandler(proxyP2PCoordinator.HandleControl)
	}

	publicPushHandler := api.NewPublicPushHandler(sessionMgr, db)

	// 5. Start Tunnel Gateway (QUIC + TLS; QUIC requires TLS)
	quicAddr := cfg.Server.QUIC.Listen
	if !cfg.IsTLSEnabled() {
		quicAddr = "" // QUIC requires TLS 1.3, disable when plaintext
	}
	p2pLeaseSec := 0
	if proxyP2PCoordinator != nil {
		p2pLeaseSec = proxyP2PCoordinator.LeaseSeconds()
	}

	gw := gateway.NewGateway(gateway.GatewayConfig{
		TCPAddr:           cfg.Server.TLS.Listen,
		QUICAddr:          quicAddr,
		TLSConfig:         tunnelTLSConfig(cfg, tlsConfig),
		PublicHTTPHandler: publicPushHandler,
		ServerInstanceID:  serverInstanceID,
		AuthorizeDevice: func(fingerprint string, hello protocol.DeviceHello) (gateway.DeviceAuthorization, error) {
			decision, err := db.ObserveDeviceIdentity(repository.DeviceIdentityObservation{
				Fingerprint: fingerprint, InstallationID: hello.InstallationID, PublicKey: hello.PublicKey,
				DeviceName: hello.DeviceName, Platform: hello.Platform, Arch: hello.Arch,
				ClientVersion: hello.ClientVersion, RequestedCapabilities: hello.RequestedCapabilities,
			})
			if err != nil {
				return gateway.DeviceAuthorization{}, err
			}
			authorization := gatewayAuthorization(decision)
			refreshRDPTargetOnlineState(authorization.RDPTargets, sessionMgr)
			return authorization, nil
		},
		ResolveIdentityAccessKey: func(accessKey string) (gateway.IdentityAccessAuthorization, error) {
			resolved, err := db.ResolveIdentityAccessKey(accessKey)
			if err != nil {
				return gateway.IdentityAccessAuthorization{}, err
			}
			return gateway.IdentityAccessAuthorization{
				KeyID: resolved.KeyID, KeyDigest: resolved.KeyDigest,
				IdentityID: resolved.IdentityID, IdentityName: resolved.IdentityName,
				Capabilities: append([]string(nil), resolved.Capabilities...),
				PolicyRevision: resolved.PolicyRevision,
			}, nil
		},
		AuthorizeIdentityDevice: func(fingerprint string, hello protocol.DeviceHello, identity gateway.IdentityAccessAuthorization) (gateway.DeviceAuthorization, error) {
			decision, err := db.ObserveIdentityDevice(repository.IdentityAccessAuthorization{
				KeyID: identity.KeyID, KeyDigest: identity.KeyDigest,
				IdentityID: identity.IdentityID, IdentityName: identity.IdentityName,
				Capabilities: append([]string(nil), identity.Capabilities...),
				PolicyRevision: identity.PolicyRevision,
			}, repository.DeviceIdentityObservation{
				Fingerprint: fingerprint, InstallationID: hello.InstallationID, PublicKey: hello.PublicKey,
				DeviceName: hello.DeviceName, Platform: hello.Platform, Arch: hello.Arch,
				ClientVersion: hello.ClientVersion, RequestedCapabilities: hello.RequestedCapabilities,
			})
			if errors.Is(err, repository.ErrDeviceIdentityConflict) {
				return gateway.DeviceAuthorization{State: "identity_conflict"}, nil
			}
			if err != nil {
				return gateway.DeviceAuthorization{}, err
			}
			authorization := gatewayAuthorization(decision)
			refreshRDPTargetOnlineState(authorization.RDPTargets, sessionMgr)
			return authorization, nil
		},
		RecheckDevice: func(fingerprint, deviceID string) bool {
			return db.IsDeviceIdentityApproved(fingerprint, deviceID)
		},
		RecheckIdentityDevice: func(fingerprint, deviceID, identityID, accessKeyID string) bool {
			return db.IsIdentityDeviceAuthorized(fingerprint, deviceID, identityID, accessKeyID)
		},
		ListRDPTargets: func(controllerID string) ([]protocol.RDPTarget, error) {
			targets, err := db.ListRDPTargetsForController(controllerID)
			if err != nil {
				return nil, err
			}
			result := protocolRDPTargets(targets)
			refreshRDPTargetOnlineState(result, sessionMgr)
			return result, nil
		},
		ListProxyExits: func(clientID, ownerUserID, identityID string) ([]protocol.ProxyExit, error) {
			var exits []*session.DeviceSession
			if identityID != "" {
				exits = sessionMgr.GetExitsForIdentity(identityID)
			} else {
				exits = sessionMgr.GetExitsForOwner(ownerUserID)
			}
			result := make([]protocol.ProxyExit, 0, len(exits)+1)
			for _, exit := range exits {
				if exit == nil || exit.DeviceID == "" || exit.DeviceID == clientID {
					continue
				}
				if identityID == "" && ownerUserID == "" {
					authorized, err := db.AuthorizeClientExit(clientID, exit.DeviceID)
					if err != nil {
						return nil, err
					}
					if !authorized {
						continue
					}
				}
				name := strings.TrimSpace(exit.DeviceName)
				if name == "" {
					name = exit.DeviceID
				}
				result = append(result, protocol.ProxyExit{DeviceID: exit.DeviceID, Name: name, Online: true})
			}
			if serverExit != nil {
				result = append(result, protocol.ProxyExit{
					DeviceID: protocol.ServerExitDeviceID,
					Name:     "Relay Server",
					Online:   true,
				})
			}
			sort.Slice(result, func(i, j int) bool {
				if result[i].Name == result[j].Name {
					return result[i].DeviceID < result[j].DeviceID
				}
				return result[i].Name < result[j].Name
			})
			return result, nil
		},
		OnDeviceConnected: func(deviceID string) {
			_ = db.UpdateDeviceLastSeen(deviceID)
		},
		OnDeviceHeartbeat: func(deviceID string) {
			_ = db.UpdateDeviceLastSeen(deviceID)
		},
		OnDeviceDisconnected: func(deviceID string) {
			_ = db.UpdateDeviceLastSeen(deviceID)
			rdpCoordinator.CloseDevice(deviceID)
			if proxyP2PCoordinator != nil {
				proxyP2PCoordinator.CloseDevice(deviceID)
			}
		},
		MaxConnections:          cfg.Tunnel.MaxConnections,
		MaxConnectionsPerDevice: cfg.Tunnel.MaxConnectionsPerDevice,
		HeartbeatSec:            cfg.Tunnel.HeartbeatSec,
		RendezvousAddress:       rendezvousAddress,
		RDPLeaseSec:             rdpCoordinator.LeaseSeconds(),
		P2PEnabled:              proxyP2PCoordinator != nil,
		P2PRendezvousAddress:    p2pRendezvousAddress,
		P2PLeaseSec:             p2pLeaseSec,
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
		api.WithServerExitStatus(func() api.ServerExitRuntimeStatus {
			if serverExit == nil {
				return api.ServerExitRuntimeStatus{}
			}
			return api.ServerExitRuntimeStatus{Enabled: true, ActiveStreams: serverExit.ActiveStreams()}
		}),
		api.WithDeviceAuthorizationChanged(func(deviceID string) {
			rdpCoordinator.CloseDevice(deviceID)
			if proxyP2PCoordinator != nil {
				proxyP2PCoordinator.RevokeDevice(deviceID)
			}
			ingress.Reload()
		}),
		api.WithDeviceRevoked(func(deviceID string) {
			rdpCoordinator.CloseDevice(deviceID)
			if proxyP2PCoordinator != nil {
				proxyP2PCoordinator.RevokeDevice(deviceID)
			}
			ingress.CloseDevice(deviceID)
		}),
		api.WithP2PSessions(func() []api.P2PSessionRuntimeStatus {
			if proxyP2PCoordinator == nil {
				return []api.P2PSessionRuntimeStatus{}
			}
			snapshots := proxyP2PCoordinator.Snapshots()
			out := make([]api.P2PSessionRuntimeStatus, 0, len(snapshots))
			peer := func(report serverp2p.PeerReport) api.P2PPeerRuntimeReport {
				return api.P2PPeerRuntimeReport{
					Path: report.Path, Reason: report.Reason, RTTMs: report.RTTMs,
					CandidateSummary: report.CandidateSummary, FallbackCount: report.FallbackCount,
					ActiveStreams: report.ActiveStreams, BytesUp: report.BytesUp,
					BytesDown: report.BytesDown, UpdatedAt: report.UpdatedAt,
				}
			}
			for _, item := range snapshots {
				out = append(out, api.P2PSessionRuntimeStatus{
					SessionID: item.ID, ClientDeviceID: item.ClientDeviceID, ExitDeviceID: item.ExitDeviceID,
					LeaseExpiresAt: item.ExpiresAt, Answered: item.Answered,
					ClientReport: peer(item.ClientReport), ExitReport: peer(item.ExitReport),
				})
			}
			return out
		}),
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

func gatewayAuthorization(decision *repository.DeviceAuthorization) gateway.DeviceAuthorization {
	if decision == nil {
		return gateway.DeviceAuthorization{}
	}
	authorized := gateway.DeviceAuthorization{
		State: decision.State, DeviceID: decision.DeviceID, OwnerUserID: decision.OwnerUserID,
		IdentityID: decision.IdentityID, IdentityName: decision.IdentityName,
		AccessKeyID: decision.AccessKeyID, PolicyRevision: decision.PolicyRevision,
		ApprovedCapabilities: append([]string(nil), decision.ApprovedCapabilities...),
		RDPTargets:           protocolRDPTargets(decision.RDPTargets),
	}
	return authorized
}

func protocolRDPTargets(targets []*repository.RDPTarget) []protocol.RDPTarget {
	result := make([]protocol.RDPTarget, 0, len(targets))
	for _, target := range targets {
		if target == nil || target.DeviceID == "" {
			continue
		}
		result = append(result, protocol.RDPTarget{
			DeviceID: target.DeviceID,
			Name:     target.Name,
			Online:   target.Online,
		})
	}
	return result
}

func refreshRDPTargetOnlineState(targets []protocol.RDPTarget, sessions *session.Manager) {
	if sessions == nil {
		return
	}
	for index := range targets {
		device, online := sessions.Get(targets[index].DeviceID)
		targets[index].Online = online && device != nil && containsString(device.Grants, protocol.CapabilityRDPHost)
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func sqliteDSNIsMemory(dsn string) bool {
	value := strings.ToLower(strings.TrimSpace(dsn))
	return value == ":memory:" || strings.Contains(value, "file::memory:") || strings.Contains(value, "mode=memory")
}
