package main

import (
	"log"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"strconv"
	"strings"
	"time"
)

// startOptionalPprof exposes Go runtime profiling only when explicitly enabled.
// It refuses non-loopback addresses so diagnostics cannot accidentally become a
// remotely reachable management surface.
func startOptionalPprof() {
	value := strings.TrimSpace(os.Getenv("RELAYPROXY_PPROF_ADDR"))
	if value == "" {
		return
	}
	if port, err := strconv.Atoi(value); err == nil && port > 0 && port <= 65535 {
		value = net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	}
	host, _, err := net.SplitHostPort(value)
	if err != nil {
		log.Printf("[PProf] Ignoring invalid RELAYPROXY_PPROF_ADDR=%q: %v", value, err)
		return
	}
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		log.Printf("[PProf] Refusing non-loopback profiling address %q", value)
		return
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	server := &http.Server{Addr: value, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Printf("[PProf] Runtime profiling enabled on http://%s/debug/pprof/", value)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("[PProf] Server stopped: %v", err)
		}
	}()
}
