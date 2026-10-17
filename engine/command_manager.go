package engine

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// --- Data Operations ---

// writeAuditCapacity bounds the in-memory write audit history exposed
// via GET /write/history: the most recent 1000 write attempts (successes
// and failures) are retained, oldest first; older entries are evicted
// when the buffer is full. In-memory only — the trail survives config
// reloads but not process restarts.
const writeAuditCapacity = 1000

// Compile-time guard: the engine satisfies the optional write-audit
// role interface that the route layer type-asserts.
var _ core.WriteAuditor = (*CoreCEngine)(nil)

func (e *CoreCEngine) ReadTag(ctx context.Context, driver, tag string) (*core.TagValue, error) {
	return e.reads.ReadTag(ctx, driver, tag)
}

// hasDriver reports whether a driver with the given name is registered.
func (e *CoreCEngine) hasDriver(name string) bool {
	e.mu.RLock()
	_, ok := e.drivers[name]
	e.mu.RUnlock()
	return ok
}

func (e *CoreCEngine) WriteTag(ctx context.Context, cmd core.WriteCommand) (*core.WriteResult, error) {
	e.mu.RLock()
	d, ok := e.drivers[cmd.Driver]
	e.mu.RUnlock()
	if !ok {
		err := fmt.Errorf("driver not found: %s", cmd.Driver)
		e.recordWriteAudit(cmd, false, err.Error())
		return nil, err
	}

	results, err := d.Write(ctx, []core.WriteCommand{cmd})
	if err != nil {
		e.recordWriteAudit(cmd, false, err.Error())
		return nil, err
	}
	if len(results) == 0 {
		err := fmt.Errorf("no result returned")
		e.recordWriteAudit(cmd, false, err.Error())
		return nil, err
	}
	res := &results[0]
	e.recordWriteAudit(cmd, res.Success, res.Error)
	return res, nil
}

// recordWriteAudit appends an entry to the bounded write audit history,
// evicting the oldest entry when the buffer is full. Every WriteTag
// attempt is recorded — success and failure alike — so the audit trail is
// a complete (bounded) record of writes, not just a failure list.
func (e *CoreCEngine) recordWriteAudit(cmd core.WriteCommand, success bool, errMsg string) {
	entry := core.WriteAuditEntry{
		Timestamp: time.Now(),
		Driver:    cmd.Driver,
		Device:    cmd.Device,
		Tag:       cmd.Tag,
		Value:     cmd.Value,
		Type:      cmd.Type,
		Success:   success,
		Error:     errMsg,
		Source:    cmd.Source,
		Remote:    cmd.Remote,
	}
	e.writeAuditMu.Lock()
	defer e.writeAuditMu.Unlock()
	e.writeAuditHistory = append(e.writeAuditHistory, entry)
	if len(e.writeAuditHistory) > writeAuditCapacity {
		e.writeAuditHistory = e.writeAuditHistory[len(e.writeAuditHistory)-writeAuditCapacity:]
	}
}

// WriteHistory returns a newest-first copy of the write audit history
// (index 0 = most recent write attempt), satisfying core.WriteAuditor.
// The result is always non-nil so it serializes as a JSON array.
func (e *CoreCEngine) WriteHistory() []core.WriteAuditEntry {
	e.writeAuditMu.Lock()
	defer e.writeAuditMu.Unlock()
	n := len(e.writeAuditHistory)
	result := make([]core.WriteAuditEntry, n)
	// Index rather than range-copy: WriteAuditEntry is a wide struct and
	// gocritic's rangeValCopy flags the per-iteration copy.
	for i := range e.writeAuditHistory {
		result[n-1-i] = e.writeAuditHistory[i]
	}
	return result
}

// --- Command Execution ---
// startCommandListener launches a goroutine that listens for write commands
// from a transport's OnCommand channel and dispatches them to the engine.
// Commands are executed concurrently (bounded by commandSem) so a single
// slow write does not serialize subsequent control commands (IMPROVEMENTS
// #2). Failed writes are retried with exponential backoff; if all retries
// fail, the command is placed in the dead letter queue for later inspection.
// It is called from AddTransport for every transport (initial and runtime),
// so no transport's commands are dropped.
func (e *CoreCEngine) startCommandListener(t core.Transport, tCtx context.Context) {
	cmdCh := t.OnCommand()
	if cmdCh == nil {
		return
	}
	e.wg.Add(1)
	go func(ch <-chan core.WriteCommand) {
		defer e.wg.Done()
		for {
			select {
			case <-tCtx.Done():
				e.deadLetterStrandedCommands(ch, nil)
				return
			case cmd, ok := <-ch:
				if !ok {
					return
				}
				// Stamp the audit provenance at the trust boundary: every
				// command arriving through a transport command channel is
				// a "command"-source write. This overwrites any source a
				// client may have embedded in the payload so the write
				// audit history cannot be mislabeled by external input.
				cmd.Source = "command"
				// Acquire a concurrency slot before dispatching. When the
				// pool is full this blocks, applying backpressure to the
				// transport's command channel (which overflows to the
				// dead-letter store per #4). The select on tCtx.Done()
				// ensures the listener exits promptly on transport removal
				// or engine shutdown even while waiting for a slot.
				select {
				case e.commandSem <- struct{}{}:
					// Slot acquired — dispatch the command to a goroutine.
				case <-tCtx.Done():
					// The listener is already holding this command off
					// the channel; it must not vanish. Dead-letter it,
					// then drain whatever else is still buffered behind
					// it (B3.2).
					e.deadLetterStrandedCommands(ch, &cmd)
					return
				}
				e.wg.Add(1)
				go func(c core.WriteCommand) {
					defer e.wg.Done()
					defer func() { <-e.commandSem }()
					e.executeWriteWithRetry(c)
				}(cmd)
			}
		}
	}(cmdCh)
}

