package route

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"net/http"
	"net/http/pprof"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CoreC-Dev/CoreC/common/trace"
	"github.com/CoreC-Dev/CoreC/core"
	"github.com/CoreC-Dev/CoreC/log"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// Default HTTP server timeouts. Overridable via APIConfig.
const (
	defaultReadHeaderTimeout = 10 * time.Second
	// defaultReadTimeout and defaultWriteTimeout are 0 (disabled) by default.
	// Non-zero values set absolute deadlines on the underlying net.Conn that
	// silently kill long-lived WebSocket connections (/traffic, /memory,
	// /tags/stream) after the deadline elapses. WebSocket per-write timeouts
	// are handled separately via wsWriteTimeout, and ReadHeaderTimeout still
	// protects against slowloris attacks, so leaving these disabled is safe.
	// Operators who serve only short-lived REST requests may set explicit
	// values via APIConfig; a config value of 0 means "disabled".
	defaultReadTimeout  = 0
	defaultWriteTimeout = 0
	defaultIdleTimeout  = 120 * time.Second

	// wsWriteTimeout is the per-write timeout for WebSocket streams.
	wsWriteTimeout = 5 * time.Second

	// wsPushInterval is the default push interval for /traffic and /memory
	// WebSocket streams. Overridable via the ?interval= query parameter.
	wsPushInterval = 1 * time.Second

	// corsMaxAgeSeconds is the CORS preflight cache duration in seconds
	// (24h). Stored as an int and stringified with strconv.Itoa where
	// needed, so the numeric value is not expressed as a fragile string
	// literal.
	corsMaxAgeSeconds = 86400

	// rateLimitBucketTTL is how long a rate-limit bucket is kept after its
	// last access before being eligible for eviction. Buckets that go idle
	// for longer than this are removed so the per-IP map cannot grow
	// unbounded under a flood of distinct (e.g. spoofed) source IPs.
	rateLimitBucketTTL = 5 * time.Minute
	// rateLimitEvictInterval is how often the eviction sweep runs.
	rateLimitEvictInterval = 1 * time.Minute
)

type Config struct {
	Addr   string
	Secret string

	// TLS support (M1). When both are set, the server uses HTTPS.
	TLSCert string
	TLSKey  string

	// AllowedOrigins restricts CORS to specific origins (M3).
	// Empty means permissive default (Access-Control-Allow-Origin: *).
	AllowedOrigins []string

	// RateLimitPerSec limits requests per second per IP (M2).
	// 0 means no limit.
	RateLimitPerSec int

	// HTTP server timeouts. Zero values fall back to defaults, except for
	// ReadTimeout and WriteTimeout whose default is 0 (disabled) so that
	// long-lived WebSocket connections are not killed by an absolute
	// deadline. Set them explicitly to enable absolute deadlines.
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration

	// PprofDisabled controls whether pprof profiling endpoints are
	// disabled. Default false (pprof enabled, backward compatible).
	// Set to true to disable pprof entirely.
	PprofDisabled bool

	// PprofAddr, when non-empty, starts a separate HTTP server for pprof
	// endpoints on this address. This keeps profiling endpoints off the
	// main API port. When empty, pprof is registered on the main server
	// (unless PprofDisabled is true). The separate server does NOT require
	// authentication, so it should only be bound to a loopback or
	// private interface.
	PprofAddr string
}

var (
	httpServer    *http.Server
	serverMu      sync.Mutex // protects httpServer in ReCreateServer/CloseServer
	pprofServer   *http.Server
	pprofMu       sync.Mutex // protects pprofServer
	engine        core.Engine
	engineMu      sync.RWMutex
	ReloadFunc    func(path, payload string) error
	PatchFunc     func(patch map[string]any) error
	GetConfigFunc func() *core.Config

	// Version is the build version, injected via ldflags:
	//   -ldflags "-X github.com/CoreC-Dev/CoreC/hub/route.Version=1.0.0"
	// Default "dev" for local/test builds.
	Version = "dev"

	// startTime records when the API server was (re)created, used for uptime.
	// Stored as an atomic pointer because ReCreateServer writes it under
	// serverMu while the hello handler reads it without any lock; a bare
	// time.Time variable would be a data race (time.Time is a multi-field
	// struct). atomic.Pointer provides lock-free safe reads.
	startTime atomic.Pointer[time.Time]
)

