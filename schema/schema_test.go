package schema_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/thegorangers/stele/internal/breaking"
	"github.com/thegorangers/stele/internal/config"
	"github.com/thegorangers/stele/internal/lint"
	"github.com/thegorangers/stele/internal/lockfile"
	"github.com/thegorangers/stele/internal/source"
	"github.com/thegorangers/stele/schema"
	"gopkg.in/yaml.v3"
)

// The corpus exercises the parser's refusal of a breaking-rule id no rule
// this tool has carries, and of a permission's change field against what the
// named rule's discriminant requires. Both checks are wired at init rather
// than by a direct import inside internal/config — see
// config.BreakingRuleFact for why.
func init() {
	rules := breaking.Rules()
	facts := make([]config.BreakingRuleFact, len(rules))
	for i, r := range rules {
		facts[i] = config.BreakingRuleFact{ID: r.ID, HasDiscriminant: r.HasDiscriminant}
	}
	config.RegisterBreakingRuleFacts(facts)
}

// The corpus is one set of example files per described format, split by what
// the two checkers are expected to say about it:
//
//	valid/   both the schema and the parser accept it
//	invalid/ both reject it; the file states the reason in a leading comment
//	beyond/  the schema accepts it and the parser rejects it, because the rule
//	         is one JSON Schema cannot express. The file states which rule.
//	stricter/ the schema rejects it and the parser accepts it, deliberately.
//	         There is exactly one cause, YAML's implicit typing, and the file
//	         states it.
//
// Any other outcome is a failure. That includes a beyond/ file the schema
// happens to reject: the schema would then be enforcing a rule this corpus
// claims it cannot, and the claim, not the schema, is what has drifted. It
// also includes a stricter/ file the parser rejects, which would mean the
// divergence had been fixed and the exception should go.
const (
	expectAccept   = "valid"
	expectReject   = "invalid"
	expectBeyond   = "beyond"
	expectStricter = "stricter"
)

// categories is the corpus in the order a reader should meet it.
func categories() []string {
	return []string{expectAccept, expectReject, expectBeyond, expectStricter}
}

// format is one described file format, with the two checkers that must agree
// about it.
type format struct {
	name     string // the corpus subdirectory, and the name in failures
	filename string // the name the parser expects on disk
	schema   []byte // the embedded JSON Schema
	parse    func(path string) error
}

func formats() []format {
	return []format{
		{
			name:     "manifest",
			filename: "stele.yaml",
			schema:   schema.ManifestJSON,
			parse:    parseManifest,
		},
		{
			name:     "lock",
			filename: "stele.lock",
			schema:   schema.LockJSON,
			parse: func(path string) error {
				_, err := lockfile.Load(path)
				return err
			},
		},
		{
			name:     "baseline",
			filename: "stele.baseline",
			schema:   schema.BaselineJSON,
			parse: func(path string) error {
				_, err := lint.LoadBaseline(path)
				return err
			},
		},
	}
}

// parseManifest runs a manifest through everything that decides whether the
// tool will accept it: the manifest parser, and then the address parser over
// every dependency address, which is where an address is actually refused —
// config.Load stores the address, source.ParseAddr judges it. A schema that
// described only config.Load would be silent about the one field users most
// often get wrong.
func parseManifest(path string) error {
	f, err := config.Load(path)
	if err != nil {
		return err
	}
	hosts := source.DefaultHosts()
	for i, d := range f.Deps {
		if _, err := source.ParseAddr(d.Git, hosts); err != nil {
			return fmt.Errorf("deps[%d].git: %w", i, err)
		}
	}
	return nil
}

// TestSchemaAgreesWithParser is the whole point of the schema living here: an
// editor's opinion about a file and the tool's opinion about the same file
// must not differ. Every example is put to both, and a disagreement in either
// direction fails.
func TestSchemaAgreesWithParser(t *testing.T) {
	for _, f := range formats() {
		t.Run(f.name, func(t *testing.T) {
			sch := compile(t, f.name, f.schema)
			for _, expect := range categories() {
				dir := filepath.Join("testdata", f.name, expect)
				for _, name := range examples(t, dir) {
					t.Run(expect+"/"+name, func(t *testing.T) {
						path := filepath.Join(dir, name)
						schemaErr := validate(t, sch, path)
						parserErr := f.parse(copyAs(t, path, f.filename))
						check(t, expect, schemaErr, parserErr)
					})
				}
			}
		})
	}
}

