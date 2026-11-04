package engine

import (
	"fmt"

	"github.com/CoreC-Dev/CoreC/core"
)

// Write protection is a configuration-boundary invariant, not a driver
// feature: TagConfig.Protect declares whether a point may be written at
// all. Enforcing it in the engine (the single choke point every write path
// funnels through) makes the guard mandatory for third-party drivers too —
// a driver cannot opt out of it by forgetting to implement a check.
//
// Two values are meaningful:
//   - "read-only"  → every write command is rejected before the driver is
//     touched (API single write, /write/batch, MQTT command channel).
//   - "write-only" → writes are allowed, but the point is excluded from
//     scheduling, so it is never collected and never reaches the cache or
//     the rule engine.
//
// Empty and "read-write" mean writable (the historical behaviour: every
// configured tag was writable). Config load rejects any other spelling
// (config/validate.go), so a typo can never widen permissions silently.

// tagWriteProtectionReason returns a non-empty rejection reason when the
// named tag is configured read-only. A tag missing from the snapshot is
// not rejected here: unknown tags are the driver's "tag not found" case
// and stay that error (no behavioural change for that path).
func tagWriteProtectionReason(tags []core.TagConfig, tagName string) string {
	for i := range tags {
		if tags[i].Name != tagName {
			continue
		}
		if tags[i].Protect == "read-only" {
			return fmt.Sprintf("tag %q is read-only (protect: read-only)", tagName)
		}
		return ""
	}
	return ""
}

// isWriteOnlyTag reports whether the named tag is configured write-only.
// Such tags are skipped when building schedule tasks: collecting them would
// contradict the configuration and waste device read budget.
func isWriteOnlyTag(tags []core.TagConfig, tagName string) bool {
	for i := range tags {
		if tags[i].Name == tagName {
			return tags[i].Protect == "write-only"
		}
	}
	return false
}
