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
// missing one or names one that does not exist, and it checks
// HasDiscriminant against the (rule, category, change) fixtures in
// rules_test.go, which record what the engine actually stamps.
//
// There is deliberately no Category field here. Two of these rules
// (field_type_changed, field_oneof_changed) do not have a single category —
// their findings do, varying by the specific change — so a per-rule
// Category would have to be documented as "the worst case this rule is
// ever stamped with" for those two, which is a field admitting it cannot
// answer the question it's named after. The only reader of a category is
// report.go, and it already reads Finding.Category, the per-finding value,
// which is correct.
var registry = []RuleInfo{
	{RuleFieldRenamed, false},
	{RuleFieldRemoved, false},
	{RuleFieldNumberChanged, true},
	{RuleFieldTypeChanged, true},
	{RuleFieldCardinalityChanged, true},
	{RuleFieldOneofChanged, true},
	{RuleOneofRenamed, true},
	{RuleEnumValueRenamed, false},
	{RuleEnumValueRemoved, false},
	{RuleEnumValueNumberChanged, true},
	{RuleMessageRemoved, false},
	{RuleEnumRemoved, false},
	{RuleServiceRemoved, false},
	{RuleMethodRemoved, false},
	{RuleMethodSignatureChanged, true},
	{RuleMethodStreamingChanged, true},
	{RulePackageRenamed, false},
	{RulePackageRemoved, false},
	{RuleFileRemoved, false},
	{RuleGoPackageChanged, true},
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
