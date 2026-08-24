package config

import (
	"bytes"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Load reads, parses and validates the manifest at path.
//
// Parsing is strict: a key outside the specification is an error naming that
// key, never a silent skip.
func Load(path string) (*File, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true) // an unknown key is an error naming that key
	var f File
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := f.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &f, nil
}

// Version is the only manifest format version this tool understands.
const Version = 1

func (f *File) validate() error {
	switch {
	case f.Version == 0:
		return fmt.Errorf("version: missing; expected %d", Version)
	case f.Version != Version:
		return fmt.Errorf("version: %d is not supported; expected %d", f.Version, Version)
	}
	// A manifest is not required to declare a module. A consumer that owns no
	// protos declares none, and every input of its generate targets names a
	// dependency instead.

	declared := make(map[string]bool, len(f.Modules))
	for i, m := range f.Modules {
		if m.Path == "" {
			return fmt.Errorf("modules[%d].path: missing", i)
		}
		if declared[m.Path] {
			return fmt.Errorf("modules[%d].path: duplicate module path %q", i, m.Path)
		}
		declared[m.Path] = true
	}

	depNames := make(map[string]bool, len(f.Deps))
	for i, d := range f.Deps {
		switch {
		case d.Name == "":
			return fmt.Errorf("deps[%d].name: missing", i)
		case d.Git == "":
			return fmt.Errorf("deps[%d].git: missing for dependency %q", i, d.Name)
		case d.Ref == "":
			return fmt.Errorf("deps[%d].ref: missing for dependency %q", i, d.Name)
		}
		if depNames[d.Name] {
			return fmt.Errorf("deps[%d].name: duplicate dependency name %q", i, d.Name)
		}
		depNames[d.Name] = true
	}

	targets := make(map[string]bool, len(f.Generate))
	for i, g := range f.Generate {
		switch {
		case g.Name == "":
			return fmt.Errorf("generate[%d].name: missing", i)
		case len(g.Inputs) == 0:
			return fmt.Errorf("generate[%d].inputs: at least one input is required for target %q", i, g.Name)
		case len(g.Plugins) == 0:
			return fmt.Errorf("generate[%d].plugins: at least one plugin is required for target %q", i, g.Name)
		}
		if targets[g.Name] {
			return fmt.Errorf("generate[%d].name: duplicate target name %q", i, g.Name)
		}
		targets[g.Name] = true

		for j, in := range g.Inputs {
			switch {
			case in.Module == "" && in.Dep == "":
				return fmt.Errorf("generate[%d].inputs[%d]: one of module or dep is required", i, j)
			case in.Module != "" && in.Dep != "":
				return fmt.Errorf("generate[%d].inputs[%d]: module %q and dep %q are mutually exclusive; an input selects from one place",
					i, j, in.Module, in.Dep)
			case in.Module != "" && !declared[in.Module]:
				return fmt.Errorf("generate[%d].inputs[%d].module: %q is not declared in modules (declared: %s)",
					i, j, in.Module, strings.Join(modulePaths(f.Modules), ", "))
			case in.Dep != "" && !depNames[in.Dep]:
				return fmt.Errorf("generate[%d].inputs[%d].dep: %q is not declared in deps (declared: %s)",
					i, j, in.Dep, strings.Join(depNamesOf(f.Deps), ", "))
			}
		}
		if err := g.Managed.validate(i); err != nil {
			return err
		}
		for j, p := range g.Plugins {
			if p.Local == "" {
				return fmt.Errorf("generate[%d].plugins[%d].local: missing", i, j)
			}
			if p.Out == "" {
				return fmt.Errorf("generate[%d].plugins[%d].out: missing for plugin %q", i, j, p.Local)
			}
			if err := p.validateSource(i, j); err != nil {
				return err
			}
		}
	}
	return f.Lint.validate()
}

