package config

// engine.on-bad-quality is an enum with a single authoritative reader
// (engine.parseBadQualityPolicy, in engine/engine.go). config/ must not import
// engine/ (the layering test forbids it), so validateEngine keeps its own copy of
// the accepted set. These tests pin the copy from both sides:
//
//   - the accepted values are accepted here (kept in sync with that switch), and
//   - everything else, including a typo, is refused at load, so
//     POST /configs/validate cannot answer 200 for a value that will stop the
//     engine from starting the next time the process is restarted.

import (
	"strings"
	"testing"

	"github.com/CoreC-Dev/CoreC/core"
)

func onBadQuality(t *testing.T, value string) error {
	t.Helper()
	cfg := &core.Config{}
	cfg.Global.Engine.OnBadQuality = value
	return Validate(cfg)
}

// The four documented values plus the unset default. A regression here breaks
// every config that sets the field.
func TestOnBadQualityAcceptsDocumentedValues(t *testing.T) {
	for _, v := range []string{"", "publish", "drop", "mark-and-publish", "alert"} {
		if err := onBadQuality(t, v); err != nil {
			t.Errorf("%q rejected, but engine.parseBadQualityPolicy accepts it: %v", v, err)
		}
	}
}

// The engine lowercases before comparing(-), so these must not be refused here.
// If this ever starts failing, the two copies diverged and a config that starts
// fine would stop being savable.
func TestOnBadQualityAcceptsMixedCase(t *testing.T) {
	for _, v := range []string{"PUBLISH", "Drop", "Mark-And-Publish", "Alert"} {
		if err := onBadQuality(t, v); err != nil {
			t.Errorf("%q rejected, but the engine lowercases before matching: %v", v, err)
		}
	}
}

// The gap this closed: these used to save fine and then stop the engine on the
// following restart.
func TestOnBadQualityRejectsUnknownValues(t *testing.T) {
	for _, v := range []string{"nonsense", "publsh", "mark", "markandpublish", "alert ", " drop"} {
		err := onBadQuality(t, v)
		if err == nil {
			t.Errorf("%q accepted; it saves, applies, and then refuses the next start", v)
			continue
		}
		if !strings.Contains(err.Error(), "on-bad-quality") {
			t.Errorf("%q: error does not name the field: %v", v, err)
		}
	}
}
