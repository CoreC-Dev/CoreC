package config

// The config loader refuses YAML keys that have no field in the struct.
//
// This is a deliberate change of failure mode for configs that used to load: a
// misspelled key is now a startup error instead of a silently ignored line. The
// tradeoff cuts both ways, so it is worth stating --
//
//   - a personal config carrying a stray key (an old field name from a previous
//     release, a generator's leftover) now fails where it used to start. That is
//     a real cost, paid once, by whoever holds that file. Go disallows unknown
//     fields by default (yaml.UnmarshalStrict in yaml.v2, KnownFields(true) in
//     v3), so producing such a file has always meant working around the default,
//     and the workaround outlives the field it was written for;
//   - what it buys is the failure this package keeps finding otherwise. An
//     operator writes `api.lstin`, or `engine.workerz`, or `protect: readonly`,
//     and gets HTTP 200. The engine runs on defaults, nothing is logged, and the
//     symptom arrives much later as a device that got hammered. The cost of that
//     is not one config file; it is doubt about all the others.
//
// The tests below are the contract: what is refused, what still loads, and that
// the refusal really happens through ParseNoValidate (a decoder that rejects
// nothing would pass every "valid" case).

import (
	"strings"
	"testing"
)

func TestUnknownKeyIsRejected(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		key  string // the key the error must name
	}{
		{"top level", "glboal:\n  log-level: info\n", "glboal"},
		{"nested in engine", "global:\n  engine:\n    workerz: 4\n", "workerz"},
		{"nested in api", "global:\n  api:\n    lstin: 0.0.0.0:9091\n", "lstin"},
		{"nested in transport", "transports:\n  - name: t1\n    type: mqtt\n    broker: tcp://x:1883\n", "broker"},
		{"nested in tag", "drivers:\n  - name: d1\n    type: modbus-tcp\n    tags:\n      - name: t1\n        address: 1\n        type: int16\n        protct: read-only\n", "protct"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := ParseNoValidate([]byte(tc.yaml))
			if err == nil {
				t.Fatalf("unknown key %q accepted; the setting is invisible and the engine runs on a default", tc.key)
			}
			if !strings.Contains(err.Error(), "unknown") {
				t.Errorf("error should say the key is unknown, got: %v", err)
			}
			if !strings.Contains(err.Error(), tc.key) {
				t.Errorf("error must name %q so the typo is findable, got: %v", tc.key, err)
			}
			if cfg != nil {
				t.Errorf("a refused payload must not also yield a config: %+v", cfg)
			}
		})
	}
}

// Placement is checked, not just spelling: `broker` IS a real MQTT setting and
// still fails one level too high, because settings has its own level.
func TestRealSettingInWrongPlaceIsRejected(t *testing.T) {
	yml := "transports:\n  - name: t1\n    type: mqtt\n    broker: tcp://x:1883\n"
	_, err := ParseNoValidate([]byte(yml))
	if err == nil {
		t.Fatal("broker at transport level accepted; it belongs under settings")
	}
	if !strings.Contains(err.Error(), "broker") {
		t.Errorf("error should name the misplaced key, got: %v", err)
	}
}

// Commented-out lines and ${VAR} must still load: refusing unknown keys cannot
// mean refusing an env var before it is expanded.
func TestValidConfigStillLoads(t *testing.T) {
	t.Setenv("PROBE_SECRET", "abcdefgh12ffeeee")
	yml := `global:
  log-level: info
  api:
    listen: 127.0.0.1:9191
    secret: ${PROBE_SECRET}
  engine:
    default-tag-interval: 2s
# a comment between blocks is not a key
drivers:
  - name: d1
    type: modbus-tcp
    settings:
      host: 127.0.0.1
      port: 502
    tags:
      - name: t1
        address: 0
        type: int16
        interval: 1s
transports:
  - name: t1
    type: mqtt
    settings:
      broker: tcp://127.0.0.1:1883
      topic: out/+
`
	cfg, err := ParseNoValidate([]byte(yml))
	if err != nil {
		t.Fatalf("a valid config was refused: %v", err)
	}
	if cfg.Global.API.Secret != "abcdefgh12ffeeee" {
		t.Errorf("env substitution did not survive strict decoding: %q", cfg.Global.API.Secret)
	}
	if len(cfg.Drivers) != 1 || cfg.Drivers[0].Name != "d1" {
		t.Fatalf("driver did not land: %+v", cfg.Drivers)
	}
	if len(cfg.Transports) != 1 || cfg.Transports[0].Name != "t1" {
		t.Fatalf("transport did not land: %+v", cfg.Transports)
	}
	// The nested settings are the point: broker was refused one level up above,
	// and here it must be present under settings.
	if got := cfg.Transports[0].Settings["broker"]; got != "tcp://127.0.0.1:1883" {
		t.Errorf("settings.broker = %v, want the URL", got)
	}
}
