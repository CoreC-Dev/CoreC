// Package testutil provides shared test helpers.
//
// This file hosts the settings-key scanning helpers for the drift tests
// in driver and transport. They live in this test-only package rather
// than in a production one so that no production package exists solely
// to serve tests, and the cross-package transport→driver dependency they
// create stays test-only, where archtest (which inspects production
// imports only) is not misled by it.
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
