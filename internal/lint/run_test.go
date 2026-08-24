package lint_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thegorangers/stele/internal/lint"
)

// repo lays out a manifest and its proto files, and returns the directory.
func repo(t *testing.T, manifest string, protos map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "stele.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, src := range protos {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const oneModule = "version: 1\nmodules:\n  - path: api\n"

const dirtyProto = `syntax = "proto3";
package example.v1;

enum OrderStatus {
  ORDER_STATUS_UNSPECIFIED = 0;
  PLACED = 1;
}
`

// TestRunReportsAgainstTheRepositoryItOwns. A lint that also judged the
// dependencies would redden a build over a contract nobody in this repository
// can change, and the only recovery would be to switch the whole thing off.
func TestRunReportsAgainstTheRepositoryItOwns(t *testing.T) {
	dir := repo(t, oneModule, map[string]string{
		"api/example/v1/order.proto": dirtyProto,
	})
	rep, err := lint.Run(context.Background(), lint.Options{Dir: dir, NoLock: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Files != 1 {
		t.Errorf("Files = %d, want 1", rep.Files)
	}
	if got := findingsFor(rep.Result, prefixRule); len(got) != 1 {
		t.Fatalf("want one finding, got %d: %v", len(got), got)
	}
	if !rep.Failed() {
		t.Error("an error-severity finding must fail the run")
	}
	var b strings.Builder
	rep.Write(&b)
	out := b.String()
	// The path a reader can open, not the import path they cannot.
	if !strings.Contains(out, filepath.ToSlash(filepath.Join("api", "example/v1/order.proto"))+":6:") {
		t.Errorf("the output does not point at a path in this repository:\n%s", out)
	}
	if !strings.Contains(rep.Summary(), "1 error") {
		t.Errorf("the summary does not count the findings: %q", rep.Summary())
	}
}

// TestSeverityInTheManifestGreensTheBuild is the adoption mechanism end to
// end: the same repository, the same finding, and a run that passes because
// the manifest says so.
func TestSeverityInTheManifestGreensTheBuild(t *testing.T) {
	dir := repo(t, oneModule+`lint:
  rules:
    - id: stele/enum_value_prefix
      severity: warning
`, map[string]string{"api/example/v1/order.proto": dirtyProto})
	rep, err := lint.Run(context.Background(), lint.Options{Dir: dir, NoLock: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Failed() {
		t.Error("a demoted rule must not fail the run")
	}
	if rep.Warnings != 1 {
		t.Errorf("Warnings = %d, want 1", rep.Warnings)
	}
	var b strings.Builder
	rep.Write(&b)
	if !strings.Contains(b.String(), ": warning: ") {
		t.Errorf("a warning must still be reported:\n%s", b.String())
	}
}

// TestUnknownRuleInTheManifestIsRefused: the manifest passed its own shape
// check, and this is the only place the claim is measured against what exists.
func TestUnknownRuleInTheManifestIsRefused(t *testing.T) {
	dir := repo(t, oneModule+"lint:\n  rules:\n    - id: acme/no_money_as_double\n      severity: \"off\"\n",
		map[string]string{"api/example/v1/order.proto": dirtyProto})
	_, err := lint.Run(context.Background(), lint.Options{Dir: dir, NoLock: true})
	if err == nil {
		t.Fatal("a rule no loaded rule carries was accepted")
	}
	if !strings.Contains(err.Error(), "acme/no_money_as_double") {
		t.Errorf("the error does not name the rule:\n%v", err)
	}
}

// TestNoFilesIsAnError, for the same reason generate refuses to write nothing:
// a lint that checked no files is indistinguishable from a clean one, and
// reporting success would leave the author to find out from a green CI job
// that nothing has been checked for a month.
func TestNoFilesIsAnError(t *testing.T) {
	dir := repo(t, oneModule+"lint:\n  ignore:\n    - example\n",
		map[string]string{"api/example/v1/order.proto": dirtyProto})
	_, err := lint.Run(context.Background(), lint.Options{Dir: dir, NoLock: true})
	if err == nil {
		t.Fatal("a run that checked nothing reported success")
	}
	if !strings.Contains(err.Error(), "no files") {
		t.Errorf("the error does not say what happened:\n%v", err)
	}
}

// TestCleanRepositoryPasses is the other half of every rule's claim, at the
// level of the command: a repository that keeps the rules gets silence.
func TestCleanRepositoryPasses(t *testing.T) {
	dir := repo(t, oneModule, map[string]string{
		"api/example/v1/order.proto": `syntax = "proto3";
package example.v1;

enum OrderStatus {
  ORDER_STATUS_UNSPECIFIED = 0;
  ORDER_STATUS_PLACED = 1;
}
`})
	rep, err := lint.Run(context.Background(), lint.Options{Dir: dir, NoLock: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Failed() || len(rep.Findings) != 0 {
		t.Errorf("a clean repository produced findings: %v", rep.Findings)
	}
	if !strings.Contains(rep.Summary(), "1 file") {
		t.Errorf("a clean run must still say what it checked: %q", rep.Summary())
	}
}
