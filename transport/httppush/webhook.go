package httppush

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
)

// webhookMaxBodyBytes is the hard cap on a single webhook request body.
// It protects the process from OOM caused by oversized or malicious
// payloads (H1).
const webhookMaxBodyBytes = 10 * 1024 * 1024 // 10 MiB

// handleWebhook processes incoming POST requests containing DataPoint JSON
// (single object or array).  This is the chained-core inbound path for
// HTTP.
func (t *HTTPTransport) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Authenticate the request when a webhook secret is configured.  The
	// secret may be supplied in either an "Authorization: Bearer <secret>"
	// header or an "X-Webhook-Secret: <secret>" header.  Mismatched or
	// missing credentials are rejected with 401.  When no secret is
	// configured, authentication is skipped (backward-compatible).
	if t.webhookSecret != "" {
		auth := r.Header.Get("Authorization")
		xSecret := r.Header.Get("X-Webhook-Secret")

		// Use constant-time comparison to prevent timing side-channel
		// attacks that could leak the webhook secret byte-by-byte.
		// Hash both values to fixed 32-byte length before comparison
		// so timing doesn't leak the secret length.
		hashSecret := sha256.Sum256([]byte(t.webhookSecret))
		hashAuth := sha256.Sum256([]byte(auth))
		hashXSecret := sha256.Sum256([]byte(xSecret))
		expectedAuth := sha256.Sum256([]byte("Bearer " + t.webhookSecret))

		authOK := hmac.Equal(hashAuth[:], expectedAuth[:])
		xSecretOK := hmac.Equal(hashXSecret[:], hashSecret[:])
		if !authOK && !xSecretOK {
			slog.Warn("http webhook: unauthorized request",
				"name", t.name, "addr", t.webhookAddr, "remote", r.RemoteAddr)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}

	defer r.Body.Close()
	// Limit the body size to prevent OOM from oversized payloads.  When
	// the limit is exceeded, io.ReadAll returns an error and we reject
	// the request.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, webhookMaxBodyBytes))
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	// Determine if the payload is a JSON array or a single object.
	// For arrays, parse each element through the parser; for objects,
	// parse the whole body once.
	var rawElements []json.RawMessage
	if err := json.Unmarshal(body, &rawElements); err != nil {
		// Not an array — treat as single object
		rawElements = []json.RawMessage{body}
	}

	for _, raw := range rawElements {
		dp, err := t.dataParser.Parse(raw, "")
		if err != nil {
			slog.Error("http webhook: failed to parse payload", "error", err)
			continue
		}
		select {
		case t.dataCh <- dp:
			t.received.Add(1)
		default:
			slog.Warn("http webhook: data channel full, dropping data point",
				"driver", dp.Driver, "tag", dp.Tag)
		}
	}

	w.WriteHeader(http.StatusAccepted)
}
