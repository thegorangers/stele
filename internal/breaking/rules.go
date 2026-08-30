package breaking

import (
	"fmt"
	"sort"

	"github.com/thegorangers/stele/internal/lint"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Category says whether a finding breaks the wire encoding itself, or only
// breaks source compatibility (generated code, imports) while the wire
// encoding stays readable.
type Category int

const (
	// Wire means an old and a new binary that exchange this message no
	// longer agree on what the bytes mean.
	Wire Category = iota
	// Source means every wire-compatible peer still decodes the same
	// bytes correctly, but code generated from this contract no longer
	// compiles, or no longer resolves the same import.
	Source
)

// Finding is one breakage Classify attributes to a permanent rule id.
type Finding struct {
	// Rule is the permanent id, "break/<name>". It is a public contract:
	// renaming it silently stops matching whatever manifest entry named it.
	Rule string
	// Category is Wire or Source; see their doc comments.
	Category Category
	// Subject is the full name of the declaration the finding is about
	// (or, for a file-level finding, "file:"+path — see Change.Subject).
	Subject string
	// Path is the import path of the file Subject is declared in.
	Path string
	// Pos is where in cur's file Subject is declared, the zero Position
	// when there is nothing in cur to point at (a removal).
	Pos lint.Position
	// Message is a human-readable description of the finding.
	Message string
	// Fix is a human-readable suggestion for how to resolve the finding,
	// where one exists.
	Fix string
	// Change is the discriminant a permission matches on: the pair of
	// kinds for a type change, the destination for a oneof change, the
	// direction for a cardinality change. A removal (and a rename, which
	// is a removal plus an addition with nothing else to discriminate on)
	// has no discriminant, and Change stays empty.
	Change string
	// Closure reports whether this finding came from the resolved closure
	// reachable from owned modules' imports (closure.go), rather than from
	// this repository's own owned files. It is a field, not a
	// message-string convention, because a breakage in a dependency is a
	// different thing to act on than a breakage in this repository's own
	// contract — it cannot be fixed by editing a file here, only by not
	// taking the pin bump, or by taking it deliberately — and the report
	// task needs to render that distinction without parsing prose.
	Closure bool
}

// Classify turns Diff's neutral changes into findings, each carrying a
// permanent rule id.
//
// The rename rule, stated once here because two permanent ids would
// otherwise contradict each other: a removal and an addition within the
// same parent are reported as a rename ONLY when the declaration has a
// number to key on — a field or an enum value — and that number, its type
// and its cardinality (a field's oneof membership counts as part of its
// shape too) all match while only the name differs. A message, enum,
// service or method has no identity beyond its name: shape is not identity
// (two empty messages have identical shape; pairing them by shape alone can
// match the wrong two members of an unrelated batch of removals and
// additions and silently hide a real removal, which is the expensive
// direction to be wrong in). So those four kinds are never paired — a
// renamed message, enum, service or method is reported as a removal plus an
// addition, and the addition is never a finding on its own, exactly as an
// unrelated removal and addition would be. break/package_renamed is the one
// exception with no number to key on that still pairs: it requires an exact
// match of the whole top-level name *set* moving to one specific new
// package, which is a far stronger signal than shape, and it degrades
// loudly to break/package_removed the moment that exact match fails.
func Classify(changes []Change, prev, cur Revision) []Finding {
	var findings []Finding

	byKindParent := groupByKindAndParent(changes)

	packageFindings, swallowedPackages := classifyPackages(prev, cur)

	// declSwallowed holds the full name of every message, enum or service
	// that Diff reported Removed for any reason — outright removal, or as
	// the old half of a rename. Its children (fields, enum values, methods,
	// and any nested message/enum) are never classified on their own: they
	// would otherwise be reported as unrelated removals/additions (or,
	// worse, spuriously paired into renames of their own) on top of the
	// finding already reported for the container itself, and the same
	// double-counting break/file_removed is written to avoid applies one
	// level down.
	declSwallowed := make(map[protoreflect.FullName]bool)
	for _, c := range changes {
		if c.Kind != Removed || c.Before == nil {
			continue
		}
		switch kindOf(c.Before) {
		case declMessage, declEnum, declService:
			declSwallowed[c.Before.FullName()] = true
		}
	}

	// A message/enum/service directly under a package that was itself
	// reported package_removed or package_renamed is swallowed the same
	// way, even though "package" is not a declaration Diff indexes.
	skipContainer := func(parent protoreflect.FullName) bool {
		return declSwallowed[parent] || swallowedPackages[parent]
	}
	skipMember := func(parent protoreflect.FullName) bool {
		return declSwallowed[parent]
	}

	oneofFindings, oneofRenames := classifyOneofs(byKindParent, skipMember)

	// skipFieldOneofRename reports whether a field's oneof move from
	// before to after, within parent, is exactly a rename classifyOneofs
	// already reported at the container level — see oneofRename's doc
	// comment.
	skipFieldOneofRename := func(parent, before, after protoreflect.FullName) bool {
		for _, r := range oneofRenames {
			if r.parent == parent && r.before == before && r.after == after {
				return true
			}
		}
		return false
	}

	findings = append(findings, classifyFields(byKindParent, changes, skipMember, skipFieldOneofRename)...)
	findings = append(findings, oneofFindings...)
	findings = append(findings, classifyEnumValues(byKindParent, changes, skipMember)...)
	findings = append(findings, classifyMessages(byKindParent, skipContainer)...)
	findings = append(findings, classifyEnums(byKindParent, skipContainer)...)
	findings = append(findings, classifyServices(byKindParent, skipContainer)...)
	findings = append(findings, classifyMethods(byKindParent, changes, skipMember)...)
	findings = append(findings, packageFindings...)
	findings = append(findings, classifyFiles(changes, prev)...)
	findings = append(findings, classifyGoPackage(prev, cur)...)

	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Subject != findings[j].Subject {
			return findings[i].Subject < findings[j].Subject
		}
		return findings[i].Rule < findings[j].Rule
	})
	return findings
}

