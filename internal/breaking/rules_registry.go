package breaking

import "sort"

// Rule id constants. These are the only place a "break/..." string literal
// is written; rules.go emits findings through these constants so that the
// set here and the set the engine actually produces cannot drift apart —
// see TestRulesRegistryMatchesEngine, which parses rules.go and fails in
// both directions if they ever do.
const (
	RuleFieldRenamed            = "break/field_renamed"
	RuleFieldRemoved            = "break/field_removed"
	RuleFieldNumberChanged      = "break/field_number_changed"
	RuleFieldTypeChanged        = "break/field_type_changed"
	RuleFieldCardinalityChanged = "break/field_cardinality_changed"
	RuleFieldOneofChanged       = "break/field_oneof_changed"
	RuleOneofRenamed            = "break/oneof_renamed"
	RuleEnumValueRenamed        = "break/enum_value_renamed"
	RuleEnumValueRemoved        = "break/enum_value_removed"
	RuleEnumValueNumberChanged  = "break/enum_value_number_changed"
	RuleMessageRemoved          = "break/message_removed"
	RuleEnumRemoved             = "break/enum_removed"
	RuleServiceRemoved          = "break/service_removed"
	RuleMethodRemoved           = "break/method_removed"
	RuleMethodSignatureChanged  = "break/method_signature_changed"
	RuleMethodStreamingChanged  = "break/method_streaming_changed"
	RulePackageRenamed          = "break/package_renamed"
	RulePackageRemoved          = "break/package_removed"
	RuleFileRemoved             = "break/file_removed"
	RuleGoPackageChanged        = "break/go_package_changed"
)

// RuleInfo is one entry in the canonical break/ rule set: what Rules and
// LookupRule return.
type RuleInfo struct {
	// ID is the permanent rule id, "break/<name>".
	ID string
	// Category is the worst Category this rule is ever stamped with. Two
	// rules (field_type_changed, field_oneof_changed) fire as either Wire
	// or Source depending on the specific change; Category records Wire
	// for those, because a manifest permission keyed on Source alone must
	// not silently also permit the Wire-breaking case.
	Category Category
	// HasDiscriminant is true when this rule's findings always carry a
	// non-empty Change (the pair of kinds for a type change, the
	// direction for a cardinality change, and so on), and false when they
	// never do (a removal, a rename with nothing else to discriminate
	// on). A permission for a discriminant-carrying rule is refused
	// without a change; a permission for a rule with none is refused with
	// one.
	HasDiscriminant bool
}

// registry is the hand-maintained ledger of every break/ rule this tool
// emits. TestRulesRegistryMatchesEngine ties it to reality: it parses
// rules.go for the true set of emitted ids and fails if this list is
// missing one or names one that does not exist, and it checks Category and
// HasDiscriminant against the (rule, category, change) fixtures in
// rules_test.go, which record what the engine actually stamps.
var registry = []RuleInfo{
	{RuleFieldRenamed, Source, false},
	{RuleFieldRemoved, Source, false},
	{RuleFieldNumberChanged, Wire, true},
	{RuleFieldTypeChanged, Wire, true},
	{RuleFieldCardinalityChanged, Wire, true},
	{RuleFieldOneofChanged, Wire, true},
	{RuleOneofRenamed, Source, true},
	{RuleEnumValueRenamed, Source, false},
	{RuleEnumValueRemoved, Source, false},
	{RuleEnumValueNumberChanged, Wire, true},
	{RuleMessageRemoved, Source, false},
	{RuleEnumRemoved, Source, false},
	{RuleServiceRemoved, Wire, false},
	{RuleMethodRemoved, Wire, false},
	{RuleMethodSignatureChanged, Wire, true},
	{RuleMethodStreamingChanged, Wire, true},
	{RulePackageRenamed, Wire, false},
	{RulePackageRemoved, Wire, false},
	{RuleFileRemoved, Source, false},
	{RuleGoPackageChanged, Source, true},
}

// Rules returns the canonical break/ rule set, sorted by id.
func Rules() []RuleInfo {
	out := make([]RuleInfo, len(registry))
	copy(out, registry)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// LookupRule reports the RuleInfo for id, and whether id names a rule this
// registry knows about. It only ever matches break/ ids; a lint id such as
// "stele/enum_value_prefix" is out of scope and reports not found.
func LookupRule(id string) (RuleInfo, bool) {
	for _, r := range registry {
		if r.ID == id {
			return r, true
		}
	}
	return RuleInfo{}, false
}