// validate checks the lint block.
//
// Rule IDs are checked for shape here and for existence in the lint engine.
// The split is not squeamishness about an import cycle: this package describes
// what a manifest may say, and the set of rules that exist is a property of
// the engine and of whatever rules are loaded beside it. A manifest naming a
// third-party rule is well formed here and refused there, by a message that
// can list what is actually loaded.
func (l *Lint) validate() error {
	if l == nil {
		return nil
	}
	if len(l.Ignore) == 0 && len(l.Rules) == 0 && len(l.Plugins) == 0 {
		// As with managed: a block that configures nothing reads as if it
		// configured something.
		return fmt.Errorf("lint: at least one of ignore, plugins or rules is required")
	}
	if err := validateLintPlugins(l.Plugins); err != nil {
		return err
	}
	if err := validateIgnore("lint.ignore", l.Ignore); err != nil {
		return err
	}
	seen := make(map[string]int, len(l.Rules))
	for i, r := range l.Rules {
		if r.ID == "" {
			return fmt.Errorf("lint.rules[%d].id: missing", i)
		}
		if err := checkLintRuleID(r.ID); err != nil {
			return fmt.Errorf("lint.rules[%d].id: %w", i, err)
		}
		if first, dup := seen[r.ID]; dup {
			return fmt.Errorf("lint.rules[%d].id: duplicate configuration for %s, already set by lint.rules[%d]; "+
				"a rule has one severity, and the tool cannot honour two", i, r.ID, first)
		}
		seen[r.ID] = i
		if r.Severity != "" && !slices.Contains(LintSeverities, r.Severity) {
			return fmt.Errorf("lint.rules[%d].severity: %q is not a severity; write one of %s",
				i, r.Severity, strings.Join(LintSeverities, ", "))
		}
		if err := validateIgnore(fmt.Sprintf("lint.rules[%d].ignore", i), r.Ignore); err != nil {
			return err
		}
	}
	return nil
}

// validateLintPlugins checks the rule plugins: that each is named, that no two
// share a name, and that where its binary comes from is declared the way a
// code generation plugin's is.
//
// The name is refused when it is written as a rule id, because the two are
// easy to conflate and conflating them is not harmless: a plugin serves rules
// whose ids it declares itself, so a manifest that names the plugin after one
// of them describes a relationship the tool cannot honour, and the author
// would go looking for their severity line in the wrong place.
func validateLintPlugins(ps []LintPlugin) error {
	seen := make(map[string]int, len(ps))
	for i, p := range ps {
		field := fmt.Sprintf("lint.plugins[%d]", i)
		if p.Name == "" {
			return fmt.Errorf("%s.name: missing; a rule plugin is named here, and that name is what "+
				"every error about it says", field)
		}
		if strings.Contains(p.Name, "/") {
			return fmt.Errorf("%s.name: %q is written as a rule id, and this is the plugin's name, not a rule's; "+
				"one plugin serves the rules it declares, and each of those is configured under lint.rules by "+
				"the id the plugin gives it — name the plugin something like %s instead",
				field, p.Name, strings.ReplaceAll(p.Name, "/", "_"))
		}
		if first, dup := seen[p.Name]; dup {
			return fmt.Errorf("%s.name: %q is already declared by lint.plugins[%d]; a plugin name identifies "+
				"one binary, and an error naming it would not say which", field, p.Name, first)
		}
		seen[p.Name] = i
		if err := p.source(field).validate(); err != nil {
			return err
		}
	}
	return nil
}

// source is where this rule plugin's binary comes from. It is the same
// declaration a code generation plugin makes, checked by the same code.
func (p *LintPlugin) source(field string) binarySource {
	return binarySource{
		field: field, name: p.Name,
		module: p.Module, version: p.Version,
		downloads: p.Downloads, downloadsDeclared: p.Downloads != nil,
		path: p.Path,
	}
}

