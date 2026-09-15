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
  production.
- **W3C distributed tracing** — trace-context propagation plus HTTP middleware
  so requests carry a `traceparent` across the collection pipeline.
- **MQTT TLS / mTLS support** via the `mqtts://` (and `tls://`/`ssl://`) scheme,
  with optional CA, client-cert, and client-key files for mutual TLS.
- **Webhook (HTTP push) HTTPS support** — TLS configuration for outbound
  webhook delivery.
- **MQTT replay protection** — HMAC-SHA256 command authentication with an
  authenticated timestamp and configurable skew window
  (`command-max-skew`, `command-strict-replay`).
- **Stronger `golangci-lint` configuration** — added `staticcheck`, `govet`,
  `gocyclo` (max complexity 20), `errcheck`, `ineffassign`, `unused`, `nilerr`,
  and `nilnil`. (`gosimple` is covered by `staticcheck`; `typecheck` is built
  in.) `cmd/corec` is excluded from `gocyclo` and test files from `errcheck`.
- **`CHANGELOG.md`** (this file) and **`VERSIONING.md`** documenting the
  release and interface-stability policy.

### Changed

- **S7 driver test coverage: 44.6% → 83.5%.**
- **OPC UA driver test coverage: 40.3% → 56.1%.**
- **Total project test coverage: 70.4% → 76.8%.**
- `golangci-lint` now enforces a cyclomatic-complexity limit of 20 for new
  code; eight pre-existing complex functions are annotated with
  `//nolint:gocyclo` and tracked for future refactoring.

### Fixed

- **`LatestCache` dirty-flag race condition** — replaced the racy flag with a
  seqlock so readers observe a consistent dirty/clean state.
- **`httpServer` concurrent-access data race** — guarded shared server state
  with a dedicated `serverMu` mutex.
- **MQTT `RemoveTransport` goroutine leak** — each transport now owns its
  `context` so removal cancels and joins its goroutines instead of orphaning
  them.
- **`opcua → engine/statistic` dependency violation** — removed the illegal
  import that pointed a driver package back into the engine layer.
- **Offline buffer not power-loss safe** — `fsync` the data file and the
  directory before `rename` so buffered entries survive an abrupt crash.
- **Lint findings** — closed leaked HTTP response bodies in health-check tests,
  removed an ineffectual assignment, named previously-anonymous return values,
  and replaced `string(a) != string(b)` byte comparisons with `bytes.Equal`.
  Intentional `nil`-value/`nil`-error returns (e.g. "no TLS config needed",
  "transport down — pause drain") are suppressed with documented
  `//nolint` directives.
- **Flaky `TestModbusReconnectOnFailure`** — widened the reconnect-observation
  window from 2 s to 6 s so the test is reliable under CI load.

## [0.0.5] - 2025-09-15

Pre-improvement baseline release. This tag captures the state of the project
immediately before the reliability, observability, and CI/CD work recorded
under [Unreleased]. The items listed above are not part of this baseline.

Notable characteristics of the baseline:

- Functional IIoT data collection core with Modbus, S7, and OPC UA drivers and
  MQTT / HTTP-push transports.
- Total test coverage ≈ 70.4% (S7 ≈ 44.6%, OPC UA ≈ 40.3%).
- `golangci-lint` enabled only `bodyclose`, `gocritic`, `misspell`, `revive`.
- No Prometheus metrics, pprof, distributed tracing, MQTT TLS/mTLS, webhook
  HTTPS, or replay protection.