// declKind is the kind of protobuf declaration a Change's descriptor is.
type declKind int

const (
	declOther declKind = iota
	declField
	declEnumValue
	declMessage
	declEnum
	declService
	declMethod
	declOneof
)

func kindOf(d protoreflect.Descriptor) declKind {
	switch v := d.(type) {
	case protoreflect.FieldDescriptor:
		return declField
	case protoreflect.EnumValueDescriptor:
		return declEnumValue
	case protoreflect.MessageDescriptor:
		return declMessage
	case protoreflect.EnumDescriptor:
		return declEnum
	case protoreflect.ServiceDescriptor:
		return declService
	case protoreflect.MethodDescriptor:
		return declMethod
	case protoreflect.OneofDescriptor:
		if v.IsSynthetic() {
			// A proto3 `optional` field's compiler-synthesised oneof is
			// not a declaration the author wrote; it must never be
			// grouped, paired or reported as break/oneof_renamed. See
			// effectiveOneofDescriptor's comment for the same rule
			// applied to field_oneof_changed.
			return declOther
		}
		return declOneof
	}
	return declOther
}

// parentKey groups changes for pairing: same declaration kind, same parent
// full name.
type parentKey struct {
	kind   declKind
	parent protoreflect.FullName
}

// grouped holds, per parentKey, the removed and added changes seen there.
type grouped struct {
	removed []Change
	added   []Change
}

// declParent returns the full name of d's actual containing declaration,
// for grouping and rename-pairing purposes. This is d.FullName().Parent()
// for every kind except enum value: an enum value's FullName is scoped to
// the enum's own parent (its package or message), not to the enum itself —
// protobuf does not qualify an enum value's name with its enum's name — so
// FullName().Parent() would put every enum's values in one group with each
// other and with any sibling declaration in that scope. d.Parent() gives
// the actual EnumDescriptor instead.
func declParent(d protoreflect.Descriptor) protoreflect.FullName {
	if _, ok := d.(protoreflect.EnumValueDescriptor); ok {
		if p := d.Parent(); p != nil {
			return p.FullName()
		}
	}
	return d.FullName().Parent()
}

func groupByKindAndParent(changes []Change) map[parentKey]*grouped {
	out := make(map[parentKey]*grouped)
	for _, c := range changes {
		if c.Kind == Modified {
			continue
		}
		var d protoreflect.Descriptor
		if c.Kind == Removed {
			d = c.Before
		} else {
			d = c.After
		}
		if d == nil {
			continue // file-level change, not a declaration
		}
		k := kindOf(d)
		if k == declOther {
			continue
		}
		key := parentKey{kind: k, parent: declParent(d)}
		g := out[key]
		if g == nil {
			g = &grouped{}
			out[key] = g
		}
		if c.Kind == Removed {
			g.removed = append(g.removed, c)
		} else {
			g.added = append(g.added, c)
		}
	}
	return out
}

// paired reports whether removed and added are matched by shape, per the
// rename rule for their kind. match decides shape equality.
func pairRenames(g *grouped, match func(before, after protoreflect.Descriptor) bool) (pairs []struct{ rem, add Change }, usedRem, usedAdd map[int]bool) {
	usedRem = make(map[int]bool)
	usedAdd = make(map[int]bool)
	for ri, r := range g.removed {
		for ai, a := range g.added {
			if usedAdd[ai] {
				continue
			}
			if match(r.Before, a.After) {
				pairs = append(pairs, struct{ rem, add Change }{r, a})
				usedRem[ri] = true
				usedAdd[ai] = true
				break
			}
		}
	}
	return pairs, usedRem, usedAdd
}

// ---- fields ----

