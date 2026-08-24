package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thegorangers/stele/internal/config"
	"github.com/thegorangers/stele/internal/lint"
)

func writeManifest(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "stele.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const lintPreamble = "version: 1\nmodules:\n  - path: api\n"

// TestLintBlockIsTranslated: the manifest is the only place a repository says
// what a rule costs it, and the whole adoption argument rests on that being a
// reviewed file rather than an allow_failure somebody added to a CI job.
func TestLintBlockIsTranslated(t *testing.T) {
	f, err := config.Load(writeManifest(t, lintPreamble+`lint:
  ignore:
    - third_party
  rules:
    - id: stele/enum_value_prefix
      severity: warning
      ignore:
        - api/legacy
    - id: stele/package_version_suffix
      severity: "off"
`))
	if err != nil {
		t.Fatal(err)
	}
	got := lint.ConfigFrom(f.Lint)
	if len(got.Ignore) != 1 || got.Ignore[0] != "third_party" {
		t.Errorf("Ignore = %v", got.Ignore)
	}
	rc, ok := got.Rules["stele/enum_value_prefix"]
	if !ok {
		t.Fatalf("the rule did not reach the engine's configuration: %v", got.Rules)
	}
	if rc.Severity != lint.SeverityWarning {
		t.Errorf("severity = %v, want warning", rc.Severity)
	}
	if len(rc.Ignore) != 1 || rc.Ignore[0] != "api/legacy" {
		t.Errorf("per-rule Ignore = %v", rc.Ignore)
	}
	if got.Rules["stele/package_version_suffix"].Severity != lint.SeverityOff {
		t.Errorf("off did not survive the round trip")
	}
}

// TestAbsentLintBlockIsAbsent. No block means every rule at its default, not
// a lint that does nothing.
func TestAbsentLintBlockIsAbsent(t *testing.T) {
	f, err := config.Load(writeManifest(t, lintPreamble))
	if err != nil {
		t.Fatal(err)
	}
	if f.Lint != nil {
		t.Fatalf("Lint = %v, want nil", f.Lint)
	}
	got := lint.ConfigFrom(f.Lint)
	if len(got.Ignore) != 0 || len(got.Rules) != 0 {
		t.Errorf("a nil block must translate to an empty configuration, got %+v", got)
	}
}

func TestLintBlockIsRefused(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"an empty block", "lint:\n  rules: []\n", "at least one"},
		{"a rule with no id", "lint:\n  rules:\n    - severity: warning\n", "id: missing"},
		{"a malformed id", "lint:\n  rules:\n    - id: enum_value_prefix\n", "namespace/name"},
		{"an unknown severity", "lint:\n  rules:\n    - id: stele/x\n      severity: relaxed\n", "not a severity"},
		{"a repeated rule", "lint:\n  rules:\n    - id: stele/x\n      severity: warning\n    - id: stele/x\n      severity: \"off\"\n", "duplicate"},
		{"an empty ignore entry", "lint:\n  rules:\n    - id: stele/x\n      ignore: [\"\"]\n", "empty"},
		{"an unknown key", "lint:\n  rules:\n    - id: stele/x\n      level: warning\n", "level"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.Load(writeManifest(t, lintPreamble+tc.body))
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the error does not say %q:\n%v", tc.want, err)
			}
		})
	}
}

