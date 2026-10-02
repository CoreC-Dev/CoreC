package config

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"
)

// envVarRe matches ${ENV_VAR} placeholders in YAML data. Variable names
// must start with a letter or underscore and contain only alphanumeric
// characters and underscores, matching the common environment variable
// naming convention.
var envVarRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// expandEnvVars replaces ${ENV_VAR} placeholders in YAML data with
// environment variable values safely. Unlike naive byte-level replacement,
// which is vulnerable to YAML injection when env var values contain
// YAML-special characters (newlines, colons, braces), this function parses
// the YAML into a generic tree first, expands placeholders in string leaf
// values, and re-serializes — ensuring env var values are properly typed
// and escaped by the YAML encoder.
//
// Type inference is preserved: a value like ${PORT} with PORT=502 is
// converted to the integer 502 (not the string "502") so that YAML fields
// expecting numeric types receive the correct type.
//
// Unset environment variables are left as-is so misconfiguration is
// visible in validation errors (e.g. "api.secret is required").
func expandEnvVars(data []byte) ([]byte, error) {
	// Fast path: skip if no placeholder pattern is present.
	if !strings.Contains(string(data), "${") {
		return data, nil
	}

	// Parse YAML into a generic tree. Placeholders are treated as plain
	// string scalars by the YAML parser — they cannot inject YAML
	// structure at this stage.
	var raw any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse YAML for env var expansion: %w", err)
	}

	// Recursively expand env vars in all string leaf values, with type
	// inference to preserve YAML scalar types (int, float, bool).
	raw = expandEnvInTree(raw)

	// Re-marshal the expanded tree back to YAML bytes. The YAML encoder
	// properly quotes/escapes values containing special characters,
	// preventing injection.
	expanded, err := yaml.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("failed to re-marshal YAML after env var expansion: %w", err)
	}

	return expanded, nil
}

// expandEnvInTree recursively walks a YAML-parsed value and expands
// ${ENV_VAR} placeholders in string leaf values. After expansion, the
// resulting string is passed through inferYamlScalar so that numeric
// and boolean values retain their YAML scalar type (e.g. "502" → int64
// 502), preserving the type-inference behaviour that byte-level
// substitution provided.
func expandEnvInTree(v any) any {
	switch val := v.(type) {
	case string:
		if !strings.Contains(val, "${") {
			return val
		}
		expanded := envVarRe.ReplaceAllStringFunc(val, func(match string) string {
			varName := match[2 : len(match)-1]
			if envVal, ok := os.LookupEnv(varName); ok {
				return envVal
			}
			return match // leave as-is if env var is not set
		})
		return inferYamlScalar(expanded)
	case map[string]any:
		for k, vv := range val {
			val[k] = expandEnvInTree(vv)
		}
		return val
	case []any:
		for i, vv := range val {
			val[i] = expandEnvInTree(vv)
		}
		return val
	default:
		return v
	}
}

// inferYamlScalar attempts to convert a string to its YAML scalar
// equivalent (int, float, bool, or nil) to preserve type inference
// after env var expansion. Strings that do not match any scalar type
// are returned unchanged.
func inferYamlScalar(s string) any {
	// Try integer (base 10).
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n
	}
	// Try float.
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	// Try bool (YAML 1.2 core schema: true/false in any case).
	switch s {
	case "true", "True", "TRUE":
		return true
	case "false", "False", "FALSE":
		return false
	case "null", "Null", "NULL", "~":
		return nil
	}
	return s
}
