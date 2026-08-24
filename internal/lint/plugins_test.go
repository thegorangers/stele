package lint_test

import (
	"context"
	"os"
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

// onPath copies the external rule to a directory of its own and puts that
// directory on PATH under a plain name, so the manifest can declare it the way
// the bare-PATH tier is declared: by name and nothing else.
func onPath(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile(buildExampleRule(t))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	name := "stele-rule-example"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(dir, name), src, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return "stele-rule-example"
}

const unpinnedManifest = "lint:\n  plugins:\n    - name: stele-rule-example\n      unpinned: true\n"

// TestAnUnpinnedRuleIsVisibleInTheRun. The tier is allowed with an opt-in, and
// an opt-in that only lives in the manifest would leave the report of a run
// indistinguishable from one whose rules were all pinned — which is exactly the
// state the reader has to be able to tell apart, because it is the one where a
// finding can appear or vanish with nothing in the repository changing.
func TestAnUnpinnedRuleIsVisibleInTheRun(t *testing.T) {
	name := onPath(t)
	dir := repo(t, oneModule+unpinnedManifest,
		map[string]string{"api/example/v1/order.proto": todoProto})
	rep, err := lint.Run(context.Background(), lint.Options{Dir: dir, NoLock: true})
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	rep.Write(&out)
	for _, want := range []string{name, "not pinned"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the run output must say %q:\n%s", want, out.String())
		}
	}
	if !strings.Contains(rep.Summary(), "not pinned") {
		t.Errorf("the summary must say the run was not fully pinned: %q", rep.Summary())
	}
}

// TestAnUnpinnedRuleIsVisibleInTheListing. `stele lint --rules` is what
// somebody reads to find out which rules judge this repository; a rule listed
// there without saying that nothing pins it reads as one that is pinned.
func TestAnUnpinnedRuleIsVisibleInTheListing(t *testing.T) {
	onPath(t)
	dir := repo(t, oneModule+unpinnedManifest,
		map[string]string{"api/example/v1/order.proto": todoProto})
	rules, stop, err := lint.Rules(context.Background(), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	for _, r := range rules {
		switch {
		case r.ID() == "example/no_todo":
			if r.Unpinned == "" {
				t.Error("the external rule is whatever PATH resolves to, and the listing says nothing about it")
			}
		case r.Unpinned != "":
			t.Errorf("built-in rule %s is listed as unpinned: %q", r.ID(), r.Unpinned)
		}
	}
}

// TestADeclaredRuleIsNotAnnounced. A note printed over every run would be a
// note nobody reads, and the whole value of this one is that it is unusual.
// The tier here is `path`, which stele does not verify either — what it does
// do is name one binary in a line a reviewer sees, which is the difference the
// announcement is about. What `path` does and does not promise is in the
// README's trust section.
func TestADeclaredRuleIsNotAnnounced(t *testing.T) {
	dir := repo(t, oneModule+"lint:\n  plugins:\n    - name: example\n      path: "+
		filepath.ToSlash(buildExampleRule(t))+"\n",
		map[string]string{"api/example/v1/order.proto": todoProto})
	rep, err := lint.Run(context.Background(), lint.Options{Dir: dir, NoLock: true})
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	rep.Write(&out)
	if strings.Contains(out.String(), "not pinned") || strings.Contains(rep.Summary(), "not pinned") {
		t.Errorf("a run whose plugins are all declared says nothing about pinning:\n%s%s", out.String(), rep.Summary())
	}
}
