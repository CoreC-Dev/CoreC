// Package driver hosts the shared settings-key scanning helpers for the
// drift tests in driver and transport.
package driver

import "regexp"

// Shared extraction regexes for the two settings-parsing idioms.
var (
	TypedSettingKeyRe  = regexp.MustCompile(`Get(?:Bool|Int|String|Duration)Setting\((?:settings|config\.Settings),\s*"([a-z0-9-]+)"`)
	DirectSettingKeyRe = regexp.MustCompile(`settings\["([a-z0-9-]+)"\]`)
)

// CollectParsedSettingKeys extracts every settings key referenced by
// either parsing idiom in src into out. Exported so the transport drift
// test reuses the exact same extraction.
func CollectParsedSettingKeys(src string, out map[string]bool) {
	for _, m := range TypedSettingKeyRe.FindAllStringSubmatch(src, -1) {
		out[m[1]] = true
	}
	for _, m := range DirectSettingKeyRe.FindAllStringSubmatch(src, -1) {
		out[m[1]] = true
	}
}
