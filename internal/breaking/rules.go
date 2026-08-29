package breaking

import (
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
}

// Classify turns Diff's neutral changes into findings, each carrying a
// permanent rule id.
//
// The rename rule, stated once here because two permanent ids would
// otherwise contradict each other: a removal and an addition within the
// same parent are reported as a rename when number, type and cardinality
// (or, for a declaration with no number, its shape) all match and only the
// name differs; otherwise they are reported separately, as a removal and an
// addition (the addition is never a finding on its own).
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

	findings = append(findings, classifyFields(byKindParent, changes, skipMember)...)
	findings = append(findings, classifyEnumValues(byKindParent, changes, skipMember)...)
	findings = append(findings, classifyMessages(byKindParent, skipContainer)...)
	findings = append(findings, classifyEnums(byKindParent, skipContainer)...)
	findings = append(findings, classifyServices(byKindParent, skipContainer)...)
	findings = append(findings, classifyMethods(byKindParent, changes, skipMember)...)
	findings = append(findings, packageFindings...)
	findings = append(findings, classifyFiles(changes)...)
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
)

func kindOf(d protoreflect.Descriptor) declKind {
	switch d.(type) {
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

func classifyFields(byKindParent map[parentKey]*grouped, changes []Change, skip func(protoreflect.FullName) bool) []Finding {
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
				Rule:     "break/field_renamed",
				Category: Source,
				Subject:  p.add.Subject,
				Path:     p.add.Path,
				Pos:      p.add.Pos,
				Message:  "field " + p.rem.Subject + " was renamed to " + p.add.Subject,
			})
		}
		for i, r := range g.removed {
			if usedRem[i] {
				continue
			}
			findings = append(findings, Finding{
				Rule:     "break/field_removed",
				Category: Source,
				Subject:  r.Subject,
				Path:     r.Path,
				Pos:      r.Pos,
				Message:  "field " + r.Subject + " was removed",
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
				Rule:     "break/field_number_changed",
				Category: Wire,
				Subject:  c.Subject,
				Path:     c.Path,
				Pos:      c.Pos,
				Message:  "field " + c.Subject + " changed its field number",
			})
		}

		if before.Kind() != after.Kind() {
			cat := Wire
			if WireCompatible(before.Kind(), after.Kind()) {
				cat = Source
			}
			findings = append(findings, Finding{
				Rule:     "break/field_type_changed",
				Category: cat,
				Subject:  c.Subject,
				Path:     c.Path,
				Pos:      c.Pos,
				Message:  "field " + c.Subject + " changed type from " + before.Kind().String() + " to " + after.Kind().String(),
				Change:   before.Kind().String() + "->" + after.Kind().String(),
			})
		}

		if before.Cardinality() != after.Cardinality() {
			findings = append(findings, Finding{
				Rule:     "break/field_cardinality_changed",
				Category: Wire,
				Subject:  c.Subject,
				Path:     c.Path,
				Pos:      c.Pos,
				Message:  "field " + c.Subject + " changed cardinality from " + before.Cardinality().String() + " to " + after.Cardinality().String(),
				Change:   before.Cardinality().String() + "->" + after.Cardinality().String(),
			})
		}

		beforeOneof := effectiveOneof(before)
		afterOneof := effectiveOneof(after)
		if beforeOneof != afterOneof {
			dest := afterOneof
			if dest == "" {
				dest = "none"
			}
			findings = append(findings, Finding{
				Rule:     "break/field_oneof_changed",
				Category: Wire,
				Subject:  c.Subject,
				Path:     c.Path,
				Pos:      c.Pos,
				Message:  "field " + c.Subject + " moved oneof membership to " + dest,
				Change:   dest,
			})
		}
	}

	return findings
}

// effectiveOneof reports the name of the real (non-synthetic) oneof a field
// belongs to, or "" if it belongs to none. A proto3 optional field's
// compiler-synthesised oneof is not a real declaration the author wrote, so
// it is not part of what field_oneof_changed reacts to.
func effectiveOneof(f protoreflect.FieldDescriptor) string {
	o := f.ContainingOneof()
	if o == nil || o.IsSynthetic() {
		return ""
	}
	return string(o.Name())
}

