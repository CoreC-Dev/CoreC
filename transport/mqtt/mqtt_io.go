// Package mqtt: the I/O surface — publish paths (single/batch/forward),
// channel accessors, status snapshot, and topic-template assembly.
package mqtt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

func (t *MQTTTransport) Stop() error {
	if t.cancel != nil {
		t.cancel()
	}

	// Stop the replay-cache eviction goroutine to prevent a goroutine
	// leak when the transport is shut down.
	if t.replayCache != nil {
		t.replayCache.close()
	}

	if t.client != nil && t.client.IsConnected() {
		// Unsubscribe from command topic
		if t.commandTopic != "" {
			t.client.Unsubscribe(t.commandTopic)
		}
		// Unsubscribe from data topic
		if t.dataTopic != "" {
			t.client.Unsubscribe(t.dataTopic)
		}
		// Disconnect with configurable quiesce (paho expects milliseconds)
		t.client.Disconnect(uint(t.disconnectQuiesce / time.Millisecond))
	}

	t.mu.Lock()
	t.state = core.StateDisconnected
	t.mu.Unlock()

	// Don't close commandCh here — engine may still be reading
	slog.Info("mqtt transport stopped", "name", t.name)
	return nil
}

func (t *MQTTTransport) Publish(ctx context.Context, point core.DataPoint) error {
	if t.client == nil || !t.client.IsConnected() {
		t.failed.Add(1)
		return fmt.Errorf("mqtt transport %s is not connected", t.name)
	}

	// Build topic from template
	topic, err := t.buildTopic(point)
	if err != nil {
		t.failed.Add(1)
		return fmt.Errorf("failed to build topic: %w", err)
	}

	// Serialize payload
	payload, err := json.Marshal(point)
	if err != nil {
		t.failed.Add(1)
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	// Publish with timeout
	token := t.client.Publish(topic, t.qos, t.retained, payload)
	if ok := token.WaitTimeout(t.publishTimeout); !ok {
		t.failed.Add(1)
		return fmt.Errorf("mqtt publish timed out for topic %s", topic)
	}

	if err := token.Error(); err != nil {
		t.failed.Add(1)
		return fmt.Errorf("mqtt publish failed: %w", err)
	}

	t.published.Add(1)
	t.mu.Lock()
	t.lastPublish = time.Now()
	t.mu.Unlock()

	slog.Debug("mqtt published",
		"topic", topic,
		"driver", point.Driver,
		"tag", point.Tag,
		"value", point.Value,
	)

	return nil
}

// ForwardCommand publishes a write command to the command-forward-topic,
// enabling chained-core command passthrough. Relay nodes that have no
// local driver for the command target use this to forward commands to
// downstream nodes.
//
// When command-forward-secret is configured, the forwarded command is
// signed with HMAC-SHA256 and includes a fresh timestamp so the
// downstream node can authenticate it and enforce replay protection.
// When no secret is configured, the command is forwarded unsigned
// (backward compatible with unauthenticated command setups).
func (t *MQTTTransport) ForwardCommand(ctx context.Context, cmd core.WriteCommand) error {
	if t.commandForwardTopic == "" {
		return fmt.Errorf("no command-forward-topic configured on transport %s", t.name)
	}
	if t.client == nil || !t.client.IsConnected() {
		return fmt.Errorf("mqtt transport %s is not connected", t.name)
	}

	// Marshal the command to JSON
	payload, err := json.Marshal(cmd)
	if err != nil {
		return fmt.Errorf("failed to marshal forwarded command: %w", err)
	}

	// When a forward secret is configured, sign the command with
	// HMAC-SHA256 and add a timestamp for downstream replay protection.
	if t.commandForwardSecret != "" {
		// Parse into a map to add timestamp and signature fields
		var m map[string]json.RawMessage
		if err := json.Unmarshal(payload, &m); err != nil {
			return fmt.Errorf("failed to enrich forwarded command: %w", err)
		}
		// Add fresh timestamp (Unix milliseconds)
		tsBytes, _ := json.Marshal(time.Now().UnixMilli())
		m["timestamp"] = tsBytes
		// Re-marshal to get the canonical bytes (without signature)
		signedPayload, err := json.Marshal(m)
		if err != nil {
			return fmt.Errorf("failed to re-marshal signed forwarded command: %w", err)
		}
		// Compute signature over canonical signing bytes
		sig := computeHMACSignature(t.commandForwardSecret, commandSigningBytes(signedPayload))
		// Add signature to the payload
		var finalM map[string]json.RawMessage
		if err := json.Unmarshal(signedPayload, &finalM); err != nil {
			return fmt.Errorf("failed to add signature to forwarded command: %w", err)
		}
		sigBytes, _ := json.Marshal(sig)
		finalM["X-Signature"] = sigBytes
		payload, err = json.Marshal(finalM)
		if err != nil {
			return fmt.Errorf("failed to finalize signed forwarded command: %w", err)
		}
	}

	// Publish with timeout
	token := t.client.Publish(t.commandForwardTopic, t.qos, false, payload)
	if ok := token.WaitTimeout(t.publishTimeout); !ok {
		return fmt.Errorf("mqtt forward publish timed out for topic %s", t.commandForwardTopic)
	}
	if err := token.Error(); err != nil {
		return fmt.Errorf("mqtt forward publish failed: %w", err)
	}

	t.published.Add(1)
	t.mu.Lock()
	t.lastPublish = time.Now()
	t.mu.Unlock()

	slog.Info("mqtt command forwarded",
		"topic", t.commandForwardTopic,
		"driver", cmd.Driver,
		"tag", cmd.Tag,
		"signed", t.commandForwardSecret != "",
	)

	return nil
}

func (t *MQTTTransport) PublishBatch(ctx context.Context, points []core.DataPoint) error {
	if len(points) == 0 {
		return nil
	}

	if t.client == nil || !t.client.IsConnected() {
		t.failed.Add(uint64(len(points)))
		return fmt.Errorf("mqtt transport %s is not connected", t.name)
	}

	// Concurrent publish: fire all tokens, then wait for all to complete.
	// This reduces batch latency from N×RTT to ~RTT.
	type publishResult struct {
		index int
		err   error
	}
	results := make([]publishResult, len(points))
	var wg sync.WaitGroup
	wg.Add(len(points))

	for i := range points {
		go func(idx int) {
			defer wg.Done()
			results[idx] = publishResult{index: idx, err: t.Publish(ctx, points[idx])}
		}(i)
	}
	wg.Wait()

	var firstErr error
	for _, r := range results {
		if r.err != nil && firstErr == nil {
			firstErr = fmt.Errorf("publishBatch: point %d/%d: %w", r.index+1, len(points), r.err)
		}
	}
	return firstErr
}

func (t *MQTTTransport) OnCommand() <-chan core.WriteCommand {
	return t.commandCh
}

// OnData returns the channel of data points received from the subscribed
// data topic.  Returns a non-nil channel only when data-topic is configured.
func (t *MQTTTransport) OnData() <-chan core.DataPoint {
	if t.dataTopic == "" {
		return nil
	}
	return t.dataCh
}

func (t *MQTTTransport) Name() string { return t.name }
func (t *MQTTTransport) Type() string { return TypeName }

func (t *MQTTTransport) Status() core.TransportStatus {
	t.mu.RLock()
	defer t.mu.RUnlock()

	queueSize := 0
	if t.client != nil {
		// No direct queue size API, but we track pending commands
		queueSize = len(t.commandCh)
	}

	return core.TransportStatus{
		Name:            t.name,
		Type:            TypeName,
		State:           t.state,
		Published:       t.published.Load(),
		Failed:          t.failed.Load(),
		Received:        t.received.Load(),
		LastPublish:     t.lastPublish,
		QueueSize:       queueSize,
		DroppedCommands: t.droppedCommands.Load(),
	}
}

// ============================================================
// Helper functions
// ============================================================

func (t *MQTTTransport) buildTopic(point core.DataPoint) (string, error) {
	var buf bytes.Buffer

	data := map[string]string{
		"Driver": point.Driver,
		"Device": point.Device,
		"Group":  point.Group,
		"Tag":    point.Tag,
	}

	if err := t.topicTemplate.Execute(&buf, data); err != nil {
		return "", err
	}

	// Clean up empty segments
	topic := buf.String()
	for strings.Contains(topic, "//") {
		topic = strings.ReplaceAll(topic, "//", "/")
	}

	return topic, nil
}
