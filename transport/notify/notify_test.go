package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"text/template"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

func makePoint(tag string, value any, rule string) core.DataPoint {
	md := map[string]string{}
	if rule != "" {
		md["rule"] = rule
	}
	return core.DataPoint{
		Driver:    "modbus1",
		Device:    "dev1",
		Tag:       tag,
		Value:     value,
		Type:      core.TypeFloat32,
		Quality:   core.QualityGood,
		Timestamp: time.Now(),
		Metadata:  md,
	}
}

func TestCooldownSuppress(t *testing.T) {
	c := newCooldownTracker(100 * time.Millisecond)
	if !c.allow("r1") {
		t.Fatal("first allow should be true")
	}
	if c.allow("r1") {
		t.Fatal("second allow within window should be false")
	}
	// Different key is independent
	if !c.allow("r2") {
		t.Fatal("different key should be allowed")
	}
}

func TestCooldownDisabled(t *testing.T) {
	c := newCooldownTracker(0)
	if !c.allow("r1") {
		t.Fatal("zero window should always allow")
	}
	if !c.allow("r1") {
		t.Fatal("zero window should always allow")
	}
}

func TestCooldownExpires(t *testing.T) {
	c := newCooldownTracker(20 * time.Millisecond)
	if !c.allow("r1") {
		t.Fatal("first allow should be true")
	}
	// Poll until the window expires — no fixed Sleep.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if c.allow("r1") {
			return // success: window expired
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("cooldown did not expire within 1s")
}

func TestRenderTemplate(t *testing.T) {
	tmpl := mustParseTemplate(t, "[{{.Rule}}] {{.Tag}}={{.Value}}")
	p := makePoint("temp", 42.5, "overheat")
	got := renderTemplate(tmpl, p)
	want := "[overheat] temp=42.5"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRenderTemplateMissingRule(t *testing.T) {
	tmpl := mustParseTemplate(t, "[{{.Rule}}] {{.Tag}}")
	p := makePoint("temp", 1.0, "")
	got := renderTemplate(tmpl, p)
	want := "[] temp"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestWebhookSenderSlack(t *testing.T) {
	var received map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &received)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tmpl := mustParseTemplate(t, "🚨 [{{.Rule}}] {{.Tag}}={{.Value}}")
	s := &webhookSender{url: srv.URL, client: srv.Client(), channel: "slack", tmpl: tmpl}
	p := makePoint("temp", 99.0, "overheat")

	if err := s.send(context.Background(), p); err != nil {
		t.Fatalf("send: %v", err)
	}
	text, _ := received["text"].(string)
	if text != "🚨 [overheat] temp=99" {
		t.Fatalf("got text %q", text)
	}
}

func TestWebhookSenderTeams(t *testing.T) {
	var received map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &received)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tmpl := mustParseTemplate(t, "[{{.Rule}}] {{.Tag}}")
	s := &webhookSender{url: srv.URL, client: srv.Client(), channel: "teams", tmpl: tmpl}
	p := makePoint("press", 5.0, "high-pressure")

	if err := s.send(context.Background(), p); err != nil {
		t.Fatalf("send: %v", err)
	}
	if received["@type"] != "MessageCard" {
		t.Fatalf("expected MessageCard, got %v", received["@type"])
	}
}

func TestWebhookSenderRateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	tmpl := mustParseTemplate(t, "{{.Tag}}")
	s := &webhookSender{url: srv.URL, client: srv.Client(), channel: "slack", tmpl: tmpl}
	p := makePoint("temp", 1.0, "")

	err := s.send(context.Background(), p)
	if err != errRateLimited {
		t.Fatalf("expected errRateLimited, got %v", err)
	}
}

func TestGenericWebhookSender(t *testing.T) {
	var received map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("got method %s, want POST", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("missing auth header")
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &received)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tmpl := mustParseTemplate(t, `{"tag":"{{.Tag}}","value":{{.Value}}}`)
	s := &genericWebhookSender{
		url:     srv.URL,
		method:  http.MethodPost,
		headers: map[string]string{"Authorization": "Bearer secret"},
		tmpl:    tmpl,
		client:  srv.Client(),
	}
	p := makePoint("speed", 1200, "")

	if err := s.send(context.Background(), p); err != nil {
		t.Fatalf("send: %v", err)
	}
	if received["tag"] != "speed" {
		t.Fatalf("got tag %v", received["tag"])
	}
}

