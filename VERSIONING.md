# Versioning Policy

CoreC follows [Semantic Versioning 2.0.0](https://semver.org/spec/v2.0.0.html).
Given a version `MAJOR.MINOR.PATCH`:

- **MAJOR** is incremented for incompatible API changes. Anything documented as
  stable (see [Stability Tiers](#stability-tiers)) that changes in a
  backward-incompatible way requires a major bump.
- **MINOR** is incremented for backward-compatible new functionality. Within a
  minor version, all stable interfaces keep their signatures and semantics.
- **PATCH** is incremented for backward-compatible bug fixes that do not change
  any public API.

**Current version: `0.0.5`**

> While the major version is `0` (initial development), any minor bump may
> contain breaking changes. We still endeavour to keep the `core` and plugin
> APIs stable across minor bumps during the `0.x` series; see below. Once `1.0`
> is reached, full SemVer guarantees apply.

## Stability Tiers

Not all packages carry the same stability guarantee. Pick the tier that matches
the package you depend on:

### Tier 1 — Stable within a minor version

These are the public contract of CoreC. Their signatures and documented
behaviour will not change in a backward-incompatible way within the same minor
version. Breaking changes require a major version bump.

- **`core` package** — the domain types and port interfaces
  (`Driver`, `Transport`, `RuleEngine`, `DataPoint`, `TagValue`,
  `WriteCommand`, `EngineStats`, etc.). Drivers, transports, and the engine all
  depend on these, so they are the most stability-sensitive surfaces in the
  project.
- **Driver plugin API** — the `Driver` / `DriverConfig` contract and the
  registration helpers in `core` (e.g. `RegisterDriver`) that driver packages
  implement. A driver written against minor `N` continues to load and build
  against any `0.N.*` patch release.
- **Transport plugin API** — the `Transport` / `TransportConfig` contract and
  registration helpers, symmetric to the driver API.
- **Configuration schema** — the YAML keys documented in
  [`config.example.yaml`](./config.example.yaml). New keys may be added in a
  minor release (with sensible defaults); existing keys are not renamed or
  re-typed without a major bump.

### Tier 2 — Internal, no stability guarantee

These packages implement the engine and may change freely between minor
versions (and even patch versions). Do not import them from external code; if
you need a behaviour they expose, file an issue so we can promote a stable
surface for it.

- `engine` (and `engine/statistic`) — the collection engine and scheduler.
- `hub` (and `hub/executor`, `hub/route`) — routing and execution layer.
- `common/*` — shared internal helpers (`util`, `trace`, `observable`).
- `config` — configuration loading and validation internals (the *schema* is
  Tier 1; the Go API of this package is Tier 2).
- `log` — the logging setup package.
- `rule` — the rule-matching implementation (the `core.RuleEngine` *port* is
  Tier 1; this concrete package is Tier 2).
- `cmd/corec` — the command-line entrypoint and its flags.

## Breaking Change Policy

A change is **breaking** if, after it, existing code that compiled and ran
against the previous version no longer compiles or behaves the same against a
Tier 1 surface. Examples:

- Removing or renaming an exported type, function, method, or field in `core`.
- Changing a function/method signature in `core` or a plugin API.
- Changing the documented semantics of a Tier 1 interface (e.g. requiring a
  previously-optional config key, or changing error conditions).
- Renaming, removing, or changing the type of a documented config key.

Breaking changes **must**:

1. Be described under a new `## [MAJOR.0.0]` heading in
   [CHANGELOG.md](./CHANGELOG.md) with an explicit **Removed** / **Changed**
   subsection and, where possible, a migration note.
2. Be accompanied by a deprecation cycle in the prior minor release whenever
   feasible (see [Deprecation Policy](#deprecation-policy)).
3. Update the version constant injected via ldflags
   (`-X main.version=x.y.z` and `-X github.com/CoreC-Dev/CoreC/hub/route.Version=x.y.z`).

Non-breaking additions and fixes go under `## [Unreleased]` and are released
with a minor or patch bump.

## Deprecation Policy

A Tier 1 surface may be **deprecated** (not yet removed) to give users time to
migrate. Deprecated items:

- Remain present and functional for at least one minor release cycle before
  removal in a major release.
- Carry a `// Deprecated:` Go doc comment explaining what to use instead.
- Are listed under **Deprecated** in [CHANGELOG.md](./CHANGELOG.md).
- Continue to pass tests for the entire deprecation window.

When a deprecated item is finally removed, the removal is recorded under
**Removed** in the major-version changelog entry with a pointer to the
replacement introduced during the deprecation window.

## Releasing

1. Move `## [Unreleased]` contents to a new dated `## [x.y.z] - YYYY-MM-DD`
   section in [CHANGELOG.md](./CHANGELOG.md).
2. Update the **Current version** line above and the version injected at build
   time (`-X main.version=x.y.z` and `-X github.com/CoreC-Dev/CoreC/hub/route.Version=x.y.z`).
3. Ensure `go build ./...`, `go test -race -short -count=1 -timeout 120s ./...`, and
   `golangci-lint run --timeout 5m ./...` all pass cleanly.
4. Tag the commit `vx.y.z` and let the release workflow build the binaries.