// deadLetterStrandedCommands runs when a command listener exits (transport
// removal or engine shutdown). held is the command the listener had already
// received but could not dispatch (nil when it was idle); ch is the
// transport's command channel, whose buffered contents are drained.
// Every stranded command is dead-lettered with a clear error: on
// RemoveTransport the engine stays alive, so the queue remains queryable
// and replayable via GET /write/failed; on full engine shutdown the
// in-memory queue dies with the process, but the commands are at least
// counted and logged instead of vanishing (stage-B audit finding B3.2).
// The drain is non-blocking — shutdown is never delayed by stragglers.
func (e *CoreCEngine) deadLetterStrandedCommands(ch <-chan core.WriteCommand, held *core.WriteCommand) {
	stranded := 0
	deadLetter := func(cmd core.WriteCommand) {
		cmd.Source = "command"
		e.addDeadLetter(core.DeadLetterEntry{
			Command:  cmd,
			Error:    "transport removed before dispatch",
			FailedAt: time.Now(),
			Attempts: 0,
		})
		stranded++
	}
	if held != nil {
		deadLetter(*held)
	}
	for {
		select {
		case cmd, ok := <-ch:
			if !ok {
				return
			}
			deadLetter(cmd)
		default:
			if stranded > 0 {
				slog.Warn("command listener exit: dead-lettered stranded commands",
					"count", stranded)
			}
			return
		}
	}
}

// forwardCommand attempts to forward a write command to downstream nodes
// via transports that implement the core.CommandForwarder interface. It
// tries each forwarder-capable transport until one succeeds. Returns true
// if the command was forwarded successfully.
//
// This enables chained-core command passthrough: relay nodes that have no
// local driver for the command target forward commands to downstream
// nodes that do have the driver.
func (e *CoreCEngine) forwardCommand(ctx context.Context, cmd core.WriteCommand) bool {
	e.mu.RLock()
	transports := make([]core.Transport, 0, len(e.transports))
	for _, t := range e.transports {
		transports = append(transports, t)
	}
	e.mu.RUnlock()

	// Sort by transport name for deterministic forwarding order. Without this,
	// map iteration randomization makes the forwarder chain order non-deterministic,
	// which causes flaky tests and makes operational behavior unpredictable.
	sort.Slice(transports, func(i, j int) bool {
		return transports[i].Name() < transports[j].Name()
	})

	for _, t := range transports {
		forwarder, ok := t.(core.CommandForwarder)
		if !ok {
			continue
		}
		if err := forwarder.ForwardCommand(ctx, cmd); err != nil {
			slog.Warn("command forward failed",
				"transport", t.Name(), "driver", cmd.Driver, "tag", cmd.Tag, "error", err)
			continue
		}
		return true
	}
	return false
}

// permanentWriteErrPatterns are error-message signatures that indicate a
// write command can never succeed on retry: the command itself is
// misconfigured (unknown tag, read-only target, type mismatch). Retrying
// such a command just hammers the device writeRetryCount more times and
// pollutes the dead-letter queue (stage-B audit finding B6-1). Every other
// failure keeps the retry loop — connection-class errors may succeed once
// the driver reconnects, and unknown errors are retried fail-safe.
var permanentWriteErrPatterns = []string{
	"tag not found",            // all drivers: command references no configured tag
	"read-only area",           // modbus: discrete-input / input-register target
	"is not supported on",      // modbus: type not supported on the tag's area
	"expected bool value",      // modbus: value/type mismatch on bit areas
	"unsupported write type",   // modbus: unrepresentable command type
	"failed to create variant", // opcua: value cannot map to an OPC UA variant
	"s7 encode",                // s7: value cannot encode for the tag's type
}

// isPermanentWriteError reports whether a write failure is
// configuration-class: retrying the exact same command cannot succeed.
func isPermanentWriteError(errStr string) bool {
	for _, p := range permanentWriteErrPatterns {
		if strings.Contains(errStr, p) {
			return true
		}
	}
	return false
}

