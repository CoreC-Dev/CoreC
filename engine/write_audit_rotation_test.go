package engine

// docs/config/global.md on global.engine.write-audit-path makes four claims about
// the filesystem, each checkable without reading the implementation:
//
//   1. 每条记录在写入返回前 fsync 到追加式 JSONL 文件
//      each record is fsynced before the write call returns; append-only JSONL
//   2. 文件超过 5MiB 轮转为 <path>.old（替换上一世代）
//      over 5MiB it rotates to <path>.old, replacing the previous generation
//   3. 磁盘占用上限约 2×5MiB
//      the disk footprint is bounded at about twice the cap
//   4. 启动时把最近 1000 条回放进内存环形
//      on startup the newest 1000 are replayed into the in-memory ring
//
// Claims 2 and 3 are what an operator sizes a disk partition on; claim 4 is what a
// compliance answer rests on ("the history survives the restart"). They are tested
// against the filesystem rather than believed from the code.

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

// Claims 2 and 3: rotation to <path>.old, replacing the previous generation, with
// the footprint staying at two files rather than one per rotation.
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
	for i := 0; i < 200; i++ {
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
	// Rotation renames the file that just crossed the cap aside and starts a fresh
	// empty one, so the generation in .old is the BIG one and the live file is whatever
	// was appended after that. This test's first draft asserted "old < live" from the
	// name alone, which is backwards.
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

// Claim 4: a restart replays the newest records, and "newest" means the last lines
// appended -- not the first lines still in the file.
func TestAuditStoreReplaysNewestOnRestart(t *testing.T) {
	path := auditPath(t)

	st, err := newJSONLStore[map[string]string](path, 1<<20)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	for i := 0; i < 50; i++ {
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