func TestNotifyTransportInitSlack(t *testing.T) {
	tr, err := NewNotifyTransport(core.TransportConfig{
		Name: "notify-slack",
		Type: "notify",
		Settings: map[string]any{
			"channel":     "slack",
			"webhook-url": "https://hooks.slack.com/test",
			"cooldown":    "5m",
		},
	})
	if err != nil {
		t.Fatalf("NewNotifyTransport: %v", err)
	}
	if err := tr.Init(context.Background(), core.TransportConfig{
		Name: "notify-slack",
		Type: "notify",
		Settings: map[string]any{
			"channel":     "slack",
			"webhook-url": "https://hooks.slack.com/test",
			"cooldown":    "5m",
		},
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if tr.Type() != "notify" {
		t.Fatalf("Type: %s", tr.Type())
	}
	if tr.Name() != "notify-slack" {
		t.Fatalf("Name: %s", tr.Name())
	}
}

func TestNotifyTransportInitBadChannel(t *testing.T) {
	tr, _ := NewNotifyTransport(core.TransportConfig{Name: "x", Type: "notify"})
	err := tr.Init(context.Background(), core.TransportConfig{
		Name:     "x",
		Type:     "notify",
		Settings: map[string]any{"channel": "carrier-pigeon"},
	})
	if err == nil {
		t.Fatal("expected error for bad channel")
	}
}

func TestNotifyTransportInitMissingURL(t *testing.T) {
	tr, _ := NewNotifyTransport(core.TransportConfig{Name: "x", Type: "notify"})
	err := tr.Init(context.Background(), core.TransportConfig{
		Name:     "x",
		Type:     "notify",
		Settings: map[string]any{"channel": "slack"},
	})
	if err == nil {
		t.Fatal("expected error for missing webhook-url")
	}
}

func TestNotifyTransportPublishWithCooldown(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callCount++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tr, _ := NewNotifyTransport(core.TransportConfig{Name: "n", Type: "notify"})
	if err := tr.Init(context.Background(), core.TransportConfig{
		Name: "n",
		Type: "notify",
		Settings: map[string]any{
			"channel":     "slack",
			"webhook-url": srv.URL,
			"cooldown":    "1h",
		},
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := tr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer tr.Stop()

	p := makePoint("temp", 80.0, "overheat")
	if err := tr.Publish(context.Background(), p); err != nil {
		t.Fatalf("first Publish: %v", err)
	}
	// Second publish within cooldown window should be suppressed.
	if err := tr.Publish(context.Background(), p); err != nil {
		t.Fatalf("second Publish: %v", err)
	}
	if callCount != 1 {
		t.Fatalf("expected 1 HTTP call (cooldown suppressed 2nd), got %d", callCount)
	}

	st := tr.Status()
	if st.Published != 1 {
		t.Fatalf("expected Published=1, got %d", st.Published)
	}
}

func TestNotifyTransportOnChannels(t *testing.T) {
	tr, _ := NewNotifyTransport(core.TransportConfig{Name: "n", Type: "notify"})
	if tr.OnCommand() != nil {
		t.Fatal("OnCommand should return nil")
	}
	if tr.OnData() != nil {
		t.Fatal("OnData should return nil")
	}
}

func TestEmailSenderShort(t *testing.T) {
	if testing.Short() {
		t.Skip("email sender requires SMTP server")
	}
	// Without a real SMTP server this will fail at connection; we only
	// verify the sender builds correctly.
	_, err := newEmailSender(map[string]any{
		"smtp-host": "localhost",
		"smtp-port": 25,
		"from":      "corec@example.com",
		"to":        []any{"oncall@example.com"},
	})
	if err != nil {
		t.Fatalf("newEmailSender: %v", err)
	}
}

func mustParseTemplate(t *testing.T, expr string) *template.Template {
	t.Helper()
	tmpl, err := template.New("test").Parse(expr)
	if err != nil {
		t.Fatalf("parse template: %v", err)
	}
	return tmpl
}

func TestOptStringListCommaForm(t *testing.T) {
	got, err := optStringList(map[string]any{"to": "a@x.com, b@x.com ,c@x.com"}, "to")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"a@x.com", "b@x.com", "c@x.com"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d]=%q, want %q", i, got[i], want[i])
		}
	}
}

func TestOptStringListYAMLForm(t *testing.T) {
	got, err := optStringList(map[string]any{"to": []any{"a@x.com", "b@x.com"}}, "to")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 || got[0] != "a@x.com" || got[1] != "b@x.com" {
		t.Fatalf("got %v", got)
	}
}

func TestOptStringMapJSONForm(t *testing.T) {
	got, err := optStringMap(map[string]any{"headers": `{"Authorization":"Bearer xyz","X-Source":"corec"}`}, "headers")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["Authorization"] != "Bearer xyz" || got["X-Source"] != "corec" {
		t.Fatalf("got %v", got)
	}
}

func TestOptStringMapYAMLForm(t *testing.T) {
	got, err := optStringMap(map[string]any{"headers": map[string]any{"Authorization": "Bearer abc"}}, "headers")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["Authorization"] != "Bearer abc" {
		t.Fatalf("got %v", got)
	}
}