// checkLintRuleID checks the shape of a rule id: namespace/name, both parts
// lower_snake_case. Whether a rule of that id exists is the engine's question,
// and it is asked where the loaded rules are known.
func checkLintRuleID(id string) error {
	ns, name, ok := strings.Cut(id, "/")
	if !ok {
		return fmt.Errorf("%q is not a rule id; write it as namespace/name, such as stele/enum_value_prefix", id)
	}
	for label, part := range map[string]string{"namespace": ns, "name": name} {
		if part == "" {
			return fmt.Errorf("%q: the %s is empty; write it as namespace/name", id, label)
		}
		if part[0] < 'a' || part[0] > 'z' {
			return fmt.Errorf("%q: the %s %q must start with a lower-case letter", id, label, part)
		}
		for i := 0; i < len(part); i++ {
			c := part[i]
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
				return fmt.Errorf("%q: the %s %q must be lower_snake_case", id, label, part)
			}
		}
	}
	return nil
}

// validateIgnore refuses an entry that matches nothing. An empty string is the
// dangerous one: read as a prefix it would be silently harmless, and read as a
// path it would be a file nobody has. Either way the author believes they have
// written an exemption.
func validateIgnore(field string, entries []string) error {
	for i, e := range entries {
		if strings.TrimSpace(e) == "" {
			return fmt.Errorf("%s[%d]: empty; write an import path, or a prefix of one such as api/legacy", field, i)
		}
		if strings.ContainsAny(e, "*?[") {
			return fmt.Errorf("%s[%d]: %q looks like a glob, and these are not globs; "+
				"write an import path, or a prefix of one such as api/legacy", field, i, e)
		}
	}
	return nil
}

// validate checks the managed block of generate target i.
func (m *Managed) validate(i int) error {
	if m == nil {
		return nil
	}
	if len(m.Override) == 0 {
		// A managed block that asks for nothing is a config that reads as if
		// it configured something. Saying so beats generating without it and
		// leaving the author to find out from a diff.
		return fmt.Errorf("generate[%d].managed.override: at least one override is required", i)
	}
	seen := make(map[string]bool, len(m.Override))
	for j, o := range m.Override {
		switch {
		case o.FileOption == "":
			return fmt.Errorf("generate[%d].managed.override[%d].file_option: missing", i, j)
		case !slices.Contains(fileOptions, o.FileOption):
			return fmt.Errorf("generate[%d].managed.override[%d].file_option: %s is not a file option this tool synthesises (known: %s)",
				i, j, o.FileOption, strings.Join(fileOptions, ", "))
		case o.Value == "":
			return fmt.Errorf("generate[%d].managed.override[%d].value: missing for file option %s", i, j, o.FileOption)
		case seen[o.FileOption]:
			return fmt.Errorf("generate[%d].managed.override[%d].file_option: duplicate override for %s", i, j, o.FileOption)
		}
		seen[o.FileOption] = true
	}
	return nil
}

func depNamesOf(ds []Dep) []string {
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.Name)
	}
	return out
}

func modulePaths(ms []Module) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Path)
	}
	return out
}

// stringList accepts both a single scalar and a list of scalars. Both forms
// occur in hand-written configs, and rejecting the scalar form would be a
// gratuitous incompatibility.
type stringList []string

func (s *stringList) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		var v string
		if err := n.Decode(&v); err != nil {
			return err
		}
		*s = stringList{v}
	case yaml.SequenceNode:
		var v []string
		if err := n.Decode(&v); err != nil {
			return err
		}
		*s = v
	default:
		return fmt.Errorf("line %d: expected a string or a list of strings", n.Line)
	}
	return nil
}

// The types below carry a hand-written unmarshaler because at least one of
// their fields accepts two YAML shapes. yaml.v3 does not propagate KnownFields
// into a nested Decode, so strictness is re-established by decodeStrict.

// UnmarshalYAML decodes a dependency, normalising paths to a list.
func (d *Dep) UnmarshalYAML(n *yaml.Node) error {
	var aux struct {
		Name   string     `yaml:"name"`
		Git    string     `yaml:"git"`
		Ref    string     `yaml:"ref"`
		Module string     `yaml:"module"`
		Paths  stringList `yaml:"paths"`
	}
	if err := decodeStrict(n, &aux); err != nil {
		return err
	}
	*d = Dep{Name: aux.Name, Git: aux.Git, Ref: aux.Ref, Module: aux.Module, Paths: aux.Paths}
	return nil
}