func SetEngine(e core.Engine) {
	engineMu.Lock()
	defer engineMu.Unlock()
	engine = e
}

func init() {
	now := time.Now()
	startTime.Store(&now)
}

// getEngine returns the current engine under a read lock. Handlers must use
// this instead of reading the package-level engine variable directly to avoid
// data races with SetEngine (e.g. long-lived WebSocket goroutines overlapping
// with a test or reload that swaps the engine).
func getEngine() core.Engine {
	engineMu.RLock()
	defer engineMu.RUnlock()
	return engine
}

func ReCreateServer(cfg *Config) {
	serverMu.Lock()
	if httpServer != nil {
		_ = httpServer.Close()
		httpServer = nil
	}

	if cfg == nil || cfg.Addr == "" {
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

	// Determine pprof configuration. PprofDisabled defaults to false
	// (pprof enabled, backward compatible). When PprofAddr is set, pprof
	// runs on a separate server and is NOT registered on the main router.
	pprofOnMain := !cfg.PprofDisabled && cfg.PprofAddr == ""

	// Apply configured timeouts, falling back to defaults.
	rht := cfg.ReadHeaderTimeout
	if rht <= 0 {
		rht = defaultReadHeaderTimeout
	}
	rt := cfg.ReadTimeout
	if rt <= 0 {
		rt = defaultReadTimeout
	}
	wt := cfg.WriteTimeout
	if wt <= 0 {
		wt = defaultWriteTimeout
	}
	it := cfg.IdleTimeout
	if it <= 0 {
		it = defaultIdleTimeout
	}

	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           router(cfg.Secret, cfg.AllowedOrigins, cfg.RateLimitPerSec, pprofOnMain),
		ReadHeaderTimeout: rht,
		ReadTimeout:       rt,
		WriteTimeout:      wt,
		IdleTimeout:       it,
	}
	httpServer = server
	serverMu.Unlock()

	go func() {
		log.Infoln("RESTful API listening at %s", cfg.Addr)
		var err error
		if cfg.TLSCert != "" && cfg.TLSKey != "" {
			log.Infoln("TLS enabled — using HTTPS")
			err = server.ListenAndServeTLS(cfg.TLSCert, cfg.TLSKey)
		} else {
			err = server.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
			log.Errorln("RESTful API error: %v", err)
		}
	}()

	// Start a separate pprof server if PprofAddr is configured. This
	// keeps profiling endpoints off the main API port and does NOT
	// require authentication, so it should be bound to a loopback or
	// private interface only.
	if cfg.PprofAddr != "" && !cfg.PprofDisabled {
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
			Addr:    cfg.PprofAddr,
			Handler: pMux,
		}
		pprofMu.Unlock()
		go func() {
			log.Infoln("pprof server listening at %s (no auth)", cfg.PprofAddr)
			if err := pprofServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Errorln("pprof server error: %v", err)
			}
		}()
	}
}

// serverShutdownTimeout is the maximum time CloseServer waits for in-flight
// requests to finish during a graceful shutdown before forcefully closing
// remaining connections.
const serverShutdownTimeout = 10 * time.Second

func CloseServer() error {
	serverMu.Lock()
	defer serverMu.Unlock()
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

	// Also close the pprof server if running.
	pprofMu.Lock()
	if pprofServer != nil {
		_ = pprofServer.Close()
		pprofServer = nil
	}
	pprofMu.Unlock()

	return err
}

