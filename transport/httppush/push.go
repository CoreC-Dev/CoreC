// Package httppush implements the HTTP Push transport for CoreC.
package httppush

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CoreC-Dev/CoreC/common/trace"
	"github.com/CoreC-Dev/CoreC/common/util"
	"github.com/CoreC-Dev/CoreC/core"
	"github.com/CoreC-Dev/CoreC/transport/parser"
)

// TypeName identifies this transport's protocol. It is returned by Type()
// and embedded in Status() so the transport kind is reported consistently
// from a single source of truth rather than a scattered string literal.
const TypeName = "http"

// HTTPTransport implements core.Transport for pushing collected data to HTTP endpoints.
type HTTPTransport struct {
	mu sync.RWMutex

	name   string
	config core.TransportConfig

	// HTTP client settings
	url     string
	method  string
	headers map[string]string
	client  *http.Client
	timeout time.Duration

	// Webhook server (chained-core inbound — receives data via HTTP POST)
	webhookAddr    string // listen address, e.g. ":9090"
	webhookPath    string // URL path, e.g. "/data"
	webhookSecret  string // shared secret for authenticating webhook requests; empty = no auth
	webhookTLSCert string // PEM cert file path for the webhook server (enables HTTPS); empty = plaintext
	webhookTLSKey  string // PEM key file path for the webhook server (enables HTTPS); empty = plaintext
	webhookSrv     *http.Server
	dataParser     parser.Parser
	dataCh         chan core.DataPoint
	received       atomic.Uint64

	// State
	state core.ConnState

	// Counters
	published   atomic.Uint64
	failed      atomic.Uint64
	lastPublish time.Time

	// Command channel (HTTP is typically unidirectional push, but can receive commands via webhook if extended)
	commandCh chan core.WriteCommand

	// stopOnce ensures Stop() is idempotent — double close of commandCh would panic.
	stopOnce sync.Once

	ctx    context.Context
	cancel context.CancelFunc
}

// NewHTTPTransport creates a new HTTP transport.
func NewHTTPTransport(config core.TransportConfig) (core.Transport, error) {
	bufSize := config.BufferSize
	if bufSize <= 0 {
		bufSize = core.DefaultCommandBufferSize
	}
	t := &HTTPTransport{
		name:      config.Name,
		config:    config,
		headers:   make(map[string]string),
		state:     core.StateDisconnected,
		commandCh: make(chan core.WriteCommand, bufSize),
		dataCh:    make(chan core.DataPoint, bufSize),
	}
	return t, nil
}

