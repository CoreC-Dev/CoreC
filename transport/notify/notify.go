package notify

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// NotifyTransport implements core.Transport for notification channels.
type NotifyTransport struct {
	mu sync.RWMutex

	name    string
	config  core.TransportConfig
	channel string

	sender   sender
	cooldown *cooldownTracker

	// Stats
	published   atomic.Uint64
	failed      atomic.Uint64
	lastPublish time.Time
	state       core.ConnState

	ctx    context.Context
	cancel context.CancelFunc
}

// NewNotifyTransport is the factory registered with core.RegisterTransport.
func NewNotifyTransport(config core.TransportConfig) (core.Transport, error) {
	return &NotifyTransport{name: config.Name, config: config}, nil
}

// Init parses settings and prepares the channel sender and message template.
func (t *NotifyTransport) Init(ctx context.Context, config core.TransportConfig) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.name = config.Name
	t.config = config
	t.state = core.StateDisconnected

	channel, err := reqString(config.Settings, "channel")
	if err != nil {
		return err
	}
	t.channel = channel

	cooldownDur, err := optDuration(config.Settings, "cooldown")
	if err != nil {
		return err
	}
	t.cooldown = newCooldownTracker(cooldownDur)

	tmpl, err := optTemplate(config.Settings, "template", defaultTemplate)
	if err != nil {
		return err
	}

	s, err := buildSender(channel, config.Settings, tmpl)
	if err != nil {
		return err
	}
	t.sender = s

	slog.Info("notify transport initialized",
		"name", t.name, "channel", channel, "cooldown", cooldownDur)
	return nil
}

// Start marks the transport as ready. Notification channels are stateless
// (per-request HTTP/SMTP), so there is no long-lived connection to open.
func (t *NotifyTransport) Start(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.ctx, t.cancel = context.WithCancel(ctx)
	t.state = core.StateConnected
	slog.Info("notify transport started", "name", t.name, "channel", t.channel)
	return nil
}

// Stop cancels the context and marks the transport disconnected. It is
// idempotent and non-blocking.
func (t *NotifyTransport) Stop() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.cancel != nil {
		t.cancel()
	}
	t.state = core.StateDisconnected
	slog.Info("notify transport stopped", "name", t.name)
	return nil
}

// Publish renders the data point into a message and sends it via the
// configured channel, subject to cooldown suppression.
func (t *NotifyTransport) Publish(ctx context.Context, point core.DataPoint) error {
	t.mu.RLock()
	sender := t.sender
	cd := t.cooldown
	clientCtx := t.ctx
	t.mu.RUnlock()

	if sender == nil {
		return errNotInitialized
	}
	if clientCtx != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(clientCtx, 10*time.Second)
		defer cancel()
	}

	ruleKey := point.Metadata["rule"]
	if ruleKey == "" {
		ruleKey = point.Tag
	}
	if cd != nil && !cd.allow(ruleKey) {
		return nil // suppressed by cooldown
	}

	if err := sender.send(ctx, point); err != nil {
		t.failed.Add(1)
		return err
	}
	t.published.Add(1)
	t.mu.Lock()
	t.lastPublish = time.Now()
	t.mu.Unlock()
	return nil
}

// PublishBatch sends each point individually. A single failure does not
// abort the batch; the last error is returned.
func (t *NotifyTransport) PublishBatch(ctx context.Context, points []core.DataPoint) error {
	var lastErr error
	for _, p := range points {
		if err := t.Publish(ctx, p); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

// OnCommand returns nil — notify is a pure out-direction transport.
func (t *NotifyTransport) OnCommand() <-chan core.WriteCommand { return nil }

// OnData returns nil — notify does not ingest upstream data.
func (t *NotifyTransport) OnData() <-chan core.DataPoint { return nil }

// Name returns the transport instance name.
func (t *NotifyTransport) Name() string { return t.name }

// Type returns the transport protocol type name.
func (t *NotifyTransport) Type() string { return TypeName }

// Status reports the current transport status and counters.
func (t *NotifyTransport) Status() core.TransportStatus {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return core.TransportStatus{
		Name:        t.name,
		Type:        TypeName,
		State:       t.state,
		Published:   t.published.Load(),
		Failed:      t.failed.Load(),
		LastPublish: t.lastPublish,
	}
}