func router(secret string, allowedOrigins []string, rateLimitPerSec int, pprofEnabled bool) *chi.Mux {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(trace.Middleware)
	r.Use(safeRequestLogger)
	r.Use(middleware.Recoverer)
	r.Use(corsMiddleware(allowedOrigins))
	// httpMetricsMiddleware records method, status, and duration for every
	// request (including unauthenticated /healthz probes) into the
	// package-level httpMetricsCollector, which the /metrics handler exposes
	// to Prometheus. Placed after CORS (so CORS preflight 204s are counted
	// accurately) and before rate limiting so rate-limited (429) and
	// successful requests are both tracked.
	r.Use(httpMetricsMiddleware)
	if rateLimitPerSec > 0 {
		r.Use(rateLimitMiddleware(rateLimitPerSec))
	}

	r.Get("/", hello)
	r.Get("/version", getVersion)

	// Health endpoints — no auth required (Kubernetes probes don't
	// typically carry auth headers). Liveness checks only that the
	// process is alive; readiness checks that the engine can serve
	// traffic (running status + at least one healthy data path).
	r.Get("/healthz/live", healthzLive)
	r.Get("/healthz/ready", healthzReady)

	r.Group(func(r chi.Router) {
		if secret != "" {
			r.Use(authentication(secret))
		}

		r.Get("/configs", getConfigs)
		r.Put("/configs", updateConfigs)
		r.Patch("/configs", patchConfigs)

		r.Get("/drivers", getDrivers)
		r.Get("/drivers/{name}", getDriver)
		r.Get("/drivers/{name}/tags", getDriverTags)

		r.Get("/transports", getTransports)
		r.Get("/transports/{name}", getTransport)

		r.Get("/tags", getAllTags)
		r.Post("/write", writeTag)
		r.Get("/write/failed", getFailedWrites)

		r.Get("/rules", getRules)
		r.Patch("/rules/disable", disableRule)

		r.Get("/stats", getStats)
		r.Get("/memory", getMemory)

		r.Get("/logs", getLogs)
		r.Get("/traffic", getTraffic)
		r.Get("/tags/stream", streamTags)

		// Observability: Prometheus-format metrics and pprof profiling
		// endpoints. Both live inside the authenticated group so a
		// scraper or profiling client must present the API secret,
		// keeping them off the public surface.
		r.Get("/metrics", promMetrics)

		// pprof profiling endpoints. Registered on the main server only
		// when PprofEnabled is true and no separate pprof address is
		// configured. Both are inside the authenticated group so a
		// profiling client must present the API secret.
		if pprofEnabled {
			r.HandleFunc("/debug/pprof/*", pprof.Index)
			r.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
			r.HandleFunc("/debug/pprof/profile", pprof.Profile)
			r.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
			r.HandleFunc("/debug/pprof/trace", pprof.Trace)
		}
	})

	return r
}