func classifyFields(byKindParent map[parentKey]*grouped, changes []Change, skip func(protoreflect.FullName) bool, skipOneofRename func(parent, before, after protoreflect.FullName) bool) []Finding {
	var findings []Finding

	renamedField := make(map[string]bool) // Subject of the removed field

	for key, g := range byKindParent {
		if key.kind != declField {
			continue
		}
		if skip(key.parent) {
			continue
		}
		pairs, usedRem, usedAdd := pairRenames(g, fieldShapeEqual)
		for _, p := range pairs {
			renamedField[p.rem.Subject] = true
			findings = append(findings, Finding{
				Rule:     RuleFieldRenamed,
				Category: Source,
				Subject:  p.add.Subject,
				Path:     p.add.Path,
				Pos:      p.add.Pos,
				Message:  "field " + p.rem.Subject + " was renamed to " + p.add.Subject,
				Fix:      "confirm this is a rename, not a coincidental match; a consumer that reads by name, not by field number, still breaks until it is updated",
			})
		}
		for i, r := range g.removed {
			if usedRem[i] {
				continue
			}
			findings = append(findings, Finding{
				Rule:     RuleFieldRemoved,
				Category: Source,
				Subject:  r.Subject,
				Path:     r.Path,
				Pos:      r.Pos,
				Message:  "field " + r.Subject + " was removed",
				Fix:      "restore the field, or reserve its number and name so it is never reused",
			})
		}
		_ = usedAdd // additions are never findings on their own
	}

	for _, c := range changes {
		if c.Kind != Modified {
			continue
		}
		before, ok1 := c.Before.(protoreflect.FieldDescriptor)
		after, ok2 := c.After.(protoreflect.FieldDescriptor)
		if !ok1 || !ok2 {
			continue
		}

		if before.Number() != after.Number() {
			findings = append(findings, Finding{
				Rule:     RuleFieldNumberChanged,
				Category: Wire,
				Subject:  c.Subject,
				Path:     c.Path,
				Pos:      c.Pos,
				Message:  "field " + c.Subject + " changed its field number",
				Change:   fmt.Sprintf("%d -> %d", before.Number(), after.Number()),
				Fix:      "restore the original field number; renumbering an existing field is never safe on the wire",
			})
		}

		if before.Kind() != after.Kind() {
			cat := Wire
			if WireCompatible(before.Kind(), after.Kind()) {
				cat = Source
			}
			findings = append(findings, Finding{
				Rule:     RuleFieldTypeChanged,
				Category: cat,
				Subject:  c.Subject,
				Path:     c.Path,
				Pos:      c.Pos,
				Message:  "field " + c.Subject + " changed type from " + before.Kind().String() + " to " + after.Kind().String(),
				Change:   before.Kind().String() + " -> " + after.Kind().String(),
				Fix:      "revert the type, or add a new field instead of changing an existing one's type",
			})
		}

		if before.Cardinality() != after.Cardinality() {
			findings = append(findings, Finding{
				Rule:     RuleFieldCardinalityChanged,
				Category: Wire,
				Subject:  c.Subject,
				Path:     c.Path,
				Pos:      c.Pos,
				Message:  "field " + c.Subject + " changed cardinality from " + before.Cardinality().String() + " to " + after.Cardinality().String(),
				Change:   before.Cardinality().String() + " -> " + after.Cardinality().String(),
				Fix:      "revert the cardinality, or add a new field instead of changing an existing one's plurality",
			})
		}

		beforeOneofDesc := effectiveOneofDescriptor(before)
		afterOneofDesc := effectiveOneofDescriptor(after)
		beforeOneof := oneofDescName(beforeOneofDesc)
		afterOneof := oneofDescName(afterOneofDesc)
		if beforeOneof != afterOneof {
			var beforeFull, afterFull protoreflect.FullName
			if beforeOneofDesc != nil {
				beforeFull = beforeOneofDesc.FullName()
			}
			if afterOneofDesc != nil {
				afterFull = afterOneofDesc.FullName()
			}
			// A move that IS the container-level rename classifyOneofs
			// already reported is not a second, field-level fact about a
			// different thing — see oneofRename's doc comment. A field
			// whose before/after is not exactly that pair (joining or
			// leaving the renamed oneof from/to somewhere else) still
			// reports, because that genuinely is a different fact.
			if !skipOneofRename(before.Parent().FullName(), beforeFull, afterFull) {
				cat := oneofChangeCategory(beforeOneofDesc, afterOneofDesc)
				beforeLabel, afterLabel := beforeOneof, afterOneof
				if beforeLabel == "" {
					beforeLabel = "none"
				}
				if afterLabel == "" {
					afterLabel = "none"
				}
				findings = append(findings, Finding{
					Rule:     RuleFieldOneofChanged,
					Category: cat,
					Subject:  c.Subject,
					Path:     c.Path,
					Pos:      c.Pos,
					Message:  "field " + c.Subject + " moved oneof membership from " + beforeLabel + " to " + afterLabel,
					Change:   beforeLabel + " -> " + afterLabel,
					Fix:      "revert the oneof membership, or add a new field instead of moving an existing one between oneofs",
				})
			}
		}
	}

	return findings
}

// effectiveOneofDescriptor reports the real (non-synthetic) oneof a field
// belongs to, or nil if it belongs to none. A proto3 optional field's
// compiler-synthesised oneof is not a real declaration the author wrote, so
// it is not part of what field_oneof_changed reacts to.
func effectiveOneofDescriptor(f protoreflect.FieldDescriptor) protoreflect.OneofDescriptor {
	o := f.ContainingOneof()
	if o == nil || o.IsSynthetic() {
		return nil
	}
	return o
}

