# CoreC Control-Plane API Reference

> **Single source of truth for the frontend dashboard rewrite.**
>
> CoreC exposes a RESTful control plane (JSON over HTTP) plus four real-time
> WebSocket streams. This document is generated from the actual handler code in
> `hub/route/` and the core types in `core/` — every field, type, and enum value
> below is what the server literally emits.

---

## Table of Contents

1. [Connection & Authentication](#1--connection--authentication)
2. [Conventions (JSON encoding, enums, timestamps)](#2--conventions-json-encoding-enums-timestamps)
3. [REST Endpoints](#3--rest-endpoints)
   - [Service Info & Version](#service-info--version)
   - [Health Probes](#health-probes)
   - [Configuration](#configuration)
   - [Drivers](#drivers)
   - [Transports](#transports)
   - [Tags (live cache)](#tags-live-cache)
   - [Write / Dead-Letter Queue](#write--dead-letter-queue)
   - [Rules](#rules)
   - [Stats](#stats)
   - [Memory (one-shot)](#memory-one-shot)
   - [Metrics (Prometheus)](#metrics-prometheus)
   - [pprof](#pprof)
4. [WebSocket Endpoints](#4--websocket-endpoints)
5. [Shared Response Shapes](#5--shared-response-shapes)
6. [Prometheus Metric Catalog](#6--prometheus-metric-catalog)

---

## 1 · Connection & Authentication

| Property | Value |
|:---|:---|
| Default listen address | `0.0.0.0:9090` (configurable via `global.api.listen`) |
| Scheme | `http://` or `https://` (HTTPS when `global.api.tls-cert` + `global.api.tls-key` are set) |
| Content-Type (REST) | `application/json` (UTF-8) for all JSON endpoints; `application/yaml; charset=utf-8` for `GET /configs/raw`; `text/plain; version=0.0.4; charset=utf-8` for `GET /metrics` |
| Max request body | 1 MiB (`http.MaxBytesReader`) — oversized bodies are rejected with `400` |
| CORS | Enabled by default. `Access-Control-Allow-Origin: *`, `Allow-Methods: GET, POST, PUT, PATCH, OPTIONS`, `Allow-Headers: Content-Type, Authorization`, `Max-Age: 86400`. When `global.api.allowed-origins` is set, only listed origins are allowed (restrictive). |
| Rate limiting | Optional per-IP token bucket via `global.api.rate-limit-per-sec`. Exceeding returns `429` with `Retry-After: 1`. Client IP taken from `RemoteAddr` only (X-Forwarded-For is intentionally ignored). |

### Authentication mechanism — Bearer token

A single shared secret (`global.api.secret`) protects every endpoint except the
four public ones (`/`, `/version`, `/healthz/live`, `/healthz/ready`). The
server **refuses to start** if the secret is empty (fail-closed).

The token may be supplied either way:

```
Authorization: Bearer <secret>
```

or, for WebSocket / browser contexts that cannot set headers:

```
?token=<secret>   (query parameter)
```

Comparison is constant-time (SHA-256 hash of both values, then `hmac.Equal`),
so timing does not leak the secret length. A failed check returns:

```json
HTTP 401
{ "error": "unauthorized" }
```

### Public (unauthenticated) endpoints

`GET /`, `GET /version`, `GET /healthz/live`, `GET /healthz/ready`

### Authenticated endpoints

Everything else: `/configs*`, `/drivers*`, `/transports*`, `/tags`,
`/tags/stream`, `/write`, `/write/failed`, `/write/history`, `/rules`,
`/rules/disable`, `/rules/preview`, `/stats`, `/memory`, `/logs`, `/traffic`,
`/metrics`, `/debug/pprof/*`.

---

## 2 · Conventions (JSON encoding, enums, timestamps)

These conventions are critical for the frontend — several CoreC types do **not**
marshal as strings.

| Go type | JSON form | Notes |
|:---|:---|:---|
| `time.Time` | RFC 3339 string | e.g. `"2026-09-30T19:02:11Z"`. Zero value → `"0001-01-01T00:00:00Z"`. |
| `time.Duration` | **integer nanoseconds** | e.g. `82500000000` for 1m22.5s. **Exception:** `GET /` returns uptime as a Go duration *string* (`"1m22.5s"`) because it is formatted with `.String()`. |
| `DataType` | **string** | `"bool"`, `"int8"`, `"int16"`, `"int32"`, `"int64"`, `"uint8"`, `"uint16"`, `"uint32"`, `"uint64"`, `"float32"`, `"float64"`, `"string"`, `"bytes"`. Has a custom `MarshalJSON`. On **input** (`POST /write`) both the string (`"float32"`) and legacy integer (`9`) forms are accepted. |
| `Quality` | **integer** | `0` = good, `1` = bad, `2` = uncertain. No custom marshaler — raw int. |
| `ConnState` | **integer** | `0` = disconnected, `1` = connecting, `2` = connected, `3` = error. No custom marshaler — raw int. |
| `EngineStatus` | **string** | `"running"`, `"suspended"`, `"stopped"`. |
| `slog.Level` (log events) | **integer** | `-4` = debug, `0` = info, `4` = warn, `8` = error. |

> **Frontend tip:** render `Quality` and `ConnState` via a lookup table, not by
> string comparison. `DataType` is already a self-describing string.

All error responses use the shape:

```json
{ "error": "<human-readable message>" }
```

Internal errors return a generic `"internal server error"` (device IPs / file
paths are never leaked); the real detail is server-side logged at `slog.Error`.

---

## 3 · REST Endpoints

### Service Info & Version

#### `GET /`  — ServerInfo (public)

Purpose: lightweight service identity + uptime. **No authentication.**

Response `200`:

```jsonc
{
  "name":    "corec",                 // string — always "corec"
  "version": "v1.0.0",                // string — build version; "dev" for local builds; "v" prefix added unless "dev"
  "status":  "ok",                    // string — always "ok"
  "time":    "2026-09-30T19:02:11Z",  // string — RFC3339, current server time
  "uptime":  "1m23.456789s"           // string — Go duration string (NOT nanoseconds here)
}
```

> Note: node role / node id (from `node:` config) is **not** exposed by this
> endpoint. Node topology is configured via `node.id` / `node.role` but is not
> surfaced in the ServerInfo response.

#### `GET /version`  (public)

Purpose: version only. **No authentication.**

Response `200`:

```json
{ "version": "v1.0.0" }
```

---

### Health Probes

Both are **unauthenticated** so Kubernetes probes can reach them without a
secret. Both return `application/json`.

#### `GET /healthz/live`  (public)

Purpose: Kubernetes liveness — process is alive. Never depends on external
state (a transient downstream outage must not restart the pod).

Response `200`:

```json
{ "status": "alive" }
```

#### `GET /healthz/ready`  (public)

Purpose: Kubernetes readiness — engine is running AND at least one driver and
one transport are connected (when any are configured; zero configured = vacuously
ready).

**Ready** → `200`:

```json
{ "status": "ready" }
```

**Not ready** → `503`:

```jsonc
{
  "status": "not_ready",
  "reason": "no drivers connected",          // string — one of:
                                              //   "engine not initialized"
                                              //   "engine not running (status: \"suspended\")"
                                              //   "no drivers connected"
                                              //   "no transports connected"
  "components": {
    "drivers": [
      { "name": "plc-modbus", "connected": false },
      { "name": "opc-ua",     "connected": true  }
    ],
    "transports": [
      { "name": "cloud-mqtt", "connected": true }
    ]
  }
}
```

`drivers` and `transports` arrays are always present (possibly empty) for a
stable shape.

---

### Configuration

#### `GET /configs`  — config summary (authenticated)

Purpose: safe, secret-redacted **names-only** overview of the active config.

Response `200`:

```jsonc
{
  "global": {
    "log-level": "info",          // string
    "api": {
      "listen":     "0.0.0.0:9090", // string
      "secret-set": true            // boolean — true if a secret is configured (value NEVER exposed)
    }
  },
  "drivers": [
    { "name": "plc-modbus", "type": "modbus-tcp" }
  ],
  "transports": [
    { "name": "cloud-mqtt", "type": "mqtt" }
  ],
  "rules": [
    { "name": "high-temp-alert", "type": "tag == 'temperature' && value > 90", "action": "alert",    "priority": 1 },
    { "name": "default-catch-all", "type": "ALL",                              "action": "forward",  "priority": 999 }
  ]
}
```

> For rules, `type` holds the **match expression** and `action` the action verb.
> `action` and `priority` are omitted (`omitempty`) when zero on non-rule entries.

Fallback (no active config): `200 { "error": "no active configuration" }`.

#### `GET /configs/raw`  — full active config YAML (authenticated)

Purpose: the **complete** active configuration as YAML text with **all values
in plaintext, including secrets** (`api.secret`, MQTT broker passwords,
HTTP-push auth tokens/headers, webhook secrets, driver passwords). Feeds the
Monaco/YAML editor in the Config Center so the editor shows the server's real
config. The endpoint sits behind the same Bearer-token middleware as every
other `/configs/*` route, so it is visible only to operators holding the API
secret — no public exposure.

- **Content-Type:** `application/yaml; charset=utf-8`
- **Body:** raw YAML text (not JSON)
- Response `200`: the full YAML document.
- Response `500`: `{ "error": "internal server error" }` if the endpoint is
  not wired or marshaling fails.

#### `PUT /configs`  — hot-reload configuration (authenticated)

Purpose: full hot reload from a file path or an inline YAML payload. Performs a
diff-and-apply (suspend → diff drivers/transports → update rules → resume).
Sentinel `"***"` values are merged back from the live config before validation
so unchanged secrets are preserved.

Request body (one of `path` or `payload`):

```jsonc
{ "path":    "config.yaml" }   // relative to the active config's directory; path-traversal protected
// or
{ "payload": "<full YAML string>" }
```

- If both are given, `payload` wins.
- If `path` is empty and `payload` is empty, the current config path is reloaded.
- Client-supplied `path` must resolve inside the active config's directory
  (absolute paths and `..` escapes are rejected with `400`).

Response:
- `204 No Content` — reload succeeded (empty body).
- `400` — `{ "error": "<parse/validation message>" }`.
- `500` — `{ "error": "internal server error" }` (apply failure; detail logged server-side).

> API settings (`api.listen`, `api.secret`, TLS), `rule-providers`, `rule-groups`,
> and `engine` tuning parameters cannot be hot-applied — changes log a `WARN` and
> require a restart.

#### `PATCH /configs`  — runtime settings patch (authenticated)

Purpose: modify select runtime settings without a full reload.

Request body — a flat JSON object. Currently the **only supported key** is
`log-level`; any other key is rejected.

```json
{ "log-level": "debug" }
```

Valid `log-level` values: `debug`, `info`, `warn`, `error`.

Response:
- `204 No Content` — patch applied.
- `400` — `{ "error": "unsupported patch key(s): [...] (supported: log-level)" }`
  or `{ "error": "invalid log-level \"foo\"" }`.

#### `POST /configs/validate`  — dry-run validation (authenticated)

Purpose: validate a config payload **without applying** it, so the dashboard can
surface errors before committing a `PUT`. Runs the same sentinel-merge + validate
as `PUT /configs`, so the dry-run faithfully predicts the reload outcome.

Request body:

```json
{ "payload": "<YAML string>" }
```

(`path` is accepted by the struct but ignored — validation is payload-only.)

Response:
- `200` — `{ "valid": true }`.
- `400` — `{ "valid": false, "error": "<message>" }` (validation failure) or
  `{ "error": "<message>" }` (JSON decode failure).

#### `GET /configs/backups`  — list server-side config backups (authenticated)

Purpose: metadata for the server-side config backup set. CoreC keeps
timestamped copies of the persisted config on disk — the active config's
directory gets a `backups/` subdirectory, and every successful
dashboard-driven change (`PUT /configs` with a payload) auto-snapshots the
replaced file into it before overwriting. Up to the **10** newest files are
retained; older ones are pruned. Files are `<unix-nano>.yaml` with `0o600`
permissions (they contain the API secret and driver/transport credentials).
The list is metadata only — content is served by the endpoint below.

Response `200`:

```jsonc
{
  "backups": [                      // array — newest first; [] when empty
    { "name": "1760000000000000000.yaml", "timestamp": "2025-10-09T08:53:20Z", "size": 1842 }
  ]
}
```

- Response `503` — `{ "error": "no active config path; config backups unavailable" }`
  when the core started without a persisted config file (empty-config startup).

#### `GET /configs/backups/{name}`  — one backup's raw YAML (authenticated)

Purpose: the YAML content of one backup, identified by an exact `name` from
the list above. Mirrors `GET /configs/raw`'s security posture: the content
holds secrets in plaintext and is visible only to authenticated operators —
the same people who can already fetch the full active config, so no new
plaintext surface opens. `name` must strictly match the generated
`<unix-nano>.yaml` pattern; traversal fragments and path separators are
rejected with `400` **before** any filesystem path is joined.

- Response `200` — raw YAML text, `Content-Type: application/yaml; charset=utf-8`.
- Response `400` — `{ "error": "invalid backup name: \"<name>\"" }`.
- Response `404` — `{ "error": "config backup not found: <name>" }`.
- Response `503` — no active config path (see above).

#### `POST /configs/backups/{name}/restore`  — restore a backup (authenticated)

Purpose: roll the server back to a saved snapshot. The backup content goes
through the **exact same chain as `PUT /configs`** — parse → validate →
diff-and-apply (suspend → diff → resume) → persist — never a raw file
overwrite. A backup that no longer validates fails with `400` **before
anything is applied** (running config and on-disk file untouched). On success
the restored content is persisted, and the pre-restore state is itself
auto-snapshotted, so a restore can be undone by restoring the newer
snapshot. No request body.

- Response `204 No Content` — restored.
- Response `400` — invalid `name`, or
  `{ "error": "config backup failed validation: <message>" }`.
- Response `404` — backup not found.
- Response `500` — `{ "error": "internal server error" }` (apply failure after
  successful validation; detail logged server-side, masked like `PUT /configs`).

#### `POST /configs/backups`  — manual snapshot (authenticated)

Purpose: snapshot the current on-disk config right now — e.g. before
hand-editing the file or starting a risky batch of Dashboard changes.
Retention/pruning identical to auto-snapshots. No request body.

- Response `201` — `{ "name": "1760000000000001234.yaml" }`.
- Response `404` — `{ "error": "active config file not found on disk" }` (config
  path set but no file written yet).
- Response `503` — no active config path.

---

### Drivers

#### `GET /drivers`  — list all drivers (authenticated)

Purpose: list every southbound driver and its live status.

Response `200`:

```jsonc
{
  "drivers": [ <DriverStatus>, ... ]   // array of DriverStatus (see §5)
}
```

#### `GET /drivers/{name}`  — single driver (authenticated)

Purpose: one driver's status. `{name}` is the driver's configured name.

Response:
- `200` — a single `<DriverStatus>` object (not wrapped in an array).
- `404` — `{ "error": "driver not found" }`.

#### `GET /drivers/{name}/tags`  — driver's latest tag values (authenticated)

Purpose: latest cached values for all tags under one driver, with staleness
annotation when `engine.stale-threshold` is configured.

Response `200`:

```jsonc
{
  "tags": {
    "temperature": <DataPoint>,   // see §5 — DataPoint
    "pressure":    <DataPoint>
  }
}
```

The map key is the tag name. `is_stale` is set to `true` on any `DataPoint`
whose `timestamp` is older than `now - stale-threshold` (only when threshold > 0).

#### `POST /drivers/test`  — throwaway-instance connectivity test (authenticated)

Purpose: validate an **unsaved** driver configuration by creating a temporary
instance, running it through the same lifecycle a configured driver follows
(create → init → start), and always stopping it. Previously a bad driver
config could only be exposed by production traffic after saving + reload.

Request body — the field names are explicit json tags defined in the handler
(NOT `core.DriverConfig`, which is the YAML contract and has yaml tags only):

```jsonc
{
  "type":     "modbus-tcp",          // string — required; a registered driver type
  "settings": {                      // object — same shape as drivers[].settings in YAML
    "host": "192.168.1.100",
    "port": 502,
    "slave-id": 1
  },
  "name":    "plc-probe"             // string — optional log label; default "driver-test"
}
```

Semantics:
- **Temporary**: nothing is registered with the engine or the plugin registry,
  and nothing is written to the config. The `name` only labels log lines.
- **Closed immediately**: the instance is stopped on every path (success,
  failure, timeout) — no connection or device handle leaks.
- **5-second hard timeout** (`driverTestTimeout`): the lifecycle runs under
  `context.WithTimeout` with a goroutine+select fallback, so the handler
  always returns even if a driver blocks.
- **Both outcomes are `200`** — a completed negative test is a successful run:

```jsonc
// connected:
{ "ok": true,  "connected": true,  "latency_ms": 12, "driver_type": "modbus-tcp" }
// failed to connect (still 200):
{ "ok": false, "connected": false, "error": "dial tcp 192.168.1.100:502: connect: connection refused", "latency_ms": 3, "driver_type": "modbus-tcp" }
```

`latency_ms` covers init+start only (teardown excluded). On timeout the error
is `"driver test timed out after 5s"`.

Errors: `400` — bad JSON, `type` empty (`"type is required"`), `type` not
registered (`"unknown driver type: <type>"`), or the factory rejected the
settings.

---

### Transports

#### `GET /transports`  — list all transports (authenticated)

Purpose: list every northbound transport and its live status.

Response `200`:

```jsonc
{
  "transports": [ <TransportStatus>, ... ]   // array of TransportStatus (see §5)
}
```

#### `GET /transports/{name}`  — single transport (authenticated)

Response:
- `200` — a single `<TransportStatus>` object.
- `404` — `{ "error": "transport not found" }`.

---

### Tags (live cache)

#### `GET /tags`  — all latest cached values (authenticated)

Purpose: snapshot of every tag's latest cached value across **all** drivers,
with staleness annotation. Same shape as `/drivers/{name}/tags` but aggregated.

Response `200`:

```jsonc
{
  "tags": {
    "<tag-name>": <DataPoint>,   // key = tag name; value = DataPoint (see §5)
    ...
  }
}
```

> Keys are tag names. When two drivers share a tag name the later write wins
> (map semantics); the `DataPoint.driver` field disambiguates the source.

---

### Write / Dead-Letter Queue

#### `POST /write`  —下发控制指令 (authenticated)

Purpose: write a control value to a device register/coil via the named driver.
The engine dispatches synchronously to `driver.Write`.

Request body — `WriteCommand`:

```jsonc
{
  "driver":  "plc-modbus",   // string — required, must match a configured driver name
  "device":  "",              // string — optional device identifier
  "tag":     "temperature",   // string — required, target tag/register address
  "value":   50,              // any    — the value to write (number / bool / string)
  "type":    "float32"        // string — DataType; string form ("float32") or legacy int (9) both accepted
}
```

Response:
- `200` — `WriteResult`:

  ```jsonc
  { "success": true, "error": "" }   // "error" omitted (omitempty) on success
  // or on a driver-reported failure:
  { "success": false, "error": "modbus exception code 3" }
  ```
- `400` — `{ "error": "<JSON decode message>" }`.
- `500` — `{ "error": "internal server error" }` (e.g. `"driver not found: plc1"`
  is logged server-side; client sees the generic message).

> **Reverse-path writes** (commands arriving via MQTT command-topic) are
> retried with exponential backoff; after `write-retry-count + 1` attempts they
> enter the dead-letter queue below. Direct `POST /write` calls are **not**
> retried — they return immediately.

#### `GET /write/failed`  — dead-letter queue (authenticated)

Purpose: inspect write commands that exhausted all retries (from the
reverse-path command listener). Bounded to `DefaultDeadLetterMaxLen` (1000)
entries.

Response `200`:

```jsonc
{
  "failed_writes": [ <DeadLetterEntry>, ... ],   // array, see §5
  "count": 3                                       // int — len(failed_writes)
}
```

#### `GET /write/history`  — write audit trail (authenticated)

Purpose: the write audit history — the most recent **1000** write attempts,
**successes and failures alike**, newest first. Complements `/write/failed`
(which only holds retry-exhausted failures): control-class deployments need a
compliance trail of successful PLC writes too.

Details:
- Bounded in-memory ring (`writeAuditCapacity` = 1000); oldest entries are
  evicted. Cleared on process restart (survives config reloads).
- One entry **per attempt**: a retried command-path write produces one entry
  per retry.
- `source` records provenance — `"api"` (direct `POST /write`, with `remote` =
  caller address) or `"command"` (reverse-path transport command). Both are
  stamped server-side at the trust boundary; payload-supplied values are
  overwritten.
- `501` `{ "error": "write history not supported by this engine" }` when the
  engine does not implement the optional `core.WriteAuditor` role.

Response `200`:

```jsonc
{
  "writes": [ <WriteAuditEntry>, ... ],   // array, see §5 — newest first, [] when empty
  "count": 2                              // int — len(writes)
}
```

---

### Rules

#### `GET /rules`  — list rules + hit/miss statistics (authenticated)

Purpose: every configured rule with runtime match statistics and enable/disable
state.

Response `200`:

```jsonc
{
  "rules": [ <RuleStat>, ... ]   // array of RuleStat (see §5), in config order
}
```

#### `PATCH /rules/disable`  — enable/disable a rule at runtime (authenticated)

Purpose: toggle a rule without a full config reload. Prefer name addressing:
rule names are unique (enforced by config validation) and stable across
config reloads. Index (the rule's position in the `GET /rules` array) is kept
for backward compatibility but shifts when rules are reordered or removed.
When both fields are sent, `name` wins.

Request body:

```json
{ "name": "overtemp-alarm", "disabled": true }
```

Response:
- `204 No Content` — toggle applied.
- `400` — `{ "error": "<message>" }` (bad JSON, unknown rule name, or index
  out of range).

#### `POST /rules/preview`  — offline rule trial (authenticated)

Purpose: evaluate a match expression against a sample data point **without
saving it**. The expression is compiled in a scratch rule engine — the active
rule set, its statistics, and all routing are untouched — so rule authors can
validate syntax and semantics before committing a rule, instead of discovering
a compile error only when production data hits the saved rule.

Request body:

```jsonc
{
  "match": "tag == 'temperature' && value > 95",   // string — required; the expression under test
  "point": {                                        // DataPoint — optional; the sample to evaluate against
    "driver": "plc-modbus",
    "tag": "temperature",
    "value": 96.5,
    "type": "float64",
    "quality": 0,
    "timestamp": "2026-09-30T19:02:10Z"
  }
}
```

> `point` may be omitted (the expression is evaluated against a zero-value
> `DataPoint`) — useful for pure `value` expressions. A live sample can be
> pasted straight from `GET /tags`.

Response:
- `200` — `{ "matched": true }` or `{ "matched": false }`.
- `400` — `{ "error": "<json decode message>" }`, `{ "error": "match is required" }`,
  or `{ "error": "failed to create rule preview: <expr compile error>" }` —
  compile errors are returned **verbatim** (not masked as 500) so the frontend
  can show them next to the expression editor.
- `501` — `{ "error": "rule preview not supported by this engine" }` when the
  engine does not implement the optional `core.RulePreviewer` role.

---

### Stats

#### `GET /stats`  — engine runtime statistics (authenticated)

Purpose: aggregate engine health and throughput. This is the primary dashboard
summary endpoint.

Response `200` — `EngineStats`:

```jsonc
{
  "status":          "running",            // string — EngineStatus: "running" | "suspended" | "stopped"
  "uptime":          82500000000,          // integer — nanoseconds (time.Duration)
  "drivers":         2,                    // int — count of configured drivers
  "transports":      1,                    // int — count of configured transports
  "rules":           5,                    // int — count of active rules
  "total_read":      1284503,              // uint64 — total data points read from drivers
  "total_publish":   1284000,              // uint64 — total data points published to transports
  "total_errors":    17,                   // uint64 — total processing errors
  "total_dropped":   42,                   // uint64 — total dropped (databus + log bus overflow)
  "points_per_sec":  156.78,               // float64 — total_read / uptime_seconds
  "driver_stats": {                        // map[string]DriverStatus — keyed by driver name
    "plc-modbus": <DriverStatus>
  },
  "transport_stats": {                     // map[string]TransportStatus — keyed by transport name
    "cloud-mqtt": <TransportStatus>
  }
}
```

> `uptime` is in **nanoseconds** here (unlike `GET /` which returns a duration
> string). Divide by `1e9` for seconds. `points_per_sec` is `total_read /
> uptime.Seconds()` (0 before the first second elapses).

---

### Memory (one-shot)

#### `GET /memory`  — **WebSocket** (see §4)

`GET /memory` is registered as a WebSocket upgrade, not a plain JSON GET. A
plain HTTP GET without an Upgrade header will fail the handshake. See the
WebSocket section.

---

### Metrics (Prometheus)

#### `GET /metrics`  — Prometheus text exposition (authenticated)

Purpose: scrape endpoint for Prometheus. Returns the text exposition format
(version 0.0.4). See §6 for the full metric catalog.

- **Content-Type:** `text/plain; version=0.0.4; charset=utf-8`
- Response `200`: Prometheus-format text body.

---

### pprof

#### `GET /debug/pprof/*`  — Go profiling (authenticated, conditional)

Purpose: `net/http/pprof` endpoints (CPU/heap/goroutine/block/mutex profiles).

Availability:
- **Disabled** when `global.api.pprof-disabled: true`.
- **Separate unauthenticated server** at `global.api.pprof-addr` (e.g.
  `127.0.0.1:6060`) when set — bind to loopback/private only, no auth.
- **On the main API port, authenticated** otherwise (default).

Sub-endpoints: `/debug/pprof/` (index), `/debug/pprof/cmdline`,
`/debug/pprof/profile`, `/debug/pprof/symbol`, `/debug/pprof/trace`, plus the
standard `/{name}` profile handlers (heap, goroutine, etc.).

---

## 4 · WebSocket Endpoints

All four streams use `coder/websocket` (RFC 6455). Authentication is the same
Bearer/`?token=` mechanism as REST. Messages are JSON-encoded via `wsjson.Write`
(text frames). Each write has a 5-second timeout (`wsWriteTimeout`).

| Path | Query params | Push frequency | Message shape | Purpose |
|:---|:---|:---|:---|:---|
| `/logs` | — | event-driven (one message per `slog` call at or above the current level) | `LogEvent` (see §5) | Real-time log stream — captures every `slog.Info/Warn/Error/Debug` call. |
| `/traffic` | `?interval=<duration>` (default `1s`) | periodic (every `interval`) | `TrafficMessage` | Real-time throughput counters. |
| `/memory` | `?interval=<duration>` (default `1s`) | periodic (every `interval`) | `MemoryMessage` | Real-time Go runtime memory + GC + goroutine stats. Sampled via `runtime/metrics` (no STW pause). |
| `/tags/stream` | `?driver=<name>` (optional filter) | event-driven (one message per published `DataPoint`) | `DataPoint` (see §5) | Real-time data-point stream. `?driver=plc-modbus` restricts to one driver; omit for all. |

### `?interval` format

A Go duration string, e.g. `500ms`, `2s`, `100ms`. Invalid/empty → falls back
to `1s`.

### `TrafficMessage`

```jsonc
{
  "read":    1284503,   // uint64 — total data points read (engine TotalRead)
  "publish": 1284000,   // uint64 — total data points published (engine TotalPublish)
  "dropped": 42         // uint64 — total dropped (engine TotalDropped)
}
```

### `MemoryMessage`

```jsonc
{
  "alloc":       8388608,   // uint64 — heap bytes in use (HeapAlloc analog)
  "total_alloc": 67108864,  // uint64 — cumulative bytes allocated (TotalAlloc analog)
  "sys":         50331648,  // uint64 — total memory classes bytes (Sys analog)
  "num_gc":      143,       // uint64 — completed GC cycles (NumGC analog)
  "goroutines":  27         // int    — runtime.NumGoroutine()
}
```

### WebSocket origin policy

When `global.api.allowed-origins` is empty, cross-origin upgrades are allowed
(permissive, matching CORS `*`). When origins are listed, only those origins may
upgrade; others get `403`.

---

## 5 · Shared Response Shapes

### `DriverStatus`

```jsonc
{
  "name":            "plc-modbus",        // string
  "type":            "modbus-tcp",        // string — driver type
  "state":           2,                   // int — ConnState: 0=disconnected,1=connecting,2=connected,3=error
  "last_read":       "2026-09-30T19:02:10Z", // string — RFC3339, last successful read
  "last_error":      "",                  // string — last error message ("" if none)
  "tag_count":       12,                  // int — number of configured tags
  "read_count":      4503,                // uint64 — total reads performed
  "error_count":     7,                   // uint64 — total errors reported
  "reconnect_count": 2                    // uint64 — total reconnect attempts
}
```

### `TransportStatus`

```jsonc
{
  "name":             "cloud-mqtt",          // string
  "type":             "mqtt",               // string — transport type
  "state":            2,                    // int — ConnState: 0=disconnected,1=connecting,2=connected,3=error
  "published":        1284000,              // uint64 — total messages published
  "failed":           17,                   // uint64 — total publish failures
  "received":         0,                    // uint64 — data points ingested via OnData (chained-core inbound)
  "last_publish":     "2026-09-30T19:02:11Z", // string — RFC3339
  "queue_size":       3,                    // int — current outbound queue size
  "dropped_commands": 0                     // uint64 — write commands dropped at ingress (channel full)
}
```

### `DataPoint`

The fundamental data unit. Used in `/tags`, `/drivers/{name}/tags`, and the
`/tags/stream` WebSocket.

```jsonc
{
  "driver":    "plc-modbus",               // string
  "device":    "",                         // string — device identifier
  "group":     "sensors",                  // string — tag group
  "tag":       "temperature",              // string — tag name
  "value":     72.5,                       // any — the value (number/bool/string/null)
  "type":      "float32",                  // string — DataType
  "quality":   0,                          // int — Quality: 0=good,1=bad,2=uncertain
  "timestamp": "2026-09-30T19:02:10Z",     // string — RFC3339
  "metadata":  { "src": "opc" },           // map[string]string — omitted when empty (omitempty)
  "is_stale":  false                       // bool — set by API layer when stale; omitted when false (omitempty)
}
```

> `metadata` and `is_stale` use `omitempty` — they are absent from the JSON when
> empty/false. `value` may be `null` when `on-bad-quality: mark-and-publish`
> drops the value on bad-quality points.

### `WriteResult`

```jsonc
{ "success": true }                        // "error" omitted on success (omitempty)
// failure:
{ "success": false, "error": "modbus exception code 3" }
```

### `DeadLetterEntry`

```jsonc
{
  "command": {                            // WriteCommand — the original command that failed
    "driver":  "plc-modbus",
    "device":  "",
    "tag":     "setpoint",
    "value":   50,
    "type":    "float32"
  },
  "error":    "connection reset by peer", // string — the failure message
  "failed_at": "2026-09-30T19:02:11Z",    // string — RFC3339, when it entered the DLQ
  "attempts": 4                           // int — total attempts before giving up (retry-count + 1)
}
```

### `WriteAuditEntry`

One write attempt in the audit history (`GET /write/history`). One entry per
attempt, success or failure.

```jsonc
{
  "timestamp": "2026-09-30T19:02:11Z",    // string — RFC3339, when the attempt was recorded
  "driver":    "plc-modbus",              // string — target driver
  "device":    "",                        // string — device identifier
  "tag":       "pump_status",             // string — tag written
  "value":     true,                      // unknown — the value that was written
  "type":      "bool",                    // string — DataType
  "success":   true,                      // boolean — outcome of the attempt
  "error":     "modbus exception code 3", // string — omitted (omitempty) on success
  "source":    "api",                     // "api" | "command" — stamped server-side
  "remote":    "192.168.1.50:53412"       // string — caller address; omitted for command-source writes
}
```

### `RuleStat`

```jsonc
{
  "index":     0,                         // int — position in the rule list (used by PATCH /rules/disable)
  "name":      "high-temp-alert",         // string
  "type":      "simple",                  // string — rule type: "simple", "rule-set", "sub-rule"
  "match":     "tag == 'temperature' && value > 90", // string — match expression
  "action":    "alert",                   // string — "forward"|"drop"|"alert"|"transform"|"mirror"
  "target":    "cloud-mqtt",              // string — target transport ("" if N/A)
  "targets":   [],                        // []string — for mirror action; multiple targets
  "priority":  1,                         // int — lower = higher priority
  "disabled":  false,                     // bool — runtime disabled flag
  "hit_count": 42,                        // uint64 — total matches
  "hit_at":    "2026-09-30T19:02:11Z",    // string — RFC3339, last match time
  "miss_count": 1284400,                  // uint64 — total non-matches
  "miss_at":   "2026-09-30T19:02:11Z"     // string — RFC3339, last miss time
}
```

### `LogEvent` (WebSocket `/logs`)

```jsonc
{
  "level":     0,                         // int — slog.Level: -4=debug, 0=info, 4=warn, 8=error
  "type":      "info",                    // string — "debug"|"info"|"warning"|"error"
  "payload":   "config updated method=PUT path=/configs",  // string — message + " key=value" attrs
  "timestamp": "2026-09-30T19:02:11Z"     // string — RFC3339
}
```

> `payload` is a single string: the log message followed by space-separated
> `key=value` attribute pairs. Parse with a simple `split(" ")` after the
> message, or just display it verbatim.

---

## 6 · Prometheus Metric Catalog

`GET /metrics` emits the following families in Prometheus text exposition format
(`text/plain; version=0.0.4`). All are produced from the engine's in-memory
counters — no external Prometheus client library is used.

### Counters (monotonically increasing)

| Metric | Labels | Description |
|:---|:---|:---|
| `corec_reads_total` | — | Total data points read from drivers |
| `corec_publishes_total` | — | Total data points published to transports |
| `corec_errors_total` | — | Total processing errors |
| `corec_dropped_total` | — | Total data points dropped |
| `corec_log_dropped_total` | — | Log events dropped (full source/subscriber channel) |
| `corec_driver_read_total` | `driver`, `type` | Total reads by this driver |
| `corec_driver_errors_total` | `driver` | Total errors by this driver |
| `corec_driver_reconnect_total` | `driver` | Total reconnect attempts by this driver |
| `corec_transport_published_total` | `transport`, `type` | Total messages published by this transport |
| `corec_transport_failed_total` | `transport` | Total publish failures for this transport |
| `corec_transport_received_total` | `transport` | Total data points received (chained-core inbound) |
| `corec_transport_dropped_commands_total` | `transport` | Write commands dropped at ingress (channel full); emitted only when > 0 |
| `corec_offline_buffer_drained_total` | — | Batches replayed from the offline buffer |
| `corec_offline_buffer_pushed_total` | — | Batches persisted to the offline buffer |
| `corec_flush_batches_dropped_total` | — | Full batches dropped from batcher flush queues (backpressure) |
| `corec_http_requests_total` | `method`, `status` | HTTP requests served by the API gateway |

### Gauges (instantaneous)

| Metric | Labels | Description |
|:---|:---|:---|
| `corec_drivers` | — | Number of configured drivers |
| `corec_transports` | — | Number of configured transports |
| `corec_rules` | — | Number of configured rules |
| `corec_uptime_seconds` | — | Engine uptime in seconds |
| `corec_points_per_second` | — | Current data points processed per second |
| `corec_driver_tags` | `driver` | Tags configured for this driver |
| `corec_driver_connected` | `driver` | 1 if driver connected, 0 otherwise |
| `corec_transport_queue_size` | `transport` | Current outbound queue size |
| `corec_transport_connected` | `transport` | 1 if transport connected, 0 otherwise |
| `corec_offline_buffer_pending` | — | Batches held in the offline buffer awaiting replay |
| `corec_goroutines` | — | Number of running goroutines |
| `corec_mem_heap_alloc_bytes` | — | Heap bytes allocated and in use |
| `corec_mem_heap_sys_bytes` | — | Heap bytes obtained from the OS |
| `corec_mem_stack_inuse_bytes` | — | Stack bytes in use |
| `corec_mem_total_alloc_bytes` | — | Cumulative bytes allocated |
| `corec_gc_count` | — | Total GC completions |
| `corec_gc_pause_total_seconds` | — | Total GC pause time (seconds) |
| `corec_cpu_count` | — | Logical CPUs available to the process |

### Histograms

| Metric | Buckets | Description |
|:---|:---|:---|
| `corec_http_request_duration_seconds` | 0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5, +Inf | HTTP request duration (seconds) |
| `corec_read_latency_seconds` | engine-defined | Driver read latency (seconds) |
| `corec_publish_latency_seconds` | engine-defined | Transport publish latency (seconds) |
| `corec_data_age_seconds` | engine-defined | Data age: publish time minus collection timestamp (seconds) |

> Per-driver, per-transport, offline-buffer, flush-dropped, latency, and
> data-age metrics are emitted only when the engine satisfies the corresponding
> optional role interface (always true for the real `*CoreCEngine`; omitted for
> test mocks). Label values are Prometheus-escaped (backslash, quote, newline).

---

## Appendix · Endpoint Quick Reference

| Method | Path | Auth | Purpose |
|:---|:---|:---|:---|
| GET | `/` | ✗ | Service info (name/version/status/time/uptime) |
| GET | `/version` | ✗ | Version only |
| GET | `/healthz/live` | ✗ | Liveness probe |
| GET | `/healthz/ready` | ✗ | Readiness probe |
| GET | `/configs` | ✓ | Config summary (names-only, secret-redacted) |
| GET | `/configs/raw` | ✓ | Full active config YAML, secrets in plaintext (`application/yaml`) |
| PUT | `/configs` | ✓ | Hot-reload config (`{path}` or `{payload}`) |
| PATCH | `/configs` | ✓ | Patch runtime settings (`{log-level}`) |
| POST | `/configs/validate` | ✓ | Dry-run validate (`{payload}`) |
| GET | `/configs/backups` | ✓ | Server-side config backup metadata (newest first) |
| GET | `/configs/backups/{name}` | ✓ | One backup's raw YAML (auth'd, mirrors `/configs/raw`) |
| POST | `/configs/backups/{name}/restore` | ✓ | Restore a backup through the full validate → apply chain |
| POST | `/configs/backups` | ✓ | Manual snapshot of the current on-disk config |
| GET | `/drivers` | ✓ | List all drivers + status |
| GET | `/drivers/{name}` | ✓ | Single driver status |
| GET | `/drivers/{name}/tags` | ✓ | Driver's latest tag values |
| POST | `/drivers/test` | ✓ | Throwaway-instance connectivity test for an unsaved driver config |
| GET | `/transports` | ✓ | List all transports + status |
| GET | `/transports/{name}` | ✓ | Single transport status |
| GET | `/tags` | ✓ | All latest cached tag values |
| POST | `/write` | ✓ | Write control command to device |
| GET | `/write/failed` | ✓ | Dead-letter queue (failed writes) |
| GET | `/write/history` | ✓ | Write audit history (recent writes, success + failure, newest first) |
| GET | `/rules` | ✓ | Rules + hit/miss statistics |
| PATCH | `/rules/disable` | ✓ | Enable/disable a rule by name (recommended) or index |
| POST | `/rules/preview` | ✓ | Offline rule trial — evaluate a match expression against a sample point |
| GET | `/stats` | ✓ | Engine runtime statistics |
| GET | `/memory` | ✓ | **WebSocket** — memory/GC/goroutine stream |
| GET | `/logs` | ✓ | **WebSocket** — real-time log stream |
| GET | `/traffic` | ✓ | **WebSocket** — throughput counters |
| GET | `/tags/stream` | ✓ | **WebSocket** — real-time data-point stream |
| GET | `/metrics` | ✓ | Prometheus text metrics |
| GET | `/debug/pprof/*` | ✓* | Go profiling (conditional; *unauthenticated on separate pprof-addr) |