func fieldShapeEqual(before, after protoreflect.Descriptor) bool {
	b, ok1 := before.(protoreflect.FieldDescriptor)
	a, ok2 := after.(protoreflect.FieldDescriptor)
	if !ok1 || !ok2 {
		return false
	}
	return b.Number() == a.Number() && b.Kind() == a.Kind() && b.Cardinality() == a.Cardinality()
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
				Rule:     "break/enum_value_renamed",
				Category: Source,
				Subject:  p.add.Subject,
				Path:     p.add.Path,
				Pos:      p.add.Pos,
				Message:  "enum value " + p.rem.Subject + " was renamed to " + p.add.Subject,
			})
		}
		for i, r := range g.removed {
			if usedRem[i] {
				continue
			}
			findings = append(findings, Finding{
				Rule:     "break/enum_value_removed",
				Category: Source,
				Subject:  r.Subject,
				Path:     r.Path,
				Pos:      r.Pos,
				Message:  "enum value " + r.Subject + " was removed",
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
				Rule:     "break/enum_value_number_changed",
				Category: Wire,
				Subject:  c.Subject,
				Path:     c.Path,
				Pos:      c.Pos,
				Message:  "enum value " + c.Subject + " changed its number",
			})
		}
	}

	return findings
}

// ---- messages ----

func classifyMessages(byKindParent map[parentKey]*grouped, skip func(protoreflect.FullName) bool) []Finding {
	var findings []Finding
	for key, g := range byKindParent {
		if key.kind != declMessage {
			continue
		}
		if skip(key.parent) {
			continue
		}
		pairs, usedRem, _ := pairRenames(g, messageShapeEqual)
		for _, p := range pairs {
			findings = append(findings, Finding{
				Rule:     "break/message_renamed",
				Category: Source,
				Subject:  p.add.Subject,
				Path:     p.add.Path,
				Pos:      p.add.Pos,
				Message:  "message " + p.rem.Subject + " was renamed to " + p.add.Subject,
			})
		}
		for i, r := range g.removed {
			if usedRem[i] {
				continue
			}
			findings = append(findings, Finding{
				Rule:     "break/message_removed",
				Category: Source,
				Subject:  r.Subject,
				Path:     r.Path,
				Pos:      r.Pos,
				Message:  "message " + r.Subject + " was removed",
			})
		}
	}
	return findings
}

// messageShapeEqual reports whether two messages have the same set of
// direct fields by (number, kind, cardinality), ignoring field names: this
// is what tells a message rename apart from an unrelated removal and
// addition. It intentionally does not compare nested declarations, which
// are indexed and diffed separately.
func messageShapeEqual(before, after protoreflect.Descriptor) bool {
	b, ok1 := before.(protoreflect.MessageDescriptor)
	a, ok2 := after.(protoreflect.MessageDescriptor)
	if !ok1 || !ok2 {
		return false
	}
	return fieldSetEqual(b.Fields(), a.Fields())
}

func fieldSetEqual(b, a protoreflect.FieldDescriptors) bool {
	if b.Len() != a.Len() {
		return false
	}
	type shape struct {
		num  protoreflect.FieldNumber
		kind protoreflect.Kind
		card protoreflect.Cardinality
	}
	toShapes := func(fs protoreflect.FieldDescriptors) []shape {
		out := make([]shape, fs.Len())
		for i := 0; i < fs.Len(); i++ {
			f := fs.Get(i)
			out[i] = shape{f.Number(), f.Kind(), f.Cardinality()}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].num < out[j].num })
		return out
	}
	bs, as := toShapes(b), toShapes(a)
	for i := range bs {
		if bs[i] != as[i] {
			return false
		}
	}
	return true
}

// ---- enums ----

