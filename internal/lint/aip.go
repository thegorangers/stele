package lint

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// The rules in this file implement the API Improvement Proposals,
// https://google.aip.dev. What is decidable from one linked descriptor, which
// AIPs are not, and why the answer is a ledger rather than a list somebody
// maintains, is in docs/AIP.md and in internal/aip.
//
// # Why these are warnings by default, and what a warning means here
//
// Every rule in the stele namespace was chosen because a measured fleet
// already kept it: switching it on cost that fleet almost nothing, so it
// fails a build. The AIP rules are the opposite. They are guidance a
// repository may never have been written against, and the same fleet breaks
// them in the hundreds. Two ways out of that were rejected:
//
//   - A profile switched off by default. It ships dead. The roadmap's own
//     objection to rules that default to off applies with more force here,
//     because the whole value of this guidance is that somebody reads it who
//     was not already looking for it.
//   - One warning line per finding. Thousands of lines is not a signal; it is
//     a wall a reader learns to scroll past, which is the same failure as the
//     profile nobody enables in a different colour.
//
// So they are on, they warn, and their output is rolled up to one line per
// rule above a threshold. See Report.Write.
//
// # The constraint every rule here is written under
//
// Roughly half of what AIP says is decidable only because of an annotation —
// google.api.http, google.api.resource, google.api.field_behavior. At a
// descriptor, a file that does not import the annotation is indistinguishable
// from a file whose author did not annotate: the extension is simply absent
// either way. A rule that reported the second as the first would say the same
// thing about a repository that annotated everything and whose descriptor set
// was assembled without the import.
//
// No rule here reads an annotation, and that is what decides the opening set
// rather than what the guidance considers most important. It also decides
// against the four broadest checks in docs/AIP.md §3, which fire on every file
// of every repository that has not adopted the annotations: a summary line
// that reads the same on every run of every repository teaches nothing and
// ages into furniture. The rules here are ones whose count falls as the work
// is done.

// aipID spells an AIP rule's ID, so that the reserved namespace is written
// once. The number leads the name: see rule.NamespaceAIP.
func aipID(name string) string { return NamespaceAIP + "/" + name }

// AIP returns the rules that implement the API Improvement Proposals. They
// ship with the tool and run by default, at warning.
func AIP() []Rule {
	return []Rule{
		deleteReturnsEmpty{},
		listRequestPageSize{},
		listRequestPageToken{},
		listResponseNextPageToken{},
		timestampFieldTimeSuffix{},
	}
}

// Well-known type names the rules below compare against. They are written
// once because a typo in one of them is a rule that silently never fires.
const (
	typeEmpty     = "google.protobuf.Empty"
	typeTimestamp = "google.protobuf.Timestamp"
	typeOperation = "google.longrunning.Operation"
)

// --- AIP-135: standard methods, delete ---

type deleteReturnsEmpty struct{}

func (deleteReturnsEmpty) ID() string { return aipID("135_delete_returns_empty") }
func (deleteReturnsEmpty) Description() string {
	return "a method named DeleteX returns google.protobuf.Empty, or the soft-deleted X (AIP-135)"
}

// Check accepts three responses, and the third is the one that keeps the rule
// honest: Empty for the ordinary case, the resource itself for a soft delete
// that returns what it marked, and google.longrunning.Operation for a delete
// that takes long enough to need one. A rule that accepted only the first
// would report a correct soft delete as a defect, and a rule that is wrong
// about a shape somebody chose deliberately is the kind that gets switched
// off along with everything beside it.
func (r deleteReturnsEmpty) Check(f File) ([]Finding, error) {
	var out []Finding
	eachMethod(f.Desc, func(_ protoreflect.ServiceDescriptor, m protoreflect.MethodDescriptor) {
		resource, ok := afterPrefix(string(m.Name()), "Delete")
		if !ok {
			return
		}
		got := string(m.Output().FullName())
		switch got {
		case typeEmpty, typeOperation:
			return
		}
		if string(m.Output().Name()) == resource {
			return
		}
		out = append(out, Finding{
			Pos:     f.Pos(m),
			Message: fmt.Sprintf("%s returns %s", m.Name(), got),
			Fix: fmt.Sprintf("return %s. A delete has nothing to report but success, and a bespoke "+
				"%s is a message that exists to be empty and cannot be added to later without a consumer "+
				"noticing. Returning %s instead is the soft-delete shape and is also accepted",
				typeEmpty, m.Output().Name(), resource),
		})
	})
	return out, nil
}

// --- AIP-158: pagination ---

