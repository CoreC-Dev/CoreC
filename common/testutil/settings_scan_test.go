package testutil

import "testing"

func TestCollectParsedSettingKeysNotifyHelpers(t *testing.T) {
	src := `
channel, err := reqString(config.Settings, "channel")
cooldown, err := optDuration(config.Settings, "cooldown")
url, err := reqString(settings, "webhook-url")
method, err := optString(settings, "method")
headers, err := optStringMap(settings, "headers")
host, err := reqString(settings, "smtp-host")
port, err := reqInt(settings, "smtp-port")
user, err := optString(settings, "smtp-user")
pass, err := optString(settings, "smtp-pass")
from, err := reqString(settings, "from")
to, err := optStringList(settings, "to")
tmpl, err := optTemplate(config.Settings, "template", defaultTemplate)
subjectTmpl, err := optTemplate(settings, "subject", "[CoreC] {{.Rule}}")
`
	out := make(map[string]bool)
	CollectParsedSettingKeys(src, out)
	want := []string{"channel", "cooldown", "webhook-url", "method", "headers", "smtp-host", "smtp-port", "smtp-user", "smtp-pass", "from", "to", "template", "subject"}
	for _, key := range want {
		if !out[key] {
			t.Errorf("notify helper key %q not collected", key)
		}
	}
	if len(out) != len(want) {
		t.Errorf("collected %d keys, want %d: %v", len(out), len(want), out)
	}
}

func TestCollectParsedSettingKeysTypedAndDirect(t *testing.T) {
	src := `
host := GetStringSetting(config.Settings, "broker")
port := GetIntSetting(settings, "port")
tls := GetBoolSetting(settings, "tls")
raw := settings["topic"]
`
	out := make(map[string]bool)
	CollectParsedSettingKeys(src, out)
	for _, key := range []string{"broker", "port", "tls", "topic"} {
		if !out[key] {
			t.Errorf("key %q not collected", key)
		}
	}
	if len(out) != 4 {
		t.Errorf("collected %d keys, want 4: %v", len(out), out)
	}
}

func TestCollectParsedSettingKeysIgnoresUnrelatedCalls(t *testing.T) {
	src := `
v, err := reqString(other, "not-a-setting")
s := GetStringSetting(opts, "also-not")
x := settings[variableKey]
`
	out := make(map[string]bool)
	CollectParsedSettingKeys(src, out)
	if len(out) != 0 {
		t.Errorf("expected no keys, got %v", out)
	}
}
