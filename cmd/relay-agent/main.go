package main

import (
	"context"
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

	"relayproxy/agent/app"
	"relayproxy/agent/bridge"
	"relayproxy/agent/divert"
	"relayproxy/agent/exit"
	"relayproxy/agent/gui"
	"relayproxy/agent/singleton"
	"relayproxy/agent/startup"
	"relayproxy/internal/config"
	"relayproxy/internal/deviceidentity"
)

// Version is stamped at build time with -ldflags "-X main.Version=x.y.z".
var Version = "1.0.0"

func main() {
	configPath := flag.String("config", "", "Path to configuration file (Windows/macOS default: ~/.relayproxy/relay-agent.yaml)")
	serverFlag := flag.String("server", "", "Override server address")
	exitFlag := flag.String("exit", "", "Default exit node ID (omit to auto-select when exactly one exit is online)")
	socksFlag := flag.String("socks5", "", "Override SOCKS5 listen address")
	httpFlag := flag.String("http", "", "Override HTTP proxy listen address")
	insecureFlag := flag.Bool("insecure", false, "Allow self-signed or unverified TLS certificates")

	guiFlag := flag.Bool("gui", false, "Launch the native desktop window (default when started without arguments)")
	noGuiFlag := flag.Bool("no-gui", false, "Run headless without the desktop window")
	minimizedFlag := flag.Bool("minimized", false, "Start the desktop window minimized to the system tray")
	hiddenFlag := flag.Bool("hidden", false, "Alias for --minimized (kept for existing autostart entries)")
	versionFlag := flag.Bool("version", false, "Print the version and exit")
	startupHelperFlag := flag.String(startup.HelperFlagName(), "", "Internal Windows startup helper action")
	networkServiceFlag := flag.Bool(divert.NetworkServiceModeFlagName(), false, "Internal Windows network service mode")
	networkServiceSIDFlag := flag.String(divert.NetworkServiceSIDFlagName(), "", "Internal Windows network service allowed SID")
	networkServiceHelperFlag := flag.String(divert.NetworkServiceHelperFlagName(), "", "Internal Windows network service helper action")

	var noWebFlag bool
	flag.BoolVar(&noWebFlag, "no-web", false, "Disable the embedded web management page")
	var webPortFlag int
	flag.IntVar(&webPortFlag, "web-port", 0, "Override the web management port")
	webListenFlag := flag.String("web-listen", "", "Override the web management listen address")

	flag.Parse()

	if *networkServiceFlag {
		if err := divert.RunWindowsNetworkService(*networkServiceSIDFlag); err != nil {
			log.Printf("[NetworkService] %v", err)
			os.Exit(1)
		}
		return
	}
	if *networkServiceHelperFlag != "" {
		if err := divert.RunWindowsNetworkServiceHelper(*networkServiceHelperFlag, *networkServiceSIDFlag); err != nil {
			log.Printf("[NetworkService] helper failed: %v", err)
			os.Exit(1)
		}
		return
	}

	if *startupHelperFlag != "" {
		if err := startup.RunElevatedHelper(*startupHelperFlag, "RelayProxy Agent", *configPath); err != nil {
			log.Printf("[Startup] Elevated autostart helper failed: %v", err)
			os.Exit(1)
		}
		return
	}

	if *versionFlag {
		fmt.Printf("relay-agent %s (%s/%s)\n", Version, runtime.GOOS, runtime.GOARCH)
		return
	}

	// A GUI restart starts the replacement process before the current process
	// exits. Wait here, before singleton acquisition, so the two instances never
	// race for the named mutex or local proxy listeners.
	gui.WaitForRestartParent()

	startMinimized := resolveStartMinimized(*minimizedFlag, *hiddenFlag)
	wantGUI := resolveGUIMode(*guiFlag, *noGuiFlag, startMinimized)

	// A bare double-click or a minimized login launch can land on the
	// console-subsystem binary when the user picks relay-agent.exe instead of
	// relay-agent-gui.exe. Hide its private console before anything is printed so
	// no black window sits behind the desktop UI. detachConsole leaves an
	// inherited terminal alone.
	if wantGUI {
		detachConsole()
	}

	if !wantGUI {
		log.Println("==================================================")
		log.Printf("      RelayProxy Agent v%s Starting...", Version)
		log.Println("==================================================")
	}

	// Single-instance enforcement (Windows Named Mutex). The desktop window and
	// the headless process share one mutex so a second launch can never start a
	// second pair of local proxy listeners.
	instanceLock, err := singleton.Acquire("RelayProxyAgent_Instance")
	if err != nil {
		log.Printf("[Agent] Notice: %v", err)
		if wantGUI {
			// Surface the instance that is already running instead of failing
			// silently when the user double-clicks the icon again.
			gui.ActivateExistingWindow()
		}
		log.Println("[Agent] Another instance is already active. Exiting.")
		return
	}
	defer instanceLock.Release()

	configExplicit := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "config" {
			configExplicit = true
		}
	})
	*configPath, err = resolveConfigPath(*configPath, configExplicit)
	if err != nil {
		log.Fatalf("[Config] Failed to resolve configuration path: %v", err)
	}
	cfgFile, err := loadOrCreateAgentConfig(*configPath, nil)
	if err != nil {
		log.Fatalf("[Config] Failed to load configuration %s: %v", *configPath, err)
	}
	log.Printf("[Config] Loaded %s", *configPath)
	// Apply CLI flag overrides
	if *serverFlag != "" {
		cfgFile.Server.Address = *serverFlag
	}
	if *exitFlag != "" {
		cfgFile.Proxy.DefaultExitID = *exitFlag
	}
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "web-listen":
			cfgFile.Web.Listen = *webListenFlag
		case "web-port":
			if webPortFlag < 1 || webPortFlag > 65535 {
				log.Fatalf("[Config] --web-port must be between 1 and 65535")
			}
			cfgFile.Web.Port = webPortFlag
		}
	})
	if noWebFlag {
		cfgFile.Web.Enabled = config.BoolPtr(false)
	}
	if err := config.NormalizeAgentConfig(cfgFile); err != nil {
		log.Fatalf("[Config] Invalid startup configuration: %v", err)
	}
	networkMode := cfgFile.Network.Mode
	if runtime.GOOS == "windows" && runtime.GOARCH != "amd64" && networkMode == "divert" {
		// WinDivert currently ships only x64 binaries. Keep the ARM64 Agent
		// usable with SOCKS5/HTTP instead of exiting before the UI can explain
		// why transparent interception is unavailable.
		log.Printf("[Agent] network.mode=divert is unavailable on Windows %s; starting with SOCKS5/HTTP only", runtime.GOARCH)
		networkMode = ""
	}
	if runtime.GOOS == "windows" && runtime.GOARCH == "amd64" && networkMode == "divert" {
		switch {
		case divert.PlatformServiceReady():
			log.Println("[Agent] RelayProxy Network Service is ready; transparent proxy will run without GUI elevation")
		case wantGUI && !startMinimized:
			if err := divert.EnsurePlatformService(); err != nil {
				// Keep the desktop usable after a cancelled/failed UAC prompt.
				// The saved configuration remains "divert", so the UI still
				// shows that a restart/repair is required.
				log.Printf("[Agent] RelayProxy Network Service unavailable: %v; starting without transparent interception", err)
				networkMode = ""
			}
		case startMinimized:
			// Login startup must never produce a surprise UAC prompt. The
			// service is installed once from an interactive settings change.
			log.Println("[Agent] RelayProxy Network Service is not ready; skipping transparent interception during login startup")
			networkMode = ""
		}
	}

	identityPath := filepath.Join(filepath.Dir(*configPath), "device-identity.json")
	deviceIdentity, err := deviceidentity.LoadOrCreate(identityPath)
	if err != nil {
		log.Fatalf("[Identity] Failed to load or create installation identity: %v", err)
	}
	log.Printf("[Identity] Installation %s fingerprint %s", deviceIdentity.InstallationID, deviceIdentity.Fingerprint())

	socksListen := net.JoinHostPort(cfgFile.Proxy.SOCKS5.Listen, strconv.Itoa(cfgFile.Proxy.SOCKS5.Port))
	if *socksFlag != "" {
		socksListen = *socksFlag
	}

	httpListen := net.JoinHostPort(cfgFile.Proxy.HTTP.Listen, strconv.Itoa(cfgFile.Proxy.HTTP.Port))
	if *httpFlag != "" {
		httpListen = *httpFlag
	}

	agentCfg := app.AgentConfig{
		Identity:      deviceIdentity,
		// The identity access key is intentionally not part of relay-agent.yaml
		// or UI status/export surfaces. A protected credential store will replace
		// this bootstrap environment source when the settings UI lands.
		AccessKey:     strings.TrimSpace(os.Getenv("RELAYPROXY_ACCESS_KEY")),
		DeviceName:    cfgFile.Device.Name,
		ServerAddress: cfgFile.Server.Address,
		QUICPort:      cfgFile.Server.QUICPort,
		TCPPort:       cfgFile.Server.TCPPort,
		// The binary advertises supported capabilities; the server decides which
		// subset is granted. This is not a client-side authorization choice.
		Mode:          "BOTH",
		TransportMode: cfgFile.Transport.Mode,
		SOCKS5Enabled: cfgFile.Proxy.SOCKS5.Enabled,
		SOCKS5Listen:  socksListen,
		HTTPEnabled:   cfgFile.Proxy.HTTP.Enabled,
		HTTPListen:    httpListen,
		DefaultExitID: cfgFile.Proxy.DefaultExitID,
		ExitEnabled:   cfgFile.Exit.Enabled,
		ExitUpstream: exit.UpstreamConfig{
			Mode: cfgFile.Exit.Upstream.Mode, Address: cfgFile.Exit.Upstream.Address,
			Username: cfgFile.Exit.Upstream.Username, Password: cfgFile.Exit.Upstream.Password,
		},
		RDPEnabled:      cfgFile.RDP.Enabled,
		RDPAddress:      cfgFile.RDP.Address,
		P2PEnabled:      cfgFile.P2P.Enabled,
		P2PMode:         cfgFile.P2P.Mode,
		P2PPunchTimeout: time.Duration(cfgFile.P2P.PunchTimeoutMs) * time.Millisecond,
		P2PKeepalive:    time.Duration(cfgFile.P2P.KeepaliveSec) * time.Second,
		P2PIdleTimeout:  time.Duration(cfgFile.P2P.IdleTimeoutSec) * time.Second,
		P2PMaxSessions:  cfgFile.P2P.MaxExitSessions,
		P2PFallback:     cfgFile.P2P.Fallback,
		AllowInternet:   cfgFile.Exit.AllowInternet,
		AllowPrivateNet: cfgFile.Exit.AllowPrivateNetwork,
		AllowLoopback:   cfgFile.Exit.AllowLoopback,
		AccessMode:      cfgFile.Exit.Access.Mode,
		AccessDomains:   cfgFile.Exit.Access.Domains,
		AccessCIDRs:     cfgFile.Exit.Access.CIDRs,
		NetworkMode:     networkMode,
		DivertConfig:    cfgFile.DivertConfig(),
		Routing:         cfgFile.Routing,
		InsecureTLS:     *insecureFlag,
		PlainTCP:        !cfgFile.IsServerTLSEnabled(),
		ConnectTimeout:  10 * time.Second,
	}

	if agentCfg.PlainTCP && agentCfg.TransportMode != "tcp_only" {
		agentCfg.TransportMode = "tcp_only"
		log.Println("[Agent] TLS disabled: forcing transport mode to tcp_only (QUIC requires TLS)")
	}

	agent, err := app.NewAgent(agentCfg)
	if err != nil {
		log.Fatalf("[Agent] Invalid startup settings: %v", err)
	}
	if err := agent.Start(); err != nil {
		log.Fatalf("[Agent] Failed to start agent: %v", err)
	}
	defer agent.Close()

	log.Printf("[Agent] Running with automatic server approval. SOCKS5: %s, HTTP: %s", socksListen, httpListen)

	// Initialize UIBridge (the only surface the desktop UI talks to)
	uiBridge := bridge.NewUIBridge(agent, *configPath)
	gui.Version = Version

	// The browser UI uses the same embedded page and UIBridge as the native
	// Windows window. It is enabled by default on loopback so Linux/macOS and
	// service-style launches remain fully manageable without a desktop session.
	var webServer *gui.WebServer
	if !noWebFlag && cfgFile.IsWebEnabled() {
		webServer, err = gui.StartWeb(uiBridge, gui.WebOptions{
			Listen: cfgFile.Web.Listen,
			Port:   cfgFile.Web.Port,
			Token:  cfgFile.Web.Token,
		})
		if err != nil {
			log.Fatalf("[Web] Failed to start management page: %v", err)
		}
		log.Printf("[Web] Management page: http://%s/", webServer.Addr())
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := webServer.Close(ctx); err != nil {
				log.Printf("[Web] Failed to stop management page cleanly: %v", err)
			}
		}()
	}

	// --- Desktop window -----------------------------------------------------
	// The window owns the process lifetime: closing it (or choosing 退出 from the
	// tray) shuts the agent down. gui.Run blocks until then.
	if wantGUI && cfgFile.IsGUIEnabled() {
		webURL := ""
		if webServer != nil {
			webURL = webServer.BrowserURL()
			go func() {
				<-webServer.Done()
				gui.RequestQuit()
			}()
		}
		err := gui.Run(uiBridge, gui.Options{
			ConfigPath:     *configPath,
			StartMinimized: startMinimized,
			MinimizeToTray: cfgFile.IsMinimizeToTray(),
			Theme:          cfgFile.GUI.Theme,
			WebURL:         webURL,
			// Keep the native window title stable. The single-instance activation
			// path uses it to find a window that may currently be hidden in the tray.
			Title: gui.DefaultWindowTitle,
		})
		switch {
		case err == nil:
			log.Println("[Agent] Desktop session ended, RelayProxy Agent cleanly stopped.")
			return
		case errors.Is(err, gui.ErrExternalUI):
			log.Println("[Agent] macOS management UI opened; continuing in background.")
		case errors.Is(err, gui.ErrUnsupported):
			log.Println("[Agent] Falling back to headless mode.")
		default:
			fallbackURL := ""
			if webServer != nil {
				fallbackURL = webServer.BrowserURL()
			}
			gui.ShowStartupError(err, fallbackURL)
			log.Printf("[Agent] Desktop UI failed: %v; continuing in headless mode", err)
		}
	}

	// --- Headless -----------------------------------------------------------
	log.Println("[Agent] Press Ctrl+C to terminate.")

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigChan)
	var webDone <-chan struct{}
	if webServer != nil {
		webDone = webServer.Done()
	}
	select {
	case sig := <-sigChan:
		log.Printf("[Agent] Received signal %v, shutting down...", sig)
	case <-webDone:
		log.Println("[Agent] Shutdown requested from the web management page.")
	}
	_ = agent.Close()
	log.Println("[Agent] RelayProxy Agent cleanly stopped.")
}

// resolveGUIMode decides between the desktop window and headless operation.
//
// Explicit flags always win. Otherwise the desktop window is chosen for the
// GUI-named binary (relay-agent-gui.exe), an explicit GUI/minimized launch, and
// a bare double-click (no arguments at all) select the desktop window; ordinary
// CLI arguments keep the process headless.
func resolveStartMinimized(minimizedFlag, hiddenFlag bool) bool {
	// Persisted GUI preferences must never hide a manual launch. Starting in the
	// tray is reserved for an explicit command-line request (used by autostart).
	return minimizedFlag || hiddenFlag
}

func resolveGUIMode(guiFlag, noGuiFlag, minimizedFlag bool) bool {
	if noGuiFlag {
		return false
	}
	if guiFlag || minimizedFlag {
		return true
	}
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		return false
	}
	if len(os.Args) == 1 {
		return true
	}
	return strings.Contains(strings.ToLower(filepath.Base(os.Args[0])), "gui")
}

func defaultConfigFile() *config.AgentConfigFile {
	cfg := &config.AgentConfigFile{}
	cfg.Server.Address = "127.0.0.1"
	return cfg
}
