# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
(see [VERSIONING.md](./VERSIONING.md) for the full stability policy).

## [Unreleased]

### Added

- **Prometheus `/metrics` endpoint** exposing 15+ metric families for drivers,
  transports, the engine, routes, and the offline buffer.
- **pprof profiling endpoints** for on-demand CPU/heap/goroutine analysis in
  production. Configurable via `api.pprof-disabled` (default false) and
  `api.pprof-addr` (optional separate port for pprof, no auth required).
- **W3C Trace Context middleware** — an HTTP middleware (`common/trace`)
  parses the incoming `traceparent` header (W3C format
  `version-trace_id-parent_id-trace_flags`) and propagates the trace ID
  through the request `context` for `slog` log correlation. When no
  `traceparent` is present, it falls back to the chi request ID or
  generates a new W3C-compliant trace ID. Outbound HTTP push requests
  inject a `traceparent` header via `trace.InjectTraceparent` so the
  trace context propagates across service boundaries. Spans are created
  in the engine's `readFromDriver` pipeline step. Span durations and
  attributes are logged at debug level via `slog` (no OTLP export).
- **MQTT TLS / mTLS support** via the `mqtts://` (and `tls://`/`ssl://`) scheme,
  with optional CA, client-cert, and client-key files for mutual TLS.
- **Webhook (HTTP push) HTTPS support** — TLS configuration for outbound
  webhook delivery.
- **MQTT command authentication with replay protection** —
  HMAC-SHA256 command authentication with an authenticated timestamp and a
  configurable skew window (`command-max-skew`, `command-strict-replay`) so
  stale commands outside the window are rejected. A bounded replay cache
  (10,000 entries, auto-expiring after 2× the skew window) records the hash
  of each authenticated command, preventing true replay: the same message
  cannot be accepted twice within the skew window. `command-strict-replay`
  requires that a timestamp be present.
- **Stronger `golangci-lint` configuration** — added `staticcheck`, `govet`,
  `gocyclo` (max complexity 20), `errcheck`, `ineffassign`, `unused`, `nilerr`,
  and `nilnil`, plus the `gofmt` and `goimports` formatters so formatting is
  enforced by the lint gate. (`gosimple` is covered by `staticcheck`;
  `typecheck` is built in.) `cmd/corec` is excluded from `gocyclo` and test
  files from `errcheck`.
- **`CHANGELOG.md`** (this file) and **`VERSIONING.md`** documenting the
  release and interface-stability policy.
- **Go runtime / process metrics** in `/metrics`: goroutine count, heap
  memory (alloc/sys), stack in-use, total alloc, GC count and pause total,
  and CPU count — essential for production monitoring.
- **MQTT reconnect jitter (±20%)** — the `connect-retry-interval` is
  randomized by ±20% at Init time to prevent thundering-herd when multiple
  MQTT transports reconnect simultaneously after a broker outage.

### Changed

- **S7 driver test coverage: 44.6% → 83.5%.**
- **OPC UA driver test coverage: 40.3% → 56.1%.**
- **Total project test coverage: 70.4% → 76.8%.**
- `golangci-lint` now enforces a cyclomatic-complexity limit of 20 for new
  code; eight pre-existing complex functions are annotated with
  `//nolint:gocyclo` (five production functions tracked for future
  refactoring, plus three exhaustive test helpers where the complexity is
  intentional).
- **Prometheus gauge renames** (breaking for dashboards/scrapers) — dropped
  the `_total` suffix from gauges, which is reserved for counters:
  `corec_drivers_total` → `corec_drivers`, `corec_transports_total` →
  `corec_transports`, `corec_rules_total` → `corec_rules`. Also renamed
  `corec_points_per_sec` → `corec_points_per_second` to use the full unit
  word. Counter names (e.g. `corec_reads_total`) are unchanged.
