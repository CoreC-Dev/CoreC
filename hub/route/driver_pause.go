package route

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/CoreC-Dev/CoreC/core"
)

// pauseResumeRequest is the (empty) body of POST /drivers/{name}/pause
// and POST /drivers/{name}/resume.
type pauseResumeRequest struct{}

// driverPausable is the optional role the pause endpoints type-assert:
// engines without the role answer 501.
type driverPausable interface {
	PauseDriver(name string) error
	ResumeDriver(name string) error
	PausedDrivers() []string
}

// resolveDriverPausable validates that the named driver exists (404)
// and the engine supports pausing (501), returning the role provider.
// Pause state lives in the scheduler, not the driver, so existence is
// checked through the driver registry's status list.
func resolveDriverPausable(w http.ResponseWriter, r *http.Request, name string) (driverPausable, bool) {
	provider, ok := getEngine().(driverPausable)
	if !ok || provider == nil {
		renderError(w, r, http.StatusNotImplemented, "driver pause/resume not supported by this engine")
		return nil, false
	}
	if !driverExists(name) {
		renderError(w, r, http.StatusNotFound, "driver not found: "+name)
		return nil, false
	}
	return provider, true
}

func driverExists(name string) bool {
	var dm core.DriverManager = getEngine()
	for _, d := range dm.ListDrivers() {
		if d.Name == name {
			return true
		}
	}
	return false
}

// pauseDriver suspends the named driver's collection tasks without
// removing them, so "temporarily stop collecting while a device is
// under repair" does not require rewriting the
// whole config — a full Remove+Add round trip.
func pauseDriver(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	provider, ok := resolveDriverPausable(w, r, name)
	if !ok {
		return
	}
	var req pauseResumeRequest
	if r.Body != http.NoBody {
		if err := json.NewDecoder(limitedBody(r).Body).Decode(&req); err != nil {
			renderError(w, r, http.StatusBadRequest, err.Error())
			return
		}
	}
	if err := provider.PauseDriver(name); err != nil {
		renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	slog.Info("driver paused via API",
		"method", r.Method, "path", r.URL.Path, "driver", name, "remote", r.RemoteAddr)
	renderNoContent(w)
}

// resumeDriver clears the pause flag; the driver's tasks resume at
// their next tick.
func resumeDriver(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	provider, ok := resolveDriverPausable(w, r, name)
	if !ok {
		return
	}
	if err := provider.ResumeDriver(name); err != nil {
		renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	slog.Info("driver resumed via API",
		"method", r.Method, "path", r.URL.Path, "driver", name, "remote", r.RemoteAddr)
	renderNoContent(w)
}

// getPausedDrivers serves GET /drivers/paused — the sorted names of
// drivers whose collection is currently suspended.
func getPausedDrivers(w http.ResponseWriter, r *http.Request) {
	provider, ok := getEngine().(driverPausable)
	if !ok || provider == nil {
		renderError(w, r, http.StatusNotImplemented, "driver pause/resume not supported by this engine")
		return
	}
	render(w, r, http.StatusOK, map[string]any{
		"paused": provider.PausedDrivers(),
	})
}
