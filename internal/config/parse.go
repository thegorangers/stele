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

// UnmarshalYAML decodes a plugin, normalising opt to a list.
func (p *Plugin) UnmarshalYAML(n *yaml.Node) error {
	var aux struct {
		Local   string     `yaml:"local"`
		Module  string     `yaml:"module"`
		Version string     `yaml:"version"`
		Out     string     `yaml:"out"`
		Opt     stringList `yaml:"opt"`
	}
	if err := decodeStrict(n, &aux); err != nil {
		return err
	}
	*p = Plugin{Local: aux.Local, Module: aux.Module, Version: aux.Version, Out: aux.Out, Opt: aux.Opt}
	return nil
}

// validateSource checks the pair that says where a plugin binary comes from.
//
// The two halves are refused separately because they are different mistakes. A
// version without a module asks for a version of nothing, and the tool would
// silently run whatever PATH holds while the manifest claimed a version — the
// exact drift this is here to end. A module without a version asks the tool to
// choose, and any choice it made would be a choice the manifest does not
// record.
func (p *Plugin) validateSource(i, j int) error {
	switch {
	case p.Module == "" && p.Version == "":
		return nil
	case p.Module == "":
		return fmt.Errorf("generate[%d].plugins[%d].version: %s is declared without a module; "+
			"a version can only be honoured for a plugin the tool installs itself", i, j, p.Version)
	case p.Version == "":
		return fmt.Errorf("generate[%d].plugins[%d].version: missing for plugin %q; "+
			"a declared module must name the exact version to install", i, j, p.Local)
	case !exactVersion(p.Version):
		return fmt.Errorf("generate[%d].plugins[%d].version: %q is not an exact version for plugin %q; "+
			"write the version to install, such as v1.36.11, so that the manifest states what generated the code",
			i, j, p.Version, p.Local)
	}
	return nil
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