func classifyEnums(byKindParent map[parentKey]*grouped, skip func(protoreflect.FullName) bool) []Finding {
	var findings []Finding
	for key, g := range byKindParent {
		if key.kind != declEnum {
			continue
		}
		if skip(key.parent) {
			continue
		}
		pairs, usedRem, _ := pairRenames(g, enumShapeEqual)
		for _, p := range pairs {
			findings = append(findings, Finding{
				Rule:     "break/enum_renamed",
				Category: Source,
				Subject:  p.add.Subject,
				Path:     p.add.Path,
				Pos:      p.add.Pos,
				Message:  "enum " + p.rem.Subject + " was renamed to " + p.add.Subject,
			})
		}
		for i, r := range g.removed {
			if usedRem[i] {
				continue
			}
			findings = append(findings, Finding{
				Rule:     "break/enum_removed",
				Category: Source,
				Subject:  r.Subject,
				Path:     r.Path,
				Pos:      r.Pos,
				Message:  "enum " + r.Subject + " was removed",
			})
		}
	}
	return findings
}

func enumShapeEqual(before, after protoreflect.Descriptor) bool {
	b, ok1 := before.(protoreflect.EnumDescriptor)
	a, ok2 := after.(protoreflect.EnumDescriptor)
	if !ok1 || !ok2 {
		return false
	}
	if b.Values().Len() != a.Values().Len() {
		return false
	}
	bn := make([]protoreflect.EnumNumber, b.Values().Len())
	for i := range bn {
		bn[i] = b.Values().Get(i).Number()
	}
	an := make([]protoreflect.EnumNumber, a.Values().Len())
	for i := range an {
		an[i] = a.Values().Get(i).Number()
	}
	sort.Slice(bn, func(i, j int) bool { return bn[i] < bn[j] })
	sort.Slice(an, func(i, j int) bool { return an[i] < an[j] })
	for i := range bn {
		if bn[i] != an[i] {
			return false
		}
	}
	return true
}

// ---- services ----

func classifyServices(byKindParent map[parentKey]*grouped, skip func(protoreflect.FullName) bool) []Finding {
	var findings []Finding
	for key, g := range byKindParent {
		if key.kind != declService {
			continue
		}
		if skip(key.parent) {
			continue
		}
		pairs, usedRem, _ := pairRenames(g, serviceShapeEqual)
		for _, p := range pairs {
			findings = append(findings, Finding{
				Rule:     "break/service_renamed",
				Category: Wire,
				Subject:  p.add.Subject,
				Path:     p.add.Path,
				Pos:      p.add.Pos,
				Message:  "service " + p.rem.Subject + " was renamed to " + p.add.Subject,
			})
		}
		for i, r := range g.removed {
			if usedRem[i] {
				continue
			}
			findings = append(findings, Finding{
				Rule:     "break/service_removed",
				Category: Wire,
				Subject:  r.Subject,
				Path:     r.Path,
				Pos:      r.Pos,
				Message:  "service " + r.Subject + " was removed",
			})
		}
	}
	return findings
}

func serviceShapeEqual(before, after protoreflect.Descriptor) bool {
	b, ok1 := before.(protoreflect.ServiceDescriptor)
	a, ok2 := after.(protoreflect.ServiceDescriptor)
	if !ok1 || !ok2 {
		return false
	}
	if b.Methods().Len() != a.Methods().Len() {
		return false
	}
	type shape struct {
		name             protoreflect.Name
		in, out          protoreflect.FullName
		streamC, streamS bool
	}
	toShapes := func(ms protoreflect.MethodDescriptors) []shape {
		out := make([]shape, ms.Len())
		for i := 0; i < ms.Len(); i++ {
			m := ms.Get(i)
			out[i] = shape{m.Name(), m.Input().FullName(), m.Output().FullName(), m.IsStreamingClient(), m.IsStreamingServer()}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
		return out
	}
	bs, as := toShapes(b.Methods()), toShapes(a.Methods())
	for i := range bs {
		if bs[i] != as[i] {
			return false
		}
	}
	return true
}

// ---- methods ----

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
		pairs, usedRem, _ := pairRenames(g, methodShapeEqual)
		for _, p := range pairs {
			findings = append(findings, Finding{
				Rule:     "break/method_renamed",
				Category: Wire,
				Subject:  p.add.Subject,
				Path:     p.add.Path,
				Pos:      p.add.Pos,
				Message:  "method " + p.rem.Subject + " was renamed to " + p.add.Subject,
			})
		}
		for i, r := range g.removed {
			if usedRem[i] {
				continue
			}
			findings = append(findings, Finding{
				Rule:     "break/method_removed",
				Category: Wire,
				Subject:  r.Subject,
				Path:     r.Path,
				Pos:      r.Pos,
				Message:  "method " + r.Subject + " was removed",
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
				Rule:     "break/method_signature_changed",
				Category: Wire,
				Subject:  c.Subject,
				Path:     c.Path,
				Pos:      c.Pos,
				Message:  "method " + c.Subject + " changed its input or output type",
				Change:   string(before.Input().FullName()) + "/" + string(before.Output().FullName()) + "->" + string(after.Input().FullName()) + "/" + string(after.Output().FullName()),
			})
		}
		if before.IsStreamingClient() != after.IsStreamingClient() || before.IsStreamingServer() != after.IsStreamingServer() {
			findings = append(findings, Finding{
				Rule:     "break/method_streaming_changed",
				Category: Wire,
				Subject:  c.Subject,
				Path:     c.Path,
				Pos:      c.Pos,
				Message:  "method " + c.Subject + " changed its streaming shape",
			})
		}
	}

	return findings
}

