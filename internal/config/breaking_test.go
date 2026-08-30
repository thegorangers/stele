package config_test

import (
	"strings"
	"testing"

	"github.com/thegorangers/stele/internal/breaking"
	"github.com/thegorangers/stele/internal/config"
)

// This package's tests validate breaking.rules and breaking.allow entries
// against the real rule registry, not a second hand-written list. See
// config.BreakingRuleFact for why this is wired at init rather than by a
// direct import inside the config package.
func init() {
	rules := breaking.Rules()
	facts := make([]config.BreakingRuleFact, len(rules))
	for i, r := range rules {
		facts[i] = config.BreakingRuleFact{ID: r.ID, HasDiscriminant: r.HasDiscriminant}
	}
	config.RegisterBreakingRuleFacts(facts)
}

func TestLoad_NoBreakingBlockIsNil(t *testing.T) {
	f := mustLoad(t, "version: 1\nmodules:\n  - path: api\n")
	if f.Breaking != nil {
		t.Fatalf("Breaking = %#v, want nil: absence must not become \"everything off\"", f.Breaking)
	}
}

func TestLoad_BreakingValid(t *testing.T) {
	f := mustLoad(t, `
version: 1
modules:
  - path: api
breaking:
  base: master
  rules:
    - id: break/field_type_changed
      severity: off
      reason: consumers pin us at fixed points; a rename never lands unnoticed
    - id: break/field_renamed
      severity: warning
      reason: source-only, on a probationary period while adoption completes
    - id: break/message_removed
      severity: error
  allow:
    - rule: break/field_type_changed
      subject: example.orders.v1.Order.total
      change: int32 -> int64
      reason: widening; no consumer stores this in a 32-bit field
    - rule: break/message_removed
      subject: example.orders.v1.Draft
      reason: dead type, never shipped to a consumer
`)
	if f.Breaking == nil {
		t.Fatal("Breaking is nil, want a parsed block")
	}
	if f.Breaking.Base != "master" {
		t.Fatalf("Base = %q, want master", f.Breaking.Base)
	}
	if len(f.Breaking.Rules) != 3 {
		t.Fatalf("Rules = %d entries, want 3", len(f.Breaking.Rules))
	}
	if len(f.Breaking.Allow) != 2 {
		t.Fatalf("Allow = %d entries, want 2", len(f.Breaking.Allow))
	}
}

// reason is NOT required on a rule left at error.
func TestLoad_BreakingRuleAtErrorNoReasonRequired(t *testing.T) {
	mustLoad(t, `
version: 1
modules:
  - path: api
breaking:
  rules:
    - id: break/message_removed
      severity: error
`)
}

func TestLoad_BreakingInvalid(t *testing.T) {
	for _, tc := range []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "unknown key at breaking level",
			yaml: "version: 1\nmodules:\n  - path: api\nbreaking:\n  bogus: true\n",
			want: "bogus",
		},
		{
			name: "unknown key in a rules entry",
			yaml: "version: 1\nmodules:\n  - path: api\nbreaking:\n  rules:\n    - id: break/message_removed\n      level: warning\n",
			want: "level",
		},
		{
			name: "unknown key in an allow entry",
			yaml: "version: 1\nmodules:\n  - path: api\nbreaking:\n  allow:\n    - rule: break/message_removed\n      subject: example.orders.v1.Draft\n      reason: dead type\n      note: nope\n",
			want: "note",
		},
		{
			name: "severity outside error|warning|off",
			yaml: "version: 1\nmodules:\n  - path: api\nbreaking:\n  rules:\n    - id: break/message_removed\n      severity: relaxed\n",
			want: "error, warning, off",
		},
		{
			name: "two rule entries naming one id",
			yaml: "version: 1\nmodules:\n  - path: api\nbreaking:\n  rules:\n    - id: break/message_removed\n      severity: error\n    - id: break/message_removed\n      severity: warning\n      reason: because\n",
			want: "duplicate",
		},
		{
			name: "rule at warning with no reason",
			yaml: "version: 1\nmodules:\n  - path: api\nbreaking:\n  rules:\n    - id: break/message_removed\n      severity: warning\n",
			want: "reason",
		},
		{
			name: "rule at off with no reason",
			yaml: "version: 1\nmodules:\n  - path: api\nbreaking:\n  rules:\n    - id: break/message_removed\n      severity: \"off\"\n",
			want: "reason",
		},
		{
			name: "rule id no rule carries",
			yaml: "version: 1\nmodules:\n  - path: api\nbreaking:\n  rules:\n    - id: break/no_such_rule\n      severity: error\n",
			want: "break/no_such_rule",
		},
		{
			name: "permission with no reason",
			yaml: "version: 1\nmodules:\n  - path: api\nbreaking:\n  allow:\n    - rule: break/message_removed\n      subject: example.orders.v1.Draft\n",
			want: "reason",
		},
		{
			name: "permission for discriminant rule with no change",
			yaml: "version: 1\nmodules:\n  - path: api\nbreaking:\n  allow:\n    - rule: break/field_type_changed\n      subject: example.orders.v1.Order.total\n      reason: widening\n",
			want: "change",
		},
		{
			name: "permission for non-discriminant rule with a change",
			yaml: "version: 1\nmodules:\n  - path: api\nbreaking:\n  allow:\n    - rule: break/message_removed\n      subject: example.orders.v1.Draft\n      change: something\n      reason: because\n",
			want: "change",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.Load(write(t, tc.yaml))
			if err == nil {
				t.Fatalf("want an error naming %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not name %q", err, tc.want)
			}
		})
	}
}
