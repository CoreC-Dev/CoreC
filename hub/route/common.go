package route

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sync/atomic"

	"github.com/coder/websocket"
)

// maxRequestBody limits JSON body size to prevent OOM from oversized requests.
// 1 MiB is sufficient for typical config payloads; if large rule sets exceed
// this, consider making it configurable via APIConfig.
const maxRequestBody = 1 << 20 // 1 MiB

// wsAllowedOrigins is the set of origins permitted to upgrade to a WebSocket
// connection. It is populated by router() from the configured AllowedOrigins.
// When empty, cross-origin upgrades are allowed (matching the permissive
// default of corsMiddleware for local/dev deployments).
//
// This is required because coder/websocket's Accept performs its own Origin
// check independent of the chi CORS middleware. Without feeding OriginPatterns,
// any cross-origin WS upgrade (e.g. dashboard at :3080 → CoreC at :9090) is
// rejected with HTTP 403, silently breaking all realtime pages.
//
// Stored as an atomic.Value so router() can swap it during hot-reload without
// racing concurrent acceptWS callers (BR-4).
var wsAllowedOrigins atomic.Value // stores []string

// getWSAllowedOrigins atomically loads the current allowed-origins slice.
func getWSAllowedOrigins() []string {
	v := wsAllowedOrigins.Load()
	if v == nil {
		return nil
	}
	return v.([]string)
}

// setWSAllowedOrigins atomically stores the allowed-origins slice.
func setWSAllowedOrigins(origins []string) {
	wsAllowedOrigins.Store(origins)
}

// acceptWS accepts a WebSocket connection with OriginPatterns derived from the
// configured allowed origins. When no origins are configured (permissive mode),
// it allows all origins — matching corsMiddleware's behavior.
func acceptWS(w http.ResponseWriter, r *http.Request) (*websocket.Conn, error) {
	opts := &websocket.AcceptOptions{}
	allowed := getWSAllowedOrigins()
	if len(allowed) == 0 {
		// Permissive mode: allow any origin (local/dev default).
		opts.InsecureSkipVerify = true
	} else {
		opts.OriginPatterns = allowed
	}
	return websocket.Accept(w, r, opts)
}

func render(w http.ResponseWriter, r *http.Request, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		slog.Error("failed to encode JSON response", "error", err)
	}
}

func renderError(w http.ResponseWriter, r *http.Request, status int, message string) {
	render(w, r, status, map[string]string{"error": message})
}

// renderInternalError logs the real error internally but returns a generic
// message to the client. This prevents leaking device IPs, file paths,
// and other internal details through error responses.
func renderInternalError(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("internal error",
		"method", r.Method,
		"path", r.URL.Path,
		"error", err,
	)
	renderError(w, r, http.StatusInternalServerError, "internal server error")
}

func renderNoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

// limitedBody wraps the request body with a size limit to prevent OOM/DoS.
func limitedBody(r *http.Request) *http.Request {
	r.Body = http.MaxBytesReader(nil, r.Body, maxRequestBody)
	return r
}