// effectiveOneof reports the name of the real (non-synthetic) oneof a field
// belongs to, or "" if it belongs to none.
func effectiveOneof(f protoreflect.FieldDescriptor) string {
	return oneofDescName(effectiveOneofDescriptor(f))
}

func oneofDescName(o protoreflect.OneofDescriptor) string {
	if o == nil {
		return ""
	}
	return string(o.Name())
}

// oneofChangeCategory decides break/field_oneof_changed's Category from the
// field's oneof membership before and after. There is one rule, applied
// uniformly to every direction (join, leave, or move between two distinct
// oneofs): does sibling-clearing semantics change on the wire? A oneof only
// clears a sibling on decode when it has one — a member beyond the field
// under discussion. So the category depends solely on whether EITHER side's
// oneof (when it has one) has such a sibling, never on which of the three
// directions this is:
//
//   - If either side's oneof has another member, the answer is yes: before,
//     a decoder receiving two of that oneof's fields on the wire (from an
//     old-schema peer, or a malformed/adversarial message) kept both;
//     after, it keeps only the last one, on at least one side of the
//     change. That is Wire.
//   - If neither side's oneof (where present) has any other member, the
//     answer is no in both directions: there was never a sibling to clear,
//     so a decoder behaves identically byte for byte, including a move
//     between two distinct SINGLETON oneofs. Only the generated accessor's
//     shape changes. That is Source.
func oneofChangeCategory(before, after protoreflect.OneofDescriptor) Category {
	if before != nil && before.Fields().Len() > 1 {
		return Wire
	}
	if after != nil && after.Fields().Len() > 1 {
		return Wire
	}
	return Source
}

// fieldShapeEqual is the rename rule for fields: same number, kind,
// cardinality AND oneof membership. Oneof membership is part of the shape,
// not just the name, so "string a = 1;" becoming
// "oneof choice { string b = 1; }" is never paired into a rename — the move
// into the oneof is a real, separate fact, not something a rename finding
// should absorb and hide.
func fieldShapeEqual(before, after protoreflect.Descriptor) bool {
	b, ok1 := before.(protoreflect.FieldDescriptor)
	a, ok2 := after.(protoreflect.FieldDescriptor)
	if !ok1 || !ok2 {
		return false
	}
	return b.Number() == a.Number() && b.Kind() == a.Kind() && b.Cardinality() == a.Cardinality() &&
		effectiveOneof(b) == effectiveOneof(a)
}

// ---- oneofs ----

// classifyOneofs pairs a removed oneof with an added oneof in the same
// message as a rename, and reports break/oneof_renamed for the pair. A
// oneof's name is not encoded on the wire — only each member field's
// oneof_index, a small integer, is — so the bytes a renamed oneof produces
// are identical before and after. What breaks is generated code: protoc-gen-go
// derives the wrapper type (M_Pick -> M_Choose) and the getter (GetPick)
// from the oneof's name, so every consumer using either identifier stops
// compiling. That is exactly Source: the wire stays readable, only
// generated code and the identifiers it exports change.
//
// This reintroduces exactly the kind of pairing that classifyMessages,
// classifyEnums, classifyServices and classifyMethods deliberately do NOT
// do — see the comment on Classify. Those four container kinds were pairing
// by "shape", and shape is not identity for them: two empty messages, two
// enums with no values, two services with no methods are indistinguishable,
// so pairing by shape there can marry two unrelated declarations and hide a
// real removal underneath a false rename.
//
// A oneof does not have that problem, because a oneof's "shape" here is
// specifically defined as the SET of field numbers its members occupy, and
// that set is an unambiguous identity, not a resemblance. A field belongs
// to at most one oneof in its message — protobuf's oneof_index on
// FieldDescriptorProto is a single value, never a set — so any two oneofs
// declared in the same message have disjoint field-number sets by
// construction. An exact match between a removed oneof's member numbers and
// an added oneof's member numbers therefore cannot be confused with a
// second candidate: no other oneof sharing that message could share even
// one of those numbers. That is the one thing the dropped container
// pairings never had.
//
// If the member set differs at all — a field joined, left, or the oneof was
// otherwise restructured alongside the rename — this deliberately does NOT
// fire. break/field_oneof_changed already reports the field-level truth for
// whichever fields actually moved; firing a oneof rename on top of that
// would tell two different stories about the same restructuring.
func classifyOneofs(byKindParent map[parentKey]*grouped, skip func(protoreflect.FullName) bool) ([]Finding, []oneofRename) {
	var findings []Finding
	var renames []oneofRename

	for key, g := range byKindParent {
		if key.kind != declOneof {
			continue
		}
		if skip(key.parent) {
			continue
		}
		pairs, _, _ := pairRenames(g, oneofFieldSetEqual)
		for _, p := range pairs {
			before, _ := p.rem.Before.(protoreflect.OneofDescriptor)
			after, _ := p.add.After.(protoreflect.OneofDescriptor)
			findings = append(findings, Finding{
				Rule:     RuleOneofRenamed,
				Category: Source,
				Subject:  p.add.Subject,
				Path:     p.add.Path,
				Pos:      p.add.Pos,
				Message:  "oneof " + p.rem.Subject + " was renamed to " + p.add.Subject,
				Change:   oneofDescName(before) + " -> " + oneofDescName(after),
				Fix:      "revert the oneof's name, or add a new oneof instead of renaming an existing one — the generated wrapper type and getter are derived from its name and every caller using them stops compiling",
			})
			renames = append(renames, oneofRename{
				parent: key.parent,
				before: before.FullName(),
				after:  after.FullName(),
			})
		}
	}

	return findings, renames
}

