// Package httppush implements the HTTP Push transport for CoreC.
package httppush

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CoreC-Dev/CoreC/common/util"
	"github.com/CoreC-Dev/CoreC/core"
	"github.com/CoreC-Dev/CoreC/transport/parser"
)

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
	webhookAddr string // listen address, e.g. ":9090"
	webhookPath string // URL path, e.g. "/data"
	webhookSrv  *http.Server
	dataParser  parser.Parser
	dataCh      chan core.DataPoint
	received    atomic.Uint64

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
		t.timeout = 5 * time.Second
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
		t.webhookPath = getStringSetting(settings, "webhook-path", "/data")
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
		mux := http.NewServeMux()
		mux.HandleFunc(t.webhookPath, t.handleWebhook)
		t.webhookSrv = &http.Server{
			Addr:    t.webhookAddr,
			Handler: mux,
		}
		go func() {
			slog.Info("http webhook server starting",
				"name", t.name, "addr", t.webhookAddr, "path", t.webhookPath)
			if err := t.webhookSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				slog.Error("http webhook server error", "name", t.name, "error", err)
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
	t.mu.RUnlock()

	payload, err := json.Marshal(points)
	if err != nil {
		t.failed.Add(uint64(len(points)))
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(payload))
	if err != nil {
		t.failed.Add(uint64(len(points)))
		return fmt.Errorf("failed to create http request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		t.failed.Add(uint64(len(points)))
		return fmt.Errorf("http push error: %w", err)
	}
	defer resp.Body.Close()

	// Drain body
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.failed.Add(uint64(len(points)))
		return fmt.Errorf("http push returned non-2xx status: %d", resp.StatusCode)
	}

	t.published.Add(uint64(len(points)))
	t.mu.Lock()
	t.lastPublish = time.Now()
	t.mu.Unlock()

	return nil
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

// handleWebhook processes incoming POST requests containing DataPoint JSON
// (single object or array).  This is the chained-core inbound path for
// HTTP.
func (t *HTTPTransport) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
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
func (t *HTTPTransport) Type() string { return "http" }

func (t *HTTPTransport) Status() core.TransportStatus {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return core.TransportStatus{
		Name:        t.name,
		Type:        "http",
		State:       t.state,
		Published:   t.published.Load(),
		Failed:      t.failed.Load(),
		Received:    t.received.Load(),
		LastPublish: t.lastPublish,
		QueueSize:   0,
	}
}

// getStringSetting reads a string from a settings map with a default.
func getStringSetting(s map[string]any, key, def string) string {
	if v, ok := s[key].(string); ok {
		return v
	}
	return def
}
