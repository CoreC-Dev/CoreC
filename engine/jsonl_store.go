package engine

import (
	"bufio"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/CoreC-Dev/CoreC/core"
)

// errPersistenceDisabled is returned when no path is configured; callers
// treat a nil store as "persistence disabled" (the memory-only, historical
// behaviour).
var errPersistenceDisabled = errors.New("persistence disabled")

// jsonlStore is an append-only JSON Lines file holding one bounded generation
// of records. The engine keeps two durable queues with it:
//
//   - the write dead-letter queue (global.engine.dead-letter-path)
//   - the write audit trail       (global.engine.write-audit-path)
//
// Sharing one engine instead of two near-identical copies means the
// crash-safety and size-bound discipline is written once and holds for both:
// every record is written+flushed (fsync) before append returns, so a crash
// leaves at most a torn FINAL line, which load skips; and when the file passes
// maxBytes it rotates to <path>.old (replacing any previous rotation), bounding
// the disk footprint at ~2x maxBytes -- newest wins, the same discipline as the
// offline buffer.
type jsonlStore[T any] struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	file     *os.File
	writer   *bufio.Writer
}

// defaultStoreBytes caps one generation of a JSONL store.
const defaultStoreBytes = 5 * 1024 * 1024

// newJSONLStore opens (creating if needed) the JSONL file. An empty path
// returns nil, errPersistenceDisabled -- persistence disabled.
func newJSONLStore[T any](path string, maxBytes int64) (*jsonlStore[T], error) {
	if path == "" {
		return nil, errPersistenceDisabled
	}
	if maxBytes <= 0 {
		maxBytes = defaultStoreBytes
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
	// startup purely to REPLAY an oversized file -- rotating now would
	// destroy the exact history the operator enabled persistence to keep.
	// Rotation only happens on append, when there is fresh content to
	// protect (the caught-in-testing bug of the dead-letter store).
	return &jsonlStore[T]{path: path, maxBytes: maxBytes, file: f, writer: bufio.NewWriter(f)}, nil
}

// append persists one record as a JSON line. fsync keeps the promise that a
// persisted record survives a power loss; failures are logged and never
// propagate -- the in-memory structure stays primary.
func (st *jsonlStore[T]) append(entry T) {
	if st == nil {
		return
	}
	line, err := json.Marshal(entry)
	if err != nil {
		slog.Warn("jsonl store: marshal failed", "path", st.path, "error", err)
		return
	}

	st.mu.Lock()
	defer st.mu.Unlock()
	if _, err := st.writer.Write(append(line, '\n')); err != nil {
		slog.Warn("jsonl store: append failed", "path", st.path, "error", err)
		return
	}
	if err := st.writer.Flush(); err != nil {
		slog.Warn("jsonl store: flush failed", "path", st.path, "error", err)
		return
	}
	if err := st.file.Sync(); err != nil {
		slog.Warn("jsonl store: fsync failed", "path", st.path, "error", err)
	}
	if err := st.rotateLocked(); err != nil {
		slog.Warn("jsonl store: rotation check failed", "path", st.path, "error", err)
	}
}

// rotateLocked moves the file to <path>.old once it passes the byte cap.
// Caller holds st.mu.
func (st *jsonlStore[T]) rotateLocked() error {
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
	slog.Info("jsonl store: rotated", "path", st.path, "max_bytes", st.maxBytes)
	return nil
}

// load replays persisted records in file order (oldest first), keeping at
// most limit entries. Reads .old first, then the current file; a torn trailing
// line (crash during append) is skipped.
func (st *jsonlStore[T]) load(limit int) []T {
	if st == nil || limit <= 0 {
		return nil
	}
	var entries []T
	for _, path := range []string{st.path + ".old", st.path} {
		f, err := os.Open(path)
		if err != nil {
			continue // .old commonly absent; missing current is created on open
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64*1024), 1<<20) // 1MiB max line
		for scanner.Scan() {
			var e T
			if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
				continue // torn/garbage line -- skip, keep replaying
			}
			entries = append(entries, e)
		}
		f.Close()
	}
	if len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	return entries
}

// close flushes and closes the file (engine shutdown). Safe on a nil store.
func (st *jsonlStore[T]) close() {
	if st == nil {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	_ = st.writer.Flush()
	_ = st.file.Close()
}

// deadLetterStore is the dead-letter queue's on-disk generation.
type deadLetterStore = jsonlStore[core.DeadLetterEntry]

// errDeadLetterDisabled is kept for callers that check the sentinel.
var errDeadLetterDisabled = errPersistenceDisabled

// newDeadLetterStore opens (creating if needed) the dead-letter JSONL file.
// An empty path returns nil, errDeadLetterDisabled -- persistence disabled,
// the historical memory-only behaviour.
func newDeadLetterStore(path string, maxBytes int64) (*deadLetterStore, error) {
	return newJSONLStore[core.DeadLetterEntry](path, maxBytes)
}