// UnmarshalYAML decodes a generation input, normalising paths to a list.
func (in *Input) UnmarshalYAML(n *yaml.Node) error {
	var aux struct {
		Module string     `yaml:"module"`
		Dep    string     `yaml:"dep"`
		Paths  stringList `yaml:"paths"`
	}
	if err := decodeStrict(n, &aux); err != nil {
		return err
	}
	*in = Input{Module: aux.Module, Dep: aux.Dep, Paths: aux.Paths}
	return nil
}

// UnmarshalYAML decodes the lint block, normalising ignore to a list.
func (l *Lint) UnmarshalYAML(n *yaml.Node) error {
	var aux struct {
		Ignore  stringList   `yaml:"ignore"`
		Plugins []LintPlugin `yaml:"plugins"`
		Rules   []LintRule   `yaml:"rules"`
	}
	if err := decodeStrict(n, &aux); err != nil {
		return err
	}
	*l = Lint{Ignore: aux.Ignore, Plugins: aux.Plugins, Rules: aux.Rules}
	return nil
}

// UnmarshalYAML decodes one rule's configuration, normalising ignore to a list.
func (r *LintRule) UnmarshalYAML(n *yaml.Node) error {
	var aux struct {
		ID       string     `yaml:"id"`
		Severity string     `yaml:"severity"`
		Ignore   stringList `yaml:"ignore"`
	}
	if err := decodeStrict(n, &aux); err != nil {
		return err
	}
	*r = LintRule{ID: aux.ID, Severity: aux.Severity, Ignore: aux.Ignore}
	return nil
}

// UnmarshalYAML decodes a plugin, normalising opt to a list.
func (p *Plugin) UnmarshalYAML(n *yaml.Node) error {
	var aux struct {
		Local     string      `yaml:"local"`
		Module    string      `yaml:"module"`
		Version   string      `yaml:"version"`
		Downloads *[]Download `yaml:"downloads"`
		Path      string      `yaml:"path"`
		Out       string      `yaml:"out"`
		Opt       stringList  `yaml:"opt"`
	}
	if err := decodeStrict(n, &aux); err != nil {
		return err
	}
	*p = Plugin{
		Local: aux.Local, Module: aux.Module, Version: aux.Version,
		Path: aux.Path, Out: aux.Out, Opt: aux.Opt,
	}
	if aux.Downloads != nil {
		// An explicitly empty list is kept as an empty non-nil slice so that
		// validation can refuse it by name. Read as "no downloads" it would be
		// a plugin that quietly fell through to a PATH lookup, which is the
		// one thing the download tier exists to prevent.
		p.Downloads = *aux.Downloads
		if p.Downloads == nil {
			p.Downloads = []Download{}
		}
	}
	return nil
}

// UnmarshalYAML decodes a rule plugin. It keeps an explicitly empty downloads
// list apart from an absent one, for the reason Plugin does.
func (p *LintPlugin) UnmarshalYAML(n *yaml.Node) error {
	var aux struct {
		Name      string      `yaml:"name"`
		Module    string      `yaml:"module"`
		Version   string      `yaml:"version"`
		Downloads *[]Download `yaml:"downloads"`
		Path      string      `yaml:"path"`
	}
	if err := decodeStrict(n, &aux); err != nil {
		return err
	}
	*p = LintPlugin{Name: aux.Name, Module: aux.Module, Version: aux.Version, Path: aux.Path}
	if aux.Downloads != nil {
		p.Downloads = *aux.Downloads
		if p.Downloads == nil {
			p.Downloads = []Download{}
		}
	}
	return nil
}

// Tiers a plugin's binary can be declared on, named so that an error can say
// which two a manifest asked for at once.
const (
	tierModule    = "module"
	tierDownloads = "downloads"
	tierPath      = "path"
)

