package config

// These cases verify that malformed global.engine duration values are
// rejected at validation time (the layer behind both config load and
// POST /configs/validate), while empty values keep the default.

import (
	"strings"
	"testing"

	"github.com/CoreC-Dev/CoreC/core"
)

func TestValidateRejectsMalformedEngineDurations(t *testing.T) {
	cases := []struct {
		name string
		key  string
		set  func(e *core.EngineConfig, v string)
		bad  string
	}{
		{"DefaultTagInterval", "engine.default-tag-interval", func(e *core.EngineConfig, v string) { e.DefaultTagInterval = v }, "30秒"},
		{"ShutdownTimeout", "engine.shutdown-timeout", func(e *core.EngineConfig, v string) { e.ShutdownTimeout = v }, "30 s"},
		{"ErrorThrottleWindow", "engine.error-throttle-window", func(e *core.EngineConfig, v string) { e.ErrorThrottleWindow = v }, "10sec"},
		{"StaleThreshold", "engine.stale-threshold", func(e *core.EngineConfig, v string) { e.StaleThreshold = v }, "2m30"},
		{"WriteMinInterval", "engine.write-min-interval", func(e *core.EngineConfig, v string) { e.WriteMinInterval = v }, "200 ms"},
		{"AlertActiveWindow", "engine.alert-active-window", func(e *core.EngineConfig, v string) { e.AlertActiveWindow = v }, "15мин"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &core.Config{}
			tc.set(&cfg.Global.Engine, tc.bad)

			err := Validate(cfg)
			if err == nil {
				t.Fatalf("%s=%q accepted; the engine would silently fall back to its default", tc.name, tc.bad)
			}
			// The message has to name the field by its YAML key: that is what an
			// operator edits and what an agent reads back through
			// validate_configs. "invalid config" is not a recovery instruction.
			if !strings.Contains(err.Error(), tc.key) {
				t.Errorf("error does not name %s: %v", tc.key, err)
			}
		})
	}

	t.Run("empty keeps the default", func(t *testing.T) {
		cfg := &core.Config{}
		if err := Validate(cfg); err != nil {
			t.Fatalf("unset durations must stay valid -- empty is how the default is requested: %v", err)
		}
	})

	t.Run("valid values pass", func(t *testing.T) {
		cfg := &core.Config{}
		e := &cfg.Global.Engine
		e.DefaultTagInterval = "2s"
		e.ShutdownTimeout = "30s"
		e.ErrorThrottleWindow = "10m"
		e.StaleThreshold = "500ms"
		e.WriteMinInterval = "200ms"
		e.AlertActiveWindow = "1h"
		if err := Validate(cfg); err != nil {
			t.Fatalf("valid durations rejected: %v", err)
		}
	})

	t.Run("non-positive rejected", func(t *testing.T) {
		for _, v := range []string{"-5s", "0s"} {
			cfg := &core.Config{}
			cfg.Global.Engine.DefaultTagInterval = v
			if err := Validate(cfg); err == nil {
				t.Errorf("default-tag-interval=%q accepted; a non-positive interval can never fire", v)
			}
		}
	})
}
