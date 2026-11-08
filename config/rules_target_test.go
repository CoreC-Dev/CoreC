package config

import (
	"strings"
	"testing"

	"github.com/CoreC-Dev/CoreC/core"
)

// TestValidateForwardRequiresTarget pins the P1-2 half that config.Validate
// can enforce without importing the rule engine: forward/mirror with no
// destination is accepted by the old validator but silently drops every
// matching point at runtime.
func TestValidateForwardRequiresTarget(t *testing.T) {
	cfg := &core.Config{
		Rules: []core.RuleConfig{{Name: "r", Match: "ALL", Action: "forward"}},
	}
	err := Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "requires target") {
		t.Fatalf("forward without target: err = %v, want 'requires target'", err)
	}

	cfg.Rules[0].Target = "mqtt1"
	cfg.Transports = []core.TransportConfig{{
		Name: "mqtt1", Type: "mqtt",
		Settings: map[string]any{"broker": "tcp://localhost:1883"},
	}}
	if err := Validate(cfg); err != nil {
		t.Fatalf("forward with target rejected: %v", err)
	}
}