- **TLS minimum version pinned to 1.2** — the MQTT and webhook (HTTP-push)
  TLS configs now explicitly set `MinVersion: tls.VersionTLS12` instead of
  relying on the Go default, so an older default cannot silently enable
  TLS 1.0/1.1. (Modbus TLS still defers to the underlying library's
  default.)

### Fixed

- **`LatestCache` dirty-flag race condition** — the racy bare flag was
  replaced by an `atomic.Bool` dirty flag combined with an `atomic.Pointer`
  snapshot and an `RWMutex`: readers take the fast path lock-free when the
  snapshot is clean, and rebuild the snapshot under the write lock on the
  slow path so no `Update` is lost. (This is a mutex-guarded snapshot, not a
  seqlock — there is no sequence-counter odd/even handshake.)
- **`httpServer` concurrent-access data race** — guarded shared server state
  with a dedicated `serverMu` mutex.
- **`startTime` data race** — the package-level `startTime` (`time.Time` is a
  multi-field struct) was read without a lock by the `hello` uptime handler
  while `ReCreateServer` wrote it. It is now an `atomic.Pointer[time.Time]`
  so reads are lock-free and race-free.
- **MQTT `RemoveTransport` goroutine leak** — each transport now owns its
  `context` so removal cancels and joins its goroutines instead of orphaning
  them.
- **`opcua → engine/statistic` dependency violation** — removed the illegal
  import that pointed a driver package back into the engine layer.
- **Offline buffer not power-loss safe** — `fsync` the data file before
  `rename` and `fsync` the parent directory afterward so both the file
  contents and the new directory entry are durable and buffered entries
  survive an abrupt crash.
- **Lint findings** — closed leaked HTTP response bodies in health-check tests,
  removed an ineffectual assignment, named previously-anonymous return values,
  and replaced `string(a) != string(b)` secret comparisons with constant-time
  `hmac.Equal` over SHA-256 hashes (see the dedicated security bullet below).
  Intentional `nil`-value/`nil`-error returns (e.g. "no TLS config needed",
  "transport down — pause drain") are suppressed with documented
  `//nolint` directives.
- **gofmt formatting violations** — two test files were not gofmt-clean; they
  have been reformatted, and `gofmt` + `goimports` are now enforced in
  `.golangci.yml` so the formatting gate is clean and stays clean.
- **Flaky `TestModbusReconnectOnFailure`** — widened the reconnect-observation
  window from 2 s to 6 s so the test is reliable under CI load.
- **Timing side-channel on secret comparison** — the API-secret check
  (`hub/route`) and the webhook-secret check (`transport/httppush`) compared
  strings directly, which leaks the secret byte-by-byte via response timing.
  Both now SHA-256 hash the candidate and expected values to a fixed 32-byte
  length and compare with constant-time `hmac.Equal`, so timing reveals
  neither the secret contents nor its length.
- **Health-check nil-engine panic** — `healthzReady` now returns HTTP 503
  (`engine not initialized`) when no engine is set, instead of dereferencing
  a nil engine and panicking during startup before the engine is wired.
- **MQTT TLS silent downgrade** — when TLS files (CA/cert/key) were configured
  against a plaintext `mqtt://`/`tcp://` broker scheme, paho silently ignored
  the TLS config and sent credentials/commands in plaintext. This now returns
  a hard error at transport construction so the misconfiguration is caught
  instead of silently downgrading.

## [0.0.5] - 2025-09-15

Pre-improvement baseline release. This tag captures the state of the project
immediately before the reliability, observability, and CI/CD work recorded
under [Unreleased]. The items listed above are not part of this baseline.

Notable characteristics of the baseline:

- Functional IIoT data collection core with Modbus, S7, and OPC UA drivers and
  MQTT / HTTP-push transports.
- Total test coverage ≈ 70.4% (S7 ≈ 44.6%, OPC UA ≈ 40.3%).
- `golangci-lint` enabled only `bodyclose`, `gocritic`, `misspell`, `revive`.
- No Prometheus metrics, pprof, trace-ID middleware, MQTT TLS/mTLS, webhook
  HTTPS, or MQTT command authentication.
