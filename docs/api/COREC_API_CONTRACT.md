# CoreC API Contract

Precise, source-derived reference for the CoreC Go backend HTTP/WebSocket API.
Every field name, type, and JSON tag below is taken directly from the Go source
under `CoreC/` (router: `hub/route/server.go`; handlers: `hub/route/*.go`;
types: `core/types.go`, `core/driver.go`, `core/transport.go`, `core/engine.go`,
`core/rule.go`; config: `config/config.go` + `core/engine.go`; executor:
`hub/executor/executor.go`; metrics: `hub/route/metrics.go`).

> **Critical serialization note (read before typing the frontend):**
> Go's `encoding/json` is used with no custom encoder. Several fields are
> *integer-typed enums with only a `String()` helper* and therefore serialize
> as **raw integers**, not strings:
> - `ConnState` (`state` field) → `0=disconnected`, `1=connecting`, `2=connected`, `3=error`
> - `Quality` (`quality` field) → `0=good`, `1=bad`, `2=uncertain`
> - `slog.Level` (`level` field in `/logs` events) → `-8=debug`, `0=info`, `4=warn`, `8=error`
> - `time.Duration` (`uptime` in `/stats`) → **integer nanoseconds** (divide by 1e9 for seconds)
>
> The following *do* serialize as strings (they have `MarshalJSON` or are string types):
> - `DataType` (`type` field) → `"bool"|"int8"|"int16"|"int32"|"int64"|"uint8"|"uint16"|"uint32"|"uint64"|"float32"|"float64"|"string"|"bytes"`
> - `EngineStatus` (`status` field) → `"running"|"suspended"|"stopped"`
> - `time.Time` (`timestamp`, `last_read`, etc.) → RFC3339 string with nanoseconds, e.g. `"2024-01-01T12:00:00.123456789Z"`
>
> The `GET /` root handler's `uptime` is a **string** (e.g. `"5m32.1s"`) because it
> uses `time.Since(...).String()`, *not* a `time.Duration` field. Do not confuse
> the two.

---

## 1. Server, Auth, Middleware

Server bootstrap: `hub.Start` → `route.ReCreateServer` (in `hub/route/server.go`).
Router: `chi` v5 (`router()` in `server.go`). Listen address and TLS come from
`global.api.listen`, `global.api.tls-cert`, `global.api.tls-key`.

### 1.1 Authentication

All endpoints listed in §2.2 are inside an authenticated `chi.Group` that mounts
the `authentication(secret)` middleware (`server.go:517`). The token is read from,
in order:

1. `Authorization` request header. If it starts with `Bearer `, the prefix is
   stripped and the remainder is the token.
2. If no `Authorization` header, the `token` query parameter is used.

Both the configured secret and the supplied token are SHA-256-hashed to a fixed
32 bytes and compared with `hmac.Equal` (constant-time, length-leak-safe).
Failure → `401` `{"error":"unauthorized"}`.

> The server **refuses to start** if `global.api.secret` is empty (`server.go:154`),
> and config validation requires `api.secret` (≥ 8 chars) whenever `api.listen`
> is set (`config/config.go:199`). So in any running server, secret is non-empty
> and the authenticated group always enforces auth.

### 1.2 Middleware stack (order)

Applied to every request, in order (`server.go:286`):

1. `middleware.RequestID` — adds a request ID.
2. `trace.Middleware` — parses W3C `traceparent` header; propagates trace ID in context.
3. `safeRequestLogger` — DEBUG-level access log; **redacts the `token` query param**.
4. `middleware.Recoverer` — panic recovery.
5. `corsMiddleware(allowedOrigins)` — CORS; handles `OPTIONS` preflight with `204`.
6. `httpMetricsMiddleware` — records method/status/duration for `/metrics`.
7. `rateLimitMiddleware(rateLimitPerSec)` — **only if** `global.api.rate-limit-per-sec > 0`; per-IP (from `r.RemoteAddr` only, **never** trusts `X-Forwarded-For`/`X-Real-IP`); `429` with `Retry-After: 1` on exhaustion.

### 1.3 CORS

- `allowed-origins` empty (default) → permissive: `Access-Control-Allow-Origin: *`.
- `allowed-origins` set → only the request's `Origin` is echoed back if it is in the list.
- Always sets: `Access-Control-Allow-Methods: GET, POST, PUT, PATCH, OPTIONS`,
  `Access-Control-Allow-Headers: Content-Type, Authorization`, `Access-Control-Max-Age: 86400`.
- `OPTIONS` requests short-circuit with `204 No Content`.

### 1.4 Request body limit

All request bodies are wrapped with `http.MaxBytesReader(nil, r.Body, 1<<20)`
via `limitedBody(r)` (`common.go:43`) → **1 MiB max**. Oversized bodies fail JSON
decode with an error.

### 1.5 Standard response shapes

- Success: `200` (or `204` for mutations) with `Content-Type: application/json`,
  body encoded with `json.NewEncoder(w).Encode(data)`.
- Error: `{"error":"<message>"}` with the appropriate status code. Internal errors
  are masked to `{"error":"internal server error"}` (`500`) via `renderInternalError`;
  the real error is logged but **not** exposed (no IP/path leakage).
- Mutations that succeed return `204 No Content` with **empty body**
  (`renderNoContent`).

---

## 2. Endpoint Inventory

### 2.1 Endpoints OUTSIDE the auth group (no secret required)

| Method | Path | Handler | Purpose |
|---|---|---|---|
| `GET` | `/` | `hello` | Service identity + uptime |
| `GET` | `/version` | `getVersion` | Build version |
| `GET` | `/healthz/live` | `healthzLive` | Kubernetes liveness probe |
| `GET` | `/healthz/ready` | `healthzReady` | Kubernetes readiness probe |

### 2.2 Endpoints INSIDE the auth group (secret required)

| Method | Path | Handler | Purpose |
|---|---|---|---|
| `GET` | `/configs` | `getConfigs` | Safe (secret-redacted) config overview |
| `PUT` | `/configs` | `updateConfigs` | Full config reload (file path or YAML payload) |
| `PATCH` | `/configs` | `patchConfigs` | Selective runtime patch (only `log-level`) |
| `GET` | `/drivers` | `getDrivers` | All driver statuses |
| `GET` | `/drivers/{name}` | `getDriver` | One driver status |
| `GET` | `/drivers/{name}/tags` | `getDriverTags` | Latest cached values for a driver's tags |
| `GET` | `/transports` | `getTransports` | All transport statuses |
| `GET` | `/transports/{name}` | `getTransport` | One transport status |
| `GET` | `/tags` | `getAllTags` | Latest cached values across all drivers |
| `POST` | `/write` | `writeTag` | Write a value to a device tag |
| `GET` | `/write/failed` | `getFailedWrites` | Dead-letter queue of failed writes |
| `GET` | `/rules` | `getRules` | Rule list with hit/miss stats + disabled flag |
| `PATCH` | `/rules/disable` | `disableRule` | Enable/disable a rule by index |
| `GET` | `/stats` | `getStats` | Aggregate engine statistics |
| `GET` | `/metrics` | `promMetrics` | Prometheus text-format metrics |
| `GET` | `/logs` | `getLogs` | **WebSocket** — real-time log stream |
| `GET` | `/traffic` | `getTraffic` | **WebSocket** — periodic throughput snapshot |
| `GET` | `/memory` | `getMemory` | **WebSocket** — periodic Go runtime memory snapshot |
| `GET` | `/tags/stream` | `streamTags` | **WebSocket** — real-time DataPoint stream |
| `GET` | `/debug/pprof/*`, `/debug/pprof/cmdline`, `/debug/pprof/profile`, `/debug/pprof/symbol`, `/debug/pprof/trace` | stdlib `pprof` | **Only when** `pprof-disabled=false` **and** `pprof-addr` is empty (i.e. pprof runs on the main port, behind auth). When `pprof-addr` is set, these run on a **separate unauthenticated** server at that address. |