// listMethods visits the methods a pagination rule applies to: named ListX,
// and not server-streaming.
//
// The streaming exclusion is not a convenience. A server-streaming method
// hands the client the whole collection as it goes, which is what pagination
// exists to avoid; requiring a page token of it would be requiring two
// answers to one question, and the finding would be one nobody should act on.
func listMethods(fd protoreflect.FileDescriptor, fn func(protoreflect.MethodDescriptor, string)) {
	eachMethod(fd, func(_ protoreflect.ServiceDescriptor, m protoreflect.MethodDescriptor) {
		collection, ok := afterPrefix(string(m.Name()), "List")
		if !ok || m.IsStreamingServer() {
			return
		}
		fn(m, collection)
	})
}

type listRequestPageSize struct{}

func (listRequestPageSize) ID() string { return aipID("158_list_request_page_size") }
func (listRequestPageSize) Description() string {
	return "the request of a ListX method carries an int32 page_size (AIP-158)"
}

func (r listRequestPageSize) Check(f File) ([]Finding, error) {
	var out []Finding
	listMethods(f.Desc, func(m protoreflect.MethodDescriptor, _ string) {
		in := m.Input()
		fd := in.Fields().ByName("page_size")
		switch {
		case fd == nil:
			out = append(out, Finding{
				Pos: f.Pos(m),
				Message: fmt.Sprintf("the request of %s (%s) carries no page_size",
					m.Name(), in.FullName()),
				Fix: "add int32 page_size = N;. Without it the server decides how much it returns and the " +
					"caller cannot ask for less, so the first collection that outgrows one response breaks " +
					"every consumer at once",
			})
		case fd.Kind() != protoreflect.Int32Kind || fd.IsList():
			out = append(out, Finding{
				Pos:     posOf(f, fd, m),
				Message: fmt.Sprintf("page_size in %s is %s, not int32", in.FullName(), fieldType(fd)),
				Fix: "make it int32. The type is part of the convention every generated client and every " +
					"gateway reads it through",
			})
		}
	})
	return out, nil
}

type listRequestPageToken struct{}

func (listRequestPageToken) ID() string { return aipID("158_list_request_page_token") }
func (listRequestPageToken) Description() string {
	return "the request of a ListX method carries a string page_token (AIP-158)"
}

func (r listRequestPageToken) Check(f File) ([]Finding, error) {
	var out []Finding
	listMethods(f.Desc, func(m protoreflect.MethodDescriptor, _ string) {
		in := m.Input()
		fd := in.Fields().ByName("page_token")
		switch {
		case fd == nil:
			out = append(out, Finding{
				Pos: f.Pos(m),
				Message: fmt.Sprintf("the request of %s (%s) carries no page_token",
					m.Name(), in.FullName()),
				Fix: "add string page_token = N;. A response that can be truncated with no way to ask for " +
					"the rest is a response that silently loses data",
			})
		case fd.Kind() != protoreflect.StringKind || fd.IsList():
			out = append(out, Finding{
				Pos:     posOf(f, fd, m),
				Message: fmt.Sprintf("page_token in %s is %s, not string", in.FullName(), fieldType(fd)),
				Fix: "make it string. It is opaque to the caller, and a typed token is a promise about the " +
					"server's paging strategy that the server then cannot change",
			})
		}
	})
	return out, nil
}

type listResponseNextPageToken struct{}

func (listResponseNextPageToken) ID() string { return aipID("158_list_response_next_page_token") }
func (listResponseNextPageToken) Description() string {
	return "the response of a ListX method carries a string next_page_token (AIP-158)"
}

func (r listResponseNextPageToken) Check(f File) ([]Finding, error) {
	var out []Finding
	listMethods(f.Desc, func(m protoreflect.MethodDescriptor, _ string) {
		outMsg := m.Output()
		fd := outMsg.Fields().ByName("next_page_token")
		switch {
		case fd == nil:
			out = append(out, Finding{
				Pos: f.Pos(m),
				Message: fmt.Sprintf("the response of %s (%s) carries no next_page_token",
					m.Name(), outMsg.FullName()),
				Fix: "add string next_page_token = N;. It is the only way a caller can tell a page that is " +
					"the last one from a page that merely looks like it",
			})
		case fd.Kind() != protoreflect.StringKind || fd.IsList():
			out = append(out, Finding{
				Pos:     posOf(f, fd, m),
				Message: fmt.Sprintf("next_page_token in %s is %s, not string", outMsg.FullName(), fieldType(fd)),
				Fix:     "make it string, the same opaque type the request's page_token is",
			})
		}
	})
	return out, nil
}

// --- AIP-142: time and duration ---

type timestampFieldTimeSuffix struct{}

func (timestampFieldTimeSuffix) ID() string { return aipID("142_timestamp_field_time_suffix") }
func (timestampFieldTimeSuffix) Description() string {
	return "a google.protobuf.Timestamp field is named _time, or _times when repeated (AIP-142)"
}

