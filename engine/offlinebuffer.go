package engine

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// OfflineBuffer stores failed publish batches to disk so they can be
// replayed after the transport recovers. Each batch is written as a
// numbered JSON file inside the configured directory, making the
// storage crash-safe (each file is atomically renamed into place)
// and easy to inspect or purge manually.
//
// The buffer is bounded by maxEntries; when full, the oldest entries
// are evicted to make room for newer ones (newer data is more
// valuable in real-time industrial monitoring).
type OfflineBuffer struct {
	dir        string
	maxEntries int

	mu sync.Mutex
	// nextSeq is the next sequence number to assign. It is persisted
	// by scanning the directory on startup so that sequence numbers
	// remain monotonic across restarts.
	nextSeq uint64
}

// offlineEntry is the on-disk JSON representation of a buffered batch.
type offlineEntry struct {
	Seq    uint64           `json:"seq"`
	Time   time.Time        `json:"time"`
	Points []core.DataPoint `json:"points"`
	Source string           `json:"source"` // transport name
}

// NewOfflineBuffer creates an OfflineBuffer rooted at dir.
// If dir does not exist it is created. maxEntries <= 0 defaults to 10000.
func NewOfflineBuffer(dir string, maxEntries int) (*OfflineBuffer, error) {
	if dir == "" {
		return nil, fmt.Errorf("offline buffer: path is required")
	}
	if maxEntries <= 0 {
		maxEntries = 10000
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("offline buffer: cannot create dir %q: %w", dir, err)
	}

	ob := &OfflineBuffer{
		dir:        dir,
		maxEntries: maxEntries,
	}
	ob.nextSeq = ob.scanMaxSeq() + 1
	return ob, nil
}

// scanMaxSeq reads the directory and returns the highest sequence
// number found, so that new entries don't collide with old ones
// after a restart.
func (ob *OfflineBuffer) scanMaxSeq() uint64 {
	entries, err := os.ReadDir(ob.dir)
	if err != nil {
		return 0
	}
	var maxSeq uint64
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		seq, ok := parseSeqFile(ent.Name())
		if !ok {
			continue
		}
		if seq > maxSeq {
			maxSeq = seq
		}
	}
	return maxSeq
}

func parseSeqFile(name string) (uint64, bool) {
	base := strings.TrimSuffix(name, ".json")
	seq, err := strconv.ParseUint(base, 10, 64)
	if err != nil {
		return 0, false
	}
	return seq, true
}

func seqFileName(seq uint64) string {
	return fmt.Sprintf("%010d.json", seq)
}

// Push writes a failed batch to disk. If the buffer is full, the
// oldest entries are evicted first.
func (ob *OfflineBuffer) Push(points []core.DataPoint, source string) error {
	if len(points) == 0 {
		return nil
	}

	ob.mu.Lock()
	defer ob.mu.Unlock()

	// Evict oldest entries if at capacity.
	ob.evictIfNeeded()

	seq := ob.nextSeq
	ob.nextSeq++

	entry := offlineEntry{
		Seq:    seq,
		Time:   time.Now(),
		Points: points,
		Source: source,
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("offline buffer: marshal: %w", err)
	}

	// Atomic write: write to temp file then rename.
	fullPath := filepath.Join(ob.dir, seqFileName(seq))
	tmpPath := fullPath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("offline buffer: write: %w", err)
	}
	if err := os.Rename(tmpPath, fullPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("offline buffer: rename: %w", err)
	}

	return nil
}

// evictIfNeeded removes the oldest entries until the count is below
// maxEntries. Caller must hold ob.mu.
func (ob *OfflineBuffer) evictIfNeeded() {
	entries, err := os.ReadDir(ob.dir)
	if err != nil {
		return
	}

	// Collect and sort sequence numbers.
	var seqs []uint64
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		if seq, ok := parseSeqFile(ent.Name()); ok {
			seqs = append(seqs, seq)
		}
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })

	// Evict oldest (lowest seq) until we have room for one more.
	for len(seqs) >= ob.maxEntries {
		oldest := seqs[0]
		seqs = seqs[1:]
		path := filepath.Join(ob.dir, seqFileName(oldest))
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			slog.Warn("offline buffer: failed to evict", "file", path, "error", err)
		}
	}
}

// Drain attempts to replay all buffered batches. For each batch, it
// calls tryPublish; if that returns nil the batch is deleted from
// disk, otherwise draining stops (the transport is likely still down).
// Returns the number of batches successfully drained.
func (ob *OfflineBuffer) Drain(tryPublish func([]core.DataPoint) error) (int, error) {
	ob.mu.Lock()
	entries, err := os.ReadDir(ob.dir)
	ob.mu.Unlock()
	if err != nil {
		return 0, fmt.Errorf("offline buffer: read dir: %w", err)
	}

	// Collect and sort sequence numbers (oldest first).
	var seqs []uint64
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		if seq, ok := parseSeqFile(ent.Name()); ok {
			seqs = append(seqs, seq)
		}
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })

	drained := 0
	for _, seq := range seqs {
		path := filepath.Join(ob.dir, seqFileName(seq))

		ob.mu.Lock()
		data, err := os.ReadFile(path)
		ob.mu.Unlock()
		if err != nil {
			if os.IsNotExist(err) {
				continue // already drained by another goroutine
			}
			return drained, fmt.Errorf("offline buffer: read entry: %w", err)
		}

		var entry offlineEntry
		if err := json.Unmarshal(data, &entry); err != nil {
			slog.Warn("offline buffer: corrupt entry, removing", "file", path, "error", err)
			ob.mu.Lock()
			_ = os.Remove(path)
			ob.mu.Unlock()
			continue
		}

		if err := tryPublish(entry.Points); err != nil {
			// Transport still down — stop draining.
			return drained, nil
		}

		ob.mu.Lock()
		_ = os.Remove(path)
		ob.mu.Unlock()
		drained++
	}

	return drained, nil
}

// Len returns the number of buffered batches currently on disk.
func (ob *OfflineBuffer) Len() int {
	ob.mu.Lock()
	defer ob.mu.Unlock()
	entries, err := os.ReadDir(ob.dir)
	if err != nil {
		return 0
	}
	count := 0
	for _, ent := range entries {
		if !ent.IsDir() && strings.HasSuffix(ent.Name(), ".json") {
			count++
		}
	}
	return count
}

// Close is a no-op for the file-based buffer; files remain on disk
// for replay after restart.
func (ob *OfflineBuffer) Close() error {
	return nil
}
