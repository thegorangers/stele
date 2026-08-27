package rule_test

import (
	"testing"

	"github.com/thegorangers/stele/rule"
)

// TestReservedNamespaces holds the reservations this tool makes over the rule
// ID space. A namespace is free until somebody publishes a rule in it, and
// reserving one afterwards is a rename of somebody else's rule ID — which
// under RELEASING.md does not fail loudly, it silently stops matching the
// ignore list that named it. So the reservation is made before the first rule
// in the namespace ships, not after.
func TestReservedNamespaces(t *testing.T) {
	if rule.NamespaceAIP != "aip" {
		t.Errorf("the AIP namespace is %q, and it is a published contract", rule.NamespaceAIP)
	}
	for _, ns := range []string{rule.NamespaceBuiltin, rule.NamespaceAIP} {
		if !rule.Reserved(ns) {
			t.Errorf("%q must be reserved: a third-party rule that claimed it could take over the "+
				"configuration of a rule that ships here", ns)
		}
	}
	for _, ns := range []string{"house", "example", "aips", "stele_house"} {
		if rule.Reserved(ns) {
			t.Errorf("%q is not this tool's to reserve", ns)
		}
	}
}
