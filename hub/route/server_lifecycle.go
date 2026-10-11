package route

import (
	"context"
	"net"
	"net/http"
	"net/http/pprof"
	"time"

	"github.com/CoreC-Dev/CoreC/log"
)

// serverShutdownTimeout is the maximum time CloseServer waits for in-flight
// requests to finish during a graceful shutdown before forcefully closing
// remaining connections.
const serverShutdownTimeout = 10 * time.Second

func ReCreateServer(cfg *Config) {
	serverMu.Lock()
	// Cancel the previous server's lifecycle context to stop any
	// background goroutines (e.g. rate-limiter eviction) and prevent
	// accumulation on hot-reload.
	if serverCancel != nil {
		serverCancel()
		serverCancel = nil
	}

	if cfg == nil || cfg.Addr == "" {
		// Shut down the existing server if any.
		if httpServer != nil {
			_ = httpServer.Close()
			httpServer = nil
		}
		if serverListener != nil {
			_ = serverListener.Close()
			serverListener = nil
		}
		serverAddr = ""
		handlerPtr.Store(nil)
		serverMu.Unlock()
		return
	}

	if cfg.Secret == "" {
		serverMu.Unlock()
		// Fail closed: an empty secret would leave every endpoint
		// (including POST /write, PUT /configs, PATCH /rules/disable)
		// publicly accessible. Refuse to start the server instead of
		// silently running in an insecure mode. Operators must set
		// api.secret in the configuration.
		log.Errorln("API server refusing to start: api.secret is empty. " +
			"An empty secret disables authentication and exposes all endpoints. " +
			"Set api.secret in the configuration before starting the server.")
		return
	}

	now := time.Now()
	startTime.Store(&now)

	// Create a lifecycle context for this server instance. It is
	// cancelled when ReCreateServer or CloseServer is called again,
	// stopping background goroutines (e.g. rate-limiter eviction).
	ctx, cancel := context.WithCancel(context.Background())
	serverCancel = cancel

	// Determine pprof configuration. PprofDisabled defaults to false
	// (pprof enabled, backward compatible). When PprofAddr is set, pprof
	// runs on a separate server and is NOT registered on the main router.
	pprofOnMain := !cfg.PprofDisabled && cfg.PprofAddr == ""

	// Build the new router and swap it atomically. If the server is
	// already listening on the same address, this is the ONLY change —
	// the listener and http.Server stay alive, achieving zero-downtime.
	newHandler := router(ctx, cfg.Secret, cfg.AllowedOrigins, cfg.RateLimitPerSec, pprofOnMain)
	h := http.Handler(newHandler)
	handlerPtr.Store(&h)

	// If the server is already running on the same address, the handler
	// swap above is sufficient — no need to close/recreate the listener.
	if httpServer != nil && serverListener != nil && serverAddr == cfg.Addr {
		serverMu.Unlock()
		log.Infoln("API server hot-reloaded at %s (zero-downtime handler swap)", cfg.Addr)
		return
	}

	// Address changed or first startup: close the old listener if any.
	if serverListener != nil {
		_ = serverListener.Close()
		serverListener = nil
	}
	if httpServer != nil {
		_ = httpServer.Close()
		httpServer = nil
	}

	// Apply configured timeouts, falling back to defaults.
	rht, rt, wt, it := resolveTimeouts(cfg)

	// Create the listener upfront so a bind failure is reported
	// synchronously rather than in the goroutine below.
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		serverCancel = nil
		handlerPtr.Store(nil)
		serverAddr = ""
		serverMu.Unlock()
		log.Errorln("API server failed to bind %s: %v", cfg.Addr, err)
		return
	}
	serverListener = ln
	serverAddr = cfg.Addr

	server := &http.Server{
		Handler:           atomicHandler{},
		ReadHeaderTimeout: rht,
		ReadTimeout:       rt,
		WriteTimeout:      wt,
		IdleTimeout:       it,
	}
	httpServer = server
	serverMu.Unlock()

	go serveAPI(server, ln, cfg)

	// Start a separate pprof server if PprofAddr is configured.
	startPprofServer(cfg)
}

// resolveTimeouts applies configured timeouts, falling back to defaults
// for any non-positive value.
func resolveTimeouts(cfg *Config) (rht, rt, wt, it time.Duration) {
	rht = cfg.ReadHeaderTimeout
	if rht <= 0 {
		rht = defaultReadHeaderTimeout
	}
	rt = cfg.ReadTimeout
	if rt <= 0 {
		rt = defaultReadTimeout
	}
	wt = cfg.WriteTimeout
	if wt <= 0 {
		wt = defaultWriteTimeout
	}
	it = cfg.IdleTimeout
	if it <= 0 {
		it = defaultIdleTimeout
	}
	return rht, rt, wt, it
}

