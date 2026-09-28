package config

import (
	"strings"
	"testing"

	"github.com/CoreC-Dev/CoreC/core"
	"github.com/goccy/go-yaml"
)

// secretedConfig builds a representative config carrying every kind of secret
// the redaction/merge must handle: api.secret, mqtt credentials, http webhook
// secret + auth headers, and a driver password. Non-secret fields (broker,
// host, topic, port) are included so tests can assert they survive untouched.
func secretedConfig() *core.Config {
	return &core.Config{
		Node: core.NodeConfig{ID: "edge-1", Role: "collector"},
		Global: core.GlobalConfig{
			LogLevel: "info",
			API: core.APIConfig{
				Listen: "0.0.0.0:9090",
				Secret: "super-secret-api-token-2026",
			},
		},
		Transports: []core.TransportConfig{
			{
				Name: "mqtt-out",
				Type: "mqtt",
				Settings: map[string]any{
					"broker":               "ssl://broker.example:8883",
					"client-id":            "edge-1-pub",
					"topic-template":       "topo/edge-1/data/{driver}/{tag}",
					"username":             "mqtt-user",
					"password":             "mqtt-p@ssw0rd-2026",
					"command-secret":       "cmd-shared-secret",
					"command-forward-secret": "fwd-shared-secret",
					"tls-ca-file":          "/etc/corec/ca.pem",
					"tls-cert-file":        "/etc/corec/cert.pem",
					"tls-key-file":         "/etc/corec/key.pem",
					"retained":             true,
				},
			},
			{
				Name: "http-push",
				Type: "http",
				Settings: map[string]any{
					"url":            "https://sink.example/ingest",
					"method":         "POST",
					"timeout":        "30s",
					"webhook-addr":   "0.0.0.0:9091",
					"webhook-path":   "/data",
					"webhook-secret": "wh-secret-abc-123",
					"headers": map[string]any{
						"Authorization": "Bearer ingest-token-xyz",
						"X-API-Key":     "api-key-987",
						"Content-Type":  "application/json",
					},
				},
			},
		},
		Drivers: []core.DriverConfig{
			{
				Name: "plc-1",
				Type: "modbus-tcp",
				Settings: map[string]any{
					"host":     "192.168.10.5",
					"port":     502,
					"timeout":  "5s",
					"username": "mbuser",
					"password": "mb-p@ss-555",
				},
				Tags: []core.TagConfig{{Name: "temp", Address: "40001", Type: "float32"}},
			},
		},
		Rules: []core.RuleConfig{{Name: "fwd", Match: "ALL", Action: "forward"}},
	}
}

// allSecretValues returns the set of real secret values planted in
// secretedConfig(), so a test can assert NONE of them survive a redact.
func allSecretValues() []string {
	return []string{
		"super-secret-api-token-2026",
		"mqtt-user", "mqtt-p@ssw0rd-2026",
		"cmd-shared-secret", "fwd-shared-secret",
		"wh-secret-abc-123",
		"Bearer ingest-token-xyz", "api-key-987",
		"mbuser", "mb-p@ss-555",
	}
}

// ─── Redaction: no secret leaks ───────────────────────────────────────

func TestRedact_ReplacesAllSecretsWithSentinel(t *testing.T) {
	orig := secretedConfig()
	redacted, err := Redact(orig)
	if err != nil {
		t.Fatalf("Redact returned error: %v", err)
	}
	if redacted == nil {
		t.Fatal("Redact returned nil config")
	}

	// Every planted secret value must be gone from the redacted config.
	// Serialize to YAML and scan: no real secret string may appear anywhere.
	data, err := yaml.Marshal(redacted)
	if err != nil {
		t.Fatalf("marshal redacted: %v", err)
	}
	out := string(data)
	for _, secret := range allSecretValues() {
		if strings.Contains(out, secret) {
			t.Errorf("redacted config still contains secret %q (red-team: data leak)", secret)
		}
	}
}

