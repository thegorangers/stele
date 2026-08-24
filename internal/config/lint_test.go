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