Path params use chi syntax: `{name}` is `chi.URLParam(r, "name")`.

---

## 3. REST Endpoint Details

### 3.1 `GET /` — service hello

Handler `hello` (`server.go:542`). No auth. No query params.

**Response `200`:**
```json
{
  "name": "corec",
  "version": "dev",
  "status": "ok",
  "time": "2024-01-01T12:00:00.123456789Z",
  "uptime": "5m32.123456s"
}
```
| Field | Go type | JSON type | Source |
|---|---|---|---|
| `name` | `string` | string | literal `"corec"` |
| `version` | `string` | string | `route.Version` (ldflags `-X ...route.Version=`); default `"dev"` |
| `status` | `string` | string | literal `"ok"` |
| `time` | `time.Time` | RFC3339 string | `time.Now().Format(time.RFC3339)` |
| `uptime` | `string` | string | `time.Since(startTime).String()` — **human-readable duration string**, NOT nanoseconds |

### 3.2 `GET /version`

Handler `getVersion` (`server.go:556`). No auth.

**Response `200`:** `{"version":"<build-version>"}` (default `"dev"`).

### 3.3 `GET /healthz/live`

Handler `healthzLive` (`health.go:20`). No auth. Writes a hardcoded body
(not via `render`):
```json
{"status":"alive"}
```
Always `200`. Intended for Kubernetes liveness (process-alive only; never depends
on drivers/transports).

### 3.4 `GET /healthz/ready`

Handler `healthzReady` (`health.go:79`). No auth.

Engine is **ready** iff (`readinessReason`, `health.go:106`):
1. `stats.Status == "running"`, AND
2. If `stats.Drivers > 0`, at least one driver has `State == 2` (connected), AND
3. If `stats.Transports > 0`, at least one transport has `State == 2` (connected).

**When ready — `200`:**
```json
{"status":"ready"}
```
(minimal, for high-frequency probes.)

**When not ready — `503`:**
```json
{
  "status": "not_ready",
  "reason": "engine not running (status: \"stopped\")",
  "components": {
    "drivers": [
      {"name": "plc-modbus", "connected": false}
    ],
    "transports": [
      {"name": "cloud-mqtt", "connected": true}
    ]
  }
}
```
| Field | Go type | JSON type | Notes |
|---|---|---|---|
| `status` | `string` | string | always `"not_ready"` in this branch |
| `reason` | `string` | string | one of: `"engine not running (status: \"<status>\")"`, `"no drivers connected"`, `"no transports connected"`, or `"engine not initialized"` |
| `components.drivers[]` | `[]componentState` | array | always present (possibly empty) |
| `components.transports[]` | `[]componentState` | array | always present (possibly empty) |
| `components.drivers[].name` | `string` | string | |
| `components.drivers[].connected` | `bool` | boolean | `state == 2` (connected) |
| `components.transports[].name` | `string` | string | |
| `components.transports[].connected` | `bool` | boolean | |

### 3.5 `GET /configs`

Handler `getConfigs` (`configs.go:42`). Auth required. Returns a
**secret-redacted** overview built by `buildConfigOverview` (`configs.go:55`).

**Response `200`** (struct `configOverview`, `configs.go:18`):
```json
{
  "global": {
    "log-level": "info",
    "api": {
      "listen": "0.0.0.0:9090",
      "secret-set": true
    }
  },
  "drivers": [
    {"name": "plc-modbus", "type": "modbus-tcp"}
  ],
  "transports": [
    {"name": "cloud-mqtt", "type": "mqtt"}
  ],
  "rules": [
    {"name": "high-temp-alert", "type": "tag == 'temperature' && value > 95", "action": "alert", "priority": 1}
  ]
}
```
| Field | Go type / json tag | JSON type | Notes |
|---|---|---|---|
| `global` | `globalOverview` / `"global"` | object | |
| `global.log-level` | `string` / `"log-level"` | string | |
| `global.api` | `apiOverview` / `"api"` | object | |
| `global.api.listen` | `string` / `"listen"` | string | |
| `global.api.secret-set` | `bool` / `"secret-set"` | boolean | `true` iff a secret is configured; **value never exposed** |
| `drivers[]` | `[]entrySummary` / `"drivers"` | array | |
| `transports[]` | `[]entrySummary` / `"transports"` | array | |
| `rules[]` | `[]entrySummary` / `"rules"` | array | |
| `*.name` | `string` / `"name"` | string | |
| `*.type` | `string` / `"type"` | string | driver/transport `Type`; **for rules this is the `Match` expression** (see note) |
| `*.action` | `string` / `"action,omitempty"` | string | **rules only**; omitted (zero value) for drivers/transports |
| `*.priority` | `int` / `"priority,omitempty"` | number | **rules only**; omitted for drivers/transports |

> **Quirk:** `entrySummary.Type` is reused for both the driver/transport `Type`
> and the rule `Match` expression (see `buildConfigOverview`: `Type: rl.Match`).
> So for rule entries, `type` holds the match expression string, not an action.
> The rule's action is in `action`. Frontend code should treat `drivers[].type`
> and `rules[].type` as semantically different fields despite the same JSON key.

**Fallback** (no active config / `GetConfigFunc` returns nil):
`200` `{"error":"no active configuration"}`.

### 3.6 `PUT /configs` — full reload

Handler `updateConfigs` (`configs.go:77`). Auth required. Body limited to 1 MiB.

**Request body** (struct `putConfigRequest`, `configs.go:11`):
```json
{
  "path": "config.yaml",
  "payload": "<full YAML string, optional>"
}
```
| Field | Go type / json tag | JSON type | Required | Notes |
|---|---|---|---|---|
| `path` | `string` / `"path"` | string | no | Relative path resolved against the directory of the currently-persisted config path; **absolute paths and `..` traversal are rejected** (`executor.resolveConfigPath`). If empty, reloads the current path. |
| `payload` | `string` / `"payload"` | string | no | Inline YAML. When non-empty, `path` is ignored and the payload is parsed directly. |

If `payload` is empty and `path` is empty, the current config path is reloaded.
On success the executor calls `ApplyConfig(cfg, false)` which suspends the
engine, diffs drivers/transports/rules, and resumes. **API config**
(`api.listen`, `api.secret`, `api.tls-*`), **rule-providers**, **rule-groups**,
and **engine tuning** (`global.engine.*`) cannot be hot-applied — changes log a
warning and require restart.

**Response:** `204 No Content` (empty body) on success.
`400` `{"error":"<decode error>"}` on bad JSON.
`500` `{"error":"internal server error"}` on reload failure (real error logged).

### 3.7 `PATCH /configs` — selective runtime patch

Handler `patchConfigs` (`configs.go:110`). Auth required. Body limited to 1 MiB.

**Request body:** an arbitrary JSON object (`map[string]any`).

**Supported keys** (from `executor.Patch`, `executor.go:142`):
| Key | Value | Effect |
|---|---|---|
| `log-level` | string, one of `debug`/`info`/`warn`/`warning`/`error`/`silent` | Sets the global log level live via `log.SetLevel`. |

**Any other key is rejected** with `400`
`{"error":"unsupported patch key(s): [...] (supported: log-level)"}`.

Example:
```json
{"log-level": "debug"}
```

**Response:** `204 No Content` on success.
`400` `{"error":"<message>"}` on bad JSON, unsupported key, or invalid level.

### 3.8 `GET /drivers`

Handler `getDrivers` (`drivers.go:11`). Auth required. Returns
`{"drivers": dm.ListDrivers()}` where `ListDrivers()` returns `[]core.DriverStatus`.

**Response `200`:**
```json
{
  "drivers": [
    {
      "name": "plc-modbus",
      "type": "modbus-tcp",
      "state": 2,
      "last_read": "2024-01-01T12:00:00.123456789Z",
      "last_error": "",
      "tag_count": 3,
      "read_count": 12345,
      "error_count": 2,
      "reconnect_count": 1
    }
  ]
}
```
The `drivers` field is the only top-level key (map literal
`map[string]any{"drivers": ...}`).

