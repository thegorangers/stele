// Package managed synthesises the per-file options a managed-mode generator
// writes into descriptors.
//
// The options matter because they travel: they are serialised into the
// descriptor that generated code embeds, so a file generated with them and a
// file generated without them differ byte for byte even when every message in
// them is identical. Reproducing them is therefore not a nicety for other
// languages, it is what makes output comparable at all.
//
// Nothing in this package was written from documentation. The option set, its
// spelling and its odd corners were read off real generated artefacts with
// internal/managed/extract, anonymised, and frozen as golden files; the rules
// below are the smallest thing that reproduces those measurements. Where a
// rule could not be measured, the code says so rather than guessing quietly.
package managed

import (
	"path"
	"regexp"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

// Override is a value applied to the files under a path.
//
// Path is a prefix of the file's import path, in the coordinates an import
// statement uses. An empty Path matches every file; an empty Value disables
// the override.
type Override struct {
	Path  string
	Value string
}

// Config carries what the caller must supply, because it cannot be derived
// from the file: where the generated Go code will live.
type Config struct {
	// GoPackagePrefix is joined with the file's directory to form go_package.
	GoPackagePrefix Override
}

// Apply rewrites fd's options in place.
//
// Every option it sets, it overwrites: the artefacts show a source file that
// declared its own go_package coming out with a different one, so this is
// replacement, not defaulting. Options the synthesiser has nothing to say
// about are left as they were found.
func Apply(fd *descriptorpb.FileDescriptorProto, cfg Config) {
	if fd == nil {
		return
	}
	pkg := fd.GetPackage()
	parts := packageParts(pkg)
	if len(parts) == 0 {
		return
	}

	opts := fd.GetOptions()
	if opts == nil {
		opts = &descriptorpb.FileOptions{}
		fd.Options = opts
	}

	opts.JavaPackage = proto.String("com." + pkg)
	opts.JavaOuterClassname = proto.String(javaOuterClassname(fd.GetName()))
	opts.JavaMultipleFiles = proto.Bool(true)
	opts.ObjcClassPrefix = proto.String(objcClassPrefix(parts))
	opts.CsharpNamespace = proto.String(strings.Join(titleEach(parts), "."))
	opts.PhpNamespace = proto.String(strings.Join(titleEach(parts), `\`))
	opts.PhpMetadataNamespace = proto.String(strings.Join(append(titleEach(parts), "GPBMetadata"), `\`))
	opts.RubyPackage = proto.String(strings.Join(titleEach(parts), "::"))

	if v := goPackage(fd.GetName(), cfg.GoPackagePrefix); v != "" {
		opts.GoPackage = proto.String(v)
	}
}

// goPackage returns the go_package for a file, or "" when the override does
// not apply to it.
//
// The measured form is the prefix joined with the file's directory, plus an
// explicit package alias after a semicolon — the alias appears even when the
// source file declared a go_package without one.
func goPackage(file string, o Override) string {
	if o.Value == "" || !underPath(file, o.Path) {
		return ""
	}
	dir := path.Dir(file)
	if dir == "." {
		return o.Value + ";" + goPackageAlias(nil)
	}
	elems := strings.Split(dir, "/")
	return o.Value + "/" + dir + ";" + goPackageAlias(elems)
}

// goPackageAlias names the Go package for a directory. A trailing version
// element carries no meaning on its own, so it is glued to the element before
// it: acme/widget/v1 becomes widgetv1.
func goPackageAlias(elems []string) string {
	switch len(elems) {
	case 0:
		return ""
	case 1:
		return sanitiseIdentifier(elems[0])
	}
	last := elems[len(elems)-1]
	if isVersion(last) {
		return sanitiseIdentifier(elems[len(elems)-2] + last)
	}
	return sanitiseIdentifier(last)
}

// underPath reports whether file lies under the selector's path. The selector
// names a directory, so it matches on element boundaries: a selector of "acme"
// does not reach "acmecorp/...".
func underPath(file, selector string) bool {
	selector = strings.Trim(selector, "/")
	if selector == "" {
		return true
	}
	return file == selector || strings.HasPrefix(file, selector+"/")
}

// javaOuterClassname turns a file name into a class name: the base name
// without its extension, upper-camel-cased, with "Proto" appended.
//
// SHORTCUT: no collision handling; limit: a file whose outer class name
// equals a top-level message, enum or service in it — the reference generator
// disambiguates, we would emit a name it does not; upgrade: measure the
// disambiguation on an artefact that exhibits it, then implement it here.
// None of the artefacts read so far contains such a file, so the rule cannot
// be reproduced without inventing it.
func javaOuterClassname(file string) string {
	base := path.Base(file)
	base = strings.TrimSuffix(base, path.Ext(base))
	var b strings.Builder
	for _, word := range strings.FieldsFunc(base, func(r rune) bool { return r == '_' || r == '-' || r == '.' }) {
		b.WriteString(upperFirst(word))
	}
	return b.String() + "Proto"
}

// objcClassPrefix builds the Objective-C prefix from the initials of the
// package elements, skipping a trailing version element, padded to three
// characters with X.
//
// SHORTCUT: no truncation and no reserved-name avoidance; limit: a package
// with more than three meaningful elements yields a longer prefix, and a
// package whose initials spell the reserved GPB is emitted unchanged;
// upgrade: obtain an artefact with either shape and extend from what it
// shows. Every package measured had two or three meaningful elements and none
// collided, so both rules would be guesses.
func objcClassPrefix(parts []string) string {
	if isVersion(parts[len(parts)-1]) {
		parts = parts[:len(parts)-1]
	}
	var b strings.Builder
	for _, p := range parts {
		if p == "" {
			continue
		}
		b.WriteString(strings.ToUpper(p[:1]))
	}
	prefix := b.String()
	for len(prefix) < 3 {
		prefix += "X"
	}
	return prefix
}

// packageParts splits a proto package into its elements.
func packageParts(pkg string) []string {
	if pkg == "" {
		return nil
	}
	return strings.Split(pkg, ".")
}

// titleEach upper-cases the first character of each element, leaving the rest
// alone: the artefacts show "paymentgateway" becoming "Paymentgateway", not
// "PaymentGateway".
func titleEach(parts []string) []string {
	out := make([]string, 0, len(parts)+1)
	for _, p := range parts {
		out = append(out, upperFirst(p))
	}
	return out
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// versionPattern matches the version elements used by the convention these
// files follow: v1, v2beta1, v1alpha2.
var versionPattern = regexp.MustCompile(`^v[0-9]+(?:(?:alpha|beta|test)[0-9]*)?$`)

func isVersion(s string) bool { return versionPattern.MatchString(s) }

// sanitiseIdentifier makes a directory element usable as a Go package name.
var notIdentifier = regexp.MustCompile(`[^A-Za-z0-9_]`)

func sanitiseIdentifier(s string) string { return notIdentifier.ReplaceAllString(s, "_") }
