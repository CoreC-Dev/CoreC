# CoreC Engine & Rule Engine Audit Report

**Date:** 2025-09-14  
**Scope:** `engine/`, `rule/`, `core/` packages  
**Auditor:** Automated static analysis + manual code review  

---

## Executive Summary

The CoreC engine is a well-structured industrial IoT data collection core with good separation of concerns, reasonable concurrency patterns, and extensive test coverage (96 test functions across the audited packages). The codebase shows evidence of iterative bug-fixing (reload tests, concurrent tests, dedup tests). However, the audit identified **1 Critical**, **3 High**, **6 Medium**, **5 Low**, and **3 Info** issues spanning dead code, goroutine leaks, nil-safety gaps, and performance concerns.

---

## Detailed Findings

### 1. CRITICAL: DataBus Subscriber Filter Is Never Applied

| Field | Value |
|-------|-------|
| **Severity** | Critical |
| **File** | `engine/databus.go:128-139` |
| **Category** | Data flow correctness |

**Description:**  
The `Subscribe(filter string)` and `SubscribeWithBuffer(filter string, size int)` methods accept a `filter` parameter and store it in the `subscriber` struct (line 108). However, `Broadcast()` (lines 128-139) iterates over all subscribers and sends to every channel **without checking `sub.filter`**:

```go
func (b *DataBus) Broadcast(point core.DataPoint) {
    b.mu.RLock()
    defer b.mu.RUnlock()
    for _, sub := range b.subscribers {
        select {
        case sub.ch <- point:  // ← no filter check
        default:
            b.dropped.Add(1)
        }
    }
}
```

**Impact:**  
Any consumer that subscribes with a filter (e.g., `bus.Subscribe("driver:plc1")`) will receive **all** data points from all drivers, not just the filtered subset. This defeats the purpose of subscription filtering, causes unnecessary data processing in subscribers, and can overwhelm consumers that expect a narrow data stream. In a hub/websocket scenario, this means every WebSocket client receives every data point regardless of their subscription filter.

**Recommendation:**  
Implement filter matching in `Broadcast`:
```go
for _, sub := range b.subscribers {
    if sub.filter != "" && !matchFilter(sub.filter, point) {
        continue
    }
    // send...
}
```
Define a filter syntax (e.g., `driver:plc1`, `tag:temperature`, or a glob pattern) and a `matchFilter` function. Alternatively, if filtering is intentionally done at a higher layer, remove the `filter` parameter to avoid misleading API consumers.

---

### 2. HIGH: Rule Engine Indexes Are Dead Code — Match Always Linear Scans

| Field | Value |
|-------|-------|
| **Severity** | High |
| **File** | `rule/engine.go:166-200` (index building), `rule/engine.go:314-318` (Match) |
| **Category** | Rule engine / Performance |

**Description:**  
`SetRules` builds four index structures for O(1) rule lookup:
- `byTag map[string][]core.Rule` — rules matching `tag == 'xxx'`
- `byDriver map[string][]core.Rule` — rules matching `driver == 'xxx'`
- `allRules []core.Rule` — rules matching `ALL`
- `other []core.Rule` — complex expressions

These are stored in the engine struct (lines 197-200). However, `Match()` **never uses them**:

```go
func (e *Engine) Match(point core.DataPoint) *MatchResult {
    e.mu.RLock()
    defer e.mu.RUnlock()
    return e.matchInRules(point, e.rules)  // ← always scans ALL rules
}
```

The comment at line 310-312 explains: *"A linear scan is used so that every evaluated rule updates its hit/miss statistics."* While this is a valid design choice for statistics accuracy, it means the index-building code is **dead code** that wastes CPU and memory on every `SetRules` call.

**Impact:**  
- Wasted CPU in `SetRules` building indexes that are never queried (O(n) classification + map allocation per rule set update).
- Wasted memory holding four unused index structures.
- With hundreds of rules (common in industrial settings), every `Match` call is O(n) instead of O(1) for the common `tag == 'xxx'` case.
- Misleading: the log message at line 203-205 reports index sizes, suggesting optimization is active.

