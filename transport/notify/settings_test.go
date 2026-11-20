package notify

import (
	"strings"
	"testing"
)

func TestValidateSettingsSMTPPortNumeric(t *testing.T) {
	tr := &NotifyTransport{}
	for _, port := range []any{587, int64(587), float64(587)} {
		settings := map[string]any{
			"channel":   "email",
			"smtp-host": "smtp.example.com",
			"smtp-port": port,
			"from":      "corec@example.com",
		}
		if err := tr.ValidateSettings(settings); err != nil {
			t.Errorf("smtp-port %T(%v): unexpected error: %v", port, port, err)
		}
	}
}

func TestValidateSettingsSMTPPortNonInteger(t *testing.T) {
	tr := &NotifyTransport{}
	for _, port := range []any{"587", true, []any{587}, map[string]any{"port": 587}} {
		err := tr.ValidateSettings(map[string]any{"smtp-port": port})
		if err == nil {
			t.Errorf("smtp-port %T(%v): expected rejection", port, port)
			continue
		}
		if !strings.Contains(err.Error(), `notify setting "smtp-port" must be an integer`) {
			t.Errorf("smtp-port %T(%v): unexpected error message: %v", port, port, err)
		}
	}
}

func TestValidateSettingsStringKeys(t *testing.T) {
	tr := &NotifyTransport{}
	for _, key := range []string{"channel", "cooldown", "template", "webhook-url", "url", "method", "body-template", "smtp-host", "smtp-user", "smtp-pass", "from", "subject", "body"} {
		if err := tr.ValidateSettings(map[string]any{key: ""}); err != nil {
			t.Errorf("%s as string: unexpected error: %v", key, err)
		}
		err := tr.ValidateSettings(map[string]any{key: 587})
		if err == nil {
			t.Errorf("%s as int: expected rejection", key)
			continue
		}
		if !strings.Contains(err.Error(), "must be a string") {
			t.Errorf("%s as int: unexpected error message: %v", key, err)
		}
	}
}

func TestValidateSettingsListMapExempt(t *testing.T) {
	tr := &NotifyTransport{}
	settings := map[string]any{
		"headers": map[string]any{"Authorization": "Bearer x"},
		"to":      []any{"a@x.com"},
	}
	if err := tr.ValidateSettings(settings); err != nil {
		t.Fatalf("headers map and to list: unexpected error: %v", err)
	}
}

func TestValidateSettingsUnknownKey(t *testing.T) {
	tr := &NotifyTransport{}
	err := tr.ValidateSettings(map[string]any{"no-such-key": ""})
	if err == nil || !strings.Contains(err.Error(), "unknown notify setting") {
		t.Fatalf("expected unknown-key rejection, got: %v", err)
	}
}