// oneofRename is one message's oneof rename, as classifyOneofs paired it.
// classifyFields uses it to suppress break/field_oneof_changed for exactly
// the fields whose oneof move IS that rename — the same underlying fact the
// container-level finding already reported, not a fact of its own. A field
// whose membership changed some other way (into the renamed oneof from
// elsewhere, or out of it to somewhere else) still reports normally,
// because before/after there is not this exact pair.
type oneofRename struct {
	parent protoreflect.FullName
	before protoreflect.FullName
	after  protoreflect.FullName
}

// oneofFieldNumbers returns the set of field numbers o's members occupy.
func oneofFieldNumbers(o protoreflect.OneofDescriptor) map[protoreflect.FieldNumber]bool {
	fields := o.Fields()
	set := make(map[protoreflect.FieldNumber]bool, fields.Len())
	for i := 0; i < fields.Len(); i++ {
		set[fields.Get(i).Number()] = true
	}
	return set
}

// oneofFieldSetEqual is the rename rule for oneofs: identical member
// field-number sets. See classifyOneofs for why that is a sound rename
// pairing where the dropped container pairings were not.
func oneofFieldSetEqual(before, after protoreflect.Descriptor) bool {
	b, ok1 := before.(protoreflect.OneofDescriptor)
	a, ok2 := after.(protoreflect.OneofDescriptor)
	if !ok1 || !ok2 {
		return false
	}
	bs := oneofFieldNumbers(b)
	as := oneofFieldNumbers(a)
	if len(bs) == 0 {
		// protoc refuses to compile an empty oneof today, so this is
		// unreachable in practice — but that unreachability belongs to
		// protoc, not to this rule. Two empty sets are exactly the "no
		// sharper than shape" case that made pairing unsound for the four
		// dropped container kinds, so the guard is explicit here rather
		// than left to rest on a constraint this engine does not own.
		return false
	}
	if len(bs) != len(as) {
		return false
	}
	for n := range bs {
		if !as[n] {
			return false
		}
	}
	return true
}

// ---- enum values ----

func classifyEnumValues(byKindParent map[parentKey]*grouped, changes []Change, skip func(protoreflect.FullName) bool) []Finding {
	var findings []Finding

	for key, g := range byKindParent {
		if key.kind != declEnumValue {
			continue
		}
		if skip(key.parent) {
			continue
		}
		pairs, usedRem, _ := pairRenames(g, func(before, after protoreflect.Descriptor) bool {
			b, ok1 := before.(protoreflect.EnumValueDescriptor)
			a, ok2 := after.(protoreflect.EnumValueDescriptor)
			return ok1 && ok2 && b.Number() == a.Number()
		})
		for _, p := range pairs {
			findings = append(findings, Finding{
				Rule:     RuleEnumValueRenamed,
				Category: Source,
				Subject:  p.add.Subject,
				Path:     p.add.Path,
				Pos:      p.add.Pos,
				Message:  "enum value " + p.rem.Subject + " was renamed to " + p.add.Subject,
				Fix:      "confirm this is a rename; a consumer that reads by name, not by number, still breaks until it is updated",
			})
		}
		for i, r := range g.removed {
			if usedRem[i] {
				continue
			}
			findings = append(findings, Finding{
				Rule:     RuleEnumValueRemoved,
				Category: Source,
				Subject:  r.Subject,
				Path:     r.Path,
				Pos:      r.Pos,
				Message:  "enum value " + r.Subject + " was removed",
				Fix:      "restore the enum value, or reserve its number and name so it is never reused",
			})
		}
	}

	for _, c := range changes {
		if c.Kind != Modified {
			continue
		}
		before, ok1 := c.Before.(protoreflect.EnumValueDescriptor)
		after, ok2 := c.After.(protoreflect.EnumValueDescriptor)
		if !ok1 || !ok2 {
			continue
		}
		if before.Number() != after.Number() {
			findings = append(findings, Finding{
				Rule:     RuleEnumValueNumberChanged,
				Category: Wire,
				Subject:  c.Subject,
				Path:     c.Path,
				Pos:      c.Pos,
				Message:  "enum value " + c.Subject + " changed its number",
				Change:   fmt.Sprintf("%d -> %d", before.Number(), after.Number()),
				Fix:      "restore the original enum value number; renumbering an existing value is never safe on the wire",
			})
		}
	}

	return findings
}

// ---- messages ----