// binarySource is the part of a plugin declaration that says where its binary
// comes from, seen apart from what the plugin is for.
//
// It exists because there are two kinds of plugin in this manifest — a code
// generator and a rule — and exactly one question about where either comes
// from. The tiers, the words and the refusals are shared by being the same
// code rather than by being written twice and compared by eye; a second
// implementation would be a second set of mistakes, and the manifest would
// pin one kind of plugin more carefully than the other for no reason anybody
// could state.
//
// field is the manifest coordinate of the declaration, such as
// `generate[0].plugins[1]` or `lint.plugins[0]`, so that every message points
// at the line that has to be edited.
type binarySource struct {
	field     string
	name      string
	module    string
	version   string
	downloads []Download
	// downloadsDeclared distinguishes an absent downloads key from one
	// written with no entries. The second is refused by name: read as "no
	// downloads" it would be a plugin that quietly fell through to a PATH
	// lookup, which is the one thing the download tier exists to prevent.
	downloadsDeclared bool
	path              string
}

// tiers returns the tiers this declaration uses, in the order they are
// documented. Emptiness is what decides: a tier is declared when any field
// belonging to it carries a value, so a half-written tier is still detected as
// that tier and reported against it rather than silently falling through to
// the PATH lookup.
func (b binarySource) tiers() []string {
	var out []string
	if b.module != "" || b.version != "" {
		out = append(out, tierModule)
	}
	if b.downloadsDeclared {
		out = append(out, tierDownloads)
	}
	if b.path != "" {
		out = append(out, tierPath)
	}
	return out
}

// validate checks the declaration that says where a binary comes from: that at
// most one tier is declared, and that the declared one is complete.
//
// Two tiers at once is refused before anything else, because every later
// message would have to guess which of the two the author meant.
//
// Within the module tier the two halves are refused separately, because they
// are different mistakes. A version without a module asks for a version of
// nothing, and the tool would silently run whatever PATH holds while the
// manifest claimed a version — the exact drift this is here to end. A module
// without a version asks the tool to choose, and any choice it made would be a
// choice the manifest does not record.
func (b binarySource) validate() error {
	declared := b.tiers()
	if len(declared) > 1 {
		return fmt.Errorf("%s: plugin %q declares both %s and %s; "+
			"a plugin comes from exactly one place, and the tool cannot honour two",
			b.field, b.name, declared[0], declared[1])
	}
	switch {
	case len(declared) == 0:
		// A bare name, looked up on PATH. Nothing is pinned and nothing is
		// claimed to be.
		return nil
	case declared[0] == tierDownloads:
		return b.validateDownloads()
	case declared[0] == tierPath:
		return nil
	}
	switch {
	case b.module == "":
		return fmt.Errorf("%s.version: %s is declared without a module; "+
			"a version can only be honoured for a plugin the tool installs itself", b.field, b.version)
	case b.version == "":
		return fmt.Errorf("%s.version: missing for plugin %q; "+
			"a declared module must name the exact version to install", b.field, b.name)
	case !exactVersion(b.version):
		return fmt.Errorf("%s.version: %q is not an exact version for plugin %q; "+
			"write the version to install, such as v1.36.11, so that the manifest states what generated the code",
			b.field, b.version, b.name)
	}
	return nil
}