#### `DriverStatus` (struct `core.DriverStatus`, `core/driver.go:57`)

| Field | Go type | json tag | JSON type | Notes |
|---|---|---|---|---|
| `Name` | `string` | `"name"` | string | |
| `Type` | `string` | `"type"` | string | driver protocol type, e.g. `modbus-tcp`, `s7`, `opcua` |
| `State` | `ConnState` | `"state"` | **integer** | `0=disconnected, 1=connecting, 2=connected, 3=error` (no `MarshalJSON`; raw int) |
| `LastRead` | `time.Time` | `"last_read"` | RFC3339 string | zero time → `"0001-01-01T00:00:00Z"` |
| `LastError` | `string` | `"last_error"` | string | empty when no error |
| `TagCount` | `int` | `"tag_count"` | number | |
| `ReadCount` | `uint64` | `"read_count"` | number | |
| `ErrorCount` | `uint64` | `"error_count"` | number | |
| `ReconnectCount` | `uint64` | `"reconnect_count"` | number | |

> Note: `DriverCapabilities` (`core/driver.go:69`) has JSON tags but is **NOT**
> included in `DriverStatus` and therefore not returned by `GET /drivers`. The
> `/drivers` endpoints expose `DriverStatus` only.

### 3.9 `GET /drivers/{name}`

Handler `getDriver` (`drivers.go:17`). Auth required. Path param `name`.

Returns the matching `core.DriverStatus` object **directly** (not wrapped in a
`{"drivers":...}` map) — `render(w, r, 200, d)`.

**Response `200`:** a single `DriverStatus` object (shape as in §3.8, without the
outer `drivers` array).

**`404`** `{"error":"driver not found"}` when no driver matches `name`.

### 3.10 `GET /drivers/{name}/tags`

Handler `getDriverTags` (`drivers.go:32`). Auth required. Path param `name`.

