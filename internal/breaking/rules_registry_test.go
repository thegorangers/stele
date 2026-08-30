package breaking

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"testing"
)

// ruleConstants parses rules_registry.go and returns the string value of
// every top-level "Rule*" constant declared there — the RuleFieldRenamed =
// "break/field_renamed" style declarations rules.go emits findings
// through.
func ruleConstants(t *testing.T) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "rules_registry.go", nil, 0)
	if err != nil {
		t.Fatalf("parse rules_registry.go: %v", err)
	}
	consts := make(map[string]string)
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				v, err := stringLitValue(lit.Value)
				if err != nil {
					t.Fatalf("const %s literal %s: %v", name.Name, lit.Value, err)
				}
				consts[name.Name] = v
			}
		}
	}
	return consts
}

// emittedRuleIDs parses rules.go itself and returns every "break/..." id
// it finds emitted as the value of a Finding's Rule: field, resolving each
// Rule* constant identifier back to its literal string via ruleConstants.
// This is the true set the engine can produce; it is derived from the
// source rather than restated as a second hand-written list, so a rule
// added, renamed or removed in rules.go changes this set automatically the
// next time the test runs.
func emittedRuleIDs(t *testing.T) map[string]bool {
	t.Helper()
	consts := ruleConstants(t)

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "rules.go", nil, 0)
	if err != nil {
		t.Fatalf("parse rules.go: %v", err)
	}
	ids := make(map[string]bool)
	ast.Inspect(file, func(n ast.Node) bool {
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		ident, ok := kv.Key.(*ast.Ident)
		if !ok || ident.Name != "Rule" {
			return true
		}
		switch val := kv.Value.(type) {
		case *ast.BasicLit:
			if val.Kind != token.STRING {
				return true
			}
			v, err := stringLitValue(val.Value)
			if err != nil {
				t.Fatalf("Rule literal %s: %v", val.Value, err)
			}
			if v != "" {
				ids[v] = true
			}
		case *ast.Ident:
			v, ok := consts[val.Name]
			if !ok {
				t.Fatalf("rules.go sets Rule: %s, which is not one of the constants declared in rules_registry.go", val.Name)
			}
			ids[v] = true
		default:
			t.Fatalf("rules.go sets Rule: to an unrecognised expression %T", kv.Value)
		}
		return true
	})
	return ids
}

func stringLitValue(raw string) (string, error) {
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return "", fmt.Errorf("not a quoted string: %s", raw)
	}
	return raw[1 : len(raw)-1], nil
}

// fixtureFacts is what the (rule, category, change) fixtures in
// rules_test.go's cases table say about each rule: whether it ever fires
// with a non-empty Change. There is no per-rule Category here — see
// RuleInfo's doc comment: two rules fire under either Category depending on
// the specific change, so "the" category is a per-finding fact
// (Finding.Category, asserted exactly by TestClassify against these same
// fixtures), not a per-rule one the registry could hold.
type fixtureFacts struct {
	sawChange   bool
	sawNoChange bool
}

// fixtureFactsByRule reads cases (the fixture table rules_test.go already
// maintains and asserts against in TestClassify) and reduces it to what
// each rule id is observed to stamp. This is the source of truth for
// HasDiscriminant: it is what the engine's own tests already pin, not a
// second, independently maintained list.
func fixtureFactsByRule() map[string]*fixtureFacts {
	out := make(map[string]*fixtureFacts)
	for _, c := range cases {
		for _, p := range c.want {
			f := out[p.rule]
			if f == nil {
				f = &fixtureFacts{}
				out[p.rule] = f
			}
			if p.change == "" {
				f.sawNoChange = true
			} else {
				f.sawChange = true
			}
		}
	}
	return out
}

func TestRulesRegistryMatchesEngine(t *testing.T) {
	emitted := emittedRuleIDs(t)
	if len(emitted) == 0 {
		t.Fatal("found zero break/ literals in rules.go; the parser probably isn't finding the file")
	}

	registered := make(map[string]RuleInfo)
	for _, r := range Rules() {
		if registered[r.ID].ID != "" {
			t.Errorf("Rules() lists %s more than once", r.ID)
		}
		registered[r.ID] = r
	}

	var missingFromRegistry []string
	for id := range emitted {
		if _, ok := registered[id]; !ok {
			missingFromRegistry = append(missingFromRegistry, id)
		}
	}
	sort.Strings(missingFromRegistry)
	if len(missingFromRegistry) > 0 {
		t.Errorf("rules.go emits %v, which Rules() does not list", missingFromRegistry)
	}

	var orphaned []string
	for id := range registered {
		if !emitted[id] {
			orphaned = append(orphaned, id)
		}
	}
	sort.Strings(orphaned)
	if len(orphaned) > 0 {
		t.Errorf("Rules() lists %v, which rules.go never emits", orphaned)
	}

	facts := fixtureFactsByRule()
	if len(facts) != len(emitted) {
		t.Fatalf("fixture table covers %d rules, rules.go emits %d; the fixture table in rules_test.go must "+
			"assert a firing case for every rule for this test to say anything about HasDiscriminant",
			len(facts), len(emitted))
	}

	for id, f := range facts {
		info, ok := registered[id]
		if !ok {
			continue // already reported above
		}
		if f.sawChange && f.sawNoChange {
			t.Errorf("%s: fixtures fire it both with and without Change; HasDiscriminant cannot be derived from an inconsistent rule", id)
			continue
		}
		wantDiscriminant := f.sawChange
		if info.HasDiscriminant != wantDiscriminant {
			t.Errorf("%s: registry says HasDiscriminant=%v, fixtures show %v", id, info.HasDiscriminant, wantDiscriminant)
		}
	}
}

func TestRulesSortedByID(t *testing.T) {
	rules := Rules()
	for i := 1; i < len(rules); i++ {
		if rules[i-1].ID >= rules[i].ID {
			t.Fatalf("Rules() not sorted: %s before %s", rules[i-1].ID, rules[i].ID)
		}
	}
}

func TestLookupRule(t *testing.T) {
	for _, r := range Rules() {
		got, ok := LookupRule(r.ID)
		if !ok {
			t.Errorf("LookupRule(%q) reported not found", r.ID)
		}
		if got != r {
			t.Errorf("LookupRule(%q) = %+v, want %+v", r.ID, got, r)
		}
	}

	if _, ok := LookupRule("break/nope"); ok {
		t.Error(`LookupRule("break/nope") reported found, want not found`)
	}
	// This registry is break/ only: an AIP-style or lint-style id, even a
	// real one the tool loads elsewhere, must not resolve here.
	if _, ok := LookupRule("stele/enum_value_prefix"); ok {
		t.Error(`LookupRule("stele/enum_value_prefix") reported found, want not found`)
	}
}