// serveAPI runs the HTTP server in a background goroutine. It logs
// listen/startup messages and any serve errors.
func serveAPI(server *http.Server, ln net.Listener, cfg *Config) {
	log.Infoln("RESTful API listening at %s", cfg.Addr)
	var err error
	if cfg.TLSCert != "" && cfg.TLSKey != "" {
		log.Infoln("TLS enabled — using HTTPS")
		err = server.ServeTLS(ln, cfg.TLSCert, cfg.TLSKey)
	} else {
		err = server.Serve(ln)
	}
	if err != nil && err != http.ErrServerClosed {
		log.Errorln("RESTful API error: %v", err)
	}
}

// startPprofServer starts a separate pprof server if PprofAddr is
// configured. This keeps profiling endpoints off the main API port
// and does NOT require authentication, so it should be bound to a
// loopback or private interface only.
//
// SEC-006 (D11): the separate pprof server has no authentication, so it
// must only bind to a loopback address. A non-loopback PprofAddr (e.g.
// 0.0.0.0:6060 or a public IP) is refused to prevent exposing profiling
// endpoints — which leak goroutine stacks, heap profiles, and CPU data —
// to the network. This is a fail-closed behavior change: previously any
// address was accepted.
func startPprofServer(cfg *Config) {
	if cfg.PprofAddr == "" || cfg.PprofDisabled {
		return
	}
	if !isLoopbackAddr(cfg.PprofAddr) {
		log.Errorln("pprof server refusing to start on non-loopback address %s: "+
			"the separate pprof server has no authentication and must be bound to a "+
			"loopback interface only (e.g. 127.0.0.1:<port> or localhost:<port>). "+
			"Use PprofDisabled to disable pprof, or leave PprofAddr empty to register "+
			"pprof behind the authenticated main API.", cfg.PprofAddr)
		return
	}
	pprofMu.Lock()
	if pprofServer != nil {
		_ = pprofServer.Close()
	}
	pMux := http.NewServeMux()
	pMux.HandleFunc("/debug/pprof/", pprof.Index)
	pMux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	pMux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	pMux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	pMux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	pprofServer = &http.Server{
		Addr:              cfg.PprofAddr,
		Handler:           pMux,
		ReadHeaderTimeout: defaultReadHeaderTimeout,
		ReadTimeout:       defaultReadTimeout,
	}
	srv := pprofServer
	pprofMu.Unlock()
	go func() {
		log.Infoln("pprof server listening at %s (no auth)", cfg.PprofAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Errorln("pprof server error: %v", err)
		}
	}()
}

func CloseServer() error {
	serverMu.Lock()
	defer serverMu.Unlock()
	// Cancel the server's lifecycle context to stop background goroutines
	// (e.g. rate-limiter eviction loop).
	if serverCancel != nil {
		serverCancel()
		serverCancel = nil
	}
	if httpServer == nil {
		// Still close pprof server if it exists
		pprofMu.Lock()
		if pprofServer != nil {
			_ = pprofServer.Close()
			pprofServer = nil
		}
		pprofMu.Unlock()
		return nil
	}
	// Graceful shutdown: stop accepting new connections and give in-flight
	// requests up to serverShutdownTimeout to complete. This avoids killing
	// active requests (and in-progress WebSocket handshakes) mid-flight,
	// which httpServer.Close() would do abruptly. When the context expires,
	// Shutdown closes any remaining connections.
	ctx, cancel := context.WithTimeout(context.Background(), serverShutdownTimeout)
	defer cancel()
	err := httpServer.Shutdown(ctx)
	httpServer = nil
	if serverListener != nil {
		_ = serverListener.Close()
		serverListener = nil
	}
	serverAddr = ""
	handlerPtr.Store(nil)

	// Also close the pprof server if running.
	pprofMu.Lock()
	if pprofServer != nil {
		_ = pprofServer.Close()
		pprofServer = nil
	}
	pprofMu.Unlock()

	return err
}

// isLoopbackAddr reports whether addr (a host:port string) binds only to a
// loopback interface. It accepts explicit loopback IPs (127.0.0.1, ::1) and
// the literal hostname "localhost". It rejects:
//   - empty hosts (":port" binds all interfaces),
//   - non-loopback IPs (0.0.0.0, 192.168.x.x, public IPs),
//   - arbitrary hostnames, which could resolve to a non-loopback address and
//     cannot be verified statically (fail-closed).
//
// SEC-006 (D11): used to gate the unauthenticated separate pprof server.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false // arbitrary hostname; cannot verify statically
	}
	return ip.IsLoopback()
}
