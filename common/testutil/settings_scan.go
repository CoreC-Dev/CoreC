// Package testutil provides shared test helpers.
//
// This file hosts the settings-key scanning helpers for the drift tests
// in driver and transport; archtest inspects production imports only.
package testutil

import "regexp"

// Shared extraction regexes for the three settings-parsing idioms.
var (
	TypedSettingKeyRe  = regexp.MustCompile(`Get(?:Bool|Int|String|Duration)Setting\((?:settings|config\.Settings),\s*"([a-z0-9-]+)"`)
	DirectSettingKeyRe = regexp.MustCompile(`settings\["([a-z0-9-]+)"\]`)
	NotifyHelperKeyRe  = regexp.MustCompile(`(reqString|optString|reqInt|optDuration|optStringList|optStringMap|optTemplate)\((?:settings|config\.Settings),\s*"([a-z0-9-]+)"`)
)

// CollectParsedSettingKeys extracts every settings key referenced by
// any of the three parsing idioms in src into out. NotifyHelperKeyRe
// captures the key in group 2; the other regexes capture it in group 1.
func CollectParsedSettingKeys(src string, out map[string]bool) {
	for _, m := range TypedSettingKeyRe.FindAllStringSubmatch(src, -1) {
		out[m[1]] = true
	}
	for _, m := range DirectSettingKeyRe.FindAllStringSubmatch(src, -1) {
		out[m[1]] = true
	}
	for _, m := range NotifyHelperKeyRe.FindAllStringSubmatch(src, -1) {
		out[m[2]] = true
	}
}
