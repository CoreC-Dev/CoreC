package engine

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

func TestTopicTemplateToSubscriptionPattern(t *testing.T) {
	tests := []struct {
		name     string
		template string
		want     string
	}{
		{
			name:     "standard auto-generated",
			template: "topo/edge-A/data/{{.Driver}}/{{.Tag}}",
			want:     "topo/edge-A/data/#",
		},
		{
			name:     "custom with group",
			template: "factory/{{.Driver}}/{{.Group}}/{{.Tag}}",
			want:     "factory/#",
		},
		{
			name:     "custom prefix",
			template: "cloud/{{.Driver}}/{{.Tag}}",
			want:     "cloud/#",
		},
		{
			name:     "no variables",
			template: "static/topic",
			want:     "static/topic/#",
		},
		{
			name:     "variable at start",
			template: "{{.Driver}}/{{.Tag}}",
			want:     "#",
		},
		{
			name:     "single level prefix",
			template: "edge/{{.Driver}}/{{.Tag}}",
			want:     "edge/#",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := topicTemplateToSubscriptionPattern(tt.template)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNodeInfoMarshalUnmarshal(t *testing.T) {
	info := nodeInfo{
		ID:        "edge-A",
		Role:      "collector",
		Subscribe: []string{},
		Publish: &endpoint{
			Type:  "mqtt",
			Topic: "topo/edge-A/data/#",
		},
		Timestamp: time.Now().Unix(),
	}

	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded nodeInfo
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.ID != info.ID {
		t.Errorf("ID: got %q, want %q", decoded.ID, info.ID)
	}
	if decoded.Role != info.Role {
		t.Errorf("Role: got %q, want %q", decoded.Role, info.Role)
	}
	if decoded.Publish == nil {
		t.Fatal("Publish is nil")
	}
	if decoded.Publish.Type != "mqtt" {
		t.Errorf("Publish.Type: got %q, want %q", decoded.Publish.Type, "mqtt")
	}
	if decoded.Publish.Topic != "topo/edge-A/data/#" {
		t.Errorf("Publish.Topic: got %q, want %q", decoded.Publish.Topic, "topo/edge-A/data/#")
	}
}

func TestNodeInfoOmitsNilEndpoints(t *testing.T) {
	info := nodeInfo{
		ID:        "relay-B",
		Role:      "relay",
		Subscribe: []string{"edge-A"},
		// Publish and Receive are nil
		Timestamp: 1234567890,
	}

	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Verify "publish" and "receive" are omitted from JSON.
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	if _, exists := raw["publish"]; exists {
		t.Error("publish should be omitted when nil")
	}
	if _, exists := raw["receive"]; exists {
		t.Error("receive should be omitted when nil")
	}
}

func TestAutoFillNodeConfig(t *testing.T) {
	e := &CoreCEngine{}

	t.Run("no node config - no changes", func(t *testing.T) {
		config := &core.Config{
			Transports: []core.TransportConfig{
				{Name: "mqtt", Type: "mqtt", Settings: map[string]any{"broker": "tcp://x:1883"}},
			},
		}
		e.autoFillNodeConfig(config)

		// topic-template should NOT be auto-filled (no node.id)
		if _, ok := config.Transports[0].Settings["topic-template"]; ok {
			t.Error("topic-template should not be set when node.id is empty")
		}
		if len(config.Rules) != 0 {
			t.Error("rules should not be auto-added when node.id is empty")
		}
	})

	t.Run("auto-fills topic-template and command-topic", func(t *testing.T) {
		config := &core.Config{
			Node: core.NodeConfig{ID: "edge-A", Role: "collector"},
			Transports: []core.TransportConfig{
				{Name: "mqtt", Type: "mqtt", Settings: map[string]any{"broker": "tcp://x:1883"}},
			},
		}
		e.autoFillNodeConfig(config)

		got := config.Transports[0].Settings["topic-template"]
		want := "topo/edge-A/data/{{.Driver}}/{{.Tag}}"
		if got != want {
			t.Errorf("topic-template: got %q, want %q", got, want)
		}

		gotCmd := config.Transports[0].Settings["command-topic"]
		wantCmd := "topo/edge-A/cmd/#"
		if gotCmd != wantCmd {
			t.Errorf("command-topic: got %q, want %q", gotCmd, wantCmd)
		}
	})

	t.Run("preserves explicit topic-template", func(t *testing.T) {
		custom := "factory/{{.Driver}}/{{.Group}}/{{.Tag}}"
		config := &core.Config{
			Node: core.NodeConfig{ID: "edge-A", Role: "collector"},
			Transports: []core.TransportConfig{
				{Name: "mqtt", Type: "mqtt", Settings: map[string]any{
					"broker":        "tcp://x:1883",
					"topic-template": custom,
				}},
			},
		}
		e.autoFillNodeConfig(config)

		got := config.Transports[0].Settings["topic-template"]
		if got != custom {
			t.Errorf("topic-template: got %q, want %q (should be preserved)", got, custom)
		}
	})

	t.Run("preserves explicit command-topic", func(t *testing.T) {
		custom := "custom/commands/#"
		config := &core.Config{
			Node: core.NodeConfig{ID: "edge-A", Role: "collector"},
			Transports: []core.TransportConfig{
				{Name: "mqtt", Type: "mqtt", Settings: map[string]any{
					"broker":       "tcp://x:1883",
					"command-topic": custom,
				}},
			},
		}
		e.autoFillNodeConfig(config)

		got := config.Transports[0].Settings["command-topic"]
		if got != custom {
			t.Errorf("command-topic: got %q, want %q (should be preserved)", got, custom)
		}
	})

	t.Run("auto-fills parser for data-topic", func(t *testing.T) {
		config := &core.Config{
			Node: core.NodeConfig{ID: "relay-B", Role: "relay"},
			Transports: []core.TransportConfig{
				{Name: "mqtt", Type: "mqtt", Settings: map[string]any{
					"broker":    "tcp://x:1883",
					"data-topic": "upstream/#",
				}},
			},
		}
		e.autoFillNodeConfig(config)

		parser, ok := config.Transports[0].Settings["parser"]
		if !ok {
			t.Fatal("parser should be auto-filled for data-topic")
		}
		parserMap, ok := parser.(map[string]any)
		if !ok {
			t.Fatalf("parser should be a map, got %T", parser)
		}
		if parserMap["type"] != "default" {
			t.Errorf("parser type: got %q, want %q", parserMap["type"], "default")
		}
	})

	t.Run("preserves explicit parser", func(t *testing.T) {
		customParser := map[string]any{"type": "jsonpath", "driver": "lora"}
		config := &core.Config{
			Node: core.NodeConfig{ID: "gateway", Role: "relay"},
			Transports: []core.TransportConfig{
				{Name: "mqtt", Type: "mqtt", Settings: map[string]any{
					"broker":    "tcp://x:1883",
					"data-topic": "lora/+/up",
					"parser":    customParser,
				}},
			},
		}
		e.autoFillNodeConfig(config)

		got := config.Transports[0].Settings["parser"]
		parserMap, ok := got.(map[string]any)
		if !ok {
			t.Fatalf("parser should be a map, got %T", got)
		}
		if parserMap["type"] != "jsonpath" {
			t.Errorf("parser type: got %q, want %q", parserMap["type"], "jsonpath")
		}
	})

	t.Run("auto-adds forward rule when no rules", func(t *testing.T) {
		config := &core.Config{
			Node: core.NodeConfig{ID: "edge-A", Role: "collector"},
			Transports: []core.TransportConfig{
				{Name: "mqtt", Type: "mqtt", Settings: map[string]any{"broker": "tcp://x:1883"}},
			},
		}
		e.autoFillNodeConfig(config)

		if len(config.Rules) != 1 {
			t.Fatalf("expected 1 auto rule, got %d", len(config.Rules))
		}
		if config.Rules[0].Name != "topo-auto-forward" {
			t.Errorf("rule name: got %q, want %q", config.Rules[0].Name, "topo-auto-forward")
		}
		if config.Rules[0].Target != "mqtt" {
			t.Errorf("rule target: got %q, want %q", config.Rules[0].Target, "mqtt")
		}
	})

	t.Run("preserves existing rules", func(t *testing.T) {
		config := &core.Config{
			Node: core.NodeConfig{ID: "edge-A", Role: "collector"},
			Transports: []core.TransportConfig{
				{Name: "mqtt", Type: "mqtt", Settings: map[string]any{"broker": "tcp://x:1883"}},
			},
			Rules: []core.RuleConfig{
				{Name: "custom-rule", Match: "ALL", Action: "forward", Target: "mqtt"},
			},
		}
		e.autoFillNodeConfig(config)

		if len(config.Rules) != 1 {
			t.Fatalf("expected 1 rule (the original), got %d", len(config.Rules))
		}
		if config.Rules[0].Name != "custom-rule" {
			t.Errorf("rule name: got %q, want %q", config.Rules[0].Name, "custom-rule")
		}
	})

	t.Run("custom topic prefix", func(t *testing.T) {
		config := &core.Config{
			Node: core.NodeConfig{ID: "edge-A", Role: "collector", TopicPrefix: "factory"},
			Transports: []core.TransportConfig{
				{Name: "mqtt", Type: "mqtt", Settings: map[string]any{"broker": "tcp://x:1883"}},
			},
		}
		e.autoFillNodeConfig(config)

		got := config.Transports[0].Settings["topic-template"]
		want := "factory/edge-A/data/{{.Driver}}/{{.Tag}}"
		if got != want {
			t.Errorf("topic-template: got %q, want %q", got, want)
		}
	})

	t.Run("skips non-mqtt transports", func(t *testing.T) {
		config := &core.Config{
			Node: core.NodeConfig{ID: "edge-A", Role: "collector"},
			Transports: []core.TransportConfig{
				{Name: "http", Type: "http", Settings: map[string]any{"url": "http://x:9091/ing!ingest"}},
			},
		}
		e.autoFillNodeConfig(config)

		if _, ok := config.Transports[0].Settings["topic-template"]; ok {
			t.Error("topic-template should not be set for non-mqtt transport")
		}
	})
}

func TestSanitizeBroker(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"tcp://broker:1883", "tcp___broker_1883"},
		{"tcp://192.168.1.1:1883", "tcp___192-168-1-1_1883"},
		{"ws://broker:9001", "ws___broker_9001"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizeBroker(tt.input)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
