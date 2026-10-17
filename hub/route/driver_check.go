package route

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
	"github.com/CoreC-Dev/CoreC/log"
)

// driverTestTimeout is the hard upper bound for a single connectivity
// test (temporary driver init + start). A driver that has not reported
// an outcome within this window is reported as a failed test, so a
// hanging device can never pin the API handler.
const driverTestTimeout = 5 * time.Second

// testDriverRequest is the request body of POST /drivers/test. The json
// tags are defined HERE, explicitly: core.DriverConfig carries only yaml
// tags (it is the YAML config contract), so the API layer must not
// deserialize into it directly.
type testDriverRequest struct {
	// Type is the registered driver type to instantiate, e.g. "modbus-tcp".
	Type string `json:"type"`
	// Settings are the protocol-specific connection parameters, exactly
	// as they would appear under drivers[].settings in the YAML config.
	Settings map[string]any `json:"settings"`
	// Name labels the temporary instance in logs; defaults to
	// "driver-test". Informational only — nothing is registered.
	Name string `json:"name"`
}

// driverTestResult carries the outcome of the temporary instance's
// lifecycle run from the worker goroutine to the handler.
type driverTestResult struct {
	connected bool
	err       string
}

// loggerInjector and metricsInjector are the optional injection
// interfaces drivers may implement (mirroring engine/driver_manager.go).
// The temporary instance gets the same wiring a configured driver would.
type loggerInjector interface {
	SetLogger(core.Logger)
}

type metricsInjector interface {
	SetMetrics(core.Metrics)
}

// testDriver creates a THROWAWAY driver instance from the request, drives
// it through the same lifecycle a configured driver follows (create →
// init → start), measures how long the connection attempt took, and
// always tears the instance down. The instance is never registered with
// the engine or the plugin registry, and the request is never persisted
// to the config — this is a pure connectivity probe for validating a
// driver config before saving it.
//
// Both outcomes are 200: a completed test with connected=false is a
// successful test run, not a server error.
func testDriver(w http.ResponseWriter, r *http.Request) {
	var req testDriverRequest
	if err := json.NewDecoder(limitedBody(r).Body).Decode(&req); err != nil {
		slog.Info("driver test rejected",
			"method", r.Method,
			"path", r.URL.Path,
			"remote", r.RemoteAddr,
			"error", err)
		renderError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	if req.Type == "" {
		renderError(w, r, http.StatusBadRequest, "type is required")
		return
	}
	if !slices.Contains(core.RegisteredDrivers(), req.Type) {
		renderError(w, r, http.StatusBadRequest, "unknown driver type: "+req.Type)
		return
	}

	name := req.Name
	if name == "" {
		name = "driver-test"
	}
	cfg := core.DriverConfig{
		Name:     name,
		Type:     req.Type,
		Settings: req.Settings,
	}

	driver, err := core.CreateDriver(cfg)
	if err != nil {
		slog.Info("driver test rejected",
			"method", r.Method,
			"path", r.URL.Path,
			"driver_type", req.Type,
			"remote", r.RemoteAddr,
			"error", err)
		renderError(w, r, http.StatusBadRequest, err.Error())
		return
	}

	// Optional wiring, mirroring engine.AddDriver: drivers that support
	// SetLogger/SetMetrics get the same backends a configured instance
	// would; others keep their internal defaults.
	if li, ok := driver.(loggerInjector); ok {
		li.SetLogger(log.DefaultLogger())
	}
	if mi, ok := driver.(metricsInjector); ok {
		mi.SetMetrics(core.NoopMetrics{})
	}

	// Hard timeout around the whole lifecycle so a hanging device cannot
	// pin the handler. Parented on r.Context(): a disconnected client
	// also aborts the attempt instead of holding device I/O open.
	ctx, cancel := context.WithTimeout(r.Context(), driverTestTimeout)
	defer cancel()

	start := time.Now()
	var result driverTestResult
	select {
	case result = <-runDriverTestLifecycle(ctx, driver, cfg):
	case <-ctx.Done():
		// The lifecycle goroutine keeps running in the background and
		// stops the instance when the blocked call returns; the handler
		// must not wait for it.
		result = driverTestResult{err: "driver test timed out after " + driverTestTimeout.String()}
		slog.Warn("driver test timed out",
			"method", r.Method,
			"path", r.URL.Path,
			"driver_type", req.Type,
			"timeout", driverTestTimeout)
	}
	latencyMs := time.Since(start).Milliseconds()

	if result.connected {
		slog.Info("driver test connected",
			"method", r.Method,
			"path", r.URL.Path,
			"driver_type", req.Type,
			"latency_ms", latencyMs,
			"remote", r.RemoteAddr)
		render(w, r, http.StatusOK, map[string]any{
			"ok":          true,
			"connected":   true,
			"latency_ms":  latencyMs,
			"driver_type": req.Type,
		})
		return
	}

	slog.Info("driver test failed",
		"method", r.Method,
		"path", r.URL.Path,
		"driver_type", req.Type,
		"latency_ms", latencyMs,
		"remote", r.RemoteAddr,
		"error", result.err)
	render(w, r, http.StatusOK, map[string]any{
		"ok":          false,
		"connected":   false,
		"error":       result.err,
		"latency_ms":  latencyMs,
		"driver_type": req.Type,
	})
}

// runDriverTestLifecycle drives the temporary instance through the same
// lifecycle the engine uses when adding a driver (init → start) and
// always stops it afterwards. The outcome is delivered on the returned
// buffered channel the moment it is known — before teardown — so the
// measured latency covers the connection attempt, not the cleanup. If the
// caller's deadline fires first, it returns immediately while this
// goroutine finishes its cleanup on its own.
func runDriverTestLifecycle(ctx context.Context, driver core.Driver, cfg core.DriverConfig) <-chan driverTestResult {
	ch := make(chan driverTestResult, 1)
	go func() {
		// Stop on every path — success, failure, and timeout alike — so
		// no connection or device handle is ever leaked.
		defer stopDriverTestInstance(driver, cfg.Name)
		result := driverTestResult{}
		if err := driver.Init(ctx, cfg); err != nil {
			result.err = err.Error()
		} else if err := driver.Start(ctx); err != nil {
			result.err = err.Error()
		} else {
			result.connected = true
		}
		ch <- result
	}()
	return ch
}

// stopDriverTestInstance stops the temporary instance with a bounded
// wait. Stop() is contractually re-entrant and non-blocking, but a
// misbehaving driver must not pin the lifecycle goroutine (and its device
// handles) forever.
func stopDriverTestInstance(driver core.Driver, name string) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := driver.Stop(); err != nil {
			slog.Debug("driver test stop failed", "name", name, "error", err)
		}
	}()
	select {
	case <-done:
	case <-time.After(driverTestTimeout):
		slog.Warn("driver test: Stop() did not return in time; instance may leak", "name", name)
	}
}