**Recommendation:**  
Either:
1. **Remove the dead index code** and update the log message to not report index sizes, OR
2. **Implement indexed lookup** with a two-phase approach: use indexes for matching, then do a separate pass to update miss-counts for non-matching indexed rules. This preserves statistics while enabling O(1) for the common case.

---

### 3. HIGH: Tag-File Watcher Goroutine Leak on Reload

| Field | Value |
|-------|-------|
| **Severity** | High |
| **File** | `engine/tagfile.go:88-129` (watcher loop), `engine/engine.go` (reload callback) |
| **Category** | Hot reload / Memory management |

**Description:**  
When a tags-file changes, the reload callback (in `engine.go`) removes the current watcher from `e.tagFileWatchers` map **without calling `w.stop()`** (to avoid deadlock — the callback runs inside the watcher's own goroutine). It then calls `AddDriver()` which starts a **new** watcher. The old watcher's goroutine continues running after the callback returns.

The sequence on each file change:
1. Old watcher detects change → calls `onReload` callback
2. Callback removes watcher from map (without stopping it)
3. Callback calls `AddDriver` → starts new watcher, adds to map
4. Old watcher's `loop()` returns to its `for` loop — **still running**
5. Old watcher's `hash` is updated, so it won't re-trigger until the file changes again

**The critical bug:** When the file changes a **second** time, the old watcher (still running) detects the change and calls `onReload` again. The callback does `delete(e.tagFileWatchers, driverName)` — which deletes the **new** watcher from the map (they share the same `driverName` key). Then `AddDriver` starts yet another watcher. Now there are **two** orphaned watcher goroutines plus the newest one.

After N file changes, there are **N+1** watcher goroutines running, but only 1 is in the map. `stopAllTagFileWatchers()` can only stop the one in the map; the other N are leaked permanently.

**Impact:**  
- Goroutine leak: each tags-file change permanently leaks a goroutine.
- Cascading reloads: orphaned watchers trigger spurious reloads on subsequent file changes.
- The orphaned watcher deletes the active watcher from the map, causing the active watcher to also be orphaned on the next `stopAllTagFileWatchers` call.
- In production with frequent tags-file edits, this causes unbounded goroutine growth.

**Recommendation:**  
Redesign the reload mechanism to avoid running the callback inside the watcher goroutine. Options:
1. **Channel-based reload requests:** The watcher sends changed tags to a channel; a separate goroutine (outside the watcher) processes reloads and can safely stop the old watcher.
2. **Generation counter:** Each watcher has a generation number. The callback checks if its generation is stale before proceeding; if so, it exits its loop.
3. **fsnotify-based watcher:** Use filesystem events instead of polling, with a single watcher per driver that's replaced atomically.

---

### 4. HIGH: Stats() and Subscribe() Panic If Called Before Start()

| Field | Value |
|-------|-------|
| **Severity** | High |
| **File** | `engine/engine.go:927` (Stats), `engine/engine.go:880` (Subscribe) |
| **Category** | Engine lifecycle / Nil safety |

**Description:**  
`New()` does not initialize `e.dataBus` (it remains `nil`). `e.dataBus` is only created in `Start()` (line 257). However, `Stats()` and `Subscribe()` access `e.dataBus` without nil checks:

```go
// Stats() — line 927:
TotalDropped: uint64(e.dataBus.Dropped() + e.dataBus.PushDropped() + log.Dropped()),

// Subscribe() — line 880:
func (e *CoreCEngine) Subscribe(filter string) (...) {
    return e.dataBus.Subscribe(filter)  // nil dereference
}
```

Similarly, `SubscribeWithBuffer` (line 886) has the same issue.

**Impact:**  
Calling `Stats()` or `Subscribe()` on a newly created engine (before `Start()`) causes a **nil pointer dereference panic**. This can happen if:
- A hub/API server starts serving before the engine is started.
- A health check endpoint calls `Stats()` during initialization.
- `Reload()` fails in `Start()` — the engine is stopped, `dataBus` is closed but not nil'd, but if a second `Reload()` calls `Stop()` then `Stats()` is called in between, the closed dataBus's `Dropped()` still works (atomic load), so this specific path is safe. But the pre-Start path is not.

**Recommendation:**  
Add nil guards:
```go
func (e *CoreCEngine) Stats() core.EngineStats {
    e.mu.RLock()
    defer e.mu.RUnlock()
    var totalDropped uint64
    if e.dataBus != nil {
        totalDropped = uint64(e.dataBus.Dropped() + e.dataBus.PushDropped() + log.Dropped())
    }
    // ...
}
```
Or initialize `e.dataBus` in `New()` with a default-sized bus.

---

### 5. MEDIUM: Reload Is Not Atomic — No Rollback on Start Failure

| Field | Value |
|-------|-------|
| **Severity** | Medium |
| **File** | `engine/engine.go:469-482` |
| **Category** | Engine lifecycle / Hot reload |

**Description:**  
`Reload()` calls `Stop()` then `Start()` without holding a lock during the transition:

```go
func (e *CoreCEngine) Reload(config *core.Config) error {
    if err := e.Stop(); err != nil { return err }
    // ← no lock held here; engine is fully stopped
    ctx := e.parentCtx
    return e.Start(ctx, config)  // ← if this fails, engine stays stopped
}
```

Two issues:
1. **No rollback:** If `Start()` fails (e.g., bad driver config), the engine remains in `stopped` state with the **old** config lost. There's no attempt to restart with the previous config.
2. **Race window:** Between `Stop()` and `Start()`, the engine is stopped. Concurrent API calls (`AddDriver`, `ReadTag`, etc.) will fail or behave unexpectedly. No lock prevents this.

**Impact:**  
- A failed reload leaves the engine **permanently stopped** with no automatic recovery.
- In a production system, a config typo in a reload could take down data collection with no way to recover except a full process restart.
- Concurrent API calls during reload can see inconsistent state.

**Recommendation:**  
1. Save the old config before `Stop()`. If `Start()` fails, attempt to restart with the old config.
2. Hold a write lock for the entire Reload duration, or use a dedicated `reloadMu` to serialize reloads.
```go
func (e *CoreCEngine) Reload(config *core.Config) error {
    e.reloadMu.Lock()
    defer e.reloadMu.Unlock()
    oldConfig := e.currentConfig
    if err := e.Stop(); err != nil { return err }
    if err := e.Start(ctx, config); err != nil {
        slog.Error("reload failed, rolling back", "error", err)
        return e.Start(ctx, oldConfig) // best-effort rollback
    }
    return nil
}
```

---

### 6. MEDIUM: arithCache Grows Without Bound

| Field | Value |
|-------|-------|
| **Severity** | Medium |
| **File** | `rule/arith.go:30` |
| **Category** | Memory management |

**Description:**  
`arithCache` is a package-level `sync.Map` that caches compiled arithmetic programs by expression string:

```go
var arithCache sync.Map

// In EvalArith:
if cached, ok := arithCache.Load(exprStr); ok {
    program = cached.(*vm.Program)
} else {
    p, err := expr.Compile(exprStr, ...)
    arithCache.Store(exprStr, p)
}
```

Entries are **never evicted**. Every unique expression string adds a permanent entry.

**Impact:**  
- If transform expressions are dynamically generated (e.g., including tag names, device IDs, or timestamps), each unique expression permanently consumes memory.
- In a long-running process with dynamic transforms, this causes **unbounded memory growth** — a slow memory leak.
- The `sync.Map` also has higher per-entry overhead than a regular map with a mutex.

**Recommendation:**  
1. If expressions are static (from config), this is acceptable — document the assumption.
2. If expressions can be dynamic, use a bounded LRU cache (e.g., `hashicorp/golang-lru`) with a configurable max size.
3. Add a `ClearArithCache()` function for use during `Reload()` to release memory from old configs.

---

### 7. MEDIUM: Discovery Registry Never Evicts Stale Nodes

| Field | Value |
|-------|-------|
| **Severity** | Medium |
| **File** | `engine/discovery.go:211-213` (registry update), `engine/discovery.go:240-302` (reconcile) |
| **Category** | Discovery mechanism |

**Description:**  
`onDiscoveryMessage` adds every discovered node to `d.registry` and **never removes entries**:

```go
d.regMu.Lock()
d.registry[info.ID] = &info  // ← never deleted
d.regMu.Unlock()
```

There is no TTL, no heartbeat timeout, and no eviction loop. The `reconcile()` method checks `d.subscribe` against `d.registry` and auto-adds transports for discovered upstreams. Once a node is in the registry, it stays forever — even if it has gone offline.

Additionally, `d.autoAdded` tracks `"broker|upstreamID"` keys that are never cleaned up. If an upstream goes offline and comes back with a different endpoint, the old transport remains and no new one is added.

**Impact:**  
- Stale nodes accumulate in the registry, consuming memory.
- `reconcile()` may auto-add transports for nodes that are no longer alive, creating dead connections.
- In dynamic topologies (nodes joining/leaving), the system cannot adapt to node departures.
- MQTT retained messages mean a dead node's last heartbeat persists, so the registry always has an entry even after the node is gone.

**Recommendation:**  
1. Add a `lastSeen time.Time` field to `nodeInfo` and an eviction loop that removes nodes not seen within `2 * heartbeatInterval`.
2. In `reconcile()`, check if the discovered node's heartbeat is fresh before auto-adding a transport.
3. Clean up `autoAdded` entries when a node is evicted from the registry.

---

### 8. MEDIUM: Scheduler Deadband Filtering Aliases Source Slice

| Field | Value |
|-------|-------|
| **Severity** | Medium |
| **File** | `engine/scheduler.go:364` |
| **Category** | Scheduler logic / Data integrity |

**Description:**  
The deadband filter reuses the source slice's backing array:

```go
if len(runner.task.DeadBands) > 0 {
    filtered := values[:0]  // ← aliases values' backing array
    for _, v := range values {
        threshold, ok := runner.task.DeadBands[v.Tag]
        if !ok || threshold <= 0 {
            filtered = append(filtered, v)  // ← writes to same memory
            continue
        }
        // ...
    }
    values = filtered
}
```

`values[:0]` creates a zero-length slice with the same backing array as `values`. The loop then appends to `filtered`, which overwrites elements in the original `values` array. Since the loop reads from `values` (via range) and writes to `filtered` (which shares the same array), this is safe **only because** `append` never writes past the current read position (the range iterator is always ahead of or equal to the append position).

However, this relies on a subtle invariant: the filtered output is always ≤ the input length. If the code were modified to prepend or insert, it would corrupt data. More importantly, if the driver's `Read()` implementation reuses the same slice across calls, the deadband filter would corrupt the next read's data.

**Impact:**  
- Currently safe due to the append-after-read invariant, but fragile and error-prone for future modifications.
- If a driver implementation caches and reuses its return slice (a valid optimization), the deadband filter would silently corrupt data on the next tick.

**Recommendation:**  
Allocate a fresh slice for the filtered output:
```go
filtered := make([]core.TagValue, 0, len(values))
```
This is a minor allocation cost (the slice header + backing array) that eliminates the aliasing risk entirely.

---

### 9. MEDIUM: statistic.Manager init() Goroutine Never Stops

| Field | Value |
|-------|-------|
| **Severity** | Medium |
| **File** | `engine/statistic/manager.go:12-15` |
| **Category** | Statistics / Lifecycle |

**Description:**  
The package `init()` function starts a goroutine that runs forever:

```go
func init() {
    DefaultManager = NewManager()
    go DefaultManager.handle()  // ← runs forever, never stopped
}
```

`handle()` ticks every second and swaps temp counters into blip counters. There is no `Stop()` method and no way to terminate this goroutine.

**Impact:**  
- The goroutine runs for the entire process lifetime, even if the engine is never started or has been stopped.
- In test environments, this goroutine interferes with goroutine leak detection (it's always "leaked").
- The `readBlip`/`publishBlip` values are updated every second even when no data is flowing, which is harmless but wasteful.
- If `DefaultManager` is replaced (e.g., in tests), the old goroutine keeps running and updating the old manager.

**Recommendation:**  
1. Make the goroutine stoppable via a context or stop channel:
```go
var defaultManagerStop context.CancelFunc
func init() {
    ctx, cancel := context.WithCancel(context.Background())
    DefaultManager = NewManager()
    defaultManagerStop = cancel
    go DefaultManager.handle(ctx)
}
```
2. For tests, provide a `StopDefaultManager()` function.
3. Alternatively, start the goroutine lazily on first `PushRead`/`PushPublish` call instead of in `init()`.

---

### 10. MEDIUM: StaleThreshold() Has Data Race During Reload

| Field | Value |
|-------|-------|
| **Severity** | Medium |
| **File** | `engine/engine.go:873-874` (read), `engine/engine.go:146-171` (write) |
| **Category** | Cache consistency / Concurrent access |

**Description:**  
`StaleThreshold()` reads `e.staleThreshold` without a lock:

```go
func (e *CoreCEngine) StaleThreshold() time.Duration {
    return e.staleThreshold  // ← no lock
}
```

`applyEngineConfig()` writes to `e.staleThreshold` without a lock (it's called from `Start()` outside the `e.mu.Lock()` block):

```go
func (e *CoreCEngine) Start(...) error {
    e.mu.Lock()
    ...
    e.mu.Unlock()
    e.applyEngineConfig(&config.Global.Engine)  // ← writes e.staleThreshold without lock
}
```

During `Reload()`, `Stop()` is called, then `Start()` calls `applyEngineConfig()`. If a concurrent goroutine calls `StaleThreshold()` during this window, it reads `e.staleThreshold` while `applyEngineConfig` is writing it — a data race.

**Impact:**  
- Data race detected by `-race` detector during concurrent reload + API queries.
- On most architectures, `time.Duration` (int64) reads/writes are atomic, so the practical impact is reading a stale or torn value. But it's technically undefined behavior per the Go memory model.

**Recommendation:**  
Either:
1. Read `e.staleThreshold` under `e.mu.RLock()` in `StaleThreshold()`.
2. Or make `staleThreshold` an `atomic.Int64` (store as nanoseconds).
3. Or move `applyEngineConfig()` inside the `e.mu.Lock()` block in `Start()`.

---

### 11. LOW: OfflineBuffer Eviction Is O(n log n) Per Push

| Field | Value |
|-------|-------|
| **Severity** | Low |
| **File** | `engine/offlinebuffer.go:148-175` |
| **Category** | Memory management / Performance |

**Description:**  
`evictIfNeeded()` is called on every `Push()`. It reads the entire directory, parses all sequence numbers, sorts them, and evicts the oldest:

```go
func (ob *OfflineBuffer) evictIfNeeded() {
    entries, err := os.ReadDir(ob.dir)  // ← O(n) I/O
    // ... collect seqs ...
    sort.Slice(seqs, ...)               // ← O(n log n)
    for len(seqs) >= ob.maxEntries {    // ← O(k) evictions
        os.Remove(...)
    }
}
```

With `maxEntries=10000`, every failed-batch push reads 10000 directory entries, parses 10000 filenames, and sorts 10000 integers.

**Impact:**  
- High I/O and CPU overhead on every push when the buffer is near capacity.
- In a burst of transport failures (many batches buffered rapidly), this creates a performance bottleneck.
- Directory I/O under mutex (`ob.mu`) blocks concurrent `Drain()` calls.

**Recommendation:**  
Track `currentCount` and `oldestSeq` in memory. Only do a directory scan when `currentCount >= maxEntries`. After eviction, update `currentCount` in memory. On startup, scan once to initialize these values.

---

### 12. LOW: commandLoop Goroutine Is Redundant

| Field | Value |
|-------|-------|
| **Severity** | Low |
| **File** | `engine/engine.go:1099` (approx) |
| **Category** | Engine lifecycle / Code quality |

**Description:**  
The `commandLoop` goroutine does nothing except wait for `e.ctx.Done()`:

```go
func (e *CoreCEngine) commandLoop() {
    defer e.wg.Done()
    <-e.ctx.Done()
}
```

It's tracked by `e.wg` so `Stop()` waits for it, but it performs no work. The actual command listeners are started by `startCommandListener()` in `AddTransport()`, which are separately tracked by `e.wg`.

**Impact:**  
- One unnecessary goroutine per engine instance.
- Minor confusion for readers — the name suggests it processes commands, but it doesn't.

**Recommendation:**  
Remove `commandLoop` entirely. The `e.wg.Wait()` in `Stop()` already waits for the real listener goroutines started by `startCommandListener` and `startDataListener`.

---

### 13. LOW: extractSingleFieldEq Misclassifies Expressions With &&/|| in Strings

| Field | Value |
|-------|-------|
| **Severity** | Low |
| **File** | `rule/engine.go:211-225` |
| **Category** | Rule engine / Correctness |

**Description:**  
`extractSingleFieldEq` checks for `&&` or `||` anywhere in the expression to determine if it's a compound expression:

```go
if strings.Contains(cond, "&&") || strings.Contains(cond, "||") {
    return "", false
}
```

This is a naive substring check. An expression like `tag == 'foo && bar'` would be incorrectly classified as compound, even though the `&&` is inside a string literal. The rule would be placed in the `other` (linear scan) bucket instead of `byTag`.

**Impact:**  
- Currently **no impact** because the indexes are dead code (see Finding #2). `Match` always linear-scans `e.rules`.
- If the indexes were activated, this would cause misclassification, degrading performance (more rules in the linear-scan bucket) but not correctness.

**Recommendation:**  
If indexes are activated, use a proper parser to detect top-level `&&`/`||` operators (not those inside string literals). Since the indexes are currently dead code, this is low priority.

---

### 14. LOW: DataBus Broadcast Holds RLock During All Subscriber Sends

| Field | Value |
|-------|-------|
| **Severity** | Low |
| **File** | `engine/databus.go:128-139` |
| **Category** | Cache consistency / Performance |

**Description:**  
`Broadcast()` holds `b.mu.RLock()` for the entire loop over subscribers:

```go
func (b *DataBus) Broadcast(point core.DataPoint) {
    b.mu.RLock()
    defer b.mu.RUnlock()
    for _, sub := range b.subscribers {
        select {
        case sub.ch <- point:
        default:
            b.dropped.Add(1)
        }
    }
}
```

If there are many subscribers (e.g., hundreds of WebSocket clients), this blocks `Subscribe()` and `Close()` calls for the duration of the loop. Each send is non-blocking (`default` case), so the hold time is O(n_subscribers), which is typically fast, but could be a bottleneck at scale.

**Impact:**  
- With 100+ subscribers, `Broadcast` holds the RLock for 100+ channel sends, blocking new subscriptions.
- In practice, channel sends to buffered channels are fast, so this is unlikely to be a real bottleneck unless subscribers have full buffers.

**Recommendation:**  
Snapshot the subscribers slice under RLock, then release the lock and iterate the snapshot:
```go
b.mu.RLock()
subs := make([]subscriber, len(b.subscribers))
copy(subs, b.subscribers)
b.mu.RUnlock()
for _, sub := range subs { ... }
```

---

### 15. LOW: Scheduler Task ID Collides on Same Interval String

| Field | Value |
|-------|-------|
| **Severity** | Low |
| **File** | `engine/engine.go` (scheduleDriverTags) |
| **Category** | Scheduler logic |

**Description:**  
Task IDs are generated as `fmt.Sprintf("%s_%s", driverName, intervalStr)`. Tags with the same interval string are grouped into one task. However, `"1s"` and `"1000ms"` are different strings but the same duration. This creates two separate tasks for the same effective interval, doubling the polling frequency for that interval.

**Impact:**  
- Minor inefficiency: two tasks poll at the same frequency instead of one.
- No correctness issue — both tasks read the same tags and the results are deduplicated by the cache (last-write-wins).

**Recommendation:**  
Parse the interval string to a `time.Duration` and use the duration (not the string) for task ID generation: `fmt.Sprintf("%s_%v", driverName, intervalDuration)`.

---

### 16. INFO: Batch Retry May Cause Duplicates on Partial Success

| Field | Value |
|-------|-------|
| **Severity** | Info |
| **File** | `engine/batcher.go:237` |
| **Category** | Batch processing |

**Description:**  
The batch retry logic acknowledges that partial publish success followed by retry can cause duplicates:

```go
slog.Warn("batch publish retry (note: partial success may cause duplicates on retry; use QoS≥1 or downstream dedup by driver+tag+timestamp)", ...)
```

This is a documented, accepted trade-off. The recommendation is to use QoS≥1 (for MQTT) or downstream deduplication.

**Impact:**  
Downstream systems may receive duplicate data points if a batch publish partially succeeds and is retried.

**Recommendation:**  
Already documented. Consider implementing idempotent batch publishing (e.g., batch-level transaction IDs) if duplicate sensitivity is high.

---

### 17. INFO: DataBus Push Drop-Oldest Is Best-Effort

| Field | Value |
|-------|-------|
| **Severity** | Info |
| **File** | `engine/databus.go:67-84` |
| **Category** | Data flow correctness |

**Description:**  
When the DataBus is full, `Push` drains one element and retries. Between drain and retry, another goroutine could fill the slot, causing the new point to be dropped:

```go
select {
case <-b.ch:           // drain oldest
    b.pushDropped.Add(1)
default:
}
select {
case b.ch <- point:    // retry — might fail if slot was taken
default:
    b.pushDropped.Add(1)  // drop new point
}
```

**Impact:**  
Under high contention, the new point may be dropped even though a slot was just freed. This is acceptable for a best-effort strategy — the alternative (blocking) could cause producer backpressure.

**Recommendation:**  
No action needed. The behavior is correct for a lossy, best-effort bus. The `pushDropped` counter accurately tracks drops.

---

### 18. INFO: Test Coverage Assessment

| Field | Value |
|-------|-------|
| **Severity** | Info |
| **File** | Various test files |
| **Category** | Test coverage |

**Description:**  
The audited packages have **96 test functions** covering:

| Area | Test Count | Coverage Quality |
|------|-----------|-----------------|
| Rule engine (expr, arith, P1/P2) | 20 | Good — covers regex, range, contains, suffix, prefix, bool/numeric/string comparisons, sub-rules, rule-sets, circular detection |
| Engine lifecycle | 19 | Good — start/stop/reload/suspend/resume, double-stop, concurrent replacement |
| DataBus / Cache / Scheduler | 5 | Adequate — basic functionality, but missing edge cases |
| Hot reload | 8 | Good — clears old drivers/transports, concurrent reload, provider cleanup |
| Chained core | 11 | Good — OnData→DataBus, dedup, multiple transports, scenarios |
| Offline buffer | 7 | Good — push/drain, eviction, restart, atomic write |
| Discovery | 5 | Adequate — topic patterns, node info, auto-fill, but no integration test |
| Bad quality / Dead letter | 4 | Good — policy parsing, DLQ eviction |
| Tag file | 3 | Good — change detection, no-change, bad interval |
| Statistics | 2 | Adequate — basic counting, drops |
| Core types/registry | 10 | Good — data types, quality, conn state, registry |

**Gaps identified:**
1. **No test for DataBus filter** — because the filter doesn't work (Finding #1). A test would have caught this.
2. **No test for rule engine index usage** — because indexes are dead code (Finding #2). A benchmark comparing indexed vs. linear would reveal the gap.
3. **No test for tag-file watcher goroutine leak** — tests only cover single reload, not repeated reloads that trigger the leak (Finding #3).
4. **No test for Stats()/Subscribe() before Start()** — would have caught the nil panic (Finding #4).
5. **No test for arithCache growth** — no test verifies cache behavior under dynamic expressions (Finding #6).
6. **No test for discovery node eviction** — because eviction doesn't exist (Finding #7).
7. **No race detector tests** — the `-race` flag couldn't be run in this environment (no gcc). The `StaleThreshold` race (Finding #10) would be caught by `-race`.
8. **No benchmark for OfflineBuffer eviction** — the O(n log n) per-push cost (Finding #11) is not benchmarked.

**Recommendation:**  
Add tests for each gap above. Enable CI with `-race` detector. Add a goroutine leak detector (e.g., `goleak`) to test teardown.

---

## Summary Table

| # | Severity | File | Category | Description |
|---|----------|------|----------|-------------|
| 1 | **Critical** | `engine/databus.go:128` | Data flow | Subscriber filter parameter is accepted but never applied — all subscribers receive all points |
| 2 | **High** | `rule/engine.go:314` | Rule engine / Perf | Rule indexes (byTag, byDriver, allRules, other) are built but never used; Match always linear-scans |
| 3 | **High** | `engine/tagfile.go:88` | Hot reload / Memory | Tag-file watcher goroutine leak on reload — N file changes = N leaked goroutines |
| 4 | **High** | `engine/engine.go:927` | Lifecycle / Nil safety | Stats() and Subscribe() panic if called before Start() (nil dataBus) |
| 5 | **Medium** | `engine/engine.go:469` | Lifecycle / Reload | Reload is not atomic; no rollback on Start failure; race window during transition |
| 6 | **Medium** | `rule/arith.go:30` | Memory | arithCache (global sync.Map) grows without bound — unbounded memory with dynamic expressions |
| 7 | **Medium** | `engine/discovery.go:211` | Discovery | Registry never evicts stale nodes; no TTL or heartbeat timeout |
| 8 | **Medium** | `engine/scheduler.go:364` | Scheduler / Data | Deadband filter aliases source slice via `values[:0]` — fragile, risks corruption if driver reuses slice |
| 9 | **Medium** | `engine/statistic/manager.go:12` | Statistics / Lifecycle | init() goroutine runs forever, never stoppable; interferes with leak detection |
| 10 | **Medium** | `engine/engine.go:873` | Cache / Race | StaleThreshold() reads without lock while applyEngineConfig writes without lock during Reload |
| 11 | **Low** | `engine/offlinebuffer.go:148` | Memory / Perf | Eviction is O(n log n) per push — reads+sorts entire directory on every Push |
| 12 | **Low** | `engine/engine.go:1099` | Lifecycle / Quality | commandLoop goroutine is redundant — does nothing but wait for ctx.Done() |
| 13 | **Low** | `rule/engine.go:213` | Rule engine | extractSingleFieldEq naively checks for &&/\|\| in strings — misclassifies expressions with operators in literals |
| 14 | **Low** | `engine/databus.go:128` | Cache / Perf | Broadcast holds RLock during all subscriber sends — blocks Subscribe/Close at scale |
| 15 | **Low** | `engine/engine.go` | Scheduler | Task ID uses interval string not duration — "1s" and "1000ms" create duplicate tasks |
| 16 | **Info** | `engine/batcher.go:237` | Batch processing | Batch retry may cause duplicates on partial success — documented trade-off |
| 17 | **Info** | `engine/databus.go:67` | Data flow | Push drop-oldest is best-effort — new point may be dropped under contention |
| 18 | **Info** | Various tests | Test coverage | 96 tests total; 8 coverage gaps identified for critical/edge-case paths |

---

## Severity Distribution

| Severity | Count |
|----------|-------|
| Critical | 1 |
| High | 3 |
| Medium | 6 |
| Low | 5 |
| Info | 3 |
| **Total** | **18** |

---

## Priority Recommendations

**Fix immediately (Critical/High):**
1. Implement DataBus subscriber filtering or remove the `filter` parameter (Finding #1)
2. Remove dead rule index code or implement indexed Match (Finding #2)
3. Fix tag-file watcher goroutine leak with channel-based reload (Finding #3)
4. Add nil guards for Stats()/Subscribe() or initialize dataBus in New() (Finding #4)

**Fix soon (Medium):**
5. Add rollback to Reload on Start failure (Finding #5)
6. Bound arithCache with LRU eviction (Finding #6)
7. Add node TTL/eviction to Discovery registry (Finding #7)
8. Allocate fresh slice in deadband filter (Finding #8)
9. Make statistic manager goroutine stoppable (Finding #9)
10. Fix StaleThreshold data race with atomic or lock (Finding #10)

**Improve when convenient (Low/Info):**
11-18. Address performance, code quality, and test coverage gaps as noted above.
