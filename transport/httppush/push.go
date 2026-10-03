// Package httppush implements the HTTP Push transport for CoreC.
package httppush

import (
	"bytes"
	"context"
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

	t.parseHTTPSettings(settings)
	t.initHTTPClient(settings)

	if err := t.parseWebhookSettings(settings); err != nil {
		return err
	}

	// Must have at least an outbound URL or an inbound webhook
	if t.url == "" && t.webhookAddr == "" {
		return fmt.Errorf("http transport: either url or webhook-addr is required")
	}

	slog.Info("http transport initialized",
		"name", t.name,
		"url", t.url,
		"method", t.method,
	)
	return nil
}

// parseHTTPSettings extracts the outbound HTTP settings (url, method, headers,
// timeout) from the settings map into the transport fields.
func (t *HTTPTransport) parseHTTPSettings(settings map[string]any) {
	if u, ok := settings["url"].(string); ok {
		t.url = u
	}
	if m, ok := settings["method"].(string); ok && m != "" {
		t.method = m
	} else {
		t.method = http.MethodPost
	}
	if headersMap, ok := settings["headers"].(map[string]any); ok {
		for k, v := range headersMap {
			t.headers[k] = fmt.Sprintf("%v", v)
		}
	}
	if timeoutStr, ok := settings["timeout"].(string); ok {
		if d, err := time.ParseDuration(timeoutStr); err == nil {
			t.timeout = d
		}
	}
	if t.timeout == 0 {
		t.timeout = core.DefaultTransportTimeout
	}
}

// initHTTPClient creates a custom HTTP client with a configurable connection
// pool.
func (t *HTTPTransport) initHTTPClient(settings map[string]any) {
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
}

// parseWebhookSettings extracts the inbound webhook configuration (chained-core
// inbound) from the settings map. Returns an error if the parser config is
// invalid.
func (t *HTTPTransport) parseWebhookSettings(settings map[string]any) error {
	addr, ok := settings["webhook-addr"].(string)
	if !ok || addr == "" {
		return nil
	}
	t.webhookAddr = addr
	t.webhookPath = util.GetStringSetting(settings, "webhook-path", "/data")
	// Optional shared secret for authenticating webhook POSTs.
	if secret, ok := settings["webhook-secret"].(string); ok {
		t.webhookSecret = secret
	}
	// Optional TLS for the webhook server.
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
	return nil
}

func (t *HTTPTransport) Start(ctx context.Context) error {
	t.ctx, t.cancel = context.WithCancel(ctx)

	t.mu.Lock()
	t.state = core.StateConnected
	t.mu.Unlock()

	// Start webhook server if configured (chained-core inbound)
	if t.webhookAddr != "" {
		// Fail-closed: if a webhook address is configured but no
		// webhook-secret is set, reject startup. Accepting unauthenticated
		// requests would allow any client to POST data to the webhook.
		if t.webhookSecret == "" {
			return fmt.Errorf("http transport %s: webhook-addr %q is configured but webhook-secret is empty; refusing to start without webhook authentication (set webhook-secret or remove webhook-addr)",
				t.name, t.webhookAddr)
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

	snap := t.snapshotForPublish()
	if !snap.ok {
		t.failed.Add(uint64(len(points)))
		return fmt.Errorf("http transport %s is not connected", t.name)
	}
	retryCount := snap.retryCount
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

		status, serr, retryable := t.sendHTTPRequest(ctx, snap.client, snap.method, snap.url, snap.headers, payload)
		if serr != nil {
			if !retryable {
				t.failed.Add(uint64(len(points)))
				return serr
			}
			lastErr = serr
			slog.Warn("http push failed, will retry",
				"name", t.name, "attempt", attempt+1, "max", maxAttempts, "error", serr)
			continue
		}

		if status >= 200 && status < 300 {
			t.published.Add(uint64(len(points)))
			t.mu.Lock()
			t.lastPublish = time.Now()
			t.mu.Unlock()
			return nil
		}

		lastErr = fmt.Errorf("http push returned non-2xx status: %d", status)
		// Only 5xx is retried; 4xx and other non-2xx are terminal.
		if status < 500 || status >= 600 {
			break
		}
		slog.Warn("http push returned 5xx, will retry",
			"name", t.name, "attempt", attempt+1, "max", maxAttempts, "status", status)
	}

	t.failed.Add(uint64(len(points)))
	return lastErr
}

// publishSnapshot captures the publish-relevant state under a read lock.
type publishSnapshot struct {
	client     *http.Client
	url        string
	method     string
	headers    map[string]string
	retryCount int
	ok         bool
}

// snapshotForPublish captures the publish-relevant state under a read lock.
// Returns ok=false when the transport is not connected.
func (t *HTTPTransport) snapshotForPublish() publishSnapshot {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.state != core.StateConnected {
		return publishSnapshot{}
	}
	headers := make(map[string]string, len(t.headers))
	for k, v := range t.headers {
		headers[k] = v
	}
	return publishSnapshot{
		client:     t.client,
		url:        t.url,
		method:     t.method,
		headers:    headers,
		retryCount: t.config.RetryCount,
		ok:         true,
	}
}

// sendHTTPRequest performs a single HTTP attempt. Returns the status code (0
// on error), an error, and retryable (true for network errors that may succeed
// on retry, false for request-creation errors that will not change).
func (t *HTTPTransport) sendHTTPRequest(ctx context.Context, client *http.Client, method, url string, headers map[string]string, payload []byte) (status int, err error, retryable bool) {
	req, rerr := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(payload))
	if rerr != nil {
		return 0, fmt.Errorf("failed to create http request: %w", rerr), false
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
		return 0, fmt.Errorf("http push error: %w", derr), true
	}

	// Drain and close the body so the connection can be reused.
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode, nil, false
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
