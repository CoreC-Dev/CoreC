package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/smtp"
	"strings"
	"text/template"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// sender is the per-channel delivery strategy. Each implementation renders
// the DataPoint into its own format and delivers it to the target medium.
type sender interface {
	send(ctx context.Context, point core.DataPoint) error
}

// buildSender constructs the appropriate sender for the given channel.
func buildSender(channel string, settings map[string]any, tmpl *template.Template) (sender, error) {
	switch channel {
	case "slack", "teams":
		return newWebhookSender(settings, tmpl, channel)
	case "email":
		return newEmailSender(settings)
	case "webhook":
		return newGenericWebhookSender(settings)
	default:
		return nil, fmt.Errorf("unsupported channel: %s", channel)
	}
}

// --- Slack / Teams (JSON webhook) ---

// webhookSender posts a JSON payload to an incoming webhook URL. Used for
// both Slack and Microsoft Teams with channel-specific body formatting.
type webhookSender struct {
	url     string
	client  *http.Client
	channel string
	tmpl    *template.Template
}

func newWebhookSender(settings map[string]any, tmpl *template.Template, channel string) (*webhookSender, error) {
	url, err := reqString(settings, "webhook-url")
	if err != nil {
		return nil, err
	}
	return &webhookSender{
		url:     url,
		client:  &http.Client{Timeout: 10 * time.Second},
		channel: channel,
		tmpl:    tmpl,
	}, nil
}

func (w *webhookSender) send(ctx context.Context, point core.DataPoint) error {
	text := renderTemplate(w.tmpl, point)
	payload := w.buildPayload(text)
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal webhook payload: %w", err)
	}
	return postJSON(ctx, w.client, w.url, body)
}

// buildPayload formats the message for the target channel. Slack uses a
// simple text field; Teams uses a MessageCard.
func (w *webhookSender) buildPayload(text string) any {
	if w.channel == "teams" {
		return map[string]any{
			"@type":      "MessageCard",
			"@context":   "http://schema.org/extensions",
			"themeColor": "FF0000",
			"text":       text,
		}
	}
	return map[string]any{"text": text}
}

// --- Generic webhook ---

// genericWebhookSender posts a user-templated JSON body to a configurable
// URL with custom method and headers (DingTalk, Feishu, custom integrations).
type genericWebhookSender struct {
	url     string
	method  string
	headers map[string]string
	tmpl    *template.Template
	client  *http.Client
}

func newGenericWebhookSender(settings map[string]any) (*genericWebhookSender, error) {
	url, err := reqString(settings, "url")
	if err != nil {
		return nil, err
	}
	method, err := optString(settings, "method")
	if err != nil {
		return nil, err
	}
	if method == "" {
		method = http.MethodPost
	}
	headers, err := optStringMap(settings, "headers")
	if err != nil {
		return nil, err
	}
	tmpl, err := optTemplate(settings, "body-template", `{"text":"{{.Tag}} = {{.Value}}"}`)
	if err != nil {
		return nil, err
	}
	return &genericWebhookSender{
		url:     url,
		method:  method,
		headers: headers,
		tmpl:    tmpl,
		client:  &http.Client{Timeout: 10 * time.Second},
	}, nil
}

func (g *genericWebhookSender) send(ctx context.Context, point core.DataPoint) error {
	body := []byte(renderTemplate(g.tmpl, point))
	req, err := http.NewRequestWithContext(ctx, g.method, g.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range g.headers {
		req.Header.Set(k, v)
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("webhook returned status %d", resp.StatusCode)
	}
	return nil
}

// postJSON is the shared HTTP POST helper for JSON payloads.
func postJSON(ctx context.Context, client *http.Client, url string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return errRateLimited
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("http status %d", resp.StatusCode)
	}
	return nil
}

// --- Email (SMTP) ---

// emailSender sends notifications via SMTP with optional PLAIN auth.
type emailSender struct {
	host, user, pass, from string
	port                   int
	to                     []string
	subjectTmpl            *template.Template
	bodyTmpl               *template.Template
}

func newEmailSender(settings map[string]any) (*emailSender, error) {
	host, err := reqString(settings, "smtp-host")
	if err != nil {
		return nil, err
	}
	port, err := reqInt(settings, "smtp-port")
	if err != nil {
		return nil, err
	}
	user, err := optString(settings, "smtp-user")
	if err != nil {
		return nil, err
	}
	pass, err := optString(settings, "smtp-pass")
	if err != nil {
		return nil, err
	}
	from, err := reqString(settings, "from")
	if err != nil {
		return nil, err
	}
	to, err := optStringList(settings, "to")
	if err != nil {
		return nil, err
	}
	if len(to) == 0 {
		return nil, fmt.Errorf("%w: to (recipient list)", errMissingSetting)
	}
	subjectTmpl, err := optTemplate(settings, "subject", "[CoreC] {{.Rule}}")
	if err != nil {
		return nil, err
	}
	bodyTmpl, err := optTemplate(settings, "body",
		"Tag {{.Tag}} on {{.Driver}}/{{.Device}} = {{.Value}} at {{.Timestamp}}")
	if err != nil {
		return nil, err
	}
	return &emailSender{
		host:        host,
		port:        port,
		user:        user,
		pass:        pass,
		from:        from,
		to:          to,
		subjectTmpl: subjectTmpl,
		bodyTmpl:    bodyTmpl,
	}, nil
}

func (e *emailSender) send(ctx context.Context, point core.DataPoint) error {
	subject := renderTemplate(e.subjectTmpl, point)
	body := renderTemplate(e.bodyTmpl, point)
	addr := fmt.Sprintf("%s:%d", e.host, e.port)
	var auth smtp.Auth
	if e.user != "" {
		auth = smtp.PlainAuth("", e.user, e.pass, e.host)
	}
	msg := buildEmailMessage(e.from, strings.Join(e.to, ", "), subject, body)
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	return smtp.SendMail(addr, auth, e.from, e.to, msg)
}

// buildEmailMessage constructs an RFC 5322 message with headers and body.
func buildEmailMessage(from, to, subject, body string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(body)
	return []byte(b.String())
}

// reqInt extracts a required integer setting. YAML integers decode as int.
func reqInt(settings map[string]any, key string) (int, error) {
	v, ok := settings[key]
	if !ok {
		return 0, fmt.Errorf("%w: %s", errMissingSetting, key)
	}
	switch n := v.(type) {
	case int:
		return n, nil
	case int64:
		return int(n), nil
	case float64:
		return int(n), nil
	default:
		return 0, fmt.Errorf("setting %s must be an integer", key)
	}
}

// errRateLimited is returned when the channel responds with HTTP 429.
var errRateLimited = fmt.Errorf("channel rate limited (HTTP 429)")