Calls `da.LatestValues(name)` (snapshot of that driver's cached tags) and
annotates staleness via `sp.StaleThreshold()` (`tags.go:18` `annotateStaleness`):
any `DataPoint` whose `Timestamp` is older than `now - stale-threshold` gets
`IsStale = true`. If `stale-threshold` is 0 (disabled, the default), no
annotation is applied.

**Response `200`:**
```json
{
  "tags": {
    "temperature": {
      "driver": "plc-modbus",
      "device": "",
      "group": "sensors",
      "tag": "temperature",
      "value": 42.5,
      "type": "float32",
      "quality": 0,
      "timestamp": "2024-01-01T12:00:00.123456789Z",
      "metadata": { "source": "modbus" },
      "is_stale": false
    }
  }
}
```
The `tags` object is a `map[string]core.DataPoint` keyed by **tag name**.
See §6 for the full `DataPoint` shape.

> **Quirk:** if `name` does not match a cached driver, `LatestValues(name)`
> returns `nil` (`cache.GetByDriver` returns nil for unknown drivers) and
> `annotateStaleness` passes `nil` through unchanged. The response is then
> `{"tags": null}` (not `{"tags": {}}` and not a `404`). The handler does not
> validate driver existence. Frontend code should treat `tags` as
> `Record<string, DataPoint> | null`. By contrast, `GET /tags` always returns a
> non-nil object (`cache.GetAll` allocates a fresh map), so its `tags` is `{}`
> when empty, never `null`.

### 3.11 `GET /transports`

Handler `getTransports` (`transports.go:11`). Auth required.
`{"transports": tm.ListTransports()}` → `[]core.TransportStatus`.

**Response `200`:**
```json
{
  "transports": [
    {
      "name": "cloud-mqtt",
      "type": "mqtt",
      "state": 2,
      "published": 10000,
      "failed": 5,
      "received": 0,
      "last_publish": "2024-01-01T12:00:00.123456789Z",
      "queue_size": 3,
      "dropped_commands": 0
    }
  ]
}
```

#### `TransportStatus` (struct `core.TransportStatus`, `core/transport.go:67`)

| Field | Go type | json tag | JSON type | Notes |
|---|---|---|---|---|
| `Name` | `string` | `"name"` | string | |
| `Type` | `string` | `"type"` | string | `mqtt` or `http` |
| `State` | `ConnState` | `"state"` | **integer** | `0=disconnected, 1=connecting, 2=connected, 3=error` |
| `Published` | `uint64` | `"published"` | number | total messages published |
| `Failed` | `uint64` | `"failed"` | number | total publish failures |
| `Received` | `uint64` | `"received"` | number | data points ingested via `OnData` (chained-core inbound) |
| `LastPublish` | `time.Time` | `"last_publish"` | RFC3339 string | zero time → `"0001-01-01T00:00:00Z"` |
| `QueueSize` | `int` | `"queue_size"` | number | current outbound queue size |
| `DroppedCommands` | `uint64` | `"dropped_commands"` | number | write commands dropped at ingress (command channel full) |

### 3.12 `GET /transports/{name}`

Handler `getTransport` (`transports.go:17`). Auth required. Path param `name`.

Returns the matching `core.TransportStatus` **directly** (no wrapper).
**`404`** `{"error":"transport not found"}` when no match.

### 3.13 `GET /tags`

Handler `getAllTags` (`tags.go:37`). Auth required. No query params.

Calls `da.LatestValues("")` → `cache.GetAll()` (`engine/cache.go:134`), a
`map[string]DataPoint` keyed by **tag name** aggregated across all drivers.
When two drivers share a tag name, the last visited wins, but each `DataPoint`
carries its own `Driver` field. Staleness annotation applied as in §3.10.

**Response `200`:** `{"tags": { "<tag-name>": <DataPoint>, ... }}` (see §6).

### 3.14 `POST /write`

Handler `writeTag` (`tags.go:46`). Auth required. Body limited to 1 MiB.

**Request body** (struct `core.WriteCommand`, `core/types.go:189`):
```json
{
  "driver": "plc-modbus",
  "device": "",
  "tag": "pump_status",
  "value": true,
  "type": "bool"
}
```
| Field | Go type | json tag | JSON type | Required | Notes |
|---|---|---|---|---|---|
| `driver` | `string` | `"driver"` | string | yes | target driver name |
| `device` | `string` | `"device"` | string | no | device identifier (often empty) |
| `tag` | `string` | `"tag"` | string | yes | tag name to write |
| `value` | `any` | `"value"` | any JSON value | yes | bool/number/string; must be coercible to `type` |
| `type` | `DataType` | `"type"` | **string** | yes | accepts string (`"bool"`,`"float32"`,…) **or** legacy integer (0–12); see §6.1 |

**Response `200`** (struct `core.WriteResult`, `core/types.go:198`):
```json
{"success": true}
```
| Field | Go type | json tag | JSON type | Notes |
|---|---|---|---|---|
| `Success` | `bool` | `"success"` | boolean | |
| `Error` | `string` | `"error,omitempty"` | string | omitted (empty) on success |

**Errors:**
- `400` `{"error":"<json decode error>"}` — malformed body.
- `500` `{"error":"internal server error"}` — write failed (real error logged;
  the audit log records `driver`, `tag`, `success` but **never the value**, which
  may carry process-sensitive data).

### 3.15 `GET /write/failed` — dead-letter queue

Handler `getFailedWrites` (`tags.go:88`). Auth required. No query params.

Returns `sp.DeadLetterEntries()` (`engine/command_manager.go:228`) — write
commands that failed after all retries.

**Response `200`:**
```json
{
  "failed_writes": [
    {
      "command": {
        "driver": "plc-modbus",
        "device": "",
        "tag": "pump_status",
        "value": true,
        "type": "bool"
      },
      "error": "connection refused",
      "failed_at": "2024-01-01T12:00:00.123456789Z",
      "attempts": 4
    }
  ],
  "count": 1
}
```
| Field | Go type / json tag | JSON type | Notes |
|---|---|---|---|
| `failed_writes` | `[]DeadLetterEntry` / `"failed_writes"` | array | |
| `count` | `int` / `"count"` | number | `len(entries)` |

#### `DeadLetterEntry` (struct `core.DeadLetterEntry`, `core/types.go:205`)

| Field | Go type | json tag | JSON type | Notes |
|---|---|---|---|---|
| `Command` | `WriteCommand` | `"command"` | object | the original write command (shape as in §3.14) |
| `Error` | `string` | `"error"` | string | last error message (no `omitempty` — always present) |
| `FailedAt` | `time.Time` | `"failed_at"` | RFC3339 string | |
| `Attempts` | `int` | `"attempts"` | number | total attempt count = `write-retry-count + 1` |

### 3.16 `GET /rules`

Handler `getRules` (`rules.go:12`). Auth required. No query params.
Returns `{"rules": rm.GetRuleStats()}` → `[]core.RuleStat` (built by
`rule/engine.go:181` `RuleStats()`).

**Response `200`:**
```json
{
  "rules": [
    {
      "index": 0,
      "name": "high-temp-alert",
      "type": "simple",
      "match": "tag == 'temperature' && value > 95",
      "action": "alert",
      "target": "cloud-mqtt",
      "targets": [],
      "priority": 1,
      "disabled": false,
      "hit_count": 12,
      "hit_at": "2024-01-01T12:00:00.123456789Z",
      "miss_count": 988,
      "miss_at": "2024-01-01T11:59:59Z"
    }
  ]
}
```

#### `RuleStat` (struct `core.RuleStat`, `core/rule.go:66`)

| Field | Go type | json tag | JSON type | Notes |
|---|---|---|---|---|
| `Index` | `int` | `"index"` | number | position in the rule list (used by `PATCH /rules/disable`) |
| `Name` | `string` | `"name"` | string | |
| `Type` | `string` | `"type"` | string | rule kind: `"simple"`, `"rule-set"`, or `"sub-rule"` |
| `Match` | `string` | `"match"` | string | the match expression / payload. For `simple`: the expression; for `rule-set`: `"RULE-SET:<provider-name>"`; for `sub-rule`: `"SUB-RULE:<name>"` |
| `Action` | `string` | `"action"` | string | one of `"forward"`, `"drop"`, `"alert"`, `"transform"`, `"mirror"` (lowercase) |
| `Target` | `string` | `"target"` | string | single target transport name; empty for `drop`/mirror-with-only-targets |
| `Targets` | `[]string` | `"targets"` | array of strings **or `null`** | mirror targets. **No `omitempty`**: when the rule has no targets and no single `target`, the slice is `nil` and serializes as JSON `null` (not `[]`). When a single `target` is set, `Targets` is populated as `[target]` (see `newSimpleRule`/`newRuleSetRule`/`newSubRuleRef`). |
| `Priority` | `int` | `"priority"` | number | lower = higher precedence |
| `Disabled` | `bool` | `"disabled"` | boolean | true when administratively disabled via `PATCH /rules/disable` |
| `HitCount` | `uint64` | `"hit_count"` | number | times the rule matched |
| `HitAt` | `time.Time` | `"hit_at"` | RFC3339 string | last match time; zero time `"0001-01-01T00:00:00Z"` if never hit |
| `MissCount` | `uint64` | `"miss_count"` | number | times the rule was evaluated but did not match |
| `MissAt` | `time.Time` | `"miss_at"` | RFC3339 string | last miss time |

`Action` values come from `actionToString` (`rule/engine.go:230`):
`forward|drop|alert|transform|mirror`.

### 3.17 `PATCH /rules/disable`

Handler `disableRule` (`rules.go:23`). Auth required. Body limited to 1 MiB.

**Request body** (struct `disableRuleRequest`, `rules.go:17`):
```json
{"index": 0, "disabled": true}
```
| Field | Go type | json tag | JSON type | Required | Notes |
|---|---|---|---|---|---|
| `index` | `int` | `"index"` | number | yes | rule index (from `GET /rules` `[].index`) |
| `disabled` | `bool` | `"disabled"` | boolean | yes | `true` = disable, `false` = re-enable |

Calls `rm.SetRuleDisabled(index, disabled)` (`rule/engine.go:212`).

**Response:** `204 No Content` on success.
**`400`** `{"error":"<message>"}` on bad JSON or out-of-range index
(`"rule index out of range: <n>"` or `"rule at index <n> is not a RuleWrapper"`).

### 3.18 `GET /stats`

Handler `getStats` (`stats.go:10`). Auth required. No query params.
Returns `sp.Stats()` directly → `core.EngineStats` (`engine/engine.go:586`).

**Response `200`:**
```json
{
  "status": "running",
  "uptime": 332123456000,
  "drivers": 1,
  "transports": 2,
  "rules": 2,
  "total_read": 12345,
  "total_publish": 12000,
  "total_errors": 3,
  "total_dropped": 0,
  "points_per_sec": 37.18,
  "driver_stats": {
    "plc-modbus": { "...": "DriverStatus, see §3.8" }
  },
  "transport_stats": {
    "cloud-mqtt": { "...": "TransportStatus, see §3.11" }
  }
}
```

#### `EngineStats` (struct `core.EngineStats`, `core/engine.go:315`)

| Field | Go type | json tag | JSON type | Notes |
|---|---|---|---|---|
| `Status` | `EngineStatus` | `"status"` | **string** | `"running"`, `"suspended"`, or `"stopped"` (string-typed) |
| `Uptime` | `time.Duration` | `"uptime"` | **integer (nanoseconds)** | `time.Since(startTime)`; **divide by 1e9 for seconds**. The `/metrics` endpoint already converts to seconds (`corec_uptime_seconds`). |
| `Drivers` | `int` | `"drivers"` | number | configured driver count |
| `Transports` | `int` | `"transports"` | number | configured transport count |
| `Rules` | `int` | `"rules"` | number | configured rule count |
| `TotalRead` | `uint64` | `"total_read"` | number | |
| `TotalPublish` | `uint64` | `"total_publish"` | number | |
| `TotalErrors` | `uint64` | `"total_errors"` | number | |
| `TotalDropped` | `uint64` | `"total_dropped"` | number | sum of databus drops + log drops |
| `PointsPerSec` | `float64` | `"points_per_sec"` | number | `total_read / uptime.Seconds()` (0 before start) |
| `DriverStats` | `map[string]DriverStatus` | `"driver_stats"` | object | keyed by driver name; values are `DriverStatus` (§3.8) |
| `TransportStats` | `map[string]TransportStatus` | `"transport_stats"` | object | keyed by transport name; values are `TransportStatus` (§3.11) |

> `points_per_sec` is computed as `float64(totalRead) / uptime.Seconds()`, so it
> is a lifetime average, not an instantaneous rate.

---

## 4. WebSocket Endpoints

All four use `coder/websocket` (`github.com/coder/websocket`). The server calls
`websocket.Accept(w, r, nil)` — **no subprotocol negotiation**. Messages are
written with `wsjson.Write` as **JSON text frames**. Each write has a 5-second
timeout (`wsWriteTimeout`). On any write error the goroutine returns and the
socket closes with `websocket.StatusInternalError`. Normal client disconnect →
`websocket.StatusNormalClosure`.

CORS / auth: the HTTP upgrade request passes through the same middleware stack
(§1.2), so the auth `token` query param or `Authorization` header must be present
on the upgrade request, and `Origin` must satisfy CORS. **The `token` query param
is the practical way to authenticate a browser WebSocket** (browsers cannot set
headers on WS upgrades).

> WebSocket lifetimes: `ReadTimeout`/`WriteTimeout` default to `0` (disabled) so
> absolute deadlines do not kill long-lived streams. Only `ReadHeaderTimeout`
> (default `10s`) applies to the handshake.

### 4.1 `GET /logs` — real-time log stream

Handler `getLogs` (`logs.go:12`). Auth required. Subscribes to the global log bus
(`log.Subscribe()`, `log/log.go:72`). The connection context is
`c.CloseRead(r.Context())`, so it closes when the HTTP request context is cancelled
or the client disconnects.

**Each pushed message** is a `log.Event` (`log/log.go:18`):
```json
{
  "level": 0,
  "type": "info",
  "payload": "tag written method=POST path=/write driver=plc-modbus tag=pump_status success=true remote=127.0.0.1:54321",
  "timestamp": "2024-01-01T12:00:00.123456789Z"
}
```
| Field | Go type | json tag | JSON type | Notes |
|---|---|---|---|---|
| `level` | `slog.Level` | `"level"` | **integer** | `slog.Level` is `int8`: `-8=debug, 0=info, 4=warn, 8=error` (no `MarshalJSON`; raw int) |
| `type` | `string` | `"type"` | string | `"debug"`, `"info"`, `"warning"`, `"error"` (human-readable; computed in `publishRecord`) |
| `payload` | `string` | `"payload"` | string | message + `" key=value"` for each slog attr |
| `timestamp` | `time.Time` | `"timestamp"` | RFC3339 string | the slog record time |

Messages are only published for records at or above the current global log level
(`publishRecord` drops lower levels), so lowering `log-level` via `PATCH /configs`
to `debug` increases what this stream emits.

### 4.2 `GET /traffic` — periodic throughput snapshot

Handler `getTraffic` (`traffic.go:14`). Auth required.

**Query parameters:**
| Param | Type | Default | Notes |
|---|---|---|---|
| `interval` | duration string | `1s` (`wsPushInterval`) | e.g. `500ms`, `2s`. Invalid/non-positive → falls back to `1s`. |

The connection context is `c.CloseRead(context.Background())` — **not** tied to
the request context; it lives until the client disconnects. A `time.Ticker`
pushes a snapshot every `interval`.

**Each pushed message** (built inline as `map[string]any`):
```json
{"read": 12345, "publish": 12000, "dropped": 0}
```
| Field | JSON type | Source |
|---|---|---|
| `read` | number (uint64) | `stats.TotalRead` |
| `publish` | number (uint64) | `stats.TotalPublish` |
| `dropped` | number (uint64) | `stats.TotalDropped` |

### 4.3 `GET /memory` — periodic Go runtime memory snapshot

Handler `getMemory` (`memory.go:14`). Auth required.

**Query parameters:**
| Param | Type | Default | Notes |
|---|---|---|---|
| `interval` | duration string | `1s` | same semantics as `/traffic`. |

Context is `c.CloseRead(r.Context())`. Uses `runtime/metrics.Read` (no
Stop-The-World). Pushes every `interval`.

**Each pushed message** (built inline as `map[string]any`):
```json
{
  "alloc": 1048576,
  "total_alloc": 52428800,
  "sys": 16777216,
  "num_gc": 42,
  "goroutines": 37
}
```
| Field | JSON type | runtime/metrics source |
|---|---|---|
| `alloc` | number (uint64) | `/memory/classes/heap/objects:bytes` (≈ `runtime.ReadMemStats.HeapAlloc`) |
| `total_alloc` | number (uint64) | `/gc/heap/allocs:bytes` (≈ `TotalAlloc`, cumulative) |
| `sys` | number (uint64) | `/memory/classes/total:bytes` (total memory classes) |
| `num_gc` | number (uint64) | `/gc/cycles/total:gc-cycles` (≈ `NumGC`, cumulative) |
| `goroutines` | number (int) | `runtime.NumGoroutine()` |

### 4.4 `GET /tags/stream` — real-time DataPoint stream

Handler `streamTags` (`stream.go:13`). Auth required.

**Query parameters:**
| Param | Type | Default | Notes |
|---|---|---|---|
| `driver` | string | `""` (all drivers) | Optional driver-name filter passed to `es.Subscribe(filter)`. |

Context is `c.CloseRead(r.Context())`. Subscribes to the engine's DataBus
(`e.Subscribe(filter)`); each `DataPoint` flowing through the pipeline is pushed
as-is via `wsjson.Write`.

**Each pushed message** is a single `core.DataPoint` (see §6 for the full shape).
Note: `IsStale` is `omitempty` and is **not** set during pipeline processing (only
the `/tags` and `/drivers/{name}/tags` REST handlers set it), so streamed points
will not contain `is_stale`.

Example frame:
```json
{
  "driver": "plc-modbus",
  "device": "",
  "group": "sensors",
  "tag": "temperature",
  "value": 42.5,
  "type": "float32",
  "quality": 0,
  "timestamp": "2024-01-01T12:00:00.123456789Z"
}
```

---

## 5. `GET /metrics` — Prometheus Endpoint

Handler `promMetrics` (`metrics.go:26`). Auth required. Returns
`text/plain; version=0.0.4; charset=utf-8`. Every metric is produced from the
engine's `Stats()` plus optional role interfaces (`LatencyProvider`,
`OfflineBufferStatsProvider`, `DataAgeProvider`, `FlushBatchesDroppedProvider`)
and the package-level `httpMetricsCollector`. Output is deterministic (per-driver
and per-transport metrics are emitted in **sorted name order**).

