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
`/tags/stream`, `/write`, `/write/failed`, `/rules`, `/rules/disable`,
`/stats`, `/memory`, `/logs`, `/traffic`, `/metrics`, `/debug/pprof/*`.

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

#### `GET /configs/raw`  — full redacted YAML (authenticated)

Purpose: the **complete** active configuration as YAML text with every secret
value redacted to `"***"`. Feeds a Monaco/YAML editor in the Config Center.
Secrets are restored server-side on `PUT /configs` round-trip (sentinel merge).

- **Content-Type:** `application/yaml; charset=utf-8`
- **Body:** raw YAML text (not JSON)
- Response `200`: the redacted YAML document.
- Response `500`: `{ "error": "internal server error" }` if the endpoint is not
  wired or redaction fails.

Redacted fields include: `api.secret`, MQTT broker passwords, HTTP-push auth
tokens / headers, webhook secrets, driver passwords. The sentinel is the literal
string `***`.

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

Purpose: toggle a rule without a full config reload. Index is the rule's
position in the `GET /rules` array.

Request body:

```json
{ "index": 0, "disabled": true }
```

Response:
- `204 No Content` — toggle applied.
- `400` — `{ "error": "<message>" }` (bad JSON or index out of range).

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
| GET | `/configs/raw` | ✓ | Full redacted YAML (`application/yaml`) |
| PUT | `/configs` | ✓ | Hot-reload config (`{path}` or `{payload}`) |
| PATCH | `/configs` | ✓ | Patch runtime settings (`{log-level}`) |
| POST | `/configs/validate` | ✓ | Dry-run validate (`{payload}`) |
| GET | `/drivers` | ✓ | List all drivers + status |
| GET | `/drivers/{name}` | ✓ | Single driver status |
| GET | `/drivers/{name}/tags` | ✓ | Driver's latest tag values |
| GET | `/transports` | ✓ | List all transports + status |
| GET | `/transports/{name}` | ✓ | Single transport status |
| GET | `/tags` | ✓ | All latest cached tag values |
| POST | `/write` | ✓ | Write control command to device |
| GET | `/write/failed` | ✓ | Dead-letter queue (failed writes) |
| GET | `/rules` | ✓ | Rules + hit/miss statistics |
| PATCH | `/rules/disable` | ✓ | Enable/disable a rule by index |
| GET | `/stats` | ✓ | Engine runtime statistics |
| GET | `/memory` | ✓ | **WebSocket** — memory/GC/goroutine stream |
| GET | `/logs` | ✓ | **WebSocket** — real-time log stream |
| GET | `/traffic` | ✓ | **WebSocket** — throughput counters |
| GET | `/tags/stream` | ✓ | **WebSocket** — real-time data-point stream |
| GET | `/metrics` | ✓ | Prometheus text metrics |
| GET | `/debug/pprof/*` | ✓* | Go profiling (conditional; *unauthenticated on separate pprof-addr) |
