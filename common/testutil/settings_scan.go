// Package testutil provides shared test helpers.
//
// This file hosts the settings-key scanning helpers for the drift tests
// in driver and transport (audit CFG-4 / roadmap #25). It moved here
// from a production package `driver` whose only importers were two
// _test.go files (B4, quality audit 2026-10-21) — that placement kept a
// production package alive solely for tests and created a test-only
// transport→driver dependency that archtest (which inspects production
// imports only) could not see.
package testutil

import "regexp"

// Shared extraction regexes for the two settings-parsing idioms.
var (
	TypedSettingKeyRe  = regexp.MustCompile(`Get(?:Bool|Int|String|Duration)Setting\((?:settings|config\.Settings),\s*"([a-z0-9-]+)"`)
	DirectSettingKeyRe = regexp.MustCompile(`settings\["([a-z0-9-]+)"\]`)
)

// CollectParsedSettingKeys extracts every settings key referenced by
// either parsing idiom in src into out.
func CollectParsedSettingKeys(src string, out map[string]bool) {
	for _, m := range TypedSettingKeyRe.FindAllStringSubmatch(src, -1) {
		out[m[1]] = true
	}
	for _, m := range DirectSettingKeyRe.FindAllStringSubmatch(src, -1) {
		out[m[1]] = true
	}
}
