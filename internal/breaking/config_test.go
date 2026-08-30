package breaking_test

import (
	"strings"
	"testing"

	"github.com/thegorangers/stele/internal/breaking"
	"github.com/thegorangers/stele/internal/config"
)

func TestValidateConfig_Nil(t *testing.T) {
	if err := breaking.ValidateConfig(nil); err != nil {
		t.Fatalf("ValidateConfig(nil) = %v, want nil", err)
	}
}

func TestValidateConfig_Valid(t *testing.T) {
	b := &config.Breaking{
		Rules: []config.BreakingRule{
			{ID: "break/message_removed", Severity: "error"},
		},
		Allow: []config.Permission{
			{
				Rule: "break/field_type_changed", Subject: "example.orders.v1.Order.total",
				Change: "int32 -> int64", Reason: "widening",
			},
			{
				Rule: "break/message_removed", Subject: "example.orders.v1.Draft",
				Reason: "dead type, never shipped to a consumer",
			},
		},
	}
	if err := breaking.ValidateConfig(b); err != nil {
		t.Fatalf("ValidateConfig: unexpected error: %v", err)
	}
}

func TestValidateConfig_Invalid(t *testing.T) {
	for _, tc := range []struct {
		name string
		b    *config.Breaking
		want string
	}{
		{
			name: "rule id no rule carries",
			b: &config.Breaking{Rules: []config.BreakingRule{
				{ID: "break/no_such_rule", Severity: "error"},
			}},
			want: "break/no_such_rule",
		},
		{
			name: "permission naming a rule no rule carries",
			b: &config.Breaking{Allow: []config.Permission{
				{Rule: "break/no_such_rule", Subject: "example.orders.v1.Draft", Reason: "because"},
			}},
			want: "break/no_such_rule",
		},
		{
			name: "permission for discriminant rule with no change",
			b: &config.Breaking{Allow: []config.Permission{
				{Rule: "break/field_type_changed", Subject: "example.orders.v1.Order.total", Reason: "widening"},
			}},
			want: "breaking.allow[0].change",
		},
		{
			name: "permission for non-discriminant rule with a change",
			b: &config.Breaking{Allow: []config.Permission{
				{Rule: "break/message_removed", Subject: "example.orders.v1.Draft", Change: "something", Reason: "because"},
			}},
			want: "breaking.allow[0].change",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := breaking.ValidateConfig(tc.b)
			if err == nil {
				t.Fatalf("want an error naming %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not name %q", err, tc.want)
			}
		})
	}
}
