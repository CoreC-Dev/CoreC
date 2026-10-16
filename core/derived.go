package core

// DerivedTagConfig defines a computed data point whose value is calculated
// from other tags' latest cached values via an arithmetic expression.
//
// Example:
//
//	derived-tags:
//	  - name: efficiency
//	    expr: "output_power / input_power * 100"
//	    type: float32
//	    interval: 5s
//	    unit: "%"
//	    on-missing: skip
//	    stale-guard: 30s
type DerivedTagConfig struct {
	// Name is the derived tag's identifier. It becomes the Tag field of the
	// produced DataPoint. Must be unique across all derived tags.
	Name string `yaml:"name"`

	// Expr is the arithmetic expression evaluated against the latest cache.
	// Identifiers in the expression are resolved to tag names (e.g.
	// "output_power / input_power * 100" reads tags "output_power" and
	// "input_power").
	Expr string `yaml:"expr"`

	// Type is the DataType of the produced DataPoint.
	Type DataType `yaml:"type"`

	// Group is an optional group label for the produced DataPoint.
	Group string `yaml:"group,omitempty"`

	// Interval is the evaluation period (e.g. "5s"). Must be >= 1s.
	Interval string `yaml:"interval"`

	// Unit is an optional unit string written to Metadata["unit"].
	Unit string `yaml:"unit,omitempty"`

	// OnMissing controls behavior when a referenced tag is absent from the
	// cache: "skip" (default, emit nothing), "bad-quality" (emit a
	// QualityBad point), "zero" (treat missing as 0 and evaluate).
	OnMissing string `yaml:"on-missing,omitempty"`

	// StaleGuard marks the derived point stale if any referenced tag's
	// Timestamp is older than this duration. "0" disables the check.
	StaleGuard string `yaml:"stale-guard,omitempty"`
}