// TestLintIgnoreAcceptsAScalar mirrors deps.paths and plugin.opt: both shapes
// occur in hand-written configs and refusing the scalar is a gratuitous
// incompatibility.
func TestLintIgnoreAcceptsAScalar(t *testing.T) {
	f, err := config.Load(writeManifest(t, lintPreamble+"lint:\n  ignore: third_party\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := lint.ConfigFrom(f.Lint).Ignore; len(got) != 1 || got[0] != "third_party" {
		t.Errorf("Ignore = %v", got)
	}
}

// TestLintPluginsArePinnedOnTheSameTerms: a rule that changes its mind between
// runs is worse than no rule, so a rule plugin is declared exactly as a code
// generation plugin is — the same tiers, the same words, the same refusals.
// Two vocabularies for one question would be two sets of mistakes to make.
func TestLintPluginsArePinnedOnTheSameTerms(t *testing.T) {
	f, err := config.Load(writeManifest(t, lintPreamble+`lint:
  plugins:
    - name: house_rules
      module: example.com/house/cmd/stele-rule-house
      version: v1.2.0
  rules:
    - id: house/field_comment
      severity: warning
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Lint.Plugins) != 1 {
		t.Fatalf("lint.plugins: %d entries", len(f.Lint.Plugins))
	}
	p := f.Lint.Plugins[0]
	if p.Name != "house_rules" || p.Module != "example.com/house/cmd/stele-rule-house" || p.Version != "v1.2.0" {
		t.Fatalf("lint.plugins[0] = %+v", p)
	}
}

// TestLintPluginRefusals holds the pinning discipline to the same standard the
// code generation plugins are held to, because a rule that is not pinned is a
// rule that can change what it says without anything in the repository
// changing.
func TestLintPluginRefusals(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "a module without a version",
			body: "    - name: house\n      module: example.com/house\n",
			want: "exact version",
		},
		{
			name: "a floating version",
			body: "    - name: house\n      module: example.com/house\n      version: latest\n",
			want: "not an exact version",
		},
		{
			name: "two tiers at once",
			body: "    - name: house\n      module: example.com/house\n      version: v1.0.0\n      path: ./bin/house\n",
			want: "comes from exactly one place",
		},
		{
			name: "a url without a digest",
			body: "    - name: house\n      downloads:\n        - os: linux\n          arch: amd64\n          url: https://example.com/house\n",
			want: "not a pin",
		},
		{
			name: "no name",
			body: "    - module: example.com/house\n      version: v1.0.0\n",
			want: "name",
		},
		{
			name: "two plugins with one name",
			body: "    - name: house\n      path: ./a\n    - name: house\n      path: ./b\n",
			want: "already",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.Load(writeManifest(t, lintPreamble+"lint:\n  plugins:\n"+tc.body))
			if err == nil {
				t.Fatal("must be refused")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the message must contain %q: %v", tc.want, err)
			}
		})
	}
}

// TestLintPluginNameIsNotARuleID keeps two names apart that are easy to
// conflate: the plugin is what the manifest declares and what errors call it,
// and the rule id is what the plugin serves and what configuration names.
func TestLintPluginNameIsNotARuleID(t *testing.T) {
	_, err := config.Load(writeManifest(t, lintPreamble+`lint:
  plugins:
    - name: house/field_comment
      path: ./bin/house
`))
	if err == nil || !strings.Contains(err.Error(), "rule id") {
		t.Fatalf("a plugin name that is written as a rule id must be refused, saying so: %v", err)
	}
}

// TestABarePathRulePluginIsRefused is the one place a rule plugin is declared
// on different terms from a code generation plugin, and the difference is
// deliberate.
//
// The vocabulary is shared because the question is the same one — which bytes
// ran — and two vocabularies would be two sets of mistakes. What differs is the
// consequence of answering it with "whatever is on this machine's PATH". A
// generator that changed produces different generated code, and the change is
// in the diff somebody reviews. A rule that changed produces a different
// judgement, and there is no artefact of it anywhere: the build reddens or
// greens overnight with nothing in the repository to explain it. So the tier is
// still spellable, and it is not usable by accident.
func TestABarePathRulePluginIsRefused(t *testing.T) {
	_, err := config.Load(writeManifest(t, lintPreamble+`lint:
  plugins:
    - name: stele-rule-house
`))
	if err == nil {
		t.Fatal("a rule plugin that is whatever PATH resolves must not load silently")
	}
	for _, want := range []string{"PATH", "not pinned", "unpinned: true"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message must contain %q, so the reader can both understand and act: %v", want, err)
		}
	}
}

// TestABarePathRulePluginIsAllowedWhenSaidOutLoud. The opt-in is one reviewed
// line in a file people read, which is the same mechanism `severity: warning`
// uses: the point is not to forbid the tier but to stop it being invisible.
func TestABarePathRulePluginIsAllowedWhenSaidOutLoud(t *testing.T) {
	f, err := config.Load(writeManifest(t, lintPreamble+`lint:
  plugins:
    - name: stele-rule-house
      unpinned: true
`))
	if err != nil {
		t.Fatal(err)
	}
	if p := f.Lint.Plugins[0]; !p.Unpinned {
		t.Errorf("lint.plugins[0] = %+v; the opt-in must be readable by the run that has to announce it", p)
	}
}

// TestUnpinnedOnAPinnedTierIsRefused. A manifest that says both is a manifest
// whose reader cannot tell which half is true, and the half that is false is
// the one somebody relied on.
func TestUnpinnedOnAPinnedTierIsRefused(t *testing.T) {
	for _, tier := range []string{
		"      module: example.com/house\n      version: v1.0.0\n",
		"      path: ./bin/house\n",
	} {
		_, err := config.Load(writeManifest(t, lintPreamble+
			"lint:\n  plugins:\n    - name: house\n      unpinned: true\n"+tier))
		if err == nil {
			t.Fatalf("declaring both a tier and unpinned must be refused:\n%s", tier)
		}
		if !strings.Contains(err.Error(), "unpinned") {
			t.Errorf("the message must name the field that contradicts the tier: %v", err)
		}
	}
}