### 5.1 Counters (monotonically increasing; `_total` suffix)

| Metric | Type | Labels | Source |
|---|---|---|---|
| `corec_reads_total` | counter | — | `stats.TotalRead` |
| `corec_publishes_total` | counter | — | `stats.TotalPublish` |
| `corec_errors_total` | counter | — | `stats.TotalErrors` |
| `corec_dropped_total` | counter | — | `stats.TotalDropped` |
| `corec_log_dropped_total` | counter | — | `log.Dropped()` (source-channel + subscriber fan-out drops) |
| `corec_driver_read_total` | counter | `driver`, `type` | per-driver `ReadCount` |
| `corec_driver_errors_total` | counter | `driver` | per-driver `ErrorCount` |
| `corec_driver_reconnect_total` | counter | `driver` | per-driver `ReconnectCount` |
| `corec_transport_published_total` | counter | `transport`, `type` | per-transport `Published` |
| `corec_transport_failed_total` | counter | `transport` | per-transport `Failed` |
| `corec_transport_received_total` | counter | `transport` | per-transport `Received` |
| `corec_transport_dropped_commands_total` | counter | `transport` | per-transport `DroppedCommands`; **only emitted when > 0** |
| `corec_offline_buffer_drained_total` | counter | — | batches replayed from offline buffer (only when engine implements `OfflineBufferStatsProvider`) |
| `corec_offline_buffer_pushed_total` | counter | — | batches persisted to offline buffer (only when engine implements `OfflineBufferStatsProvider`) |
| `corec_flush_batches_dropped_total` | counter | — | full batches evicted from batcher flush queues (only when engine implements `FlushBatchesDroppedProvider`) |
| `corec_http_requests_total` | counter | `method`, `status` | per `METHOD:STATUS` key from `httpMetricsCollector` |

### 5.2 Gauges (instantaneous; no `_total` suffix)

| Metric | Type | Labels | Source |
|---|---|---|---|
| `corec_drivers` | gauge | — | `stats.Drivers` |
| `corec_transports` | gauge | — | `stats.Transports` |
| `corec_rules` | gauge | — | `stats.Rules` |
| `corec_uptime_seconds` | gauge | — | `stats.Uptime.Seconds()` (seconds, float) |
| `corec_points_per_second` | gauge | — | `stats.PointsPerSec` |
| `corec_driver_tags` | gauge | `driver` | per-driver `TagCount` |
| `corec_driver_connected` | gauge | `driver` | `1` if `State==connected`, else `0` |
| `corec_transport_queue_size` | gauge | `transport` | per-transport `QueueSize` |
| `corec_transport_connected` | gauge | `transport` | `1` if `State==connected`, else `0` |
| `corec_offline_buffer_pending` | gauge | — | batches awaiting replay (only when `OfflineBufferStatsProvider`) |
| `corec_goroutines` | gauge | — | `runtime.NumGoroutine()` |
| `corec_mem_heap_alloc_bytes` | gauge | — | heap objects bytes (`HeapAlloc` analog) |
| `corec_mem_heap_sys_bytes` | gauge | — | sum of heap memory classes (objects+free+unused+released) |
| `corec_mem_stack_inuse_bytes` | gauge | — | heap stacks bytes (`StackInuse` analog) |
| `corec_mem_total_alloc_bytes` | gauge | — | cumulative bytes allocated (`TotalAlloc` analog) |
| `corec_gc_count` | gauge | — | total GC completions (`NumGC` analog) |
| `corec_gc_pause_total_seconds` | gauge | — | approximated cumulative GC STW pause seconds (midpoint-sum of `/sched/pauses/total/gc:seconds` histogram) |
| `corec_cpu_count` | gauge | — | `runtime.NumCPU()` |

