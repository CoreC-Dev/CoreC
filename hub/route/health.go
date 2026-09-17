package route

import (
	"fmt"
	"net/http"

	"github.com/CoreC-Dev/CoreC/core"
)

// healthzLive is the Kubernetes liveness probe handler.
//
// Liveness checks only that the process is running and able to respond to
// HTTP requests — it is a simple "I'm alive" signal. A failing liveness
// probe causes Kubernetes to restart the pod. It must never depend on
// external state (drivers, transports, broker connectivity) because a
// transient downstream outage should not trigger a process restart.
//
// This endpoint is registered OUTSIDE the auth group so that Kubernetes
// probes can reach it without presenting an API secret.
func healthzLive(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"alive"}`))
}

// componentState is the per-component summary included in a not-ready
// readiness response. It deliberately exposes only the name and a boolean
// connected flag — no counters, error strings, or addresses — so the
// readiness probe stays cheap to parse and does not leak operational
// details to an unauthenticated probe caller.
type componentState struct {
	Name      string `json:"name"`
	Connected bool   `json:"connected"`
}

// healthComponents groups driver and transport states for the not-ready
// readiness response. Both slices are always present (possibly empty) in
// a not-ready response so consumers can rely on a stable shape.
type healthComponents struct {
	Drivers    []componentState `json:"drivers"`
	Transports []componentState `json:"transports"`
}

// notReadyHealthResponse is the JSON body returned by healthzReady when the
// engine is not ready. It augments the status with a human-readable reason
// and per-component connectivity so an operator can see, at a glance,
// which driver or transport is failing without scraping /drivers or
// /transports. When the engine is ready, the probe keeps returning the
// minimal {"status":"ready"} body so high-frequency probes stay small.
type notReadyHealthResponse struct {
	Status     string           `json:"status"`
	Reason     string           `json:"reason"`
	Components healthComponents `json:"components"`
}

// healthzReady is the Kubernetes readiness probe handler.
//
// Readiness checks whether the engine is ready to serve real traffic. A
// failing readiness probe causes Kubernetes to remove the pod from the
// service's load-balancer (it stops routing requests to it) but does NOT
// restart the pod. This is the right place to check that the engine is
// running and that at least one data path is healthy.
//
// The engine is considered ready when ALL of the following hold:
//   - Engine status is "running" (not stopped/suspended).
//   - If any drivers are configured, at least one is connected. With zero
//     configured drivers the check is vacuously satisfied.
//   - If any transports are configured, at least one is connected. With
//     zero configured transports the check is vacuously satisfied.
//
// When ready, the response is the minimal {"status":"ready"} body so that
// high-frequency probes stay cheap. When not ready, the response includes
// a "reason" and a "components" object listing every driver and transport
// with its connected flag, letting an operator pinpoint the failing path
// without a second round-trip.
//
// This endpoint is registered OUTSIDE the auth group so that Kubernetes
// probes can reach it without presenting an API secret.
func healthzReady(w http.ResponseWriter, r *http.Request) {
	eng := getEngine()
	if eng == nil {
		writeHealthNotReady(w, r, "engine not initialized", nil, nil)
		return
	}

	stats := eng.Stats()
	drivers := eng.ListDrivers()
	transports := eng.ListTransports()

	if reason := readinessReason(stats, drivers, transports); reason != "" {
		writeHealthNotReady(w, r, reason, drivers, transports)
		return
	}

	// Ready: keep the response minimal for high-frequency probes.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ready"}`))
}

// readinessReason returns a non-empty human-readable reason when the engine
// is not ready to serve traffic, or "" when it is ready. The readiness
// rules mirror the healthzReady contract documented above. Centralising
// the decision here ensures the emitted reason matches the exact condition
// that caused the probe to fail.
func readinessReason(stats core.EngineStats, drivers []core.DriverStatus, transports []core.TransportStatus) string {
	if stats.Status != core.EngineStatusRunning {
		return fmt.Sprintf("engine not running (status: %q)", stats.Status)
	}

	if stats.Drivers > 0 && !anyConnectedDriver(drivers) {
		return "no drivers connected"
	}

	if stats.Transports > 0 && !anyConnectedTransport(transports) {
		return "no transports connected"
	}

	return ""
}

// anyConnectedDriver reports whether at least one driver is connected.
func anyConnectedDriver(drivers []core.DriverStatus) bool {
	for _, d := range drivers {
		if d.State == core.StateConnected {
			return true
		}
	}
	return false
}

// anyConnectedTransport reports whether at least one transport is connected.
func anyConnectedTransport(transports []core.TransportStatus) bool {
	for _, t := range transports {
		if t.State == core.StateConnected {
			return true
		}
	}
	return false
}

// writeHealthNotReady writes a 503 not-ready response with a reason and
// per-component connectivity details. nil driver/transport slices are
// rendered as empty arrays so the response shape is always stable.
func writeHealthNotReady(w http.ResponseWriter, r *http.Request, reason string, drivers []core.DriverStatus, transports []core.TransportStatus) {
	resp := notReadyHealthResponse{
		Status: "not_ready",
		Reason: reason,
		Components: healthComponents{
			Drivers:    make([]componentState, 0, len(drivers)),
			Transports: make([]componentState, 0, len(transports)),
		},
	}
	for _, d := range drivers {
		resp.Components.Drivers = append(resp.Components.Drivers, componentState{
			Name:      d.Name,
			Connected: d.State == core.StateConnected,
		})
	}
	for _, t := range transports {
		resp.Components.Transports = append(resp.Components.Transports, componentState{
			Name:      t.Name,
			Connected: t.State == core.StateConnected,
		})
	}
	render(w, r, http.StatusServiceUnavailable, resp)
}
