package route

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// maxRequestBody limits JSON body size to prevent OOM from oversized requests.
// 1 MiB is sufficient for typical config payloads; if large rule sets exceed
// this, consider making it configurable via APIConfig.
const maxRequestBody = 1 << 20 // 1 MiB

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