// classifyMessages reports break/message_removed. There is no
// break/message_renamed: a message has no number to key a rename pairing
// on, and pairing by shape alone is unsound — see Classify's doc comment.
// A renamed message is reported as a plain removal (and the addition,
// which is never a finding on its own).
func classifyMessages(byKindParent map[parentKey]*grouped, skip func(protoreflect.FullName) bool) []Finding {
	var findings []Finding
	for key, g := range byKindParent {
		if key.kind != declMessage {
			continue
		}
		if skip(key.parent) {
			continue
		}
		for _, r := range g.removed {
			findings = append(findings, Finding{
				Rule:     RuleMessageRemoved,
				Category: Source,
				Subject:  r.Subject,
				Path:     r.Path,
				Pos:      r.Pos,
				Message:  "message " + r.Subject + " was removed",
				Fix:      "restore the message, or keep it declared even once nothing produces it any more",
			})
		}
	}
	return findings
}

// ---- enums ----

// classifyEnums reports break/enum_removed. There is no break/enum_renamed
// for the same reason there is no break/message_renamed — see Classify's
// doc comment.
func classifyEnums(byKindParent map[parentKey]*grouped, skip func(protoreflect.FullName) bool) []Finding {
	var findings []Finding
	for key, g := range byKindParent {
		if key.kind != declEnum {
			continue
		}
		if skip(key.parent) {
			continue
		}
		for _, r := range g.removed {
			findings = append(findings, Finding{
				Rule:     RuleEnumRemoved,
				Category: Source,
				Subject:  r.Subject,
				Path:     r.Path,
				Pos:      r.Pos,
				Message:  "enum " + r.Subject + " was removed",
				Fix:      "restore the enum, or keep it declared even once nothing produces it any more",
			})
		}
	}
	return findings
}

// ---- services ----

// classifyServices reports break/service_removed. There is no
// break/service_renamed for the same reason there is no
// break/message_renamed — see Classify's doc comment.
func classifyServices(byKindParent map[parentKey]*grouped, skip func(protoreflect.FullName) bool) []Finding {
	var findings []Finding
	for key, g := range byKindParent {
		if key.kind != declService {
			continue
		}
		if skip(key.parent) {
			continue
		}
		for _, r := range g.removed {
			findings = append(findings, Finding{
				Rule:     RuleServiceRemoved,
				Category: Wire,
				Subject:  r.Subject,
				Path:     r.Path,
				Pos:      r.Pos,
				Message:  "service " + r.Subject + " was removed",
				Fix:      "restore the service, or keep it declared even once every method on it is deprecated",
			})
		}
	}
	return findings
}

// ---- methods ----

// classifyMethods reports break/method_removed, break/method_signature_changed
// and break/method_streaming_changed. There is no break/method_renamed for
// the same reason there is no break/message_renamed — see Classify's doc
// comment.
func classifyMethods(byKindParent map[parentKey]*grouped, changes []Change, skip func(protoreflect.FullName) bool) []Finding {
	var findings []Finding

	// A method removed alongside its whole service is already covered by
	// service_removed; only report method_removed when the service itself
	// survives. skip carries exactly that: it is true for the full name of
	// any service Diff reported Removed.
	for key, g := range byKindParent {
		if key.kind != declMethod {
			continue
		}
		if skip(key.parent) {
			continue
		}
		for _, r := range g.removed {
			findings = append(findings, Finding{
				Rule:     RuleMethodRemoved,
				Category: Wire,
				Subject:  r.Subject,
				Path:     r.Path,
				Pos:      r.Pos,
				Message:  "method " + r.Subject + " was removed",
				Fix:      "restore the method, or keep it declared and mark it deprecated instead of deleting it",
			})
		}
	}

	for _, c := range changes {
		if c.Kind != Modified {
			continue
		}
		before, ok1 := c.Before.(protoreflect.MethodDescriptor)
		after, ok2 := c.After.(protoreflect.MethodDescriptor)
		if !ok1 || !ok2 {
			continue
		}
		if before.Input().FullName() != after.Input().FullName() || before.Output().FullName() != after.Output().FullName() {
			findings = append(findings, Finding{
				Rule:     RuleMethodSignatureChanged,
				Category: Wire,
				Subject:  c.Subject,
				Path:     c.Path,
				Pos:      c.Pos,
				Message:  "method " + c.Subject + " changed its input or output type",
				Change: string(before.Input().FullName()) + "/" + string(before.Output().FullName()) +
					" -> " + string(after.Input().FullName()) + "/" + string(after.Output().FullName()),
				Fix: "revert the input or output type, or add a new method instead of changing an existing one's signature",
			})
		}
		if before.IsStreamingClient() != after.IsStreamingClient() || before.IsStreamingServer() != after.IsStreamingServer() {
			beforeLabel := streamingLabel(before.IsStreamingClient(), before.IsStreamingServer())
			afterLabel := streamingLabel(after.IsStreamingClient(), after.IsStreamingServer())
			findings = append(findings, Finding{
				Rule:     RuleMethodStreamingChanged,
				Category: Wire,
				Subject:  c.Subject,
				Path:     c.Path,
				Pos:      c.Pos,
				Message:  "method " + c.Subject + " changed its streaming shape from " + beforeLabel + " to " + afterLabel,
				Change:   beforeLabel + " -> " + afterLabel,
				Fix:      "revert the streaming shape, or add a new method instead of changing an existing one's shape",
			})
		}
	}

	return findings
}

