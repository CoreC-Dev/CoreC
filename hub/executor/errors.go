package executor

import (
	"errors"

	"github.com/CoreC-Dev/CoreC/hub/route"
)

// ConfigError marks a configuration problem the caller can fix: the
// payload failed to parse or failed validation (audit findings
// CFG-3/HUB-2). The route layer surfaces these as 400 with the actual
// reason, while apply-stage failures keep the masked 500 (they may
// contain internal details such as connection errors).
//
// The route layer type-asserts via the ClientFixable marker method
// instead of importing this package directly: the executor imports
// hub/route for wiring (route.ReloadFunc = Reload), so a reverse import
// would cycle.
type ConfigError struct {
	err error
}

// NewConfigError wraps a parse/validation failure as client-fixable.
func NewConfigError(err error) *ConfigError {
	if err == nil {
		return nil
	}
	return &ConfigError{err: err}
}

// Error implements error, delegating to the wrapped cause.
func (e *ConfigError) Error() string { return e.err.Error() }

// Unwrap exposes the wrapped cause for errors.Is/As chains.
func (e *ConfigError) Unwrap() error { return e.err }

// ClientFixable is the marker the route layer type-asserts: true means
// the request payload itself is at fault and the message is safe to
// return verbatim to the caller.
func (e *ConfigError) ClientFixable() bool { return true }

// classifyResourceError maps removeResource failures to the route's
// HTTP classes (injected as route.ClassifyResourceError to break the
// import cycle): missing name → 404; client-fixable config fallout →
// 400 with the reason; everything else → masked 500.
func classifyResourceError(err error) int {
	// B5: returns the route package's EXPORTED class constants — the two
	// sides of the injected boundary share one declaration, so a rename
	// or renumber is a compile error here instead of a silent 404↔400
	// misroute.
	switch {
	case errors.Is(err, ErrResourceNotFound):
		return route.ResourceErrNotFound
	case errors.As(err, new(*ConfigError)):
		return route.ResourceErrConfig
	default:
		return route.ResourceErrInternal
	}
}
