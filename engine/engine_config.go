package engine

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// applyEngineConfig overrides engine tuning parameters from the global
// config. Zero/empty values fall back to the defaults already set in New().
func (e *CoreCEngine) applyEngineConfig(cfg *core.EngineConfig) {
	if cfg.DataBusSize > 0 {
		e.dataBusSize = cfg.DataBusSize
	}
	if cfg.Workers > 0 {
		e.numWorkers = cfg.Workers
	}
	if d, err := time.ParseDuration(cfg.ShutdownTimeout); err == nil && d > 0 {
		e.shutdownTimeout = d
	}
	if d, err := time.ParseDuration(cfg.ErrorThrottleWindow); err == nil && d > 0 {
		e.errorThrottleWin = d
	}
	if d, err := time.ParseDuration(cfg.DefaultTagInterval); err == nil && d > 0 {
		e.defaultTagInterval = d
	}
	if cfg.OnBadQuality != "" {
		e.badQualityPolicy = parseBadQualityPolicy(cfg.OnBadQuality)
	}
	if d, err := time.ParseDuration(cfg.StaleThreshold); err == nil && d > 0 {
		e.staleThreshold.Store(int64(d))
	}
	if cfg.WriteRetryCount > 0 {
		e.writeRetryCount = cfg.WriteRetryCount
	}
	if cfg.CommandConcurrency > 0 {
		e.commandConcurrency = cfg.CommandConcurrency
	}
	if cfg.HighPriorityWorkers > 0 {
		e.numHighPriorityWorkers = cfg.HighPriorityWorkers
	}
}

// autoFillNodeConfig auto-generates configuration fields that are omitted
// when node auto-discovery is enabled (node.id is set).
//
// Principle: "explicit overrides auto". If a field is already set in the
// config, it is left untouched. Only omitted fields are auto-generated.
//
// Auto-generated fields:
//   - topic-template: "{prefix}/{node-id}/data/{{.Driver}}/{{.Tag}}"
//   - command-topic:  "{prefix}/{node-id}/cmd/#"
//   - parser:         { type: default } (only when data-topic is set)
//   - forward rule:   match ALL → first transport (when no rules exist)
func (e *CoreCEngine) autoFillNodeConfig(config *core.Config) {
	if config.Node.ID == "" {
		return // auto-discovery disabled
	}

	prefix := config.Node.TopicPrefix
	if prefix == "" {
		prefix = "topo"
	}

	for i := range config.Transports {
		tc := &config.Transports[i]
		if tc.Type != "mqtt" {
			continue
		}

		// Auto-fill topic-template (outbound publish topic).
		if _, ok := tc.Settings["topic-template"]; !ok {
			tc.Settings["topic-template"] = fmt.Sprintf("%s/%s/data/{{.Driver}}/{{.Tag}}", prefix, config.Node.ID)
			slog.Debug("auto-filled topic-template", "transport", tc.Name, "node", config.Node.ID)
		}

		// Auto-fill command-topic (for receiving write commands).
		if _, ok := tc.Settings["command-topic"]; !ok {
			tc.Settings["command-topic"] = fmt.Sprintf("%s/%s/cmd/#", prefix, config.Node.ID)
			slog.Debug("auto-filled command-topic", "transport", tc.Name, "node", config.Node.ID)
		}

		// Auto-fill parser for transports with explicit data-topic (third-party inbound).
		if _, hasDataTopic := tc.Settings["data-topic"]; hasDataTopic {
			if _, hasParser := tc.Settings["parser"]; !hasParser {
				tc.Settings["parser"] = map[string]any{"type": "default"}
				slog.Debug("auto-filled parser", "transport", tc.Name, "type", "default")
			}
		}
	}

	// Auto-add a forward-all rule if no rules are configured.
	// This lets collector nodes publish data without manually writing a rule.
	if len(config.Rules) == 0 && len(config.Transports) > 0 {
		config.Rules = []core.RuleConfig{
			{Name: "topo-auto-forward", Match: "ALL", Action: "forward", Target: config.Transports[0].Name},
		}
		slog.Debug("auto-added forward rule", "target", config.Transports[0].Name)
	}
}

// startDiscovery launches the topology auto-discovery module if node config
// is present. It collects the broker URLs from MQTT transports and derives
// the publish endpoint (subscription pattern) from each transport's topic-template.
func (e *CoreCEngine) startDiscovery(config *core.Config) {
	if config.Node.ID == "" {
		return // auto-discovery disabled
	}

	// Collect broker endpoints from MQTT transports.
	brokerMap := make(map[string]*brokerEndpoint) // deduplicate by broker URL
	for _, tc := range config.Transports {
		if tc.Type != "mqtt" {
			continue
		}
		broker, ok := tc.Settings["broker"].(string)
		if !ok || broker == "" {
			continue
		}

		be, exists := brokerMap[broker]
		if !exists {
			be = &brokerEndpoint{Broker: broker}
			brokerMap[broker] = be
		}

		// If this transport has a topic-template (explicit or auto-filled),
		// it's an outbound transport — advertise a publish endpoint.
		if topicTpl, ok := tc.Settings["topic-template"].(string); ok && topicTpl != "" {
			be.Publish = &endpoint{
				Type:  "mqtt",
				Topic: topicTemplateToSubscriptionPattern(topicTpl),
			}
		}

		// HTTP receive endpoint discovery (webhook-addr) is deferred to a
		// future iteration. When implemented, it will be collected from
		// HTTP transports in a separate loop and paired with the broker
		// used for discovery heartbeats.
	}

	if len(brokerMap) == 0 {
		slog.Warn("discovery: node config set but no MQTT transports found, auto-discovery disabled",
			"node", config.Node.ID)
		return
	}

	brokers := make([]brokerEndpoint, 0, len(brokerMap))
	for _, be := range brokerMap {
		brokers = append(brokers, *be)
	}

	e.discovery = NewDiscovery(config.Node, brokers, e.AddTransport)
	if err := e.discovery.Start(e.ctx); err != nil {
		slog.Error("failed to start discovery, continuing without auto-discovery", "error", err)
		e.discovery = nil
	}
}
