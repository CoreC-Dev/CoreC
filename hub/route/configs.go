package route

import (
	"encoding/json"
	"fmt"
	"io"
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
	// Fallback: no config available — return 503 Service Unavailable so the
	// client can distinguish "server running but no config loaded" from a
	// successful response with an empty config.
	render(w, r, http.StatusServiceUnavailable, map[string]any{"error": "no active configuration"})
}

// getConfigsRaw handles GET /configs/raw. It returns the FULL active
// configuration as YAML text with all values in plaintext, including secrets
// (api.secret, mqtt/http/driver passwords, webhook-secrets, auth headers).
//
// Unlike GET /configs (a names-only summary with secret-set boolean), this
// exposes the complete config so the Dashboard's Config Center can populate
// its YAML editor with the server's real configuration. The endpoint is
// authenticated (behind the same Bearer-token middleware as all /configs/*
// routes), so only operators with the API secret can retrieve it.
//
// Content-Type is application/yaml because the payload is a YAML document the
// frontend feeds directly into a Monaco YAML editor.
func getConfigsRaw(w http.ResponseWriter, r *http.Request) {
	if GetRawConfigFunc == nil {
		renderInternalError(w, r, fmt.Errorf("raw config endpoint not wired"))
		return
	}
	yamlText, err := GetRawConfigFunc()
	if err != nil {
		slog.Error("failed to produce raw config",
			"method", r.Method,
			"path", r.URL.Path,
			"remote", r.RemoteAddr,
			"error", err)
		renderInternalError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if _, err := io.WriteString(w, yamlText); err != nil {
		slog.Error("failed to write raw config response", "error", err)
	}
}

// validateConfigs handles POST /configs/validate. It performs a dry-run
// validation of the submitted config payload WITHOUT applying it, so the
// Dashboard can surface parse/validation errors before the operator commits a
// PUT /configs.
//
// Request body: {"payload": "<yaml string>"} (same shape as PUT /configs).
// Response: 200 {"valid": true} on success, or 400 {"valid": false, "error":
// "<message>"} on validation failure.
func validateConfigs(w http.ResponseWriter, r *http.Request) {
	var req putConfigRequest
	if err := json.NewDecoder(limitedBody(r).Body).Decode(&req); err != nil {
		slog.Info("config validate rejected",
			"method", r.Method,
			"path", r.URL.Path,
			"remote", r.RemoteAddr,
			"error", err)
		renderError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	if ValidateFunc == nil {
		renderInternalError(w, r, fmt.Errorf("validate endpoint not wired"))
		return
	}
	if err := ValidateFunc(req.Payload); err != nil {
		slog.Info("config validate failed",
			"method", r.Method,
			"path", r.URL.Path,
			"remote", r.RemoteAddr,
			"error", err)
		render(w, r, http.StatusBadRequest, map[string]any{
			"valid": false,
			"error": err.Error(),
		})
		return
	}
	slog.Info("config validate ok",
		"method", r.Method,
		"path", r.URL.Path,
		"remote", r.RemoteAddr)
	render(w, r, http.StatusOK, map[string]any{"valid": true})
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

	if ReloadFunc == nil {
		renderInternalError(w, r, fmt.Errorf("config reload endpoint not wired"))
		return
	}
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

	if PatchFunc == nil {
		renderInternalError(w, r, fmt.Errorf("config patch endpoint not wired"))
		return
	}
	if err := PatchFunc(req); err != nil {
		slog.Error("config patch failed",
			"method", r.Method,
			"path", r.URL.Path,
			"remote", r.RemoteAddr,
			"error", err)
		renderError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	slog.Info("config patched",
		"method", r.Method,
		"action", "patch",
		"path", r.URL.Path,
		"remote", r.RemoteAddr,
		"keys", len(req))
	renderNoContent(w)
}
