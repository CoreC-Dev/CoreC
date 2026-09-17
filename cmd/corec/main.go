package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/CoreC-Dev/CoreC/config"
	"github.com/CoreC-Dev/CoreC/core"
	"github.com/CoreC-Dev/CoreC/engine"
	"github.com/CoreC-Dev/CoreC/hub"
	"github.com/CoreC-Dev/CoreC/log"

	// Import all drivers and transports (init registration)
	_ "github.com/CoreC-Dev/CoreC/driver/all"
	_ "github.com/CoreC-Dev/CoreC/transport/all"
)

var (
	version    = "dev" // injected via ldflags: -X main.version=1.0.0
	configPath string
	logLevel   string
)

func main() {
	os.Exit(run())
}

func run() int {
	flag.StringVar(&configPath, "config", "config.yaml", "path to configuration file")
	flag.StringVar(&configPath, "c", "config.yaml", "path to configuration file (shorthand)")
	flag.StringVar(&logLevel, "log-level", "", "override log level (debug, info, warn, error)")
	flag.Parse()

	verStr := version
	if verStr != "dev" {
		verStr = "v" + verStr
	}

	fmt.Printf(`
   ____                  ____ 
  / ___|___  _ __ ___   / ___|
 | |   / _ \| '__/ _ \ | |    
 | |__| (_) | | |  __/ | |___ 
  \____\___/|_|  \___|  \____| %s

  IIoT Data Collection and Distribution Core

`, verStr)

	// Load configuration
	cfg, err := config.Load(configPath)
	if err != nil {
		slog.Error("failed to load config", "path", configPath, "error", err)
		return 1
	}

	// Setup logging
	setupLogging(cfg, logLevel)

	// Print registered drivers and transports
	slog.Info("registered drivers", "types", core.RegisteredDrivers())
	slog.Info("registered transports", "types", core.RegisteredTransports())

	// Create and start engine
	eng := engine.New()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := eng.Start(ctx, cfg); err != nil {
		slog.Error("failed to start engine", "error", err)
		return 1
	}

	// Register alert handler
	eng.OnAlert(func(point core.DataPoint, rule core.Rule) {
		slog.Warn("ALERT",
			"rule", rule.Name(),
			"driver", point.Driver,
			"tag", point.Tag,
			"value", point.Value,
		)
	})

	// Start Control Plane REST API Hub
	hub.Start(eng, cfg, configPath)

	slog.Info("CoreC is running", "config", configPath)

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	sig := <-sigCh
	slog.Info("received signal, shutting down", "signal", sig)

	// Stop API server first to prevent new requests during engine shutdown
	hub.Stop()

	if err := eng.Stop(); err != nil {
		slog.Error("engine stop error", "error", err)
	}

	slog.Info("CoreC shutdown complete")
	return 0
}

func setupLogging(cfg *core.Config, override string) {
	levelStr := cfg.Global.LogLevel
	if override != "" {
		levelStr = override
	}

	var coreLevel = slog.LevelInfo
	if l, ok := log.ParseLevel(levelStr); ok {
		coreLevel = l
	}

	// Log format defaults to "text" when unset; "json" produces structured
	// JSON lines on stdout (the Event bus payload format is unchanged).
	format := cfg.Global.LogFormat
	if format == "" {
		format = "text"
	}

	// Init installs an ObservableHandler as the global slog logger,
	// so ALL slog.Info/slog.Error calls are captured by the log bus.
	log.Init(coreLevel, format)
}
