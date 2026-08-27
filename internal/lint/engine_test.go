package lint_test

import (
	"strings"
	"testing"

	"github.com/thegorangers/stele/internal/lint"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// dirty is a file that breaks exactly one rule, twice.
const dirtySrc = `syntax = "proto3"; package example.v1;
	enum OrderStatus { ORDER_STATUS_UNSPECIFIED = 0; PLACED = 1; PAID = 2; }`

const dirtyPath = "example/v1/order.proto"

const prefixRule = "stele/enum_value_prefix"

func checkDirty(t *testing.T, cfg lint.Config) lint.Result {
	t.Helper()
	e, err := lint.New(lint.Builtin(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return e.Check([]protoreflect.FileDescriptor{compileSource(t, dirtyPath, dirtySrc)})
}

// TestSeverityDecidesWhetherTheBuildIsRed is the adoption mechanism. A
// repository that has never linted has findings on the first run, and a tool
// whose only settings are off and red gets set to off.
func TestSeverityDecidesWhetherTheBuildIsRed(t *testing.T) {
	unconfigured := checkDirty(t, lint.Config{})
	if unconfigured.Errors != 2 || unconfigured.Warnings != 0 {
		t.Fatalf("an unconfigured rule must fail the run: got %d errors, %d warnings",
			unconfigured.Errors, unconfigured.Warnings)
	}

	demoted := checkDirty(t, lint.Config{Rules: map[string]lint.RuleConfig{
		prefixRule: {Severity: lint.SeverityWarning},
	}})
	if demoted.Errors != 0 || demoted.Warnings != 2 {
		t.Errorf("a demoted rule must report without failing: got %d errors, %d warnings",
			demoted.Errors, demoted.Warnings)
	}
	if len(findingsFor(demoted, prefixRule)) != 2 {
		t.Errorf("a demoted rule must still report; it is a warning, not a silence")
	}

	off := checkDirty(t, lint.Config{Rules: map[string]lint.RuleConfig{
		prefixRule: {Severity: lint.SeverityOff},
	}})
	if n := len(findingsFor(off, prefixRule)); n != 0 {
		t.Errorf("a rule switched off must say nothing: got %d findings", n)
	}
	if off.Errors != 0 {
		t.Errorf("switching a rule off must not leave its findings behind: %d errors", off.Errors)
	}
}

// TestIgnoreIsByPathPrefix pins the matching rule. Entries are import paths or
// prefixes of them, not globs: there is no dialect to get subtly wrong, and no
// question about whether a wildcard crosses a directory separator.
func TestIgnoreIsByPathPrefix(t *testing.T) {
	cases := []struct {
		name  string
		cfg   lint.Config
		quiet bool
	}{
		{"the exact file", lint.Config{Ignore: []string{dirtyPath}}, true},
		{"the directory it is in", lint.Config{Ignore: []string{"example/v1"}}, true},
		{"the directory with a trailing slash", lint.Config{Ignore: []string{"example/v1/"}}, true},
		{"an ancestor directory", lint.Config{Ignore: []string{"example"}}, true},
		{"a prefix that is not a path component", lint.Config{Ignore: []string{"exa"}}, false},
		{"a sibling directory", lint.Config{Ignore: []string{"example/v2"}}, false},
		{"per-rule, the same file", lint.Config{Rules: map[string]lint.RuleConfig{
			prefixRule: {Ignore: []string{"example/v1"}},
		}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := len(findingsFor(checkDirty(t, tc.cfg), prefixRule))
			if tc.quiet && got != 0 {
				t.Errorf("the path is ignored, but the rule reported %d findings", got)
			}
			if !tc.quiet && got == 0 {
				t.Errorf("the path is not ignored, but the rule said nothing")
			}
		})
	}
}

// TestPerRuleIgnoreDoesNotSilenceOtherRules: an exemption is for one rule over
// one path, and an exemption that quietly covered everything would be the
// blanket switch-off this whole design exists to avoid.
func TestPerRuleIgnoreDoesNotSilenceOtherRules(t *testing.T) {
	res := checkDirty(t, lint.Config{Rules: map[string]lint.RuleConfig{
		"stele/package_version_suffix": {Ignore: []string{"example/v1"}},
	}})
	if len(findingsFor(res, prefixRule)) == 0 {
		t.Error("an exemption for one rule silenced another")
	}
}

// TestUnknownRuleIsRefused: a typo in an ignore list, or a rule that has been
// removed, means the repository believes it has a protection or an exemption
// it does not have. This is the only moment anybody would find out.
func TestUnknownRuleIsRefused(t *testing.T) {
	_, err := lint.New(lint.Builtin(), lint.Config{Rules: map[string]lint.RuleConfig{
		"stele/enum_value_prefixx": {Severity: lint.SeverityOff},
	}})
	if err == nil {
		t.Fatal("a rule nothing carries was accepted")
	}
	for _, want := range []string{"stele/enum_value_prefixx", "stele/enum_value_prefix"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q:\n%v", want, err)
		}
	}
}

// TestDuplicateRuleIDIsRefused: whichever of two rules with one ID ran, the
// other's configuration would silently describe nothing.
func TestDuplicateRuleIDIsRefused(t *testing.T) {
	dup := append(lint.Builtin(), lint.Builtin()[0])
	if _, err := lint.New(dup, lint.Config{}); err == nil {
		t.Fatal("two rules were allowed to claim one ID")
	}
}

// TestRuleIDsAreNamespaced. The namespace is not decoration: retrofitting one
// would rename every ID, a renamed ID silently stops matching the ignore list
// that named it, and a protection that switches itself off is worse than one
// that was never switched on.
func TestRuleIDsAreNamespaced(t *testing.T) {
	for _, r := range lint.Builtin() {
		if err := lint.CheckID(r.ID()); err != nil {
			t.Errorf("%v", err)
		}
		// Both namespaces are this tool's, and which one a rule is in is
		// what decides its default severity — so a rule in neither would be
		// a rule shipped at a severity nobody chose.
		if ns, _, _ := strings.Cut(r.ID(), "/"); ns != lint.NamespaceBuiltin && ns != lint.NamespaceAIP {
			t.Errorf("%s: a rule that ships here must live in the %q or %q namespace, not %q",
				r.ID(), lint.NamespaceBuiltin, lint.NamespaceAIP, ns)
		}
		if r.Description() == "" {
			t.Errorf("%s: no description; it is how somebody decides whether to switch the rule off", r.ID())
		}
	}
}

// TestMalformedRuleIDIsRefused covers the out-of-tree case: a rule that shipped
// without a namespace would take a name the built-in set may later want.
func TestMalformedRuleIDIsRefused(t *testing.T) {
	for _, id := range []string{"", "enum_value_prefix", "stele/", "/name", "Stele/name", "stele/Name", "a/b/c"} {
		if err := lint.CheckID(id); err == nil {
			t.Errorf("%q was accepted as a rule ID", id)
		}
	}
	for _, id := range []string{"stele/enum_value_prefix", "acme/no_money_as_double", "a1/b2"} {
		if err := lint.CheckID(id); err != nil {
			t.Errorf("%q was refused as a rule ID: %v", id, err)
		}
	}
}

// TestFindingsAreOrdered: a lint report that reorders itself between runs
// cannot be diffed, and diffing two reports is how anybody sees whether a
// change made things better or worse.
func TestFindingsAreOrdered(t *testing.T) {
	first := checkDirty(t, lint.Config{})
	second := checkDirty(t, lint.Config{})
	if len(first.Findings) != len(second.Findings) {
		t.Fatalf("two runs over one file found %d and %d", len(first.Findings), len(second.Findings))
	}
	for i := range first.Findings {
		if first.Findings[i].String() != second.Findings[i].String() {
			t.Errorf("finding %d differs between two runs:\n%s\n%s", i, first.Findings[i], second.Findings[i])
		}
	}
	for i := 1; i < len(first.Findings); i++ {
		a, b := first.Findings[i-1], first.Findings[i]
		if a.Path > b.Path || (a.Path == b.Path && a.Pos.Line > b.Pos.Line) {
			t.Errorf("findings are not in file order: %v then %v", a, b)
		}
	}
}

// TestRenderedFindingLeadsWithTheLocation. The first token of the line is what
// an editor and a log scraper both key on.
func TestRenderedFindingLeadsWithTheLocation(t *testing.T) {
	res := checkDirty(t, lint.Config{})
	got := findingsFor(res, prefixRule)[0].String()
	head, fix, ok := strings.Cut(got, "\n")
	if !ok {
		t.Fatalf("a finding must carry a fix on its own line:\n%s", got)
	}
	if !strings.HasPrefix(head, dirtyPath+":2:") {
		t.Errorf("the line must open with path:line:col, got:\n%s", head)
	}
	if !strings.Contains(head, ": error: "+prefixRule+": ") {
		t.Errorf("the line must name the severity and the rule, got:\n%s", head)
	}
	if !strings.HasPrefix(fix, "    ") || strings.TrimSpace(fix) == "" {
		t.Errorf("the fix must be indented under the finding, got: %q", fix)
	}
}

// TestCleanFileIsCountedAsChecked. A run that checked nothing looks exactly
// like a clean run without this.
func TestCleanFileIsCountedAsChecked(t *testing.T) {
	e, err := lint.New(lint.Builtin(), lint.Config{})
	if err != nil {
		t.Fatal(err)
	}
	res := e.Check([]protoreflect.FileDescriptor{
		compileSource(t, "example/v1/ok.proto", `syntax = "proto3"; package example.v1;
			enum OrderStatus { ORDER_STATUS_UNSPECIFIED = 0; ORDER_STATUS_PLACED = 1; }`),
	})
	if len(res.Findings) != 0 {
		t.Errorf("a clean file produced findings: %v", res.Findings)
	}
	if res.Files != 1 {
		t.Errorf("Files = %d, want 1", res.Files)
	}
	if res.Rules != len(lint.Builtin()) {
		t.Errorf("Rules = %d, want %d", res.Rules, len(lint.Builtin()))
	}

	ignored := e.Check([]protoreflect.FileDescriptor{compileSource(t, dirtyPath, dirtySrc)})
	_ = ignored
	e2, err := lint.New(lint.Builtin(), lint.Config{Ignore: []string{"example"}})
	if err != nil {
		t.Fatal(err)
	}
	res2 := e2.Check([]protoreflect.FileDescriptor{compileSource(t, dirtyPath, dirtySrc)})
	if res2.Files != 0 {
		t.Errorf("an ignored file must not be counted as checked: Files = %d", res2.Files)
	}
}
