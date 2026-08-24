package lint_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/thegorangers/stele/internal/lint"
)

// buildExampleRule compiles the rule that lives outside this module, from
// source into a temporary directory. It is never downloaded: what is being
// proved is that a rule written outside this repository runs through a real
// lint run, and that does not change with where the dependency came from.
func buildExampleRule(t *testing.T) string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go toolchain on PATH; the external rule cannot be built")
	}
	src, err := filepath.Abs(filepath.Join("host", "testdata", "examplerule"))
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "stele-rule-example")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command(goBin, "build", "-o", bin, ".")
	cmd.Dir = src
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the external rule: %v\n%s", err, out)
	}
	return bin
}

// todoProto carries what the external rule looks for and nothing a built-in
// rule objects to, so a finding in this test is the external rule's finding.
const todoProto = `syntax = "proto3";
package example.v1;

// TODO: rename this before anybody depends on it.
message Order {
  string id = 1;
}
`

// TestAPinnedExternalRuleFindsSomething is the proof the milestone is done: a
// rule from outside this repository, declared in the manifest, loaded by the
// same run that loads the built-ins, judging the same files, with its finding
// in the same report.
func TestAPinnedExternalRuleFindsSomething(t *testing.T) {
	dir := repo(t, oneModule+"lint:\n  plugins:\n    - name: example\n      path: "+
		filepath.ToSlash(buildExampleRule(t))+"\n",
		map[string]string{"api/example/v1/order.proto": todoProto})
	rep, err := lint.Run(context.Background(), lint.Options{Dir: dir, NoLock: true})
	if err != nil {
		t.Fatal(err)
	}
	got := findingsFor(rep.Result, "example/no_todo")
	if len(got) != 1 {
		t.Fatalf("the external rule produced %d findings in a file with one TODO: %+v", len(got), rep.Findings)
	}
	// The position is the declaration the comment leads, not the comment: a
	// finding points at the thing that is wrong, and the comment is evidence
	// about the message on line 5.
	if got[0].Pos.Line != 5 {
		t.Errorf("the finding is at line %d; the declaration it is about is on line 5", got[0].Pos.Line)
	}
	if got[0].Fix == "" {
		t.Error("a finding without a fix is one a reader skips")
	}
	if !rep.Failed() {
		t.Error("an external rule's finding must fail the run exactly as a built-in's does")
	}
	// The rule count includes it: a report that hid which rules ran would let
	// a plugin stop serving a rule without anybody noticing.
	if rep.Rules <= len(lint.Builtin()) {
		t.Errorf("Rules = %d, and this build carries %d built-ins plus one external rule",
			rep.Rules, len(lint.Builtin()))
	}
}

// TestAnExternalRuleIsConfiguredLikeAnyOther. Severity is the reader's
// judgement about their own repository, and it cannot stop at the process
// boundary: a rule that could not be demoted would be one that gets its plugin
// deleted instead.
func TestAnExternalRuleIsConfiguredLikeAnyOther(t *testing.T) {
	dir := repo(t, oneModule+"lint:\n  plugins:\n    - name: example\n      path: "+
		filepath.ToSlash(buildExampleRule(t))+"\n  rules:\n    - id: example/no_todo\n      severity: warning\n",
		map[string]string{"api/example/v1/order.proto": todoProto})
	rep, err := lint.Run(context.Background(), lint.Options{Dir: dir, NoLock: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Failed() {
		t.Errorf("a demoted external rule must not fail the run: %v", rep.Findings)
	}
	if rep.Warnings != 1 {
		t.Errorf("Warnings = %d, want 1", rep.Warnings)
	}
}

// TestAMissingRulePluginStopsTheRun. The recovery has to be in the message: a
// run that cannot load a rule has not checked what that rule checks, and
// carrying on would report an unchecked repository as a clean one.
func TestAMissingRulePluginStopsTheRun(t *testing.T) {
	dir := repo(t, oneModule+"lint:\n  plugins:\n    - name: house\n      path: ./bin/stele-rule-house\n",
		map[string]string{"api/example/v1/order.proto": todoProto})
	_, err := lint.Run(context.Background(), lint.Options{Dir: dir, NoLock: true})
	if err == nil {
		t.Fatal("a rule plugin that is not there must stop the run, not be skipped")
	}
	for _, want := range []string{"house", "stele-rule-house"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error must name %q:\n%v", want, err)
		}
	}
}

// TestARulePluginIsRefusedBeforeAnythingIsFetched. A broken plugin declaration
// is a defect in the manifest, and reporting it after a network round trip
// reports it later than it was known.
func TestARulePluginNeedingTheCacheSaysSo(t *testing.T) {
	dir := repo(t, oneModule+"lint:\n  plugins:\n    - name: house\n"+
		"      module: example.com/house/cmd/stele-rule-house\n      version: v1.0.0\n",
		map[string]string{"api/example/v1/order.proto": todoProto})
	_, err := lint.Run(context.Background(), lint.Options{Dir: dir, NoLock: true})
	if err == nil {
		t.Fatal("a plugin the tool must install needs a cache root, and a run without one cannot honour it")
	}
	if !strings.Contains(err.Error(), "cache root") {
		t.Errorf("the error must say what is missing:\n%v", err)
	}
}
