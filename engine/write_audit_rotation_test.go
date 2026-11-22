package engine

// These tests verify global.engine.write-audit-path's filesystem
// properties against the filesystem itself: each append fsyncs before
// returning; crossing the size cap rotates to <path>.old (replacing the
// previous generation), bounding disk use at ~2×cap; reopening replays
// the newest records into memory.

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func auditPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "audit.jsonl")
}

// Rotation to <path>.old replaces the previous generation: the footprint
// stays at two files rather than one per rotation.
func TestAuditStoreRotatesToOldAndReplaces(t *testing.T) {
	path := auditPath(t)
	dir := filepath.Dir(path)

	const capBytes int64 = 2000
	st, err := newJSONLStore[map[string]string](path, capBytes)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.close()

	// ~45 bytes a line; 200 lines is well past the cap, so rotation happens more
	// than once -- which is what proves the second rotation REPLACES the first
	// generation instead of piling another one beside it.
	for i := range 200 {
		st.append(map[string]string{"i": strconv.Itoa(i), "pad": "xxxxxxxxxxxxxxxxxxxxx"})
	}

	live, err := os.Stat(path)
	if err != nil {
		t.Fatalf("live file missing: %v", err)
	}
	if live.Size() >= capBytes {
		t.Errorf("live file = %d bytes, want < the %d cap: rotation check runs after every "+
			"append, so the live file must never be left over the cap", live.Size(), capBytes)
	}

	old, err := os.Stat(path + ".old")
	if err != nil {
		t.Fatalf(".old missing after crossing the cap: %v", err)
	}
	// Rotation renames the file that just crossed the cap aside and starts
	// a fresh empty one: .old is the BIG generation, the live file is what
	// was appended after the rotation.
	if old.Size() < capBytes {
		t.Errorf(".old = %d bytes, want >= the %d cap that triggered the rotation", old.Size(), capBytes)
	}
	if old.Size() <= live.Size() {
		t.Errorf(".old (%d bytes) should be the larger of the two; live file is %d",
			old.Size(), live.Size())
	}

	// Exactly the two documented files: no .1, .2, .old.old, no timestamped copies.
	matches, _ := filepath.Glob(filepath.Join(dir, "audit.jsonl*"))
	if len(matches) != 2 {
		t.Fatalf("store left %d files %v; the documented footprint is exactly two (path + path.old)",
			len(matches), matches)
	}
}

// A restart replays the newest records: "newest" means the last lines
// appended, not the first lines still in the file.
func TestAuditStoreReplaysNewestOnRestart(t *testing.T) {
	path := auditPath(t)

	st, err := newJSONLStore[map[string]string](path, 1<<20)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	for i := range 50 {
		st.append(map[string]string{"i": strconv.Itoa(i)})
	}
	st.close()

	st2, err := newJSONLStore[map[string]string](path, 1<<20)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st2.close()

	got := st2.load(10)
	if len(got) != 10 {
		t.Fatalf("load(10) = %d entries, want 10", len(got))
	}
	// Oldest-first, newest 10 kept: i = 40..49.
	if got[0]["i"] != "40" || got[9]["i"] != "49" {
		t.Errorf("replayed i = %s..%s, want 40..49", got[0]["i"], got[9]["i"])
	}
}
