package route

import (
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
// This endpoint is registered OUTSIDE the auth group so that Kubernetes
// probes can reach it without presenting an API secret.
func healthzReady(w http.ResponseWriter, r *http.Request) {
	eng := getEngine()
	if eng == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"not_ready","reason":"engine not initialized"}`))
		return
	}
	stats := eng.Stats()

	ready := stats.Status == core.EngineStatusRunning

	// If drivers are configured, at least one should be connected.
	if ready && stats.Drivers > 0 {
		connectedDrivers := 0
		for _, d := range eng.ListDrivers() {
			if d.State == core.StateConnected {
				connectedDrivers++
			}
		}
		if connectedDrivers == 0 {
			ready = false
		}
	}

	// If transports are configured, at least one should be connected.
	if ready && stats.Transports > 0 {
		connectedTransports := 0
		for _, t := range eng.ListTransports() {
			if t.State == core.StateConnected {
				connectedTransports++
			}
		}
		if connectedTransports == 0 {
			ready = false
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if ready {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"not_ready"}`))
	}
}