func TestRedact_SentinelAppearsForPresentSecrets(t *testing.T) {
	orig := secretedConfig()
	redacted, _ := Redact(orig)
	data, _ := yaml.Marshal(redacted)
	out := string(data)

	// Each secret FIELD must show the sentinel, proving presence is visible.
	wantSentinels := []string{
		"secret: \"***\"",        // api.secret
		"username: \"***\"",      // mqtt username (also driver)
		"password: \"***\"",      // mqtt password
		"command-secret: \"***\"", "command-forward-secret: \"***\"",
		"webhook-secret: \"***\"",
	}
	for _, w := range wantSentinels {
		if !strings.Contains(out, w) {
			t.Errorf("redacted config missing sentinel %q (operator can't see secret presence)", w)
		}
	}
}

func TestRedact_HeadersKeysPreservedValuesRedacted(t *testing.T) {
	orig := secretedConfig()
	redacted, _ := Redact(orig)
	data, _ := yaml.Marshal(redacted)
	out := string(data)

	// Header keys must remain so the operator sees which headers are set.
	for _, key := range []string{"Authorization", "X-API-Key", "Content-Type"} {
		if !strings.Contains(out, key) {
			t.Errorf("redacted headers lost key %q", key)
		}
	}
	// Header values must be redacted.
	for _, val := range []string{"Bearer ingest-token-xyz", "api-key-987", "application/json"} {
		if strings.Contains(out, val) {
			t.Errorf("redacted headers leaked value %q", val)
		}
	}
}

func TestRedact_NonSecretFieldsUntouched(t *testing.T) {
	orig := secretedConfig()
	redacted, _ := Redact(orig)
	data, _ := yaml.Marshal(redacted)
	out := string(data)

	// Non-secret config must round-trip intact so the editor shows real config.
	for _, want := range []string{
		"ssl://broker.example:8883",
		"edge-1-pub",
		"https://sink.example/ingest",
		"0.0.0.0:9091",
		"192.168.10.5",
		"forward",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("redacted config dropped non-secret value %q", want)
		}
	}
}

func TestRedact_TLSFilePathsNotRedacted(t *testing.T) {
	orig := secretedConfig()
	redacted, _ := Redact(orig)
	data, _ := yaml.Marshal(redacted)
	out := string(data)

	// File paths are not credentials; redacting them is noise. They must survive.
	for _, path := range []string{"/etc/corec/ca.pem", "/etc/corec/cert.pem", "/etc/corec/key.pem"} {
		if !strings.Contains(out, path) {
			t.Errorf("redacted config wrongly redacted TLS path %q", path)
		}
	}
}

func TestRedact_DoesNotMutateOriginal(t *testing.T) {
	orig := secretedConfig()
	origSecret := orig.Global.API.Secret
	origPwd := orig.Transports[0].Settings["password"].(string)
	origHdr := orig.Transports[1].Settings["headers"].(map[string]any)["Authorization"].(string)

	_, err := Redact(orig)
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}

	// The live config (currentCfg) must be untouched — redaction is read-only.
	if orig.Global.API.Secret != origSecret {
		t.Errorf("Redact mutated original api.secret: got %q", orig.Global.API.Secret)
	}
	if orig.Transports[0].Settings["password"].(string) != origPwd {
		t.Errorf("Redact mutated original transport password")
	}
	if orig.Transports[1].Settings["headers"].(map[string]any)["Authorization"].(string) != origHdr {
		t.Errorf("Redact mutated original header value")
	}
}

func TestRedact_EmptySecretStaysEmpty(t *testing.T) {
	cfg := &core.Config{
		Global: core.GlobalConfig{API: core.APIConfig{Listen: ":9090", Secret: ""}},
		Transports: []core.TransportConfig{{
			Name: "t", Type: "mqtt",
			Settings: map[string]any{"broker": "tcp://x:1883"}, // no password configured
		}},
		Rules: []core.RuleConfig{{Name: "r", Match: "ALL", Action: "forward"}},
	}
	redacted, _ := Redact(cfg)
	// Empty api.secret must NOT become "***" (merge must be able to tell
	// "no secret" from "secret present, redacted").
	if redacted.Global.API.Secret != "" {
		t.Errorf("empty api.secret became %q; should stay empty", redacted.Global.API.Secret)
	}
	if v, ok := redacted.Transports[0].Settings["password"]; ok {
		t.Errorf("absent password key was injected as %v", v)
	}
}