// check compares the two verdicts against the one the corpus declared.
func check(t *testing.T, expect string, schemaErr, parserErr error) {
	t.Helper()
	switch expect {
	case expectAccept:
		if schemaErr != nil {
			t.Errorf("the schema rejects an example the parser accepts:\n%v", schemaErr)
		}
		if parserErr != nil {
			t.Errorf("the parser rejects an example the schema accepts:\n%v", parserErr)
		}
	case expectReject:
		if schemaErr == nil && parserErr == nil {
			t.Errorf("both the schema and the parser accept an example that is invalid")
		}
		if schemaErr == nil {
			t.Errorf("the schema accepts what the parser rejects:\n%v", parserErr)
		}
		if parserErr == nil {
			t.Errorf("the parser accepts what the schema rejects:\n%v", schemaErr)
		}
	case expectStricter:
		if schemaErr == nil {
			t.Errorf("the schema accepts an example declared stricter-than-the-parser; " +
				"the exception is no longer real and should be removed")
		}
		if parserErr != nil {
			t.Errorf("the parser rejects an example declared stricter-than-the-parser; "+
				"the divergence is gone, and so should the exception be:\n%v", parserErr)
		}
	case expectBeyond:
		if schemaErr != nil {
			t.Errorf("the schema rejects an example declared beyond its reach; "+
				"either the rule is expressible after all, or the example is simply invalid:\n%v", schemaErr)
		}
		if parserErr == nil {
			t.Errorf("the parser accepts an example declared invalid; nothing here is beyond the schema")
		}
	}
}

// TestRejectedExamplesStateTheirReason keeps the corpus readable: a file that
// is invalid without saying why is a puzzle for the next person, and a puzzle
// is what a stale document becomes.
func TestRejectedExamplesStateTheirReason(t *testing.T) {
	for _, f := range formats() {
		for _, expect := range []string{expectReject, expectBeyond, expectStricter} {
			dir := filepath.Join("testdata", f.name, expect)
			for _, name := range examples(t, dir) {
				path := filepath.Join(dir, name)
				b, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				first, _, _ := strings.Cut(string(b), "\n")
				want := "# " + expect + ":"
				if !strings.HasPrefix(first, want) {
					t.Errorf("%s: must open with a %q comment stating why; got %q", path, want, first)
				}
			}
		}
	}
}

// TestSchemasDeclareTheDraft pins the dialect. A schema without $schema is
// interpreted by whatever the reader defaults to, which is the drift this
// whole file exists to prevent.
func TestSchemasDeclareTheDraft(t *testing.T) {
	const draft = "https://json-schema.org/draft/2020-12/schema"
	for _, f := range formats() {
		var doc struct {
			Schema string `json:"$schema"`
			ID     string `json:"$id"`
		}
		if err := json.Unmarshal(f.schema, &doc); err != nil {
			t.Fatalf("%s: %v", f.name, err)
		}
		if doc.Schema != draft {
			t.Errorf("%s: $schema is %q, want %q", f.name, doc.Schema, draft)
		}
		if doc.ID == "" {
			t.Errorf("%s: $id is missing; an editor needs a stable identity to bind a file to", f.name)
		}
	}
}

func compile(t *testing.T, name string, raw []byte) *jsonschema.Schema {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("%s schema is not valid JSON: %v", name, err)
	}
	c := jsonschema.NewCompiler()
	url := "https://stele.invalid/" + name + ".schema.json"
	if err := c.AddResource(url, doc); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	sch, err := c.Compile(url)
	if err != nil {
		t.Fatalf("%s schema does not compile: %v", name, err)
	}
	return sch
}

// validate runs one YAML example through the schema. YAML is converted to the
// JSON data model first, because that is the model JSON Schema is defined
// over; nothing in either format uses a non-string key or a YAML tag.
func validate(t *testing.T, sch *jsonschema.Schema, path string) error {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		// A file that is not YAML at all is rejected by both, and by the same
		// reading of it.
		return fmt.Errorf("%s: %w", path, err)
	}
	j, err := json.Marshal(jsonable(doc))
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(j))
	if err != nil {
		t.Fatal(err)
	}
	if err := sch.Validate(value); err != nil {
		var ve *jsonschema.ValidationError
		if errors.As(err, &ve) {
			return fmt.Errorf("%s: %s", path, ve.Error())
		}
		return err
	}
	return nil
}

// jsonable rewrites the YAML data model into the JSON one.
func jsonable(v any) any {
	switch v := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, e := range v {
			out[k] = jsonable(e)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(v))
		for k, e := range v {
			out[fmt.Sprint(k)] = jsonable(e)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, e := range v {
			out[i] = jsonable(e)
		}
		return out
	default:
		return v
	}
}

// copyAs puts an example under the name the parser expects, so that error
// messages read the way a user's would.
func copyAs(t *testing.T, path, name string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(dst, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return dst
}

func examples(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		t.Fatalf("%s: no examples; a corpus that describes nothing proves nothing", dir)
	}
	return names
}
