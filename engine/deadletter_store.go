// Package engine implements the core.Engine interface and orchestration pipeline.
package engine

import (
	"bufio"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/CoreC-Dev/CoreC/core"
)

// deadLetterStore is the optional on-disk persistence for the write
// dead-letter queue (audit finding RES-2, roadmap #29): the queue was
// memory-only, but the scenarios where dead letters matter most —
// crash, OOM, power loss — are exactly the ones that destroy the
// in-memory queue. When configured (global.engine.dead-letter-path),
// every add appends one JSON line to an append-only file; startup
// replays the most recent entries back into the in-memory queue so
// operators can query and replay them after a crash.
//
// Crash safety: each line is written+flushed (fsync) before addDeadLetter
// returns, so a crash leaves at most a torn FINAL line, which load
// skips. Bounded: when the file passes maxBytes the append rotates it to
// <path>.old (replacing any previous rotation) and starts fresh, so the
// disk footprint is bounded at ~2×maxBytes — same discipline as the
// offline buffer's newest-wins bound.
type deadLetterStore struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	file     *os.File
	writer   *bufio.Writer
}

// newDeadLetterStore opens (creating if needed) the JSONL file. A nil
// path yields a nil store — persistence disabled, the historical
// memory-only behaviour.
func newDeadLetterStore(path string, maxBytes int64) (*deadLetterStore, error) {
	if path == "" {
		return nil, nil
	}
	if maxBytes <= 0 {
		maxBytes = defaultDeadLetterStoreBytes
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	// Deliberately NO rotation check here: the constructor may run at
	// startup purely to REPLAY an oversized file — rotating now would
	// destroy the exact history the operator enabled persistence to
	// keep (the caught-in-testing bug: open-time rotation moved the
	// current generation into .old before load() ran, and with small
	// caps each subsequent rotation then overwrote it). Rotation only
	// happens on append, when there is fresh content to protect.
	return &deadLetterStore{path: path, maxBytes: maxBytes, file: f, writer: bufio.NewWriter(f)}, nil
}

// defaultDeadLetterStoreBytes caps one generation of the JSONL file.
const defaultDeadLetterStoreBytes = 5 * 1024 * 1024

// append persists one entry as a JSON line. fsync keeps the promise
// that a persisted dead letter survives a power loss; failures are
// logged and never block the enqueue path (memory queue stays primary).
func (st *deadLetterStore) append(entry core.DeadLetterEntry) {
	if st == nil {
		return
	}
	line, err := json.Marshal(entry)
	if err != nil {
		slog.Warn("dead-letter store: marshal failed", "error", err)
		return
	}

	st.mu.Lock()
	defer st.mu.Unlock()
	if _, err := st.writer.Write(append(line, '\n')); err != nil {
		slog.Warn("dead-letter store: append failed", "error", err)
		return
	}
	if err := st.writer.Flush(); err != nil {
		slog.Warn("dead-letter store: flush failed", "error", err)
		return
	}
	if err := st.file.Sync(); err != nil {
		slog.Warn("dead-letter store: fsync failed", "error", err)
	}
	if err := st.rotateIfNeededLocked(); err != nil {
		slog.Warn("dead-letter store: rotation check failed", "error", err)
	}
}

// rotateIfNeededLocked moves the file to <path>.old once it passes the
// byte cap. Caller holds st.mu.
func (st *deadLetterStore) rotateIfNeededLocked() error {
	info, err := os.Stat(st.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Size() < st.maxBytes {
		return nil
	}
	if err := st.writer.Flush(); err != nil {
		return err
	}
	if err := st.file.Close(); err != nil {
		return err
	}
	if err := os.Rename(st.path, st.path+".old"); err != nil {
		return err
	}
	f, err := os.OpenFile(st.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	st.file = f
	st.writer = bufio.NewWriter(f)
	slog.Info("dead-letter store: rotated",
		"path", st.path, "max_bytes", st.maxBytes)
	return nil
}

// load replays persisted entries into the in-memory queue, newest last,
// keeping at most cap entries. Reads .old first, then the current file;
// a torn trailing line (crash during append) is skipped.
func (st *deadLetterStore) load(cap int, addToQueue func(core.DeadLetterEntry)) {
	if st == nil || cap <= 0 {
		return
	}
	var entries []core.DeadLetterEntry
	for _, path := range []string{st.path + ".old", st.path} {
		f, err := os.Open(path)
		if err != nil {
			continue // .old commonly absent; missing current is created on open
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64*1024), 1<<20) // 1MiB max line
		for scanner.Scan() {
			var e core.DeadLetterEntry
			if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
				continue // torn/garbage line — skip, keep replaying
			}
			entries = append(entries, e)
		}
		f.Close()
	}
	if len(entries) > cap {
		entries = entries[len(entries)-cap:]
	}
	for _, e := range entries {
		addToQueue(e)
	}
	if len(entries) > 0 {
		slog.Info("dead-letter store: replayed persisted entries into the queue",
			"count", len(entries))
	}
}

// close flushes and closes the file (engine shutdown).
func (st *deadLetterStore) close() {
	if st == nil {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	_ = st.writer.Flush()
	_ = st.file.Close()
}
