package route

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/CoreC-Dev/CoreC/log"
	"github.com/go-chi/chi/v5/middleware"
)

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
				w.Header().Set("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
				w.Header().Set("Access-Control-Max-Age", strconv.Itoa(corsMaxAgeSeconds))
			} else if origin != "" {
				// Origin not in allowlist — emit Vary so caches don't serve
				// a cross-origin response to a different origin.
				w.Header().Set("Vary", "Origin")
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
func rateLimitMiddleware(ctx context.Context, perSec int) func(http.Handler) http.Handler {
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
	// The goroutine exits when ctx is cancelled (server shutdown/reload),
	// preventing the goroutine leak that occurred when ReCreateServer
	// rebuilt the router without stopping the previous eviction loop.
	go func() {
		ticker := time.NewTicker(rateLimitEvictInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
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
			// Fail-closed: an empty configured secret must never authenticate,
			// even against an empty token (sha256("") == sha256("") would
			// otherwise pass). The server refuses to start with an empty
			// secret, but this is a defense-in-depth guard.
			if secret == "" {
				renderError(w, r, http.StatusUnauthorized, "unauthorized")
				return
			}
			token := r.Header.Get("Authorization")
			if token != "" {
				if len(token) > 7 && token[:7] == "Bearer " {
					token = token[7:]
				}
			} else {
				// Only allow query-parameter token for WebSocket upgrade
				// requests, where the browser WebSocket API does not
				// support custom Authorization headers. For regular HTTP
				// requests, require the Authorization header to avoid
				// token leakage via browser history, reverse-proxy access
				// logs, and HTTP Referer headers.
				if isWebSocketUpgrade(r) {
					token = r.URL.Query().Get("token")
				}
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

// isWebSocketUpgrade reports whether the request is a WebSocket upgrade
// handshake. The browser WebSocket API cannot set custom Authorization
// headers, so these requests are permitted to carry the token via a
// query parameter as a narrowly-scoped exception.
func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}
