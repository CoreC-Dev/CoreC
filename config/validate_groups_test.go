package config

import (
	"strings"
	"testing"

	"github.com/CoreC-Dev/CoreC/core"
)

func TestValidateRuleGroups(t *testing.T) {
	ok := map[string][]core.RuleConfig{
		"grp": {{Name: "m1", Match: "ALL", Action: "drop"}},
	}
	if err := validateRuleGroups(&core.Config{RuleGroups: ok}); err != nil {
		t.Fatalf("valid group rejected: %v", err)
	}

	cases := []struct {
		name   string
		groups map[string][]core.RuleConfig
		want   string
	}{
		{
			name:   "empty group name",
			groups: map[string][]core.RuleConfig{"": {{Name: "m", Match: "ALL", Action: "drop"}}},
			want:   "group name cannot be empty",
		},
		{
			name:   "member without name",
			groups: map[string][]core.RuleConfig{"g": {{Name: "", Match: "ALL", Action: "drop"}}},
			want:   "member rule name cannot be empty",
		},
		{
			name:   "duplicate member name",
			groups: map[string][]core.RuleConfig{"g": {{Name: "m", Match: "ALL", Action: "drop"}, {Name: "m", Match: "tag", Action: "drop"}}},
			want:   "duplicate member rule name",
		},
		{
			name:   "member without match",
			groups: map[string][]core.RuleConfig{"g": {{Name: "m", Action: "drop"}}},
			want:   "match cannot be empty",
		},
		{
			name:   "member with target is rejected",
			groups: map[string][]core.RuleConfig{"g": {{Name: "m", Match: "ALL", Action: "forward", Target: "t1"}}},
			want:   "SUB-RULE reference ignores",
		},
		{
			name:   "member with targets is rejected",
			groups: map[string][]core.RuleConfig{"g": {{Name: "m", Match: "ALL", Action: "forward", Targets: []string{"t1"}}}},
			want:   "SUB-RULE reference ignores",
		},
		{
			name:   "member with transform is rejected",
			groups: map[string][]core.RuleConfig{"g": {{Name: "m", Match: "ALL", Action: "transform", Transform: &core.TransformConfig{}}}},
			want:   "SUB-RULE reference ignores",
		},
		{
			name:   "member with continue is rejected",
			groups: map[string][]core.RuleConfig{"g": {{Name: "m", Match: "ALL", Action: "alert", Continue: true}}},
			want:   "SUB-RULE reference ignores",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRuleGroups(&core.Config{RuleGroups: tc.groups})
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want containing %q", err.Error(), tc.want)
			}
		})
	}
}