func (t *HTTPTransport) Init(ctx context.Context, config core.TransportConfig) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Keep the full config so PublishBatch can read RetryCount.  This is
	// set here (under the lock) rather than only at construction so that
	// a re-Init with a different config is honoured.
	t.config = config

	settings := config.Settings

	// Parse target URL (optional when used as webhook-only inbound)
	if u, ok := settings["url"].(string); ok {
		t.url = u
	}

	// HTTP method
	if m, ok := settings["method"].(string); ok && m != "" {
		t.method = m
	} else {
		t.method = http.MethodPost
	}

	// Headers
	if headersMap, ok := settings["headers"].(map[string]any); ok {
		for k, v := range headersMap {
			t.headers[k] = fmt.Sprintf("%v", v)
		}
	}

	// Timeout
	if timeoutStr, ok := settings["timeout"].(string); ok {
		if d, err := time.ParseDuration(timeoutStr); err == nil {
			t.timeout = d
		}
	}
	if t.timeout == 0 {
		t.timeout = core.DefaultTransportTimeout
	}

	// Custom HTTP client with configurable connection pool
	maxIdleConns := util.GetIntSetting(settings, "max-idle-conns", 100)
	maxIdleConnsPerHost := util.GetIntSetting(settings, "max-idle-conns-per-host", 20)
	idleConnTimeout := util.GetDurationSetting(settings, "idle-conn-timeout", 90*time.Second)

	t.client = &http.Client{
		Timeout: t.timeout,
		Transport: &http.Transport{
			MaxIdleConns:        maxIdleConns,
			MaxIdleConnsPerHost: maxIdleConnsPerHost,
			IdleConnTimeout:     idleConnTimeout,
		},
	}

	slog.Info("http transport initialized",
		"name", t.name,
		"url", t.url,
		"method", t.method,
	)

	// Parse webhook config (chained-core inbound)
	if addr, ok := settings["webhook-addr"].(string); ok && addr != "" {
		t.webhookAddr = addr
		t.webhookPath = util.GetStringSetting(settings, "webhook-path", "/data")
		// Optional shared secret for authenticating webhook POSTs.
		// When set, requests must carry it in either an
		// "Authorization: Bearer <secret>" header or an
		// "X-Webhook-Secret: <secret>" header.  When empty, requests
		// are accepted unauthenticated (backward-compatible, but a
		// warning is logged at Start() time).
		if secret, ok := settings["webhook-secret"].(string); ok {
			t.webhookSecret = secret
		}
		// Optional TLS for the webhook server.  When both tls-cert-file
		// and tls-key-file are set, the webhook listens over HTTPS
		// (ListenAndServeTLS); when either is empty the webhook stays
		// plaintext HTTP (backward-compatible).  This mirrors the MQTT
		// transport's tls-cert-file / tls-key-file settings.
		if cert, ok := settings["tls-cert-file"].(string); ok {
			t.webhookTLSCert = cert
		}
		if key, ok := settings["tls-key-file"].(string); ok {
			t.webhookTLSKey = key
		}
		p, err := parser.New(settings)
		if err != nil {
			return fmt.Errorf("http transport: invalid parser config: %w", err)
		}
		t.dataParser = p
	}

	// Must have at least an outbound URL or an inbound webhook
	if t.url == "" && t.webhookAddr == "" {
		return fmt.Errorf("http transport: either url or webhook-addr is required")
	}

	return nil
}

func (t *HTTPTransport) Start(ctx context.Context) error {
	t.ctx, t.cancel = context.WithCancel(ctx)

	t.mu.Lock()
	t.state = core.StateConnected
	t.mu.Unlock()

	// Start webhook server if configured (chained-core inbound)
	if t.webhookAddr != "" {
		// Warn when the webhook has no shared secret: in that mode any
		// client can POST data to the webhook endpoint.  This keeps
		// backward compatibility while making the risk visible.
		if t.webhookSecret == "" {
			slog.Warn("http webhook has no webhook-secret; accepting unauthenticated requests",
				"name", t.name, "addr", t.webhookAddr, "path", t.webhookPath)
		}
		mux := http.NewServeMux()
		mux.HandleFunc(t.webhookPath, t.handleWebhook)
		t.webhookSrv = &http.Server{
			Addr:    t.webhookAddr,
			Handler: mux,
			TLSConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
			},
		}
		go func() {
			// TLS is enabled only when both the cert and key file paths
			// are configured.  When only one is set we warn and fall
			// back to plaintext rather than failing to start, so a
			// partial TLS config degrades safely instead of breaking
			// the inbound path.
			if t.webhookTLSCert != "" && t.webhookTLSKey != "" {
				slog.Info("http webhook server starting (TLS)",
					"name", t.name, "addr", t.webhookAddr, "path", t.webhookPath)
				if err := t.webhookSrv.ListenAndServeTLS(t.webhookTLSCert, t.webhookTLSKey); err != nil && err != http.ErrServerClosed {
					slog.Error("http webhook TLS server failed", "name", t.name, "error", err)
				}
			} else {
				if t.webhookTLSCert != "" || t.webhookTLSKey != "" {
					slog.Warn("http webhook TLS partially configured; both tls-cert-file and tls-key-file are required, falling back to plaintext",
						"name", t.name, "addr", t.webhookAddr)
				}
				slog.Info("http webhook server starting (plaintext)",
					"name", t.name, "addr", t.webhookAddr, "path", t.webhookPath)
				if err := t.webhookSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
					slog.Error("http webhook server failed", "name", t.name, "error", err)
				}
			}
		}()
	}

	slog.Info("http transport started", "name", t.name, "url", t.url)
	return nil
}

