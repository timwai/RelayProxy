package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	agentexit "relayproxy/agent/exit"
	"relayproxy/internal/acl"
	"relayproxy/internal/config"
	internaldirect "relayproxy/internal/direct"
	"relayproxy/internal/protocol"
	"relayproxy/server/api"
	serverdirect "relayproxy/server/direct"
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
		log.Printf("[P2P] enabled rendezvous_listen=%q rendezvous_advertise=%q agent_udp_ports=%d-%d upnp=%t lease=%ds max_sessions_per_device=%d",
			cfg.P2P.RendezvousListen, p2pRendezvousAddress, cfg.P2P.PortStart, cfg.P2P.PortEnd,
			cfg.P2P.UPnPEnabled != nil && *cfg.P2P.UPnPEnabled,
			proxyP2PCoordinator.LeaseSeconds(), cfg.P2P.MaxSessionsPerDevice)
		if p2pRendezvousAddress == "" {
			log.Printf("[P2P] rendezvous advertise address is empty; cross-NAT peers will only advertise local interface candidates")
		}
	}

	var gw *gateway.Gateway
	publicDirectRegistry := serverdirect.NewRegistry()
	publicDirectVerifier := &serverdirect.Verifier{Registry: publicDirectRegistry}
	publicDirectAuthorizationSync := &serverdirect.AuthorizationSyncer{Sessions: sessionMgr}
	publicDirectTicketSigner, err := internaldirect.GenerateTicketSigner(internaldirect.DefaultAccessTicketTTL)
	if err != nil {
		log.Fatalf("[PublicDirect] Failed to initialize ticket signer: %v", err)
	}
	publicDirectTicketIssuer := &serverdirect.TicketIssuer{
		Registry: publicDirectRegistry,
		Signer:   publicDirectTicketSigner,
		Authorize: func(clientDeviceID, exitDeviceID string) (serverdirect.TicketAuthorizationContext, bool, error) {
			context, allowed, err := db.PublicDirectAuthorizationContext(clientDeviceID, exitDeviceID)
			return serverdirect.TicketAuthorizationContext{
				PolicyRevision: context.PolicyRevision, AuthorizationRevision: context.AuthorizationRevision,
			}, allowed, err
		},
		Sync: publicDirectAuthorizationSync.Push,
	}
	publicDirectController := serverdirect.NewController(context.Background(), publicDirectRegistry, publicDirectVerifier, func(string) {
		if gw != nil {
			gw.RefreshProxyExitInventories()
		}
	})
	defer publicDirectController.Close()

	revokeIdentityPublicDirect := func(identityID string) {
		deviceIDs, err := db.DeviceIDsForIdentity(identityID)
		if err != nil {
			log.Printf("[PublicDirect] Failed to enumerate identity devices for revocation identity=%s: %v", identityID, err)
			for _, exitSession := range sessionMgr.GetExits() {
				if exitSession != nil && slices.Contains(exitSession.Capabilities, protocol.CapabilityProxyPublicDirect) {
					sessionMgr.Unregister(exitSession.DeviceID)
				}
			}
			return
		}
		revokeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, deviceID := range deviceIDs {
			publicDirectAuthorizationSync.RevokeClientFromAllExits(revokeCtx, deviceID)
		}
	}

	invalidateIdentitySessions := func(identityID string) []string {
		revokeIdentityPublicDirect(identityID)
		deviceIDs := sessionMgr.InvalidateIdentity(identityID)
		for _, deviceID := range deviceIDs {
			rdpCoordinator.CloseDevice(deviceID)
			if proxyP2PCoordinator != nil {
				proxyP2PCoordinator.RevokeDevice(deviceID)
			}
			publicDirectController.InvalidateDevice(deviceID)
		}
		return deviceIDs
	}

	authorizationExpiryStop := make(chan struct{})
	authorizationExpiryDone := make(chan struct{})
	expireAuthorizationGrants := func() {
		affected, expireErr := db.ExpireIdentityGrants(time.Now().UTC())
		if expireErr != nil {
			log.Printf("[AuthZ] Failed to expire identity grants: %v", expireErr)
			return
		}
		for _, identityID := range affected {
			deviceIDs := invalidateIdentitySessions(identityID)
			log.Printf("[AuthZ] Expired grants invalidated identity=%s devices=%d", identityID, len(deviceIDs))
		}
		if len(affected) > 0 && gw != nil {
			gw.RefreshProxyExitInventories()
		}
	}
	expireAuthorizationGrants()
	go func() {
		defer close(authorizationExpiryDone)
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				expireAuthorizationGrants()
			case <-authorizationExpiryStop:
				return
			}
		}
	}()

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
	router.SetPublicDirectControlHandler(publicDirectController.HandleControl)
	router.SetPublicDirectTicketHandler(publicDirectTicketIssuer.HandleControl)

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

	gw = gateway.NewGateway(gateway.GatewayConfig{
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
		ResolveIdentity: func(shortID string) (gateway.IdentityAuthorization, error) {
			resolved, err := db.ResolveIdentity(shortID)
			if err != nil {
				return gateway.IdentityAuthorization{}, err
			}
			return gateway.IdentityAuthorization{
				IdentityID: resolved.IdentityID, IdentityName: resolved.IdentityName,
				PolicyRevision: resolved.PolicyRevision,
			}, nil
		},
		AuthorizeIdentityDevice: func(fingerprint string, hello protocol.DeviceHello, identity gateway.IdentityAuthorization) (gateway.DeviceAuthorization, error) {
			decision, err := db.ObserveIdentityDevice(repository.IdentityAuthorization{
				IdentityID: identity.IdentityID, IdentityName: identity.IdentityName,
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
		RecheckIdentityDevice: func(fingerprint, deviceID, identityID string) bool {
			return db.IsIdentityDeviceAuthorized(fingerprint, deviceID, identityID)
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
			result, err := listAuthorizedProxyExitInventory(db, sessionMgr, clientID, ownerUserID, identityID)
			if err != nil {
				return nil, err
			}
			for i := range result {
				endpoints := publicDirectRegistry.VerifiedEndpoints(result[i].DeviceID)
				fingerprint := publicDirectRegistry.VerifiedCertificateFingerprint(result[i].DeviceID)
				if len(endpoints) == 0 || fingerprint == "" {
					continue
				}
				result[i].Direct = &protocol.ProxyDirectPaths{Public: &protocol.ProxyPublicDirectPath{
					Available: true, Transport: "quic", CertFingerprint: fingerprint, Endpoints: endpoints,
				}}
			}
			if serverExit != nil {
				authorized, err := db.AuthorizeClientExit(clientID, protocol.ServerExitDeviceID)
				if err != nil {
					return nil, err
				}
				if authorized {
					source := "legacy"
					if identityID != "" {
						source = "explicit"
					}
					result = append(result, protocol.ProxyExit{
						DeviceID: protocol.ServerExitDeviceID,
						Name:     "Relay Server", IdentityName: "系统资源",
						AuthorizationSource: source, Online: true,
					})
				}
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
			// A new authenticated session invalidates endpoints verified for the
			// previous tunnel generation until this Exit registers them again.
			publicDirectController.InvalidateDevice(deviceID)
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
			publicDirectController.InvalidateDevice(deviceID)
		},
		PublicDirectEnabled:         true,
		PublicDirectTicketVerifyKey: publicDirectTicketIssuer.PublicKey(),
		MaxConnections:              cfg.Tunnel.MaxConnections,
		MaxConnectionsPerDevice:     cfg.Tunnel.MaxConnectionsPerDevice,
		HeartbeatSec:                cfg.Tunnel.HeartbeatSec,
		RendezvousAddress:           rendezvousAddress,
		RDPLeaseSec:                 rdpCoordinator.LeaseSeconds(),
		P2PEnabled:                  proxyP2PCoordinator != nil,
		P2PRendezvousAddress:        p2pRendezvousAddress,
		P2PLeaseSec:                 p2pLeaseSec,
		P2PPortStart:                cfg.P2P.PortStart,
		P2PPortEnd:                  cfg.P2P.PortEnd,
		P2PUPnPEnabled:              cfg.P2P.UPnPEnabled != nil && *cfg.P2P.UPnPEnabled,
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
			revokeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			publicDirectAuthorizationSync.RevokeClientFromAllExits(revokeCtx, deviceID)
			cancel()
			rdpCoordinator.CloseDevice(deviceID)
			if proxyP2PCoordinator != nil {
				proxyP2PCoordinator.RevokeDevice(deviceID)
			}
			gw.RefreshProxyExitInventories()
			ingress.Reload()
		}),
		api.WithIdentityAuthorizationChanged(func(identityID string) {
			invalidateIdentitySessions(identityID)
			gw.RefreshProxyExitInventories()
			ingress.Reload()
		}),
		api.WithDeviceIdentityGrantChanged(func(targetDeviceID, granteeIdentityID string) {
			// Invalidate the grantee identity's authenticated tunnels after any
			// grant mutation. This is broader than feature-specific stream teardown,
			// but guarantees that existing Relay/P2P/RDP paths cannot retain stale
			// authority. Remaining clients receive a fresh exit inventory immediately.
			invalidateIdentitySessions(granteeIdentityID)
			gw.RefreshProxyExitInventories()
			// A target can itself be an active controller/client. Do not close
			// its main tunnel; its peer-side direct paths are revalidated on
			// candidate updates/renewal and the grantee side has been revoked.
			_ = targetDeviceID
			ingress.Reload()
		}),
		api.WithDeviceRevoked(func(deviceID string) {
			revokeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			publicDirectAuthorizationSync.RevokeClientFromAllExits(revokeCtx, deviceID)
			cancel()
			rdpCoordinator.CloseDevice(deviceID)
			if proxyP2PCoordinator != nil {
				proxyP2PCoordinator.RevokeDevice(deviceID)
			}
			gw.RefreshProxyExitInventories()
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
	close(authorizationExpiryStop)
	select {
	case <-authorizationExpiryDone:
	case <-shutdownCtx.Done():
	}
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
		PolicyRevision:       decision.PolicyRevision,
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

type proxyExitInventoryStore interface {
	ListDevices() ([]*repository.Device, error)
	ListDevicesForOwner(string) ([]*repository.Device, error)
	AuthorizeClientExit(string, string) (bool, error)
	GetDeviceIdentitySummary(string) (*repository.DeviceIdentitySummary, error)
}

type proxyExitSessionSource interface {
	GetExits() []*session.DeviceSession
}

func listAuthorizedProxyExitInventory(
	store proxyExitInventoryStore,
	sessions proxyExitSessionSource,
	clientID, ownerUserID, identityID string,
) ([]protocol.ProxyExit, error) {
	if store == nil {
		return nil, errors.New("proxy exit inventory store is unavailable")
	}
	var (
		devices []*repository.Device
		err     error
	)
	if identityID == "" && ownerUserID != "" {
		devices, err = store.ListDevicesForOwner(ownerUserID)
	} else {
		devices, err = store.ListDevices()
	}
	if err != nil {
		return nil, err
	}

	online := make(map[string]*session.DeviceSession)
	if sessions != nil {
		for _, item := range sessions.GetExits() {
			if item == nil || strings.TrimSpace(item.DeviceID) == "" {
				continue
			}
			online[item.DeviceID] = item
		}
	}

	result := make([]protocol.ProxyExit, 0, len(devices))
	for _, device := range devices {
		if device == nil || device.ID == "" || device.ID == clientID ||
			device.ApprovalState != "approved" ||
			!containsString(device.ApprovedCapabilities, protocol.CapabilityProxyExit) {
			continue
		}
		if identityID != "" || ownerUserID == "" {
			authorized, authErr := store.AuthorizeClientExit(clientID, device.ID)
			if authErr != nil {
				return nil, authErr
			}
			if !authorized {
				continue
			}
		}

		name := strings.TrimSpace(device.Name)
		if name == "" {
			name = device.ID
		}
		identityName := ""
		source := "legacy"
		if identityID != "" {
			summary, summaryErr := store.GetDeviceIdentitySummary(device.ID)
			if summaryErr != nil {
				return nil, summaryErr
			}
			identityName = summary.IdentityName
			source = "explicit"
			if summary.IdentityID == identityID {
				source = "same_identity"
			}
		}

		live := online[device.ID]
		if live != nil {
			if liveName := strings.TrimSpace(live.DeviceName); liveName != "" {
				name = liveName
			}
			if strings.TrimSpace(live.IdentityName) != "" {
				identityName = live.IdentityName
			}
		}
		result = append(result, protocol.ProxyExit{
			DeviceID: device.ID, Name: name, IdentityName: identityName,
			AuthorizationSource: source, Online: live != nil,
		})
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].Name == result[j].Name {
			return result[i].DeviceID < result[j].DeviceID
		}
		return result[i].Name < result[j].Name
	})
	return result, nil
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
