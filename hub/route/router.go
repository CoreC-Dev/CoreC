package route

import (
	"context"
	"net/http/pprof"

	"github.com/CoreC-Dev/CoreC/common/trace"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

func router(ctx context.Context, secret string, allowedOrigins []string, rateLimitPerSec int, pprofEnabled, metricsDedicated bool) *chi.Mux {
	// Populate the package-level origin allowlist so that WebSocket Accept
	// calls (which do their own Origin check, independent of CORS) honor the
	// same allowed-origins config. Without this, cross-origin WS upgrades
	// are 403'd by coder/websocket's strict default.
	// Use the atomic setter to avoid racing concurrent acceptWS callers (BR-4).
	setWSAllowedOrigins(allowedOrigins)

	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(trace.Middleware)
	r.Use(safeRequestLogger)
	r.Use(middleware.Recoverer)
	r.Use(corsMiddleware(allowedOrigins))
	// httpMetricsMiddleware records method, status, and duration for every
	// request (including unauthenticated /healthz probes) into the
	// package-level httpMetricsCollector, which the /metrics handler exposes
	// to Prometheus. Placed after CORS (so CORS preflight 204s are counted
	// accurately) and before rate limiting so rate-limited (429) and
	// successful requests are both tracked.
	r.Use(httpMetricsMiddleware)
	if rateLimitPerSec > 0 {
		r.Use(rateLimitMiddleware(ctx, rateLimitPerSec))
	}

	r.Get("/", hello)
	r.Get("/version", getVersion)

	// Health endpoints — no auth required (Kubernetes probes don't
	// typically carry auth headers). Liveness checks only that the
	// process is alive; readiness checks that the engine can serve
	// traffic (running status + at least one healthy data path).
	r.Get("/healthz/live", healthzLive)
	r.Get("/healthz/ready", healthzReady)

	r.Group(func(r chi.Router) {
		if secret != "" {
			r.Use(authentication(secret))
		}

		r.Get("/configs", getConfigs)
		r.Get("/configs/raw", getConfigsRaw)
		r.Put("/configs", updateConfigs)
		r.Patch("/configs", patchConfigs)
		r.Post("/configs/validate", validateConfigs)
		// Config change audit trail (audit HUB-4): who changed what,
		// when — previously only a server log line.
		r.Get("/configs/audit", getConfigAudit)

		// Server-side config backups: snapshot copies of the persisted
		// config (auto-created before every dashboard-driven change, plus
		// manual ones) restorable through the full validate → apply →
		// persist chain. Metadata-only listing; content mirrors
		// GET /configs/raw's authenticated-plaintext posture.
		r.Get("/configs/backups", getConfigBackups)
		r.Post("/configs/backups", createConfigBackup)
		r.Get("/configs/backups/{name}", getConfigBackupContent)
		r.Post("/configs/backups/{name}/restore", restoreConfigBackup)

		r.Get("/drivers", getDrivers)
		// Static segment before the {name} wildcard so the paused list
		// is not swallowed by the parameter route (chi prefers static
		// segments; the order also documents the intent).
		r.Get("/drivers/paused", getPausedDrivers)
		r.Get("/drivers/{name}", getDriver)
		r.Get("/drivers/{name}/tags", getDriverTags)
		r.Post("/drivers/test", testDriver)
		// Pause/resume a driver's collection without touching its
		// config (audit RES-4): the scheduler flags exist; these expose
		// them for device-maintenance scenarios.
		r.Post("/drivers/{name}/pause", pauseDriver)
		r.Post("/drivers/{name}/resume", resumeDriver)
		// Resource-level removal (audit HUB-3): splice internally, run
		// the full PUT /configs safety chain.
		r.Delete("/drivers/{name}", deleteResourceDriver)

		r.Get("/transports", getTransports)
		r.Get("/transports/{name}", getTransport)
		r.Post("/transports/{name}/test", testTransport)
		r.Delete("/transports/{name}", deleteResourceTransport)

		r.Get("/tags", getAllTags)
		r.Post("/write", writeTag)
		// Batch writes (roadmap #37): sequential per-command execution;
		// per-command results + counts, audit/DLQ identical to single.
		r.Post("/write/batch", batchWrite)
		r.Get("/write/failed", getFailedWrites)
		r.Get("/write/history", getWriteHistory)

		r.Get("/rules", getRules)
		r.Patch("/rules/disable", disableRule)
		r.Post("/rules/preview", previewRule)
		r.Delete("/rules/{name}", deleteResourceRule)

		// First-class alerts (audit OBS-4/HUB-6): active state +
		// bounded history + ack + real-time lifecycle stream. The
		// legacy /logs stream keeps serving raw log lines.
		r.Get("/alerts", getAlerts)
		r.Post("/alerts/ack", ackAlert)
		r.Get("/alerts/stream", streamAlerts)

		r.Get("/stats", getStats)
		r.Get("/memory", getMemory)

		r.Get("/logs", getLogs)
		r.Get("/traffic", getTraffic)
		r.Get("/tags/stream", streamTags)

		// Observability: Prometheus-format metrics and pprof profiling
		// endpoints. Both live inside the authenticated group so a
		// scraper or profiling client must present the API secret,
		// keeping them off the public surface. When a dedicated
		// metrics server is configured (api.metrics-addr, audit
		// OBS-12), the authenticated copy is suppressed — one
		// exposure path, the credential-free private interface.
		if !metricsDedicated {
			r.Get("/metrics", promMetrics)
		}

		// pprof profiling endpoints. Registered on the main server only
		// when PprofEnabled is true and no separate pprof address is
		// configured. Both are inside the authenticated group so a
		// profiling client must present the API secret.
		if pprofEnabled {
			r.HandleFunc("/debug/pprof/*", pprof.Index)
			r.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
			r.HandleFunc("/debug/pprof/profile", pprof.Profile)
			r.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
			r.HandleFunc("/debug/pprof/trace", pprof.Trace)
		}
	})

	return r
}