func methodShapeEqual(before, after protoreflect.Descriptor) bool {
	b, ok1 := before.(protoreflect.MethodDescriptor)
	a, ok2 := after.(protoreflect.MethodDescriptor)
	if !ok1 || !ok2 {
		return false
	}
	return b.Input().FullName() == a.Input().FullName() &&
		b.Output().FullName() == a.Output().FullName() &&
		b.IsStreamingClient() == a.IsStreamingClient() &&
		b.IsStreamingServer() == a.IsStreamingServer()
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

	for _, p := range removedPkgs {
		before := prevPkgs[p]
		var renamedTo protoreflect.FullName
		for q, after := range curPkgs {
			if usedCur[q] {
				continue
			}
			if _, existedBefore := prevPkgs[q]; existedBefore {
				continue // q is not new in cur
			}
			if sameNameSet(before, after) {
				renamedTo = q
				usedCur[q] = true
				break
			}
		}
		swallowed[p] = true
		if renamedTo != "" {
			findings = append(findings, Finding{
				Rule:     "break/package_renamed",
				Category: Wire,
				Subject:  string(p),
				Message:  "package " + string(p) + " was renamed to " + string(renamedTo),
			})
		} else {
			findings = append(findings, Finding{
				Rule:     "break/package_removed",
				Category: Wire,
				Subject:  string(p),
				Message:  "package " + string(p) + " was removed",
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

// classifyFiles fires break/file_removed only when a removed file's
// top-level declarations all survive elsewhere in cur: a file whose
// contents are altogether gone is already covered by the declaration-level
// removals, and reporting both would double-count.
func classifyFiles(changes []Change) []Finding {
	removedNames := make(map[string]bool)
	for _, c := range changes {
		if c.Kind == Removed && c.Before != nil {
			removedNames[c.Subject] = true
		}
	}

	var findings []Finding

	// A file-level removal is identified by Subject "file:"+Path with a nil
	// Before (Diff never attaches a descriptor to a file-level change). It
	// fires only when no declaration that used to live in that file was
	// itself reported removed: if one was, the file's contents are gone and
	// the declaration-level removal already covers it.
	for _, c := range changes {
		if c.Kind != Removed || c.Before != nil {
			continue
		}
		if len(c.Subject) < 5 || c.Subject[:5] != "file:" {
			continue
		}
		path := c.Path
		survived := true
		for _, d := range changes {
			if d.Kind != Removed || d.Before == nil {
				continue
			}
			if d.Path != path {
				continue
			}
			survived = false
			break
		}
		if survived {
			findings = append(findings, Finding{
				Rule:     "break/file_removed",
				Category: Source,
				Subject:  c.Subject,
				Path:     path,
				Message:  "file " + path + " was removed, breaking any import of that path",
			})
		}
	}
	return findings
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
				Rule:     "break/go_package_changed",
				Category: Source,
				Subject:  "file:" + f.Path(),
				Path:     f.Path(),
				Message:  "go_package for " + f.Path() + " changed from " + bgp + " to " + agp,
				Change:   bgp + "->" + agp,
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
