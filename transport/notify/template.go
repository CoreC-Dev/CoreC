package notify

import (
	"bytes"
	"fmt"
	"text/template"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// defaultTemplate is the default message body for Slack/Teams channels.
const defaultTemplate = "[{{.Rule}}] {{.Tag}} = {{.Value}} ({{.Driver}})"

// templateData is the execution environment for message templates.
type templateData struct {
	Rule      string
	Tag       string
	Value     any
	Driver    string
	Device    string
	Group     string
	Quality   string
	Timestamp time.Time
}

// renderTemplate executes a text/template against a DataPoint, returning the
// rendered string. On failure it returns a plain-text fallback so a bad
// template never crashes the pipeline.
func renderTemplate(tmpl *template.Template, point core.DataPoint) string {
	data := templateData{
		Rule:      point.Metadata["rule"],
		Tag:       point.Tag,
		Value:     point.Value,
		Driver:    point.Driver,
		Device:    point.Device,
		Group:     point.Group,
		Quality:   point.Quality.String(),
		Timestamp: point.Timestamp,
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Sprintf("%s = %v", point.Tag, point.Value)
	}
	return buf.String()
}
