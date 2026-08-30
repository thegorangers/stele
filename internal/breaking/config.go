package breaking

import (
	"fmt"
	"sort"
	"strings"

	"github.com/thegorangers/stele/internal/config"
)

// ValidateConfig checks a manifest's breaking block against the rules this
// tool actually has: that every rules[] and allow[] entry names a rule the
// registry carries, and that a permission's change field is present or
// absent as the named rule's discriminant requires.
//
// This lives here, not in internal/config, for the same reason
// internal/lint's rule ids are checked for existence in the lint engine and
// not in the manifest parser: internal/config describes the shape a
// manifest may take, and the set of rules that exist is a property of the
// engine. It is also the only place the check can live without an import
// cycle — internal/breaking already imports internal/config to load a
// manifest when resolving a previous revision, so internal/config cannot
// import this package back.
//
// A manifest is parsed once and validated for shape by config.Load before
// this ever runs, so there is no source position left to report by the time
// an id or a permission reaches here — config.Load's own field-path errors
// (breaking.rules[0].id, and so on) are in the same position, for the same
// reason: only a raw YAML node, not a decoded Go value, carries a line
// number, and decodeStrict's line-numbered errors are for exactly the keys
// that are still nodes when they are checked. What this function reports
// instead is the field path and the offending id or change, which is what a
// reader searches the file for.
func ValidateConfig(b *config.Breaking) error {
	if b == nil {
		return nil
	}
	for i, r := range b.Rules {
		if _, ok := LookupRule(r.ID); !ok {
			return fmt.Errorf("breaking.rules[%d].id: %s is not a rule this tool has (known: %s)",
				i, r.ID, strings.Join(knownRuleIDs(), ", "))
		}
	}
	for i, p := range b.Allow {
		field := fmt.Sprintf("breaking.allow[%d]", i)
		info, ok := LookupRule(p.Rule)
		if !ok {
			return fmt.Errorf("%s.rule: %s is not a rule this tool has (known: %s)",
				field, p.Rule, strings.Join(knownRuleIDs(), ", "))
		}
		switch {
		case info.HasDiscriminant && p.Change == "":
			return fmt.Errorf("%s.change: missing; %s carries a discriminant beyond its subject, and a "+
				"permission without it is refused rather than treated as matching anything", field, p.Rule)
		case !info.HasDiscriminant && p.Change != "":
			return fmt.Errorf("%s.change: %q is written, but %s has no discriminant beyond its subject; "+
				"removals have nothing to discriminate on beyond the subject itself", field, p.Change, p.Rule)
		}
	}
	return nil
}

// knownRuleIDs lists every rule id this tool has, sorted, for an error
// message that names what was actually meant.
func knownRuleIDs() []string {
	rs := Rules()
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.ID
	}
	sort.Strings(out)
	return out
}
