package migrate

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// bufVersion is the only buf config format version this translator reads. The
// earlier format is refused by name rather than approximated: its workspace
// model differs, and a plausible-looking translation of it would be a guess.
const bufVersion = "v2"

// bufYAML is the subset of buf.yaml this translator understands. Keys outside
// it are refused, so a config that says more than we translate cannot pass for
// one we translated in full.
type bufYAML struct {
	Version string      `yaml:"version"`
	Modules []bufModule `yaml:"modules"`
	// Lint and breaking configure checks this tool does not implement. They
	// are read only so that their presence is not an unknown key; nothing is
	// carried over, and the caller is told so.
	Lint     present `yaml:"lint"`
	Breaking present `yaml:"breaking"`
}

type bufModule struct {
	Path string `yaml:"path"`
	// Name is the registry name of the module. It has no counterpart here:
	// there is no registry.
	Name string `yaml:"name"`
	// Excludes removes files from a module. Ignoring it would change which
	// files reach the compiler, so it is refused.
	Excludes stringList `yaml:"excludes"`
}

// bufGenYAML is the subset of buf.gen.yaml this translator understands.
type bufGenYAML struct {
	Version string      `yaml:"version"`
	Managed *bufManaged `yaml:"managed"`
	Plugins []bufPlugin `yaml:"plugins"`
	Inputs  []bufInput  `yaml:"inputs"`
	Clean   *bool       `yaml:"clean"`
}

type bufManaged struct {
	Enabled  *bool         `yaml:"enabled"`
	Disable  []present     `yaml:"disable"`
	Override []bufOverride `yaml:"override"`
}

type bufOverride struct {
	FileOption string `yaml:"file_option"`
	Field      string `yaml:"field"`
	Path       string `yaml:"path"`
	Module     string `yaml:"module"`
	Value      string `yaml:"value"`
}

type bufPlugin struct {
	Local          stringList `yaml:"local"`
	Remote         string     `yaml:"remote"`
	ProtocBuiltin  string     `yaml:"protoc_builtin"`
	Out            string     `yaml:"out"`
	Opt            stringList `yaml:"opt"`
	Strategy       string     `yaml:"strategy"`
	Revision       *int       `yaml:"revision"`
	IncludeImports *bool      `yaml:"include_imports"`
	IncludeWKT     *bool      `yaml:"include_wkt"`
}

type bufInput struct {
	Directory      string     `yaml:"directory"`
	Module         string     `yaml:"module"`
	GitRepo        string     `yaml:"git_repo"`
	Ref            string     `yaml:"ref"`
	Branch         string     `yaml:"branch"`
	Depth          *int       `yaml:"depth"`
	Subdir         string     `yaml:"subdir"`
	Paths          stringList `yaml:"paths"`
	ExcludePaths   stringList `yaml:"exclude_paths"`
	Types          stringList `yaml:"types"`
	IncludeImports *bool      `yaml:"include_imports"`
	ExcludeImports *bool      `yaml:"exclude_imports"`
}

// present records that a key was given without reading what is under it. It
// exists because the blocks this tool does not implement still have to be
// accepted: decoding them into a yaml.Node fails under KnownFields, which
// checks the fields of yaml.Node itself rather than passing the subtree
// through — found by running the translator over real configs, every one of
// which carries a lint block.
type present bool

func (p *present) UnmarshalYAML(*yaml.Node) error {
	*p = true
	return nil
}

// stringList accepts a scalar as well as a list. Both forms occur in the
// configs being translated.
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

// UnmarshalYAML re-establishes strictness by hand: yaml.v3 does not propagate
// KnownFields into a nested Decode, and the types above are reached through
// one because of stringList.
func (m *bufModule) UnmarshalYAML(n *yaml.Node) error   { return decodeStrict(n, (*rawModule)(m)) }
func (p *bufPlugin) UnmarshalYAML(n *yaml.Node) error   { return decodeStrict(n, (*rawPlugin)(p)) }
func (i *bufInput) UnmarshalYAML(n *yaml.Node) error    { return decodeStrict(n, (*rawInput)(i)) }
func (o *bufOverride) UnmarshalYAML(n *yaml.Node) error { return decodeStrict(n, (*rawOverride)(o)) }
func (m *bufManaged) UnmarshalYAML(n *yaml.Node) error  { return decodeStrict(n, (*rawManaged)(m)) }

// The raw aliases exist only to reach the default decoder without recursing
// through the methods above.
type (
	rawModule   bufModule
	rawPlugin   bufPlugin
	rawInput    bufInput
	rawOverride bufOverride
	rawManaged  bufManaged
)

func decodeStrict(n *yaml.Node, out any) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: expected a mapping", n.Line)
	}
	known := yamlKeys(reflect.TypeOf(out).Elem())
	for i := 0; i+1 < len(n.Content); i += 2 {
		if k := n.Content[i]; !known[k.Value] {
			return fmt.Errorf("line %d: unknown key %s (known keys: %s)",
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
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// parseStrict decodes one buf config file, rejecting unknown keys at every
// level. The file name is carried into every message: a migration reads two
// files that share key names, and "unknown key inputs" without a file name is
// half an answer.
func parseStrict(name string, raw []byte, out any) error {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}