### 5.3 Histograms

| Metric family | Type | Source | Buckets |
|---|---|---|---|
| `corec_http_request_duration_seconds` | histogram | `httpMetricsCollector` | cumulative buckets at `0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5` seconds + `+Inf`; plus `_sum` and `_count` |
| `corec_read_latency_seconds` | histogram | `LatencyProvider.ReadLatencyHistogram()` (only when engine implements it) | from `LatencySnapshot.Buckets`; `_bucket{le=...}`, `+Inf`, `_sum`, `_count` |
| `corec_publish_latency_seconds` | histogram | `LatencyProvider.PublishLatencyHistogram()` | same |
| `corec_data_age_seconds` | histogram | `DataAgeProvider.DataAgeHistogram()` | same |

Each histogram emits `<name>_bucket{le="<upper-bound>"} <cumulative count>`,
`<name>_bucket{le="+Inf"} <total>`, `<name>_sum <float>`, `<name>_count <uint64>`.

Label values are Prometheus-escaped (`\`, `"`, `\n` escaped) via `promEscape`.

### 5.4 Conditional emission

- Per-driver / per-transport metric families are emitted **only when at least one
  driver/transport exists** (early return on empty map).
- `corec_offline_buffer_*` and `corec_flush_batches_dropped_total` require the
  engine to satisfy the corresponding optional role interface (`OfflineBufferStatsProvider` /
  `FlushBatchesDroppedProvider`). `*CoreCEngine` satisfies both; test mocks may not.
- Latency / data-age histograms require `LatencyProvider` / `DataAgeProvider`.

---

## 6. `DataPoint` / Tag Value JSON Shape

The `DataPoint` struct (`core/types.go:170`) is the standard data unit returned
by `GET /tags`, `GET /drivers/{name}/tags`, pushed over `GET /tags/stream`, and
published to transports. `GET /tags` and `GET /drivers/{name}/tags` return it as
**values of a `tags` object keyed by tag name**.

```json
{
  "driver": "plc-modbus",
  "device": "",
  "group": "sensors",
  "tag": "temperature",
  "value": 42.5,
  "type": "float32",
  "quality": 0,
  "timestamp": "2024-01-01T12:00:00.123456789Z",
  "metadata": { "source": "modbus" },
  "is_stale": false
}
```

| Field | Go type | json tag | JSON type | Notes |
|---|---|---|---|---|
| `Driver` | `string` | `"driver"` | string | source driver name |
| `Device` | `string` | `"device"` | string | device identifier (often empty) |
| `Group` | `string` | `"group"` | string | tag group |
| `Tag` | `string` | `"tag"` | string | tag name |
| `Value` | `any` | `"value"` | any JSON value | `bool`/`number`/`string`/`null`; marshaled as whatever Go value the driver produced |
| `Type` | `DataType` | `"type"` | **string** | `"bool"`,`"int8"`,...,`"bytes"` (has `MarshalJSON`) |
| `Quality` | `Quality` | `"quality"` | **integer** | `0=good, 1=bad, 2=uncertain` (no `MarshalJSON`; raw int) |
| `Timestamp` | `time.Time` | `"timestamp"` | RFC3339 string | |
| `Metadata` | `map[string]string` | `"metadata,omitempty"` | object | omitted entirely when nil/empty |
| `IsStale` | `bool` | `"is_stale,omitempty"` | boolean | **only set by the `/tags` and `/drivers/{name}/tags` REST handlers** when `stale-threshold > 0` and the value is older than the threshold; omitted from streamed/published points |

### 6.1 `DataType` values (string form in JSON)

`core.DataType.MarshalJSON` (`core/types.go:53`) returns the string form. The
`UnmarshalJSON` (`core/types.go:66`) accepts **both** the string form
(`"float32"`) and the legacy integer form (`9`) for inbound JSON (e.g. `POST /write`).

| String | Integer | Notes |
|---|---|---|
| `"bool"` | 0 | |
| `"int8"` | 1 | |
| `"int16"` | 2 | |
| `"int32"` | 3 | |
| `"int64"` | 4 | |
| `"uint8"` | 5 | |
| `"uint16"` | 6 | |
| `"uint32"` | 7 | |
| `"uint64"` | 8 | |
| `"float32"` | 9 | |
| `"float64"` | 10 | |
| `"string"` | 11 | |
| `"bytes"` | 12 | |

### 6.2 `TagValue` (raw device read — NOT directly returned by any REST endpoint)

`core.TagValue` (`core/types.go:160`) is the internal raw-read shape. Its `Error`
field has `json:"-"` and is **never** serialized. It is used inside the engine
and by `DataAccessor.ReadTag` (which is not exposed as a REST endpoint). Listed
for completeness:

| Field | Go type | json tag |
|---|---|---|
| `Tag` | `string` | `"tag"` |
| `Value` | `any` | `"value"` |
| `Type` | `DataType` | `"type"` |
| `Quality` | `Quality` | `"quality"` |
| `Timestamp` | `time.Time` | `"timestamp"` |
| `Error` | `error` | `"-"` (never serialized) |

---

## 7. Driver & Transport Status Fields (recap)

These are the exact fields returned by `GET /drivers`, `GET /drivers/{name}`,
`GET /transports`, `GET /transports/{name}`, and embedded in `GET /stats`'s
`driver_stats` / `transport_stats` maps.

### 7.1 Driver status — `core.DriverStatus` (`core/driver.go:57`)

| Field | json tag | JSON type |
|---|---|---|
| `Name` | `"name"` | string |
| `Type` | `"type"` | string |
| `State` | `"state"` | **integer** (`0=disconnected,1=connecting,2=connected,3=error`) |
| `LastRead` | `"last_read"` | RFC3339 string |
| `LastError` | `"last_error"` | string |
| `TagCount` | `"tag_count"` | number |
| `ReadCount` | `"read_count"` | number |
| `ErrorCount` | `"error_count"` | number |
| `ReconnectCount` | `"reconnect_count"` | number |

### 7.2 Transport status — `core.TransportStatus` (`core/transport.go:67`)

| Field | json tag | JSON type |
|---|---|---|
| `Name` | `"name"` | string |
| `Type` | `"type"` | string |
| `State` | `"state"` | **integer** (`0=disconnected,1=connecting,2=connected,3=error`) |
| `Published` | `"published"` | number |
| `Failed` | `"failed"` | number |
| `Received` | `"received"` | number |
| `LastPublish` | `"last_publish"` | RFC3339 string |
| `QueueSize` | `"queue_size"` | number |
| `DroppedCommands` | `"dropped_commands"` | number |

### 7.3 `ConnState` enum (shared by driver & transport `state`)

Defined in `core/types.go:124`. **No `MarshalJSON`** → raw integer in JSON.
String values (from `String()`, used only in logs/Prometheus label logic):

| Integer | String |
|---|---|
| 0 | `disconnected` |
| 1 | `connecting` |
| 2 | `connected` |
| 3 | `error` |

> The Prometheus `corec_driver_connected` / `corec_transport_connected` gauges
> map `State==connected` (2) to `1` and everything else to `0`. The `/metrics`
> handler is the only place the string form is not used; JSON uses the integer.

---

## 8. Configuration Schema (full)

Source of truth: `core.Config` (`core/engine.go:142`) and sub-structs, loaded by
`config.Load`/`config.Parse` (`config/config.go`). YAML tags shown (the on-disk
format); JSON tags are **not** defined on config structs (they are never
serialized directly — `GET /configs` returns the redacted `configOverview`, §3.5).

`${ENV_VAR}` substitution is applied to raw YAML bytes **before** parsing
(`config/config.go:42`); unset vars are left as the literal `${...}` string.

### 8.1 Top-level (`core.Config`, `core/engine.go:142`)

| Field | YAML tag | Type | Required | Notes |
|---|---|---|---|---|
| `Node` | `node,omitempty` | `NodeConfig` | no | topology auto-discovery; omitted entirely → disabled |
| `Global` | `global` | `GlobalConfig` | yes | |
| `Drivers` | `drivers` | `[]DriverConfig` | yes (≥1, or inbound transport / auto-discovery) | |
| `Transports` | `transports` | `[]TransportConfig` | yes (≥1) | |
| `Rules` | `rules` | `[]RuleConfig` | no | |
| `RuleProviders` | `rule-providers,omitempty` | `[]RuleProviderConfig` | no | hot-reloadable external rule sets; **requires restart to change** |
| `RuleGroups` | `rule-groups,omitempty` | `map[string][]RuleConfig` | no | named sub-rule groups; **requires restart to change** |

### 8.2 `node` — `NodeConfig` (`core/engine.go:158`)

| Field | YAML tag | Type | Default | Notes |
|---|---|---|---|---|
| `ID` | `id` | string | `""` | unique node id; **non-empty enables auto-discovery** |
| `Role` | `role` | string | `""` | informational: `collector`/`relay`/`aggregator`/`sink` (not enforced) |
| `Subscribe` | `subscribe,omitempty` | []string | `[]` | upstream node IDs to receive from |
| `TopicPrefix` | `topic-prefix,omitempty` | string | `topo` | prefix for auto-generated topics |

### 8.3 `global` — `GlobalConfig` (`core/engine.go:181`)

| Field | YAML tag | Type | Default | Notes |
|---|---|---|---|---|
| `LogLevel` | `log-level` | string | `"info"` | `debug`/`info`/`warn`/`warning`/`error`/`silent`; **hot-patchable** via `PATCH /configs` |
| `LogFormat` | `log-format` | string | `"text"` | `"text"` or `"json"` |
| `API` | `api` | `APIConfig` | — | see §8.4 |
| `Engine` | `engine` | `EngineConfig` | — | see §8.5; **requires restart to change** |
| `Buffer` | `buffer` | `BufferConfig` | — | see §8.6 |

### 8.4 `global.api` — `APIConfig` (`core/engine.go:258`)

| Field | YAML tag | Type | Default | Notes |
|---|---|---|---|---|
| `Listen` | `listen` | string | `""` | bind address, e.g. `0.0.0.0:9090`; empty → no API server. **Requires restart to change.** |
| `Secret` | `secret` | string | `""` | auth token; **required** when `listen` set; **≥ 8 chars**; **requires restart to change.** |
| `TLSCert` | `tls-cert,omitempty` | string | `""` | PEM path; both cert+key → HTTPS |
| `TLSKey` | `tls-key,omitempty` | string | `""` | PEM path |
| `AllowedOrigins` | `allowed-origins,omitempty` | []string | `[]` (permissive `*`) | CORS allow-list |
| `RateLimitPerSec` | `rate-limit-per-sec,omitempty` | int | `0` (disabled) | per-IP requests/sec |
| `ReadHeaderTimeout` | `read-header-timeout,omitempty` | duration string | `10s` | |
| `ReadTimeout` | `read-timeout,omitempty` | duration string | `0` (disabled) | **0 recommended** so WebSockets survive |
| `WriteTimeout` | `write-timeout,omitempty` | duration string | `0` (disabled) | **0 recommended** so WebSockets survive |
| `IdleTimeout` | `idle-timeout,omitempty` | duration string | `120s` | |
| `PprofDisabled` | `pprof-disabled,omitempty` | bool | `false` | `true` → disable pprof entirely |
| `PprofAddr` | `pprof-addr,omitempty` | string | `""` | separate unauthenticated pprof server address |

### 8.5 `global.engine` — `EngineConfig` (`core/engine.go:191`)

All optional with built-in defaults. **Requires restart to change** (logged as
warning on reload).

| Field | YAML tag | Type | Default | Notes |
|---|---|---|---|---|
| `DataBusSize` | `data-bus-size,omitempty` | int | `8192` | internal data channel capacity |
| `Workers` | `workers,omitempty` | int | `0` → `runtime.NumCPU()` | processing goroutines |
| `ShutdownTimeout` | `shutdown-timeout,omitempty` | duration string | `30s` | graceful shutdown deadline |
| `ErrorThrottleWindow` | `error-throttle-window,omitempty` | duration string | `10s` | suppress repeated error logs |
| `DefaultTagInterval` | `default-tag-interval,omitempty` | duration string | `1s` | fallback tag interval |
| `OnBadQuality` | `on-bad-quality,omitempty` | string | `"publish"` | `publish`/`drop`/`mark-and-publish`/`alert` |
| `StaleThreshold` | `stale-threshold,omitempty` | duration string | `0` (disabled) | drives `is_stale` on `/tags` & `/drivers/{name}/tags` |
| `WriteRetryCount` | `write-retry-count,omitempty` | int | `3` | retries before dead-letter; total attempts = `+1` |
| `CommandConcurrency` | `command-concurrency,omitempty` | int | `16` | max parallel writes; `1` = serial |
| `HighPriorityWorkers` | `high-priority-workers,omitempty` | int | `2` | dedicated workers for interval ≤ `1s` |

### 8.6 `global.buffer` — `BufferConfig` (`core/engine.go:299`)

| Field | YAML tag | Type | Default | Notes |
|---|---|---|---|---|
| `Enabled` | `enabled` | bool | `false` | persist failed publish batches to disk |
| `MaxSize` | `max-size` | int | `10000` (when ≤0) | max buffered batches; validation requires ≥ 10 when > 0 |
| `Path` | `path` | string | `""` | buffer file directory; **required when `enabled=true`** |

### 8.7 `drivers[]` — `DriverConfig` (`core/driver.go:33`)

| Field | YAML tag | Type | Required | Notes |
|---|---|---|---|---|
| `Name` | `name` | string | yes | unique |
| `Type` | `type` | string | yes | registered: `modbus-tcp`,`modbus-rtu`,`modbus-rtuovertcp`,`modbus-udp`,`modbus-rtuoverudp`,`modbus-tls`,`s7`,`opcua` |
| `Settings` | `settings` | `map[string]any` | no | protocol-specific (host, port, slave-id, endpoint, TLS files, reconnect params, etc.) |
| `Tags` | `tags` | `[]TagConfig` | yes (≥1) | inline tags; appended after file tags |
| `TagsFile` | `tags-file,omitempty` | string | no | external YAML tags file |
| `TagsInterval` | `tags-interval,omitempty` | duration string | no | hot-reload interval for the tags file; empty/`0` = load once |

#### `drivers[].tags[]` — `TagConfig` (`core/types.go:213`)

| Field | YAML tag | Type | Required | Notes |
|---|---|---|---|---|
| `Name` | `name` | string | yes | unique within driver |
| `Address` | `address` | string | yes | protocol-specific address |
| `Type` | `type` | string | yes | one of the `DataType` string names (§6.1) |
| `Group` | `group` | string | no | logical group |
| `Interval` | `interval` | duration string | no | overrides `default-tag-interval`; must be positive |
| `Scale` | `scale,omitempty` | float64 | no | linear scale factor |
| `Offset` | `offset,omitempty` | float64 | no | linear offset (applied as `value*scale + offset`) |
| `DeadBand` | `deadband,omitempty` | float64 | no | suppress publishes within this band |
| `ReadTimeout` | `read-timeout,omitempty` | duration string | no | per-tag read timeout; default = `interval` |

### 8.8 `transports[]` — `TransportConfig` (`core/transport.go:50`)

| Field | YAML tag | Type | Required | Notes |
|---|---|---|---|---|
| `Name` | `name` | string | yes | unique |
| `Type` | `type` | string | yes | registered: `mqtt`, `http` |
| `Settings` | `settings` | `map[string]any` | no | protocol-specific (broker, url, topic-template, command-topic, data-topic, webhook-addr, TLS files, parser, etc.) |
| `BatchSize` | `batch-size,omitempty` | int | no | default `100`; **TOP-LEVEL, not inside `settings`** (validation fails fast on misplacement) |
| `FlushInterval` | `flush-interval,omitempty` | duration string | no | |
| `RetryCount` | `retry-count,omitempty` | int | no | default `0` (no retry) |
| `BufferSize` | `buffer-size,omitempty` | int | no | command/data channel capacity; default `100` |
| `Fallback` | `fallback,omitempty` | string | no | secondary transport name on publish failure |

> Config validation (`config/config.go:235`) explicitly rejects
> `batch-size`/`flush-interval`/`retry-count`/`buffer-size`/`fallback` if they
> appear inside `settings` (silent misconfiguration guard).

### 8.9 `rules[]` — `RuleConfig` (`core/rule.go:41`)

| Field | YAML tag | Type | Required | Notes |
|---|---|---|---|---|
| `Name` | `name` | string | yes | unique |
| `Match` | `match` | string | yes | match expression (see `config.example.yaml` §455–474 for full syntax) or `ALL` / `RULE-SET:<name>` / `SUB-RULE:<name>` |
| `Action` | `action` | string | yes | `forward`/`drop`/`alert`/`transform`/`mirror` (case-insensitive) |
| `Target` | `target,omitempty` | string | no | single target transport name (must exist) |
| `Targets` | `targets,omitempty` | []string | no | mirror targets (must all exist) |
| `Priority` | `priority,omitempty` | int | no | lower = higher precedence; default `0` |
| `Transform` | `transform,omitempty` | `*TransformConfig` | no | required for `action: transform` |

#### `rules[].transform` — `TransformConfig` (`core/rule.go:60`)

| Field | YAML tag | Type | Notes |
|---|---|---|---|
| `Expression` | `expression` | string | arithmetic over `value`, e.g. `value * 9 / 5 + 32` |
| `TagRename` | `tag-rename,omitempty` | string | literal new tag name (not a template) |

### 8.10 `rule-providers[]` — `RuleProviderConfig` (`core/rule.go:52`)

| Field | YAML tag | Type | Notes |
|---|---|---|---|
| `Name` | `name` | string | referenced by `RULE-SET:<name>` |
| `Type` | `type` | string | currently `"file"` |
| `Path` | `path` | string | file path |
| `Interval` | `interval,omitempty` | duration string | hot-reload interval, e.g. `30s` |

### 8.11 `rule-groups` — `map[string][]RuleConfig` (`core/engine.go:149`)

A map from group name to a list of `RuleConfig`. Referenced by `SUB-RULE:<name>`.

### 8.12 Validation rules (`config/config.go:104` `validate`)

- At least one data source: ≥1 driver, OR an inbound transport (mqtt `data-topic`
  or http `webhook-addr`), OR auto-discovery with `node.subscribe`.
- At least one transport.
- Driver/transport names unique; types validated against the registry.
- Each driver needs ≥1 tag; tag names unique within a driver; tag `address` and
  `type` required; `type` must parse as a `DataType`; `interval` and
  `read-timeout` must be positive durations.
- `api.secret` required (≥ 8 chars) when `api.listen` set.
- `buffer.enabled=true` requires `buffer.path`; `buffer.max-size` must be ≥ 10
  when > 0.
- Rule `name`, `match`, `action` required; `action` must be a valid action;
  `target`/`targets` must reference existing transports.

---

## 9. Quick Reference: Status Codes

| Endpoint | Success | Common errors |
|---|---|---|
| `GET /`, `/version`, `/healthz/*` | `200` | — |
| `GET /configs`, `/drivers`, `/transports`, `/tags`, `/rules`, `/stats`, `/write/failed`, `/metrics` | `200` | `401` (no/bad token) |
| `GET /drivers/{name}`, `/transports/{name}` | `200` | `401`, `404` |
| `PUT /configs` | `204` | `400` (bad JSON), `500` (reload error) |
| `PATCH /configs` | `204` | `400` (bad JSON / unsupported key / invalid level) |
| `POST /write` | `200` | `400` (bad JSON), `500` (write error) |
| `PATCH /rules/disable` | `204` | `400` (bad JSON / bad index) |
| Any (rate-limited) | — | `429` with `Retry-After: 1` |
| Any (auth group, no token) | — | `401 {"error":"unauthorized"}` |

All error bodies: `{"error":"<message>"}`. Internal errors are masked to
`{"error":"internal server error"}`.

---

## 10. Frontend Typing Cheat Sheet (TypeScript-style)

```ts
// Enums serialized as NUMBERS (no MarshalJSON)
type ConnState = 0 | 1 | 2 | 3;        // disconnected|connecting|connected|error
type Quality   = 0 | 1 | 2;            // good|bad|uncertain
type SlogLevel = -8 | 0 | 4 | 8;       // debug|info|warn|error (in /logs events)

// String-typed
type DataType    = "bool"|"int8"|"int16"|"int32"|"int64"|"uint8"|"uint16"|"uint32"|"uint64"|"float32"|"float64"|"string"|"bytes";
type EngineStatus = "running"|"suspended"|"stopped";
type RuleType    = "simple"|"rule-set"|"sub-rule";
type RuleAction  = "forward"|"drop"|"alert"|"transform"|"mirror";

interface DataPoint {
  driver: string; device: string; group: string; tag: string;
  value: unknown; type: DataType; quality: Quality;
  timestamp: string;                 // RFC3339
  metadata?: Record<string, string>;
  is_stale?: boolean;                // only from /tags & /drivers/{name}/tags
}

interface DriverStatus {
  name: string; type: string; state: ConnState;
  last_read: string; last_error: string;
  tag_count: number; read_count: number; error_count: number; reconnect_count: number;
}

interface TransportStatus {
  name: string; type: string; state: ConnState;
  published: number; failed: number; received: number;
  last_publish: string; queue_size: number; dropped_commands: number;
}

interface EngineStats {
  status: EngineStatus; uptime: number;   // NANOSECONDS — divide by 1e9 for seconds
  drivers: number; transports: number; rules: number;
  total_read: number; total_publish: number; total_errors: number; total_dropped: number;
  points_per_sec: number;
  driver_stats: Record<string, DriverStatus>;
  transport_stats: Record<string, TransportStatus>;
}

interface RuleStat {
  index: number; name: string; type: RuleType; match: string; action: RuleAction;
  target: string; targets: string[]; priority: number; disabled: boolean;
  hit_count: number; hit_at: string; miss_count: number; miss_at: string;
}

interface WriteCommand { driver: string; device: string; tag: string; value: unknown; type: DataType; }
interface WriteResult { success: boolean; error?: string; }
interface DeadLetterEntry { command: WriteCommand; error: string; failed_at: string; attempts: number; }

// GET /  (uptime here is a STRING, unlike /stats)
interface Hello { name: string; version: string; status: string; time: string; uptime: string; }

// /logs WS event
interface LogEvent { level: SlogLevel; type: "debug"|"info"|"warning"|"error"; payload: string; timestamp: string; }
// /traffic WS snapshot
interface TrafficSnapshot { read: number; publish: number; dropped: number; }
// /memory WS snapshot
interface MemorySnapshot { alloc: number; total_alloc: number; sys: number; num_gc: number; goroutines: number; }
```

**Auth header for all protected endpoints and WebSocket upgrades:**
`Authorization: Bearer <api.secret>`, or `?token=<api.secret>` query param
(required for browser WebSocket clients).
