package route

import (
	"context"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
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
	httpServer             *http.Server
	serverMu               sync.Mutex                   // protects httpServer in ReCreateServer/CloseServer
	serverCancel           context.CancelFunc           // cancels the active server's lifecycle context
	serverListener         net.Listener                 // persistent listener for zero-downtime reload
	serverAddr             string                       // current listening address
	handlerPtr             atomic.Pointer[http.Handler] // swapped atomically on hot-reload
	pprofServer            *http.Server
	pprofMu                sync.Mutex // protects pprofServer
	engine                 core.Engine
	engineMu               sync.RWMutex
	ReloadFunc             func(path, payload string) error
	PatchFunc              func(patch map[string]any) error
	GetConfigFunc          func() *core.Config
	GetRawConfigFunc       func() (string, error)                 // Path A: GET /configs/raw — full redacted YAML
	GetRawConfigRevealFunc func() (string, error)                 // GET /configs/raw?reveal=true — full YAML with secrets in plaintext
	ValidateFunc           func(payload string) ([]string, error) // Path A: POST /configs/validate — dry-run; returns idle warnings + error

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

// atomicHandler delegates to the handler stored in handlerPtr, enabling
// zero-downtime hot-reload: ReCreateServer swaps handlerPtr atomically
// while the underlying net.Listener and http.Server stay alive. In-flight
// requests continue on the old handler; new requests pick up the new one.
type atomicHandler struct{}

func (atomicHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h := handlerPtr.Load(); h != nil {
		(*h).ServeHTTP(w, r)
	} else {
		http.Error(w, "server not ready", http.StatusServiceUnavailable)
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
