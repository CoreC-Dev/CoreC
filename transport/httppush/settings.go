package httppush

import (
	"github.com/CoreC-Dev/CoreC/common/util"
	"github.com/CoreC-Dev/CoreC/core"
)

// ValidateSettings implements core.SettingsValidator (audit CFG-4 /
// roadmap #25): rejects unknown keys so typos fail at
// config-validate time. The drift test
// (transport/settings_drift_test.go) fails when a key parsed by
// push.go is missing here.
func (t *HTTPTransport) ValidateSettings(settings map[string]any) error {
	return util.RejectUnknownSettings(settings, map[string]bool{
		"url": true, "method": true, "headers": true, "timeout": true,
		"max-idle-conns": true, "max-idle-conns-per-host": true, "idle-conn-timeout": true,
		"webhook-addr": true, "webhook-path": true, "webhook-secret": true,
		"tls-cert-file": true, "tls-key-file": true,
		"parser": true, // nested map consumed by transport/parser (webhook inbound)
	}, "httppush")
}

var _ core.SettingsValidator = (*HTTPTransport)(nil)