// streamingLabel names an RPC's streaming shape, for break/method_streaming_changed's Change.
func streamingLabel(client, server bool) string {
	switch {
	case client && server:
		return "bidi-streaming"
	case client:
		return "client-streaming"
	case server:
		return "server-streaming"
	default:
		return "unary"
	}
}

// ---- packages ----

// classifyPackages compares the set of packages declared across each
// revision's owned files. A package is removed when no owned file in cur
// declares it any more; it is renamed, rather than removed, when the exact
// same set of top-level declaration simple names that used to live under it
// now lives, complete, under exactly one other package that is new in cur.
func classifyPackages(prev, cur Revision) (findings []Finding, swallowed map[protoreflect.FullName]bool) {
	prevPkgs := topLevelNamesByPackage(prev)
	curPkgs := topLevelNamesByPackage(cur)

	swallowed = make(map[protoreflect.FullName]bool)
	usedCur := make(map[protoreflect.FullName]bool)

	var removedPkgs []protoreflect.FullName
	for p := range prevPkgs {
		if _, ok := curPkgs[p]; !ok {
			removedPkgs = append(removedPkgs, p)
		}
	}
	sort.Slice(removedPkgs, func(i, j int) bool { return removedPkgs[i] < removedPkgs[j] })

	// candidateCurPkgs is every package new in cur (not in prevPkgs at all),
	// sorted: ranging a map directly would let the winning candidate — and
	// so the package name named in the message — vary run to run whenever
	// more than one candidate matches.
	var candidateCurPkgs []protoreflect.FullName
	for q := range curPkgs {
		if _, existedBefore := prevPkgs[q]; !existedBefore {
			candidateCurPkgs = append(candidateCurPkgs, q)
		}
	}
	sort.Slice(candidateCurPkgs, func(i, j int) bool { return candidateCurPkgs[i] < candidateCurPkgs[j] })

	for _, p := range removedPkgs {
		before := prevPkgs[p]
		var renamedTo protoreflect.FullName
		for _, q := range candidateCurPkgs {
			if usedCur[q] {
				continue
			}
			if sameNameSet(before, curPkgs[q]) {
				renamedTo = q
				usedCur[q] = true
				break
			}
		}
		swallowed[p] = true
		// A package-level finding has no single declaration of its own to
		// point at, but leaving Path empty makes String() print a bare
		// leading colon no editor can navigate and no CI annotator can
		// split on. Point at the first owned file that declared the
		// package, prev-side and sorted for determinism.
		path := firstOwnedFileOfPackage(prev, p)
		if renamedTo != "" {
			findings = append(findings, Finding{
				Rule:     RulePackageRenamed,
				Category: Wire,
				Subject:  string(p),
				Path:     path,
				Message:  "package " + string(p) + " was renamed to " + string(renamedTo),
				Fix:      "confirm this is a rename; a consumer resolving by package path stops resolving until it is updated",
			})
		} else {
			findings = append(findings, Finding{
				Rule:     RulePackageRemoved,
				Category: Wire,
				Subject:  string(p),
				Path:     path,
				Message:  "package " + string(p) + " was removed",
				Fix:      "restore the package, or keep at least one declaration in it so it is not silently dropped",
			})
		}
	}
	return findings, swallowed
}

func topLevelNamesByPackage(r Revision) map[protoreflect.FullName][]protoreflect.Name {
	owned := ownedSet(r.Owned)
	out := make(map[protoreflect.FullName][]protoreflect.Name)
	for _, f := range r.Files {
		if _, ok := owned[f.Path()]; !ok {
			continue
		}
		pkg := f.Package()
		var names []protoreflect.Name
		for i := 0; i < f.Messages().Len(); i++ {
			names = append(names, f.Messages().Get(i).Name())
		}
		for i := 0; i < f.Enums().Len(); i++ {
			names = append(names, f.Enums().Get(i).Name())
		}
		for i := 0; i < f.Services().Len(); i++ {
			names = append(names, f.Services().Get(i).Name())
		}
		out[pkg] = append(out[pkg], names...)
	}
	return out
}

// firstOwnedFileOfPackage returns the import path of the first owned file in
// r that declares pkg, sorted, so that a package-level finding — which has no
// single declaration of its own to point at — still names a file a reader can
// open and a CI annotator can attach the finding to, deterministically.
func firstOwnedFileOfPackage(r Revision, pkg protoreflect.FullName) string {
	owned := ownedSet(r.Owned)
	var paths []string
	for _, f := range r.Files {
		if _, ok := owned[f.Path()]; !ok {
			continue
		}
		if f.Package() == pkg {
			paths = append(paths, f.Path())
		}
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return ""
	}
	return paths[0]
}