// validateDownloads checks the download tier: that there is something to
// download, that each entry is a complete statement, and that no two entries
// claim the same platform.
//
// The address and the digest are one statement in two fields: an address
// without a digest pins nothing, and a digest without an address describes
// bytes the tool will never fetch. The platform is the third part of that
// statement — without it the entry says which bytes to run but not which
// machine they run on, which is exactly what a single-url form got wrong.
//
// Two entries for one platform are refused here rather than at selection
// time, because it is a defect in the file that every reader can see, and a
// file that is wrong only on somebody else's machine is the failure mode this
// whole shape exists to end.
func (b binarySource) validateDownloads() error {
	if len(b.downloads) == 0 {
		return fmt.Errorf("%s.downloads: declared with no entries for plugin %q; "+
			"a download tier with no platforms in it pins nothing and would leave the plugin to be found on PATH",
			b.field, b.name)
	}
	seen := make(map[string]int, len(b.downloads))
	for k, d := range b.downloads {
		switch {
		case d.OS == "":
			return fmt.Errorf("%s.downloads[%d].os: missing for plugin %q; "+
				"write the GOOS the binary is for, such as linux or darwin, spelled as Go spells it",
				b.field, k, b.name)
		case d.Arch == "":
			return fmt.Errorf("%s.downloads[%d].arch: missing for plugin %q; "+
				"write the GOARCH the binary is for, such as amd64 or arm64, spelled as Go spells it",
				b.field, k, b.name)
		case d.URL == "":
			return fmt.Errorf("%s.downloads[%d].url: missing for plugin %q; "+
				"a digest pins the bytes of a download, and there is nothing here to download",
				b.field, k, b.name)
		case d.SHA256 == "":
			return fmt.Errorf("%s.downloads[%d].sha256: missing for plugin %q; "+
				"a url without a hash is an address, not a pin: whatever the server serves would be run",
				b.field, k, b.name)
		case !sha256Hex(d.SHA256):
			return fmt.Errorf("%s.downloads[%d].sha256: %q is not a sha256 for plugin %q; "+
				"write the 64 hex characters of the digest of the file at the url", b.field, k, d.SHA256, b.name)
		}
		if first, dup := seen[d.Platform()]; dup {
			return fmt.Errorf("%s.downloads[%d]: %s is already declared by downloads[%d] "+
				"for plugin %q; exactly one entry may match a platform, and the tool cannot honour two",
				b.field, k, d.Platform(), first, b.name)
		}
		seen[d.Platform()] = k
	}
	return nil
}

// source is where this code generation plugin's binary comes from.
func (p *Plugin) source(field string) binarySource {
	return binarySource{
		field: field, name: p.Local,
		module: p.Module, version: p.Version,
		downloads: p.Downloads, downloadsDeclared: p.Downloads != nil,
		path: p.Path,
	}
}

// validateSource checks where the binary of code generation plugin j of target
// i comes from.
func (p *Plugin) validateSource(i, j int) error {
	return p.source(fmt.Sprintf("generate[%d].plugins[%d]", i, j)).validate()
}

// sha256Hex reports whether s is a sha256 digest written out in hex. The
// length is the point: a truncated digest is a weaker pin than it looks, and
// looking like a pin is worse than not having one.
func sha256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// exactVersion reports whether v is a version `go install` resolves to exactly
// one build. Anything the module system would resolve at run time — latest,
// upgrade, patch, a branch name, a bare query — is refused.
func exactVersion(v string) bool {
	if !strings.HasPrefix(v, "v") {
		return false
	}
	rest := v[1:]
	if rest == "" || !(rest[0] >= '0' && rest[0] <= '9') {
		return false
	}
	return strings.Count(v, ".") >= 2
}

// decodeStrict decodes a mapping node into out, rejecting any key that out
// does not declare. It reports the offending key and its line.
func decodeStrict(n *yaml.Node, out any) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: expected a mapping", n.Line)
	}
	known := yamlKeys(reflect.TypeOf(out).Elem())
	for i := 0; i+1 < len(n.Content); i += 2 {
		k := n.Content[i]
		if !known[k.Value] {
			return fmt.Errorf("line %d: field %s not found in the specification (known fields: %s)",
				k.Line, k.Value, strings.Join(sortedKeys(known), ", "))
		}
	}
	return n.Decode(out)
}

func yamlKeys(t reflect.Type) map[string]bool {
	keys := make(map[string]bool, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("yaml"), ",")
		if name == "" {
			name = strings.ToLower(t.Field(i).Name)
		}
		keys[name] = true
	}
	return keys
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// The field order of the struct is not preserved by the map; sort for a
	// stable message.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
