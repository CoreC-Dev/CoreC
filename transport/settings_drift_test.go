// Package transport_test hosts the transport settings-allowlist drift
// test (audit CFG-4 / roadmap #25) — the transport twin of
// driver/settings_drift_test.go: every settings key the code parses must
// appear in the ValidateSettings allowlist, so a new setting without a
// validator update fails at CI time instead of rejecting user configs.
package transport_test

import (
	"os"
	"strings"
	"testing"

	"github.com/CoreC-Dev/CoreC/common/testutil"
)

var knownTransportSettings = map[string]map[string]bool{
	"mqtt": {
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
	},
	"httppush": {
		"url": true, "method": true, "headers": true, "timeout": true,
		"max-idle-conns": true, "max-idle-conns-per-host": true, "idle-conn-timeout": true,
		"webhook-addr": true, "webhook-path": true, "webhook-secret": true,
		"tls-cert-file": true, "tls-key-file": true,
		"parser": true, // nested map consumed by transport/parser
	},
	"notify": {
		"channel": true, "cooldown": true, "template": true,
		"webhook-url": true, "url": true, "method": true, "headers": true, "body-template": true,
		"smtp-host": true, "smtp-port": true, "smtp-user": true, "smtp-pass": true,
		"from": true, "to": true, "subject": true, "body": true,
	},
}

func TestTransportSettingsAllowlistsCoverParsedKeys(t *testing.T) {
	for pkg, known := range knownTransportSettings {
		t.Run(pkg, func(t *testing.T) {
			// cwd is transport/.
			entries, err := os.ReadDir(pkg)
			if err != nil {
				t.Fatalf("read %s: %v", pkg, err)
			}
			parsed := make(map[string]bool)
			for _, e := range entries {
				name := e.Name()
				if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
					continue
				}
				data, err := os.ReadFile(pkg + "/" + name)
				if err != nil {
					t.Fatalf("read file: %v", err)
				}
				testutil.CollectParsedSettingKeys(string(data), parsed)
			}
			for key := range parsed {
				if !known[key] {
					t.Errorf("%s parses setting %q but its ValidateSettings allowlist does not include it — update the transport's settings validator", pkg, key)
				}
			}
		})
	}
}