// ─── Sentinel merge: credentials survive a redacted round-trip ───────

func TestMergeSentinels_RestoresUnchangedSecrets(t *testing.T) {
	current := secretedConfig()
	// Simulate the operator round-trip: redact, then submit back unchanged.
	redacted, _ := Redact(current)
	incoming, err := yamlToConfig(redacted)
	if err != nil {
		t.Fatalf("re-parse redacted: %v", err)
	}

	MergeSentinels(incoming, current)

	// Every secret the operator left as "***" must be backfilled to the real value.
	if incoming.Global.API.Secret != current.Global.API.Secret {
		t.Errorf("api.secret not restored: got %q want %q", incoming.Global.API.Secret, current.Global.API.Secret)
	}
	if got := incoming.Transports[0].Settings["password"].(string); got != "mqtt-p@ssw0rd-2026" {
		t.Errorf("mqtt password not restored: got %q", got)
	}
	if got := incoming.Transports[0].Settings["command-forward-secret"].(string); got != "fwd-shared-secret" {
		t.Errorf("command-forward-secret not restored: got %q", got)
	}
	if got := incoming.Transports[1].Settings["webhook-secret"].(string); got != "wh-secret-abc-123" {
		t.Errorf("webhook-secret not restored: got %q", got)
	}
	hdrs := incoming.Transports[1].Settings["headers"].(map[string]any)
	if got := hdrs["Authorization"].(string); got != "Bearer ingest-token-xyz" {
		t.Errorf("Authorization header not restored: got %q", got)
	}
	if got := incoming.Drivers[0].Settings["password"].(string); got != "mb-p@ss-555" {
		t.Errorf("driver password not restored: got %q", got)
	}
}

func TestMergeSentinels_PreservesDeliberatelyChangedSecret(t *testing.T) {
	current := secretedConfig()
	redacted, _ := Redact(current)
	incoming, _ := yamlToConfig(redacted)

	// Operator rotates ONLY the mqtt password; leaves everything else as "***".
	incoming.Transports[0].Settings["password"] = "new-rotated-pwd-2027"

	MergeSentinels(incoming, current)

	// Changed secret passes through untouched.
	if got := incoming.Transports[0].Settings["password"].(string); got != "new-rotated-pwd-2027" {
		t.Errorf("changed password was overwritten by merge: got %q", got)
	}
	// Unchanged secrets still backfilled.
	if got := incoming.Transports[0].Settings["command-secret"].(string); got != "cmd-shared-secret" {
		t.Errorf("unchanged command-secret not restored: got %q", got)
	}
	if got := incoming.Global.API.Secret; got != "super-secret-api-token-2026" {
		t.Errorf("unchanged api.secret not restored: got %q", got)
	}
}

func TestMergeSentinels_RoundTripRestoresExactOriginal(t *testing.T) {
	current := secretedConfig()
	redacted, _ := Redact(current)
	incoming, _ := yamlToConfig(redacted)
	MergeSentinels(incoming, current)

	// After a no-op round-trip, the merged config must equal the original
	// current config (secrets + everything). This is the core safety guarantee:
	// a "load → submit unchanged" cycle must NOT alter any credential.
	curYaml, _ := yaml.Marshal(current)
	inYaml, _ := yaml.Marshal(incoming)
	if string(curYaml) != string(inYaml) {
		t.Errorf("round-trip altered config\n--- current ---\n%s\n--- merged ---\n%s", curYaml, inYaml)
	}
}

