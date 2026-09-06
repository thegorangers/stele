package breaking

import (
	"bytes"
	"sort"

	"github.com/thegorangers/stele/internal/lint"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Kind says what happened to a declaration or a file between two revisions.
type Kind int

const (
	// Removed means the subject exists in prev but not in cur.
	Removed Kind = iota
	// Added means the subject exists in cur but not in prev.
	Added
	// Modified means the subject exists on both sides, with a different
	// descriptor.
	Modified
)

// Change is one neutral fact about how a declaration or a file differs
// between two revisions. Diff reports changes, not breakages: a rename is a
// removal and an addition, and pairing the two into one finding is a later
// decision that needs both halves in hand, which is exactly what a stream of
// findings could not carry.
type Change struct {
	Kind Kind
	// Subject names what changed. For a message, field, oneof, enum, enum
	// value, service or method this is its protoreflect.FullName. A file has
	// no full name, so a removed or added file is reported as
	// "file:" + its import path — a tag that stops an import path being
	// mistaken for a declaration's name.
	Subject string
	// Path is the import path of the file the subject is declared in (or,
	// for a file-level change, the file itself).
	Path string
	// Pos is where in cur's file the subject is declared, when cur has
	// source information for it. It is the zero Position for a removal,
	// since nothing survives in cur to point at.
	Pos lint.Position
	// Before and After are the subject's descriptors on each side. Either
	// may be nil: Before is nil for an addition, After is nil for a
	// removal. Both are non-nil for a modification, so the classifier can
	// inspect either side without re-walking the revisions.
	Before, After protoreflect.Descriptor
}

// Diff compares two revisions' compiled descriptors and reports what
// differs between them, restricted to what each revision owns.
//
// It states the subject of every change itself, rather than deriving it from
// a source position the way internal/lint does. That constraint exists in
// lint because a rule can run in another process and can return only a line
// and a column there — deriving the subject from a position is what keeps a
// plugin's findings from being second-class. There is no plugin host here:
// Diff holds both descriptor sets directly, in the same process, so it
// simply knows a removed field's name. Deriving it from a position could not
// have named the removed field at all, because nothing at that position
// still exists in cur to point at — the position would have named the
// surviving message instead, and one permission to modify that message would
// then have licensed removing every field of it.
func Diff(prev, cur Revision) []Change {
	var changes []Change

	changes = append(changes, diffFiles(prev, cur)...)
	changes = append(changes, diffDeclarations(prev, cur)...)

	return changes
}

// diffFiles reports files present on one side and not the other, tagged so a
// file's subject can never collide with a declaration's full name.
func diffFiles(prev, cur Revision) []Change {
	prevPaths := ownedSet(prev.Owned)
	curPaths := ownedSet(cur.Owned)

	var changes []Change
	for _, p := range sortedUnion(prevPaths, curPaths) {
		_, inPrev := prevPaths[p]
		_, inCur := curPaths[p]
		switch {
		case inPrev && !inCur:
			changes = append(changes, Change{
				Kind:    Removed,
				Subject: "file:" + p,
				Path:    p,
			})
		case !inPrev && inCur:
			changes = append(changes, Change{
				Kind:    Added,
				Subject: "file:" + p,
				Path:    p,
			})
		}
	}
	return changes
}

// diffDeclarations indexes both revisions' owned declarations by full name
// and walks the union of keys, reporting a Change for every name that is not
// identically present on both sides.
func diffDeclarations(prev, cur Revision) []Change {
	prevIdx := indexRevision(prev)
	curIdx := indexRevision(cur)

	var changes []Change
	for _, name := range sortedUnion(prevIdx, curIdx) {
		before, inPrev := prevIdx[name]
		after, inCur := curIdx[name]

		switch {
		case inPrev && !inCur:
			changes = append(changes, Change{
				Kind:    Removed,
				Subject: string(name),
				Path:    declFilePath(before),
				Before:  before,
			})
		case !inPrev && inCur:
			changes = append(changes, Change{
				Kind:    Added,
				Subject: string(name),
				Path:    declFilePath(after),
				Pos:     posOf(after),
				After:   after,
			})
		default:
			if descEqual(before, after) {
				continue
			}
			changes = append(changes, Change{
				Kind:    Modified,
				Subject: string(name),
				Path:    declFilePath(after),
				Pos:     posOf(after),
				Before:  before,
				After:   after,
			})
		}
	}
	return changes
}

// declFilePath returns the import path of the file d is declared in.
func declFilePath(d protoreflect.Descriptor) string {
	if d == nil {
		return ""
	}
	if pf := d.ParentFile(); pf != nil {
		return pf.Path()
	}
	return ""
}

// posOf returns where d is declared in its own file, or the zero Position
// when d's file carries no source information (as prev's files never do —
// see Revision.Load's prev argument).
func posOf(d protoreflect.Descriptor) lint.Position {
	if d == nil {
		return lint.Position{}
	}
	pf := d.ParentFile()
	if pf == nil {
		return lint.Position{}
	}
	return lint.File{Desc: pf}.Pos(d)
}

// ownedSet turns a revision's Owned import paths into a set, for membership
// tests and for feeding sortedUnion.
func ownedSet(owned []string) map[string]struct{} {
	set := make(map[string]struct{}, len(owned))
	for _, p := range owned {
		set[p] = struct{}{}
	}
	return set
}

// indexRevision indexes every message, field, oneof, enum, enum value,
// service and method a revision owns, keyed by full name. A declaration
// outside the revision's Owned import paths — a dependency's own type — is
// never indexed, because a dependency's own history is not this diff's to
// report.
func indexRevision(r Revision) map[protoreflect.FullName]protoreflect.Descriptor {
	idx := make(map[protoreflect.FullName]protoreflect.Descriptor)
	owned := ownedSet(r.Owned)
	for _, f := range r.Files {
		if _, ok := owned[f.Path()]; !ok {
			continue
		}
		indexFile(idx, f)
	}
	return idx
}

func indexFile(idx map[protoreflect.FullName]protoreflect.Descriptor, fd protoreflect.FileDescriptor) {
	msgs := fd.Messages()
	for i := 0; i < msgs.Len(); i++ {
		indexMessage(idx, msgs.Get(i))
	}
	enums := fd.Enums()
	for i := 0; i < enums.Len(); i++ {
		indexEnum(idx, enums.Get(i))
	}
	services := fd.Services()
	for i := 0; i < services.Len(); i++ {
		indexService(idx, services.Get(i))
	}
}

func indexMessage(idx map[protoreflect.FullName]protoreflect.Descriptor, md protoreflect.MessageDescriptor) {
	if md.IsMapEntry() {
		// A map field synthesises this message (and its key/value fields);
		// the author never wrote it, so it is not a declaration to report on.
		// internal/lint's eachEnum applies the same guard for the same
		// reason.
		return
	}
	idx[md.FullName()] = md

	fields := md.Fields()
	for i := 0; i < fields.Len(); i++ {
		f := fields.Get(i)
		idx[f.FullName()] = f
	}
	oneofs := md.Oneofs()
	for i := 0; i < oneofs.Len(); i++ {
		o := oneofs.Get(i)
		idx[o.FullName()] = o
	}
	enums := md.Enums()
	for i := 0; i < enums.Len(); i++ {
		indexEnum(idx, enums.Get(i))
	}
	nested := md.Messages()
	for i := 0; i < nested.Len(); i++ {
		indexMessage(idx, nested.Get(i))
	}
}

func indexEnum(idx map[protoreflect.FullName]protoreflect.Descriptor, ed protoreflect.EnumDescriptor) {
	idx[ed.FullName()] = ed
	values := ed.Values()
	for i := 0; i < values.Len(); i++ {
		v := values.Get(i)
		idx[v.FullName()] = v
	}
}

func indexService(idx map[protoreflect.FullName]protoreflect.Descriptor, sd protoreflect.ServiceDescriptor) {
	idx[sd.FullName()] = sd
	methods := sd.Methods()
	for i := 0; i < methods.Len(); i++ {
		m := methods.Get(i)
		idx[m.FullName()] = m
	}
}

// sortedUnion returns the sorted union of two maps' keys. K must be an
// ordered string-like type, which protoreflect.FullName and the plain
// strings this package indexes files by both are.
//
// Revision.Owned is not sorted and resolve.Graph.ImportPaths gives no order
// guarantee either, so every walk over a revision's keys goes through this
// function: a diff whose output order changed between runs would make every
// downstream report unstable.
func sortedUnion[K ~string, V any](a, b map[K]V) []K {
	seen := make(map[K]struct{}, len(a)+len(b))
	for k := range a {
		seen[k] = struct{}{}
	}
	for k := range b {
		seen[k] = struct{}{}
	}
	keys := make([]K, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}

// protoMsgEqual reports whether b and a serialise identically, and is the one
// predicate every descEqual case uses instead of proto.Equal. proto.Equal
// walks message-valued extensions (custom options such as buf.validate.field
// or google.api.http) by comparing the dynamic messages that hold their
// values field by field, including each message's descriptor pointer. prev
// and cur are compiled by two independent compiler runs, so an option value
// that is byte-for-byte identical on both sides is still backed by two
// distinct descriptor instances, and proto.Equal reports it unequal purely
// on that account — a false Modified with no textual difference behind it.
// Deterministic marshalling sidesteps identity entirely: two messages that
// serialise to the same bytes are the same declaration, regardless of which
// descriptor instance produced them.
//
// A marshal error is not swallowed into "equal" or "unequal" by guesswork: a
// descriptor this package cannot even serialise is not one this package can
// call identical, so it is reported unequal (changed), which is what causes
// diffDeclarations to surface it as a Modified change rather than silently
// dropping it from the report.
func protoMsgEqual(b, a proto.Message) bool {
	bb, berr := proto.MarshalOptions{Deterministic: true}.Marshal(b)
	ab, aerr := proto.MarshalOptions{Deterministic: true}.Marshal(a)
	if berr != nil || aerr != nil {
		return false
	}
	return bytes.Equal(bb, ab)
}

// descEqual reports whether before and after are the same declaration at its
// own granularity: a message is compared on its own properties only, never
// on the fields, oneofs, enums or nested messages it contains, because those
// are indexed and compared separately — comparing a message whole would
// report the parent as Modified every time a child changed, on top of the
// child's own Change, and a rule reacting to "this message changed" would
// then fire on every field edit anywhere inside it. Likewise a service is
// compared without its methods, and an enum without its values.
//
// The descriptorpb form these produce never carries source positions —
// SourceCodeInfo lives on the file as a whole, not on an individual
// FieldDescriptorProto or DescriptorProto — so a reformatting that moves a
// declaration to a different line never reads as a change here.
func descEqual(before, after protoreflect.Descriptor) bool {
	switch b := before.(type) {
	case protoreflect.MessageDescriptor:
		a, ok := after.(protoreflect.MessageDescriptor)
		if !ok {
			return false
		}
		bp, ap := protodesc.ToDescriptorProto(b), protodesc.ToDescriptorProto(a)
		bp.Field, ap.Field = nil, nil
		bp.NestedType, ap.NestedType = nil, nil
		bp.EnumType, ap.EnumType = nil, nil
		bp.OneofDecl, ap.OneofDecl = nil, nil
		return protoMsgEqual(bp, ap)
	case protoreflect.FieldDescriptor:
		a, ok := after.(protoreflect.FieldDescriptor)
		if !ok {
			return false
		}
		if !protoMsgEqual(protodesc.ToFieldDescriptorProto(b), protodesc.ToFieldDescriptorProto(a)) {
			return false
		}
		// The raw FieldDescriptorProto compared above carries oneof_index, a
		// positional integer into the message's oneof_decl list — not the
		// oneof's identity. Swapping the names of two oneofs (or otherwise
		// reordering oneof_decl while every field stays in its own group)
		// leaves each field's oneof_index byte-identical, so that comparison
		// alone is blind to a field silently changing which oneof it
		// belongs to. Comparing the RESOLVED name of the real, non-synthetic
		// containing oneof closes that: it is the same identity
		// classifyFields and fieldShapeEqual already use (effectiveOneof, in
		// rules.go), so a field's identity is defined consistently
		// everywhere, not by two different spellings of "what oneof is
		// this".
		if effectiveOneof(b) != effectiveOneof(a) {
			return false
		}
		// A map field's own FieldDescriptorProto never changes when its key
		// or value type is retyped: it stays cardinality repeated, kind
		// message, pointing at the same synthesised *Entry type name. The
		// entry message and its key/value fields are never indexed as
		// declarations (see indexMessage's IsMapEntry guard — nobody wrote
		// them), so a retype has to be compared here, as part of the field
		// itself, or it is invisible everywhere and reported against no
		// subject at all.
		if b.IsMap() && a.IsMap() {
			if !protoMsgEqual(protodesc.ToFieldDescriptorProto(b.MapKey()), protodesc.ToFieldDescriptorProto(a.MapKey())) {
				return false
			}
			if !protoMsgEqual(protodesc.ToFieldDescriptorProto(b.MapValue()), protodesc.ToFieldDescriptorProto(a.MapValue())) {
				return false
			}
		}
		return true
	case protoreflect.OneofDescriptor:
		a, ok := after.(protoreflect.OneofDescriptor)
		if !ok {
			return false
		}
		return protoMsgEqual(protodesc.ToOneofDescriptorProto(b), protodesc.ToOneofDescriptorProto(a))
	case protoreflect.EnumDescriptor:
		a, ok := after.(protoreflect.EnumDescriptor)
		if !ok {
			return false
		}
		bp, ap := protodesc.ToEnumDescriptorProto(b), protodesc.ToEnumDescriptorProto(a)
		bp.Value, ap.Value = nil, nil
		return protoMsgEqual(bp, ap)
	case protoreflect.EnumValueDescriptor:
		a, ok := after.(protoreflect.EnumValueDescriptor)
		if !ok {
			return false
		}
		return protoMsgEqual(protodesc.ToEnumValueDescriptorProto(b), protodesc.ToEnumValueDescriptorProto(a))
	case protoreflect.ServiceDescriptor:
		a, ok := after.(protoreflect.ServiceDescriptor)
		if !ok {
			return false
		}
		bp, ap := protodesc.ToServiceDescriptorProto(b), protodesc.ToServiceDescriptorProto(a)
		bp.Method, ap.Method = nil, nil
		return protoMsgEqual(bp, ap)
	case protoreflect.MethodDescriptor:
		a, ok := after.(protoreflect.MethodDescriptor)
		if !ok {
			return false
		}
		return protoMsgEqual(protodesc.ToMethodDescriptorProto(b), protodesc.ToMethodDescriptorProto(a))
	default:
		// No other descriptor kind is ever indexed; a change here without a
		// matching case would otherwise report every occurrence as
		// unmodified.
		return false
	}
}