// Check is the rule that made the summary necessary: the measured fleet
// spells every one of these _at, so it fires in the hundreds on a repository
// that has made one consistent choice. That is the argument for a warning and
// a roll-up rather than for silence — one line saying how many there are is a
// decision somebody can take once, and a hundred lines is a decision nobody
// takes at all.
func (r timestampFieldTimeSuffix) Check(f File) ([]Finding, error) {
	var out []Finding
	eachField(f.Desc, func(fd protoreflect.FieldDescriptor) {
		if fd.Kind() != protoreflect.MessageKind || string(fd.Message().FullName()) != typeTimestamp {
			return
		}
		name := string(fd.Name())
		want, suffix := "_time", "_time"
		if fd.IsList() {
			want, suffix = "_times", "_times"
		}
		if name == strings.TrimPrefix(suffix, "_") || strings.HasSuffix(name, suffix) {
			return
		}
		out = append(out, Finding{
			Pos:     f.Pos(fd),
			Message: fmt.Sprintf("%s is a %s and is not named %s%s", fd.FullName(), typeTimestamp, "*", want),
			Fix: fmt.Sprintf("rename it to %s. The suffix is what tells a reader, and every tool that "+
				"reads these contracts, that the field is an instant rather than a duration or a date",
				renameToTime(name, want)),
		})
	})
	return out, nil
}

// renameToTime suggests the name the convention would give. The common
// deviation by a long way is the _at suffix, and naming the exact replacement
// is the difference between a finding somebody acts on and one they have to
// think about first.
func renameToTime(name, want string) string {
	if s, ok := strings.CutSuffix(name, "_at"); ok {
		return s + want
	}
	return strings.TrimSuffix(name, "_"+strings.TrimPrefix(want, "_")) + want
}

// --- shared traversal ---

// afterPrefix returns what follows prefix in name, and whether name is prefix
// followed by something that starts a new word. `Deleted` is not a `Delete`
// method and `Lister` is not a `List` one.
func afterPrefix(name, prefix string) (string, bool) {
	rest, ok := strings.CutPrefix(name, prefix)
	if !ok || rest == "" {
		return "", false
	}
	if rest[0] < 'A' || rest[0] > 'Z' {
		return "", false
	}
	return rest, true
}

// fieldType spells a field's type the way a proto file writes it, so that a
// message naming what it found can be compared with what it wanted.
func fieldType(fd protoreflect.FieldDescriptor) string {
	var t string
	switch fd.Kind() {
	case protoreflect.MessageKind, protoreflect.GroupKind:
		t = string(fd.Message().FullName())
	case protoreflect.EnumKind:
		t = string(fd.Enum().FullName())
	default:
		t = fd.Kind().String()
	}
	if fd.IsList() {
		return "repeated " + t
	}
	return t
}

// posOf points a finding at d when d is declared in the file being linted, and
// at the fallback otherwise.
//
// A rule about a method's request message can be reading a message another
// file declares. Pointing at a line number that file does not have would send
// the reader to whatever happens to be on that line here, which is worse than
// pointing at the method, and the method is the declaration this file owns.
func posOf(f File, d, fallback protoreflect.Descriptor) Position {
	if d != nil && d.ParentFile() != nil && d.ParentFile().Path() == f.Desc.Path() {
		if p := f.Pos(d); p.Line != 0 {
			return p
		}
	}
	return f.Pos(fallback)
}

// eachMethod visits every method the file declares, in declaration order.
func eachMethod(fd protoreflect.FileDescriptor, fn func(protoreflect.ServiceDescriptor, protoreflect.MethodDescriptor)) {
	ss := fd.Services()
	for i := 0; i < ss.Len(); i++ {
		s := ss.Get(i)
		ms := s.Methods()
		for j := 0; j < ms.Len(); j++ {
			fn(s, ms.Get(j))
		}
	}
}

// eachField visits every field of every message the file declares, nested ones
// included, in declaration order. Map entries are skipped: they are messages
// the author never wrote.
func eachField(fd protoreflect.FileDescriptor, fn func(protoreflect.FieldDescriptor)) {
	var msgs func(protoreflect.MessageDescriptors)
	msgs = func(ms protoreflect.MessageDescriptors) {
		for i := 0; i < ms.Len(); i++ {
			m := ms.Get(i)
			if m.IsMapEntry() {
				continue
			}
			fs := m.Fields()
			for j := 0; j < fs.Len(); j++ {
				fn(fs.Get(j))
			}
			msgs(m.Messages())
		}
	}
	msgs(fd.Messages())
	es := fd.Extensions()
	for i := 0; i < es.Len(); i++ {
		fn(es.Get(i))
	}
}