func (t *HTTPTransport) Stop() error {
	t.stopOnce.Do(func() {
		if t.cancel != nil {
			t.cancel()
		}

		// Shutdown webhook server if running
		if t.webhookSrv != nil {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = t.webhookSrv.Shutdown(shutdownCtx)
		}

		t.mu.Lock()
		t.state = core.StateDisconnected
		if t.client != nil {
			t.client.CloseIdleConnections()
		}
		t.mu.Unlock()

		close(t.commandCh)
		slog.Info("http transport stopped", "name", t.name)
	})
	return nil
}

func (t *HTTPTransport) Publish(ctx context.Context, point core.DataPoint) error {
	return t.PublishBatch(ctx, []core.DataPoint{point})
}

func (t *HTTPTransport) PublishBatch(ctx context.Context, points []core.DataPoint) error {
	// Webhook-only inbound mode: no outbound URL, skip publishing
	if t.url == "" {
		return nil
	}

	t.mu.RLock()
	if t.state != core.StateConnected {
		t.mu.RUnlock()
		t.failed.Add(uint64(len(points)))
		return fmt.Errorf("http transport %s is not connected", t.name)
	}
	client := t.client
	url := t.url
	method := t.method
	headers := make(map[string]string, len(t.headers))
	for k, v := range t.headers {
		headers[k] = v
	}
	retryCount := t.config.RetryCount
	t.mu.RUnlock()
	if retryCount < 0 {
		retryCount = 0
	}

	payload, err := json.Marshal(points)
	if err != nil {
		t.failed.Add(uint64(len(points)))
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	// Retry transient failures up to RetryCount times with exponential
	// backoff (500ms, 1s, 2s, ...).  Transient failures are network
	// errors and 5xx responses; 4xx (and other non-2xx non-5xx) responses
	// are client errors that will not change on retry and are returned
	// immediately.  A RetryCount of 0 disables retry (single attempt),
	// preserving the previous behaviour.
	var lastErr error
	backoff := 500 * time.Millisecond
	maxAttempts := retryCount + 1
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				t.failed.Add(uint64(len(points)))
				return fmt.Errorf("http push cancelled during retry: %w", ctx.Err())
			case <-time.After(backoff):
			}
			backoff *= 2
		}

		req, rerr := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(payload))
		if rerr != nil {
			t.failed.Add(uint64(len(points)))
			return fmt.Errorf("failed to create http request: %w", rerr)
		}
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		// Propagate W3C trace context to the downstream service so that
		// the trace spans can be correlated across service boundaries.
		trace.InjectTraceparent(ctx, req)

		resp, derr := client.Do(req)
		if derr != nil {
			// Network error — transient, retry.
			lastErr = fmt.Errorf("http push error: %w", derr)
			slog.Warn("http push failed, will retry",
				"name", t.name, "attempt", attempt+1, "max", maxAttempts, "error", derr)
			continue
		}

		// Drain and close the body so the connection can be reused.
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			t.published.Add(uint64(len(points)))
			t.mu.Lock()
			t.lastPublish = time.Now()
			t.mu.Unlock()
			return nil
		}

		lastErr = fmt.Errorf("http push returned non-2xx status: %d", resp.StatusCode)
		// Only 5xx is retried; 4xx and other non-2xx are terminal.
		if resp.StatusCode < 500 || resp.StatusCode >= 600 {
			break
		}
		slog.Warn("http push returned 5xx, will retry",
			"name", t.name, "attempt", attempt+1, "max", maxAttempts, "status", resp.StatusCode)
	}

	t.failed.Add(uint64(len(points)))
	return lastErr
}

func (t *HTTPTransport) OnCommand() <-chan core.WriteCommand {
	return t.commandCh
}

// OnData returns the channel of data points received via the webhook
// HTTP server.  Returns nil when webhook-addr is not configured.
func (t *HTTPTransport) OnData() <-chan core.DataPoint {
	if t.webhookAddr == "" {
		return nil
	}
	return t.dataCh
}

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

func (t *HTTPTransport) Name() string { return t.name }
func (t *HTTPTransport) Type() string { return TypeName }

func (t *HTTPTransport) Status() core.TransportStatus {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return core.TransportStatus{
		Name:        t.name,
		Type:        TypeName,
		State:       t.state,
		Published:   t.published.Load(),
		Failed:      t.failed.Load(),
		Received:    t.received.Load(),
		LastPublish: t.lastPublish,
		QueueSize:   0,
	}
}
