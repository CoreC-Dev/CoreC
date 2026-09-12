package hub

import (
	"log/slog"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
	"github.com/CoreC-Dev/CoreC/hub/executor"
	"github.com/CoreC-Dev/CoreC/hub/route"
)

// parseDuration returns the parsed duration or 0 if empty/invalid.
func parseDuration(s string) time.Duration {
	if s == "" {
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0
	}
	return d
}

// Start initializes the API routes, executor, and starts the API server.
func Start(engine core.Engine, cfg *core.Config, configPath string) {
	route.SetEngine(engine)
	executor.Init(engine, cfg, configPath)

	apiAddr := cfg.Global.API.Listen
	secret := cfg.Global.API.Secret

	if apiAddr != "" {
		routeCfg := &route.Config{
			Addr:              apiAddr,
			Secret:            secret,
			TLSCert:           cfg.Global.API.TLSCert,
			TLSKey:            cfg.Global.API.TLSKey,
			AllowedOrigins:    cfg.Global.API.AllowedOrigins,
			RateLimitPerSec:   cfg.Global.API.RateLimitPerSec,
			ReadHeaderTimeout: parseDuration(cfg.Global.API.ReadHeaderTimeout),
			ReadTimeout:       parseDuration(cfg.Global.API.ReadTimeout),
			WriteTimeout:      parseDuration(cfg.Global.API.WriteTimeout),
			IdleTimeout:       parseDuration(cfg.Global.API.IdleTimeout),
		}
		route.ReCreateServer(routeCfg)
	}
}

// Stop shuts down the API server.
func Stop() {
	if err := route.CloseServer(); err != nil {
		slog.Error("API server stop error", "error", err)
	} else {
		slog.Info("API server stopped")
	}
}
