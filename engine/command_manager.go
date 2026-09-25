package engine

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// --- Data Operations ---

func (e *CoreCEngine) ReadTag(ctx context.Context, driver, tag string) (*core.TagValue, error) {
	e.mu.RLock()
	d, ok := e.drivers[driver]
	e.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("driver not found: %s", driver)
	}

	values, err := d.Read(ctx, []string{tag})
	if err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("no value returned for tag: %s", tag)
	}
	return &values[0], nil
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
		return nil, fmt.Errorf("driver not found: %s", cmd.Driver)
	}

	results, err := d.Write(ctx, []core.WriteCommand{cmd})
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("no result returned")
	}
	return &results[0], nil
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
				return
			case cmd, ok := <-ch:
				if !ok {
					return
				}
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
				slog.Warn("command write retry cancelled by shutdown",
					"driver", cmd.Driver, "tag", cmd.Tag,
					"attempt", attempt+1, "error", lastErr)
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