func TestMergeSentinels_NewTransportWithSentinelDropsKey(t *testing.T) {
	current := secretedConfig()
	redacted, _ := Redact(current)
	incoming, _ := yamlToConfig(redacted)

	// Operator adds a brand-new transport whose password is the sentinel
	// (e.g. copy-pasted from a redacted example). There is no live value to
	// backfill → the key must be dropped, NOT persisted as literal "***".
	incoming.Transports = append(incoming.Transports, core.TransportConfig{
		Name: "new-mqtt",
		Type: "mqtt",
		Settings: map[string]any{
			"broker":   "tcp://new.example:1883",
			"password": SentinelValue, // sentinel with no current counterpart
		},
	})

	MergeSentinels(incoming, current)

	newT := incoming.Transports[len(incoming.Transports)-1]
	if _, exists := newT.Settings["password"]; exists {
		t.Errorf("sentinel with no live value should be dropped, got %v", newT.Settings["password"])
	}
	// Non-secret fields on the new transport survive.
	if newT.Settings["broker"] != "tcp://new.example:1883" {
		t.Errorf("new transport broker mangled: %v", newT.Settings["broker"])
	}
}

func TestMergeSentinels_DoesNotMutateCurrent(t *testing.T) {
	current := secretedConfig()
	curSecret := current.Global.API.Secret
	curPwd := current.Transports[0].Settings["password"].(string)

	redacted, _ := Redact(current)
	incoming, _ := yamlToConfig(redacted)
	incoming.Global.API.Secret = "operator-rotated-secret" // change one
	MergeSentinels(incoming, current)

	// currentCfg is the live config; merge must be read-only on it.
	if current.Global.API.Secret != curSecret {
		t.Errorf("merge mutated current api.secret: got %q", current.Global.API.Secret)
	}
	if current.Transports[0].Settings["password"].(string) != curPwd {
		t.Errorf("merge mutated current transport password")
	}
}

func TestMergeSentinels_NilSafe(t *testing.T) {
	// Must not panic on nil inputs.
	MergeSentinels(nil, secretedConfig())
	MergeSentinels(secretedConfig(), nil)
	MergeSentinels(nil, nil)
}

// TestRoundTrip_RedactedConfigValidatesAfterMerge is the critical red-team
// acceptance test for the parse/validate SPLIT. A config round-tripped from
// GET /configs/raw carries "***" for unchanged secrets. validate() rejects
// short sentinels (api.secret < 8 chars → "***" is 3 chars), so validating
// BEFORE the merge fails. The executor must parse (no validate) → merge →
// validate, and the merged config must validate cleanly. This test proves that
// sequence works end-to-end at the config layer.
func TestRoundTrip_RedactedConfigValidatesAfterMerge(t *testing.T) {
	current := secretedConfig()

	// 1. Redact → "***" sentinels (simulates GET /configs/raw output).
	redacted, err := Redact(current)
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	data, err := yaml.Marshal(redacted)
	if err != nil {
		t.Fatalf("marshal redacted: %v", err)
	}

	// 2. Prove the redacted YAML would FAIL validation if validated directly
	//    (this is why the split is necessary).
	direct, derr := Parse(data) // Parse validates inline
	if derr == nil {
		t.Fatalf("expected redacted config to FAIL inline validation (*** is too short), but Parse succeeded: %+v", direct)
	}
	if !strings.Contains(derr.Error(), "api.secret must be at least 8 characters") {
		t.Fatalf("expected api.secret length error, got: %v", derr)
	}

	// 3. The correct sequence: ParseNoValidate → MergeSentinels → Validate.
	cfg, err := ParseNoValidate(data)
	if err != nil {
		t.Fatalf("ParseNoValidate: %v", err)
	}
	MergeSentinels(cfg, current)
	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate after merge should pass (real secret restored), got: %v", err)
	}

	// 4. The merged config's api.secret must be the REAL value, not "***".
	if cfg.Global.API.Secret != current.Global.API.Secret {
		t.Errorf("after merge, api.secret = %q, want real %q", cfg.Global.API.Secret, current.Global.API.Secret)
	}
}

// yamlToConfig marshals a *core.Config to YAML and re-parses it, simulating
// the real round-trip: GET /configs/raw → YAML text → operator → PUT /configs
// → config.Parse. This exercises the exact serialization path the feature uses.
func yamlToConfig(cfg *core.Config) (*core.Config, error) {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	cp := &core.Config{}
	if err := yaml.Unmarshal(data, cp); err != nil {
		return nil, err
	}
	return cp, nil
}