func sameNameSet(a, b []protoreflect.Name) bool {
	if len(a) != len(b) {
		return false
	}
	as := append([]protoreflect.Name(nil), a...)
	bs := append([]protoreflect.Name(nil), b...)
	sort.Slice(as, func(i, j int) bool { return as[i] < as[j] })
	sort.Slice(bs, func(i, j int) bool { return bs[i] < bs[j] })
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}

// ---- files ----

// classifyFiles fires break/file_removed whenever a removed file's path is
// gone AND EITHER the file declared no message/enum/service at all (an
// aggregator file of pure imports, an options-only file, a file of nothing
// but top-level extend blocks — there is nothing for a declaration-level
// finding to have already reported, so there is no double-count to avoid)
// OR at least one message, enum or service that used to live in the file
// survives elsewhere in cur (under the same full name, moved or otherwise)
// — because a consumer imports a PATH, and that import breaks regardless of
// how many of the file's declarations moved rather than vanished, or
// whether it had any declarations to begin with.
//
// It fires for neither of those ONLY when the file declared at least one
// message/enum/service AND every one of them is also gone: that all-gone
// case is already fully covered by the declaration-level
// break/message_removed, break/enum_removed and break/service_removed
// findings, and reporting break/file_removed on top of them would
// double-count the same fact. A file holding both a declaration that moved
// and one that was deleted outright still fires: the moved declaration is
// the "at least one survives" that matters here, and the deleted one is
// reported separately by its own removal finding.
func classifyFiles(changes []Change, prev Revision) []Finding {
	// removedFullNames is every message/enum/service full name Diff
	// reported Removed, regardless of why — the same fact declSwallowed in
	// Classify tracks, computed independently here because this function
	// does not see it.
	removedFullNames := make(map[string]bool)
	for _, c := range changes {
		if c.Kind != Removed || c.Before == nil {
			continue
		}
		switch kindOf(c.Before) {
		case declMessage, declEnum, declService:
			removedFullNames[c.Subject] = true
		}
	}

	prevFilesByPath := make(map[string]protoreflect.FileDescriptor)
	for _, f := range prev.Files {
		prevFilesByPath[f.Path()] = f
	}

	var findings []Finding
	for _, c := range changes {
		if c.Kind != Removed || c.Before != nil {
			continue // not a file-level removal (those carry no descriptor)
		}
		if len(c.Subject) < 5 || c.Subject[:5] != "file:" {
			continue
		}
		path := c.Path
		fd, ok := prevFilesByPath[path]
		if !ok {
			continue
		}
		declared := declarationFullNames(fd)
		survives := len(declared) == 0 // no declarations at all: nothing to double-count
		for _, name := range declared {
			if !removedFullNames[string(name)] {
				survives = true
				break
			}
		}
		if survives {
			findings = append(findings, Finding{
				Rule:     RuleFileRemoved,
				Category: Source,
				Subject:  c.Subject,
				Path:     path,
				Message:  "file " + path + " was removed, breaking any import of that path",
				Fix:      "restore the file at this import path, or leave an empty file there so the import still resolves",
			})
		}
	}
	return findings
}

// declarationFullNames lists the full name of every message, enum and
// service fd declares, at every nesting depth, reusing diff.go's own
// indexFile so this walks nested declarations exactly the way Diff does.
func declarationFullNames(fd protoreflect.FileDescriptor) []protoreflect.FullName {
	idx := make(map[protoreflect.FullName]protoreflect.Descriptor)
	indexFile(idx, fd)
	var out []protoreflect.FullName
	for name, d := range idx {
		switch kindOf(d) {
		case declMessage, declEnum, declService:
			out = append(out, name)
		}
	}
	return out
}

// ---- go_package ----

func classifyGoPackage(prev, cur Revision) []Finding {
	prevOwned := ownedSet(prev.Owned)
	curFiles := make(map[string]protoreflect.FileDescriptor)
	curOwned := ownedSet(cur.Owned)
	for _, f := range cur.Files {
		if _, ok := curOwned[f.Path()]; ok {
			curFiles[f.Path()] = f
		}
	}

	var findings []Finding
	for _, f := range prev.Files {
		if _, ok := prevOwned[f.Path()]; !ok {
			continue
		}
		after, ok := curFiles[f.Path()]
		if !ok {
			continue // file removed: file_removed (or the decl removals) cover it
		}
		bgp := goPackageOf(f)
		agp := goPackageOf(after)
		if bgp != agp {
			findings = append(findings, Finding{
				Rule:     RuleGoPackageChanged,
				Category: Source,
				Subject:  "file:" + f.Path(),
				Path:     f.Path(),
				Message:  "go_package for " + f.Path() + " changed from " + bgp + " to " + agp,
				Change:   bgp + " -> " + agp,
				Fix:      "revert go_package; a consumer's generated import path moves whenever this changes",
			})
		}
	}
	return findings
}

func goPackageOf(f protoreflect.FileDescriptor) string {
	opts, ok := f.Options().(interface{ GetGoPackage() string })
	if !ok {
		return ""
	}
	return opts.GetGoPackage()
}
