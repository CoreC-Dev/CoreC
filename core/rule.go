package core

import "time"

// Rule defines a data routing rule.
type Rule interface {
	Match(point DataPoint) bool
	Action() Action
	Target() string
	Targets() []string // For mirror action
	Priority() int
	Name() string
	Payload() string             // match expression or reference
	RuleType() string            // "simple", "rule-set", "sub-rule"
	Transform() *TransformConfig // transform config (nil unless action == transform)
	String() string
}

// RuleWrapper wraps a Rule with runtime statistics and enable/disable control.
type RuleWrapper interface {
	Rule
	SetDisabled(v bool)
	IsDisabled() bool
	HitCount() uint64
	HitAt() time.Time
	MissCount() uint64
	MissAt() time.Time
	Unwrap() Rule
}

// RuleProvider supplies an external set of match-only rules.
type RuleProvider interface {
	Name() string
	Initial() error
	Update() error
	Match(point DataPoint) bool
	Count() int
}

// RuleConfig defines a rule from YAML configuration.
type RuleConfig struct {
	Name      string           `yaml:"name"`
	Match     string           `yaml:"match"`
	Action    string           `yaml:"action"`
	Target    string           `yaml:"target,omitempty"`
	Targets   []string         `yaml:"targets,omitempty"`
	Priority  int              `yaml:"priority,omitempty"`
	Transform *TransformConfig `yaml:"transform,omitempty"`
}

// RuleProviderConfig defines an external rule-set provider.
type RuleProviderConfig struct {
	Name     string `yaml:"name"`
	Type     string `yaml:"type"`               // "file"
	Path     string `yaml:"path"`               // file path
	Interval string `yaml:"interval,omitempty"` // hot-reload interval, e.g. "30s"
}

// TransformConfig defines a data transformation.
type TransformConfig struct {
	Expression string `yaml:"expression"`
	TagRename  string `yaml:"tag-rename,omitempty"`
}

// RuleStat contains runtime statistics for a rule.
type RuleStat struct {
	Index     int       `json:"index"`
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	Match     string    `json:"match"`
	Action    string    `json:"action"`
	Target    string    `json:"target"`
	Targets   []string  `json:"targets"`
	Priority  int       `json:"priority"`
	Disabled  bool      `json:"disabled"`
	HitCount  uint64    `json:"hit_count"`
	HitAt     time.Time `json:"hit_at"`
	MissCount uint64    `json:"miss_count"`
	MissAt    time.Time `json:"miss_at"`
}
