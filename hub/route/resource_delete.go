package route

import (
	"log/slog"
	"net/http"
)

// Resource-level DELETE endpoints (audit HUB-3 / roadmap #26): removing
// a single driver/transport/rule previously required the client to
// fetch the full YAML, splice it, and PUT the whole config — hostile to
// API consumers and easy to get wrong (one bad splice corrupts
// everything). These sugar endpoints splice internally and run the SAME
// validate → apply → persist chain as PUT /configs: the pre-change
// backup snapshot, the failure rollback, and the audit entry all apply.
//
// Deletion is a significant operation: the handlers log the caller for
// the audit trail; the endpoint is idempotent-404 for names the active
// config does not carry.

// Injected by hub/executor.Init — the executor imports this package
// for wiring, so these vars break the import cycle the same way
// ReloadFunc does (never the reverse import).
var (
	RemoveDriverFunc    func(name, remote string) error
	RemoveTransportFunc func(name, remote string) error
	RemoveRuleFunc      func(name, remote string) error
	// ClassifyResourceError maps an injected call's error to its HTTP
	// semantics without the route importing the executor's error
	// types: 0 = internal (500, masked), 1 = missing resource (404),
	// 2 = client-fixable config problem (400, verbatim reason).
	ClassifyResourceError func(err error) int
)

// HTTP error-class constants for ClassifyResourceError.
const (
	resourceErrInternal = 0
	resourceErrNotFound = 1
	resourceErrConfig   = 2
)

// deleteResourceDriver handles DELETE /drivers/{name}.
func deleteResourceDriver(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := RemoveDriverFunc(name, r.RemoteAddr); err != nil {
		renderResourceDeleteError(w, r, "driver", name, err)
		return
	}
	slog.Info("driver removed via resource endpoint",
		"method", r.Method, "path", r.URL.Path, "driver", name, "remote", r.RemoteAddr)
	renderNoContent(w)
}

// deleteResourceTransport handles DELETE /transports/{name}.
func deleteResourceTransport(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := RemoveTransportFunc(name, r.RemoteAddr); err != nil {
		renderResourceDeleteError(w, r, "transport", name, err)
		return
	}
	slog.Info("transport removed via resource endpoint",
		"method", r.Method, "path", r.URL.Path, "transport", name, "remote", r.RemoteAddr)
	renderNoContent(w)
}

// deleteResourceRule handles DELETE /rules/{name}.
func deleteResourceRule(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := RemoveRuleFunc(name, r.RemoteAddr); err != nil {
		renderResourceDeleteError(w, r, "rule", name, err)
		return
	}
	slog.Info("rule removed via resource endpoint",
		"method", r.Method, "path", r.URL.Path, "rule", name, "remote", r.RemoteAddr)
	renderNoContent(w)
}

// renderResourceDeleteError maps executor failures to HTTP semantics:
// a missing resource is 404 (stale client state), a config-validation
// failure (e.g. deleting a transport that rules still target) is 400
// with the actionable reason, everything else stays the masked 500.
func renderResourceDeleteError(w http.ResponseWriter, r *http.Request, kind, name string, err error) {
	switch classOf(err) {
	case resourceErrNotFound:
		renderError(w, r, http.StatusNotFound, err.Error())
	case resourceErrConfig:
		renderError(w, r, http.StatusBadRequest, err.Error())
	default:
		slog.Error("resource delete failed",
			"method", r.Method, "path", r.URL.Path,
			"kind", kind, "name", name, "remote", r.RemoteAddr, "error", err)
		renderInternalError(w, r, err)
	}
}

// classOf resolves the injected classifier, defaulting to internal.
func classOf(err error) int {
	if ClassifyResourceError == nil {
		return resourceErrInternal
	}
	return ClassifyResourceError(err)
}