// corsMiddleware adds CORS headers for browser-based dashboards.
// When no allowed-origins are configured, the default is permissive
// (Access-Control-Allow-Origin: *) so a browser Dashboard can connect
// out of the box. When one or more origins are explicitly listed, only
// those origins receive the header (restrictive mode).
func corsMiddleware(allowedOrigins []string) func(http.Handler) http.Handler {
	allowed := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		allowed[o] = true
	}
	allowAll := len(allowedOrigins) == 0
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if allowAll {
				w.Header().Set("Access-Control-Allow-Origin", "*")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
				w.Header().Set("Access-Control-Max-Age", strconv.Itoa(corsMaxAgeSeconds))
			} else if origin != "" && allowed[origin] {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
				w.Header().Set("Access-Control-Max-Age", strconv.Itoa(corsMaxAgeSeconds))
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// rateLimitMiddleware limits requests per second per client IP using a simple
// token-bucket approach. This prevents brute-force attacks on the API secret
// and flood attacks on POST /write.
//
// The client IP is always taken from r.RemoteAddr. The X-Real-IP (and
// X-Forwarded-For) headers are intentionally NOT trusted, because a client
// can spoof them to attribute requests to arbitrary IPs and bypass the
// limit. If trusted-proxy support is needed in the future, it must be an
// explicit, opt-in configuration that only honors forwarded headers from
// known proxy addresses.
//
// To keep the buckets map from growing unbounded under a spoofed-IP flood,
// a background goroutine periodically evicts buckets that have not been
// accessed within rateLimitBucketTTL.
func rateLimitMiddleware(perSec int) func(http.Handler) http.Handler {
	type bucket struct {
		mu         sync.Mutex
		tokens     int
		lastTime   time.Time
		lastAccess time.Time
	}
	var (
		bucketsMu sync.Mutex
		buckets   = make(map[string]*bucket)
	)

	// Evict stale buckets periodically so the map cannot grow unbounded.
	go func() {
		ticker := time.NewTicker(rateLimitEvictInterval)
		defer ticker.Stop()
		for now := range ticker.C {
			cutoff := now.Add(-rateLimitBucketTTL)
			bucketsMu.Lock()
			for ip, b := range buckets {
				b.mu.Lock()
				stale := b.lastAccess.Before(cutoff)
				b.mu.Unlock()
				if stale {
					delete(buckets, ip)
				}
			}
			bucketsMu.Unlock()
		}
	}()

	refill := func(b *bucket) {
		now := time.Now()
		elapsed := now.Sub(b.lastTime)
		add := int(elapsed.Seconds() * float64(perSec))
		if add > 0 {
			b.tokens += add
			if b.tokens > perSec {
				b.tokens = perSec
			}
			b.lastTime = now
		}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Always use the direct remote address. Never trust
			// client-supplied forwarding headers for rate-limit keying.
			ip := r.RemoteAddr
			now := time.Now()

			bucketsMu.Lock()
			b, ok := buckets[ip]
			if !ok {
				b = &bucket{tokens: perSec, lastTime: now, lastAccess: now}
				buckets[ip] = b
			}
			bucketsMu.Unlock()

			b.mu.Lock()
			b.lastAccess = now
			refill(b)
			if b.tokens <= 0 {
				b.mu.Unlock()
				w.Header().Set("Retry-After", strconv.Itoa(1))
				renderError(w, r, http.StatusTooManyRequests, "rate limit exceeded")
				return
			}
			b.tokens--
			b.mu.Unlock()

			next.ServeHTTP(w, r)
		})
	}
}

// safeRequestLogger logs HTTP requests at DEBUG level without exposing the token
// query parameter. Replaces chi's middleware.Logger to prevent credential
// disclosure in access logs. Logging at DEBUG means request lines are governed by
// the global log-level setting: visible when log-level=debug, silenced at info
// and above (production), and hot-reloadable via PATCH /configs.
func safeRequestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Redact token from URI for logging
		uri := r.RequestURI
		if r.URL.Query().Has("token") {
			q := r.URL.Query()
			q.Del("token")
			redacted := r.URL.Path
			if encoded := q.Encode(); encoded != "" {
				redacted += "?" + encoded
			}
			uri = redacted
		}

		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)

		log.Debugln("%s %s %d %dB in %v", r.Method, uri, ww.Status(), ww.BytesWritten(), time.Since(start))
	})
}

func authentication(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := r.Header.Get("Authorization")
			if token == "" {
				token = r.URL.Query().Get("token")
			} else if len(token) > 7 && token[:7] == "Bearer " {
				token = token[7:]
			}

			// Hash both values to fixed 32-byte length before comparison,
			// so timing doesn't leak the secret length. Using
			// subtle.ConstantTimeCompare directly returns immediately when
			// lengths differ, revealing the secret length to an attacker.
			hashSecret := sha256.Sum256([]byte(secret))
			hashToken := sha256.Sum256([]byte(token))
			if !hmac.Equal(hashSecret[:], hashToken[:]) {
				renderError(w, r, http.StatusUnauthorized, "unauthorized")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func hello(w http.ResponseWriter, r *http.Request) {
	uptime := "unknown"
	if st := startTime.Load(); st != nil {
		uptime = time.Since(*st).String()
	}
	render(w, r, http.StatusOK, map[string]string{
		"name":    "corec",
		"version": Version,
		"status":  "ok",
		"time":    time.Now().Format(time.RFC3339),
		"uptime":  uptime,
	})
}

func getVersion(w http.ResponseWriter, r *http.Request) {
	render(w, r, http.StatusOK, map[string]string{"version": Version})
}
