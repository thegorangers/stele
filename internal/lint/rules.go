package lint

import (
	"fmt"
	"path"
	"strings"

	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Builtin returns the rules that ship with this tool.
//
// # Why these, and why so few
//
// Every ID here is permanent. It goes into somebody's ignore list, and under
// the versioning policy in RELEASING.md renaming it later is a breaking
// change — so the question a rule has to answer before it ships is not "is
// this good style" but "can I defend this ID for ever". The set is therefore
// small on purpose, and each rule was measured against a real fleet of 35
// hand-written proto files before it was included: the measurement is in the
// roadmap, per rule.
//
// The set has two halves, and each has an answer to "what breaks if I ignore
// this?".
//
//   - Where a file lives and what it declares. A package that does not match
//     the directory, or is missing entirely, is the ambiguity this tool's own
//     resolver exists to catch: two repositories can then supply one import
//     path, and which one wins is an accident of arrival. A package with no
//     version cannot be evolved without breaking every consumer at once. A
//     file with no syntax is proto2, quietly, and generates different code.
//
//   - The enum zero value, and the names around it. In proto3 the zero value
//     is what an unset field reads as, so an enum whose zero value means
//     something real cannot distinguish "not set" from that meaning. This is
//     the one naming convention in the set with a runtime consequence, and it
//     is the one half of the fleet's only real deviation.
//
// What is deliberately absent is listed in the roadmap: message, field and
// service casing (mechanical, but zero findings on the measured fleet and
// nothing they protect that the tool can name), and everything that needs
// intent — whether a method should be streaming, whether a field should be
// optional, whether a response type is well chosen.
func Builtin() []Rule {
	return []Rule{
		syntaxDeclared{},
		packageDeclared{},
		packageLowerSnakeCase{},
		packageVersionSuffix{},
		directoryMatchesPackage{},
		enumZeroValueUnspecified{},
		enumValuePrefix{},
		enumValueUpperSnakeCase{},
	}
}

// builtinID spells a built-in rule's ID, so that the reserved namespace is
// written once.
func builtinID(name string) string { return NamespaceBuiltin + "/" + name }

// --- file and package ---

type syntaxDeclared struct{}

func (syntaxDeclared) ID() string { return builtinID("syntax_declared") }
func (syntaxDeclared) Description() string {
	return "a file states its syntax, rather than being proto2 by omission"
}

// Check reads the syntax off the descriptor proto rather than off the
// reflective descriptor, because that is the only place the difference
// survives: a file that declares proto2 and a file that declares nothing are
// both Proto2 reflectively, and only one of them is a mistake.
func (r syntaxDeclared) Check(f File) ([]Finding, error) {
	if protodesc.ToFileDescriptorProto(f.Desc).GetSyntax() != "" {
		return nil, nil
	}
	return []Finding{{
		Message: "the file declares no syntax, so it is proto2",
		Fix:     `add syntax = "proto3"; as the first statement, or declare proto2 explicitly if that is what is meant`,
	}}, nil
}

type packageDeclared struct{}

func (packageDeclared) ID() string { return builtinID("package_declared") }
func (packageDeclared) Description() string {
	return "a file declares a package"
}

func (r packageDeclared) Check(f File) ([]Finding, error) {
	if f.Desc.Package() != "" {
		return nil, nil
	}
	return []Finding{{
		Message: "the file declares no package, so everything it defines is in the global scope",
		Fix: "declare a package matching the directory the file is in, such as " +
			pathToPackage(path.Dir(string(f.Desc.Path()))),
	}}, nil
}

type packageLowerSnakeCase struct{}

func (packageLowerSnakeCase) ID() string { return builtinID("package_lower_snake_case") }
func (packageLowerSnakeCase) Description() string {
	return "every component of a package is lower_snake_case"
}

func (r packageLowerSnakeCase) Check(f File) ([]Finding, error) {
	pkg := string(f.Desc.Package())
	if pkg == "" {
		return nil, nil // packageDeclared has already said so
	}
	for _, c := range strings.Split(pkg, ".") {
		if lowerSnake(c) != nil {
			return []Finding{{
				Pos:     f.PosOfPackage(),
				Message: fmt.Sprintf("package %s has a component (%q) that is not lower_snake_case", pkg, c),
				Fix: "write every component in lower_snake_case; a package is a directory on disk in every " +
					"language the descriptor is generated into, and the case of a directory is not portable",
			}}, nil
		}
	}
	return nil, nil
}

type packageVersionSuffix struct{}

func (packageVersionSuffix) ID() string { return builtinID("package_version_suffix") }
func (packageVersionSuffix) Description() string {
	return "a package ends in a version, such as v1 or v2beta1"
}

func (r packageVersionSuffix) Check(f File) ([]Finding, error) {
	pkg := string(f.Desc.Package())
	if pkg == "" {
		return nil, nil
	}
	last := pkg[strings.LastIndex(pkg, ".")+1:]
	if versionComponent(last) {
		return nil, nil
	}
	return []Finding{{
		Pos:     f.PosOfPackage(),
		Message: fmt.Sprintf("package %s does not end in a version", pkg),
		Fix: "append a version component, such as " + pkg + ".v1. Without one there is nowhere to put the " +
			"next incompatible shape of this contract, so every consumer has to change on the same day",
	}}, nil
}

// versionComponent reports whether c is a version the way proto packages spell
// one: v, digits, and optionally a stability word with its own digits —
// v1, v2, v1alpha, v2beta1. It is a narrow spelling on purpose: a wider one
// would accept a package component that merely starts with a v.
func versionComponent(c string) bool {
	if len(c) < 2 || c[0] != 'v' || c[1] < '0' || c[1] > '9' {
		return false
	}
	i := 1
	for i < len(c) && c[i] >= '0' && c[i] <= '9' {
		i++
	}
	rest := c[i:]
	switch {
	case rest == "":
		return true
	case strings.HasPrefix(rest, "alpha"):
		rest = rest[len("alpha"):]
	case strings.HasPrefix(rest, "beta"):
		rest = rest[len("beta"):]
	default:
		return false
	}
	for i := 0; i < len(rest); i++ {
		if rest[i] < '0' || rest[i] > '9' {
			return false
		}
	}
	return true
}

type directoryMatchesPackage struct{}

func (directoryMatchesPackage) ID() string { return builtinID("directory_matches_package") }
func (directoryMatchesPackage) Description() string {
	return "a file's directory, in import-path coordinates, matches its package"
}

// Check compares the directory of the import path with the package. This is
// the rule closest to the tool's own reason for existing: the import path is
// the only name a consumer has for a file, and when it does not follow from
// the package, two repositories can supply one path with different contents
// and nothing in the file says which is which.
func (r directoryMatchesPackage) Check(f File) ([]Finding, error) {
	pkg := string(f.Desc.Package())
	if pkg == "" {
		return nil, nil
	}
	dir := path.Dir(string(f.Desc.Path()))
	if dir == "." {
		dir = ""
	}
	want := packageToPath(pkg)
	if dir == want {
		return nil, nil
	}
	return []Finding{{
		Pos: f.PosOfPackage(),
		Message: fmt.Sprintf("the file is at %s/ but declares package %s, which belongs at %s/",
			dir, pkg, want),
		Fix: fmt.Sprintf("move the file to %s/%s, or change the package to %s. The import path is the only "+
			"name a consumer has for this file, and it should follow from the package rather than from where "+
			"somebody put it", want, path.Base(string(f.Desc.Path())), pathToPackage(dir)),
	}}, nil
}

func packageToPath(pkg string) string { return strings.ReplaceAll(pkg, ".", "/") }
func pathToPackage(dir string) string {
	if dir == "" || dir == "." {
		return "(none)"
	}
	return strings.ReplaceAll(dir, "/", ".")
}

// --- enums ---

type enumZeroValueUnspecified struct{}

func (enumZeroValueUnspecified) ID() string { return builtinID("enum_zero_value_unspecified") }
func (enumZeroValueUnspecified) Description() string {
	return "an enum's first value is zero and named ENUM_NAME_UNSPECIFIED"
}

// Check is the one rule in the set with a runtime consequence rather than a
// stylistic one. In proto3 an unset field reads as the zero value, so an enum
// whose zero value carries a meaning cannot tell "not set" from that meaning,
// and no amount of care at the call site recovers the difference.
func (r enumZeroValueUnspecified) Check(f File) ([]Finding, error) {
	var out []Finding
	eachEnum(f.Desc, func(e protoreflect.EnumDescriptor) {
		if e.Values().Len() == 0 {
			return
		}
		zero := e.Values().Get(0)
		want := screamingSnake(string(e.Name())) + "_UNSPECIFIED"
		if zero.Number() == 0 && string(zero.Name()) == want {
			return
		}
		out = append(out, Finding{
			Pos: f.Pos(zero),
			Message: fmt.Sprintf("the first value of enum %s is %s = %d, not %s = 0",
				e.Name(), zero.Name(), zero.Number(), want),
			Fix: fmt.Sprintf("add %s = 0; as the first value. An unset field of this type reads as the "+
				"zero value, so whatever occupies zero cannot be distinguished from a field nobody set", want),
		})
	})
	return out, nil
}

type enumValuePrefix struct{}

func (enumValuePrefix) ID() string { return builtinID("enum_value_prefix") }
func (enumValuePrefix) Description() string {
	return "an enum's values are prefixed with the enum's name"
}

// Check enforces the prefix because C++ scoping rules put enum values in the
// scope enclosing the enum, not in the enum: two enums in one package with a
// value named the same thing do not compile together, and the failure lands on
// whoever imports both rather than on whoever wrote either.
func (r enumValuePrefix) Check(f File) ([]Finding, error) {
	var out []Finding
	eachEnum(f.Desc, func(e protoreflect.EnumDescriptor) {
		want := screamingSnake(string(e.Name())) + "_"
		for i := 0; i < e.Values().Len(); i++ {
			v := e.Values().Get(i)
			if strings.HasPrefix(string(v.Name()), want) {
				continue
			}
			out = append(out, Finding{
				Pos:     f.Pos(v),
				Message: fmt.Sprintf("value %s of enum %s is not prefixed with %s", v.Name(), e.Name(), want),
				Fix: fmt.Sprintf("rename it to %s%s. Enum values live in the scope around the enum, not "+
					"inside it, so an unprefixed name collides with the same name in any other enum of this "+
					"package — and the collision is a compile error for a consumer, not for this file",
					want, v.Name()),
			})
		}
	})
	return out, nil
}

type enumValueUpperSnakeCase struct{}

func (enumValueUpperSnakeCase) ID() string { return builtinID("enum_value_upper_snake_case") }
func (enumValueUpperSnakeCase) Description() string {
	return "an enum's values are UPPER_SNAKE_CASE"
}

func (r enumValueUpperSnakeCase) Check(f File) ([]Finding, error) {
	var out []Finding
	eachEnum(f.Desc, func(e protoreflect.EnumDescriptor) {
		for i := 0; i < e.Values().Len(); i++ {
			v := e.Values().Get(i)
			if upperSnake(string(v.Name())) {
				continue
			}
			out = append(out, Finding{
				Pos:     f.Pos(v),
				Message: fmt.Sprintf("value %s of enum %s is not UPPER_SNAKE_CASE", v.Name(), e.Name()),
				Fix: fmt.Sprintf("rename it to %s. Generated code derives constant names from this one, "+
					"and the derivation differs between languages when the case does",
					screamingSnake(string(v.Name()))),
			})
		}
	})
	return out, nil
}

// eachEnum visits every enum the file declares, nested ones included, in
// declaration order. Order is part of the contract: a rule that visited a map
// would report the same file differently on different runs.
func eachEnum(fd protoreflect.FileDescriptor, fn func(protoreflect.EnumDescriptor)) {
	var enums func(protoreflect.EnumDescriptors)
	enums = func(es protoreflect.EnumDescriptors) {
		for i := 0; i < es.Len(); i++ {
			fn(es.Get(i))
		}
	}
	var msgs func(protoreflect.MessageDescriptors)
	msgs = func(ms protoreflect.MessageDescriptors) {
		for i := 0; i < ms.Len(); i++ {
			m := ms.Get(i)
			if m.IsMapEntry() {
				// A map field synthesises a message the author never wrote.
				continue
			}
			enums(m.Enums())
			msgs(m.Messages())
		}
	}
	enums(fd.Enums())
	msgs(fd.Messages())
}

// upperSnake reports whether s is UPPER_SNAKE_CASE.
func upperSnake(s string) bool {
	if s == "" || !(s[0] >= 'A' && s[0] <= 'Z') {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

// screamingSnake turns an identifier into UPPER_SNAKE_CASE.
//
// The boundary rules are the ones that make an acronym come out whole:
// a lower-to-upper transition is a boundary (OrderStatus -> ORDER_STATUS), and
// so is the last upper of a run followed by a lower (HTTPServer ->
// HTTP_SERVER), while the middle of a run is not. A digit does not open a
// boundary: V2 stays V2 rather than becoming V_2.
func screamingSnake(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '_' {
			b.WriteByte('_')
			continue
		}
		if i > 0 && c >= 'A' && c <= 'Z' {
			prev := s[i-1]
			prevUpper := prev >= 'A' && prev <= 'Z'
			nextLower := i+1 < len(s) && s[i+1] >= 'a' && s[i+1] <= 'z'
			if (!prevUpper && prev != '_') || (prevUpper && nextLower) {
				b.WriteByte('_')
			}
		}
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		b.WriteByte(c)
	}
	return b.String()
}
