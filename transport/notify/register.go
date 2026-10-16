// Package notify implements a notification transport for CoreC that forwards
// data points to external alerting channels: Slack, Microsoft Teams, email
// (SMTP), and generic webhooks. It is a pure northbound transport — it has no
// reverse command channel and no inbound data stream.
package notify

import "github.com/CoreC-Dev/CoreC/core"

func init() {
	core.RegisterTransport("notify", NewNotifyTransport)
}

// TypeName identifies this transport's protocol, returned by Type() and
// embedded in Status() for consistent reporting from a single source.
const TypeName = "notify"
