package mqtt

import (
	"github.com/CoreC-Dev/CoreC/common/util"
	"github.com/CoreC-Dev/CoreC/core"
)

// ValidateSettings implements core.SettingsValidator (audit CFG-4 /
// roadmap #25): rejects unknown keys so typos like `brokr` fail at
// config-validate time instead of silently connecting nowhere. The
// drift test (transport/settings_drift_test.go) fails when a key parsed
// by publisher.go is missing here.
func (t *MQTTTransport) ValidateSettings(settings map[string]any) error {
	return util.RejectUnknownSettings(settings, map[string]bool{
		"broker": true, "client-id": true, "username": true, "password": true,
		"qos": true, "retained": true, "topic-template": true,
		"command-topic": true, "command-secret": true,
		"command-forward-topic": true, "command-forward-secret": true,
		"command-max-skew": true, "command-strict-replay": true,
		"data-topic": true, "parser": true,
		"keep-alive": true, "connect-timeout": true,
		"auto-reconnect": true, "clean-session": true,
		"connect-retry": true, "connect-retry-interval": true,
		"subscribe-timeout": true, "publish-timeout": true, "disconnect-quiesce": true,
		"tls-cert-file": true, "tls-key-file": true, "tls-ca-file": true,
		"will-topic": true, "will-payload": true, "will-qos": true, "will-retain": true,
	}, "mqtt")
}

var _ core.SettingsValidator = (*MQTTTransport)(nil)