// executeWriteWithRetry attempts a write command up to writeRetryCount+1
// times with exponential backoff. On permanent failure, the command is
// added to the dead letter queue.
func (e *CoreCEngine) executeWriteWithRetry(cmd core.WriteCommand) {
	// If no local driver matches the command target, attempt command
	// forwarding (chained-core command passthrough). Relay nodes have
	// drivers: [] and receive commands via command-topic; without
	// forwarding, commands dead-end in the dead letter queue.
	if !e.hasDriver(cmd.Driver) {
		if e.forwardCommand(e.ctx, cmd) {
			slog.Info("command forwarded to downstream node",
				"driver", cmd.Driver, "tag", cmd.Tag)
			return
		}
		slog.Error("command has no local driver and no forwarder available, added to dead letter queue",
			"driver", cmd.Driver, "tag", cmd.Tag)
		e.addDeadLetter(core.DeadLetterEntry{
			Command:  cmd,
			Error:    fmt.Sprintf("driver not found: %s (no forwarder configured)", cmd.Driver),
			FailedAt: time.Now(),
			Attempts: 1,
		})
		return
	}

	maxAttempts := e.writeRetryCount + 1
	var lastErr string

	for attempt := 0; attempt < maxAttempts; attempt++ {
		result, err := e.WriteTag(e.ctx, cmd)
		switch {
		case err != nil:
			lastErr = err.Error()
		case !result.Success:
			lastErr = result.Error
		default:
			slog.Info("command write success", "driver", cmd.Driver, "tag", cmd.Tag, "attempt", attempt+1)
			return
		}

		// Configuration-class failures (unknown tag, read-only target,
		// type mismatch) can never succeed on retry: dead-letter the
		// command immediately instead of burning writeRetryCount more
		// device round-trips (stage-B audit finding B6-1).
		if isPermanentWriteError(lastErr) {
			slog.Error("command write failed permanently, added to dead letter queue",
				"driver", cmd.Driver, "tag", cmd.Tag, "error", lastErr)
			e.addDeadLetter(core.DeadLetterEntry{
				Command:  cmd,
				Error:    lastErr,
				FailedAt: time.Now(),
				Attempts: attempt + 1,
			})
			return
		}

		if attempt < maxAttempts-1 {
			delay := defaultRetryBaseDelay * time.Duration(1<<attempt) // 100ms, 200ms, 400ms...
			if delay > defaultCommandRetryMaxDelay {
				delay = defaultCommandRetryMaxDelay
			}
			slog.Warn("command write retry",
				"driver", cmd.Driver, "tag", cmd.Tag,
				"attempt", attempt+1, "max", maxAttempts,
				"delay", delay, "error", lastErr)
			select {
			case <-e.ctx.Done():
				// Stage-B audit finding B3.2: a shutdown-cancelled retry
				// previously returned silently. The engine context only
				// cancels at full process shutdown, where the in-memory
				// dead-letter queue dies with the process anyway —
				// queueing there would be theater. Instead the
				// abandonment is logged at ERROR with the full command so
				// the operator sees exactly what did not reach the
				// device.
				slog.Error("command write abandoned by shutdown",
					"driver", cmd.Driver,
					"tag", cmd.Tag,
					"value", cmd.Value,
					"type", cmd.Type,
					"attempt", attempt+1,
					"last_error", lastErr)
				return
			case <-time.After(delay):
			}
		}
	}

	// All retries exhausted — add to dead letter queue.
	slog.Error("command write failed after retries, added to dead letter queue",
		"driver", cmd.Driver, "tag", cmd.Tag, "attempts", maxAttempts, "error", lastErr)
	e.addDeadLetter(core.DeadLetterEntry{
		Command:  cmd,
		Error:    lastErr,
		FailedAt: time.Now(),
		Attempts: maxAttempts,
	})
}

// addDeadLetter appends a failed write command to the dead letter queue,
// evicting the oldest entry if the queue is full.
func (e *CoreCEngine) addDeadLetter(entry core.DeadLetterEntry) {
	e.deadLetterMu.Lock()
	defer e.deadLetterMu.Unlock()
	e.deadLetterQueue = append(e.deadLetterQueue, entry)
	if len(e.deadLetterQueue) > e.deadLetterMaxLen {
		e.deadLetterQueue = e.deadLetterQueue[len(e.deadLetterQueue)-e.deadLetterMaxLen:]
	}
}

// DeadLetterEntries returns a copy of the current dead letter queue.
func (e *CoreCEngine) DeadLetterEntries() []core.DeadLetterEntry {
	e.deadLetterMu.Lock()
	defer e.deadLetterMu.Unlock()
	result := make([]core.DeadLetterEntry, len(e.deadLetterQueue))
	copy(result, e.deadLetterQueue)
	return result
}
