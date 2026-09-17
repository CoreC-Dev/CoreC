package route

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/CoreC-Dev/CoreC/core"
)

type putConfigRequest struct {
	Path    string `json:"path"`
	Payload string `json:"payload"`
}

// configOverview is a safe, secret-redacted summary of the active configuration
// returned by GET /configs.
type configOverview struct {
	Global     globalOverview `json:"global"`
	Drivers    []entrySummary `json:"drivers"`
	Transports []entrySummary `json:"transports"`
	Rules      []entrySummary `json:"rules"`
}

type globalOverview struct {
	LogLevel string      `json:"log-level"`
	API      apiOverview `json:"api"`
}

type apiOverview struct {
	Listen string `json:"listen"`
	Secret bool   `json:"secret-set"` // true if a secret is configured (value never exposed)
}

type entrySummary struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Action   string `json:"action,omitempty"`
	Priority int    `json:"priority,omitempty"`
}

func getConfigs(w http.ResponseWriter, r *http.Request) {
	if GetConfigFunc != nil {
		cfg := GetConfigFunc()
		if cfg != nil {
			overview := buildConfigOverview(cfg)
			render(w, r, http.StatusOK, overview)
			return
		}
	}
	// Fallback: no config available
	render(w, r, http.StatusOK, map[string]any{"error": "no active configuration"})
}

func buildConfigOverview(cfg *core.Config) configOverview {
	overview := configOverview{
		Global: globalOverview{
			LogLevel: cfg.Global.LogLevel,
			API: apiOverview{
				Listen: cfg.Global.API.Listen,
				Secret: cfg.Global.API.Secret != "",
			},
		},
	}
	for _, d := range cfg.Drivers {
		overview.Drivers = append(overview.Drivers, entrySummary{Name: d.Name, Type: d.Type})
	}
	for _, t := range cfg.Transports {
		overview.Transports = append(overview.Transports, entrySummary{Name: t.Name, Type: t.Type})
	}
	for _, rl := range cfg.Rules {
		overview.Rules = append(overview.Rules, entrySummary{Name: rl.Name, Type: rl.Match, Action: rl.Action, Priority: rl.Priority})
	}
	return overview
}

func updateConfigs(w http.ResponseWriter, r *http.Request) {
	var req putConfigRequest
	if err := json.NewDecoder(limitedBody(r).Body).Decode(&req); err != nil {
		slog.Info("config update rejected",
			"method", r.Method,
			"path", r.URL.Path,
			"remote", r.RemoteAddr,
			"error", err)
		renderError(w, r, http.StatusBadRequest, err.Error())
		return
	}

	if ReloadFunc != nil {
		if err := ReloadFunc(req.Path, req.Payload); err != nil {
			slog.Error("config update failed",
				"method", r.Method,
				"path", r.URL.Path,
				"config_path", req.Path,
				"remote", r.RemoteAddr,
				"error", err)
			renderInternalError(w, r, err)
			return
		}
	}
	slog.Info("config updated",
		"method", r.Method,
		"action", "reload",
		"path", r.URL.Path,
		"config_path", req.Path,
		"remote", r.RemoteAddr)
	renderNoContent(w)
}

func patchConfigs(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	if err := json.NewDecoder(limitedBody(r).Body).Decode(&req); err != nil {
		slog.Info("config patch rejected",
			"method", r.Method,
			"path", r.URL.Path,
			"remote", r.RemoteAddr,
			"error", err)
		renderError(w, r, http.StatusBadRequest, err.Error())
		return
	}

	if PatchFunc != nil {
		if err := PatchFunc(req); err != nil {
			slog.Error("config patch failed",
				"method", r.Method,
				"path", r.URL.Path,
				"remote", r.RemoteAddr,
				"error", err)
			renderError(w, r, http.StatusBadRequest, err.Error())
			return
		}
	}
	slog.Info("config patched",
		"method", r.Method,
		"action", "patch",
		"path", r.URL.Path,
		"remote", r.RemoteAddr,
		"keys", len(req))
	renderNoContent(w)
}
