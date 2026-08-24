package host_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bufbuild/protocompile"
	"github.com/bufbuild/protocompile/linker"
	"github.com/thegorangers/stele/internal/lint/host"
	"github.com/thegorangers/stele/rule"
)

// buildExample compiles the rule that lives outside this module.
//
// It is built from source into a temporary directory rather than downloaded,
// so the test is hermetic and offline; what it proves is unaffected, because
// what is being proved is that a rule outside this repository can be written
// against the published interface and run through the host at all.
func buildExample(t *testing.T) string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go toolchain on PATH; the external rule cannot be built")
	}
	src, err := filepath.Abs(filepath.Join("testdata", "examplerule"))
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

func compileSource(t *testing.T, path, src string) linker.File {
	t.Helper()
	c := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(protocompile.ResolverFunc(
			func(p string) (protocompile.SearchResult, error) {
				if p != path {
					return protocompile.SearchResult{}, fmt.Errorf("%s: %w", p, os.ErrNotExist)
				}
				return protocompile.SearchResult{Source: strings.NewReader(src)}, nil
			})),
		SourceInfoMode: protocompile.SourceInfoStandard,
	}
	files, err := c.Compile(context.Background(), path)
	if err != nil {
		t.Fatalf("compiling the fixture: %v", err)
	}
	return files[0].(linker.File)
}

const todoPath = "example/v1/order.proto"

// todoSrc carries a comment the external rule is looking for.
const todoSrc = `syntax = "proto3"; package example.v1;
// TODO: rename this before anybody depends on it.
message Order { string id = 1; }`

func load(t *testing.T, ps ...host.Plugin) *host.Set {
	t.Helper()
	set, err := host.Load(context.Background(), ps)
	if err != nil {
		t.Fatalf("loading %v: %v", ps, err)
	}
	t.Cleanup(func() { set.Close() })
	return set
}

// TestOutOfTreeRuleFindsSomething is the proof this slice exists for. A rule
// living outside this repository, compiled against the published interface,
// runs through the host and finds something real. A host that compiles and has
// never carried a rule from outside is not evidence of anything.
func TestOutOfTreeRuleFindsSomething(t *testing.T) {
	set := load(t, host.Plugin{Name: "example", Path: buildExample(t)})
	rules := set.Rules()
	if len(rules) != 1 {
		t.Fatalf("the external plugin serves 1 rule; the host loaded %d", len(rules))
	}
	r := rules[0]
	if r.ID() != "example/no_todo" {
		t.Fatalf("rule ID = %q", r.ID())
	}
	if r.Description() == "" {
		t.Error("a rule with no description cannot be judged by somebody deciding whether to switch it off")
	}

	found, err := r.Check(rule.File{Desc: compileSource(t, todoPath, todoSrc)})
	if err != nil {
		t.Fatalf("checking through the host: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("the external rule found %d things in a file with one TODO: %+v", len(found), found)
	}
	if found[0].Pos.Line != 3 {
		t.Errorf("finding is at line %d; the message it is about is on line 3", found[0].Pos.Line)
	}
	if found[0].Message == "" || found[0].Fix == "" {
		t.Errorf("a finding needs both what is wrong and what to do: %+v", found[0])
	}

	// The second half, the one usually skipped: it says nothing about a file
	// that keeps the rule.
	clean, err := r.Check(rule.File{Desc: compileSource(t, todoPath,
		`syntax = "proto3"; package example.v1;
// An order somebody placed.
message Order { string id = 1; }`)})
	if err != nil {
		t.Fatalf("checking a clean file: %v", err)
	}
	if len(clean) != 0 {
		t.Errorf("the external rule fired on a file that keeps it: %+v", clean)
	}
}

// buildBad compiles the misbehaving plugin, which is told by MODE which way
// to fail.
func buildBad(t *testing.T) string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go toolchain on PATH")
	}
	src, err := filepath.Abs(filepath.Join("testdata", "badrule"))
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "stele-rule-bad")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command(goBin, "build", "-o", bin, ".")
	cmd.Dir = src
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the misbehaving rule: %v\n%s", err, out)
	}
	return bin
}

// TestAMissingBinaryIsRefusedByName covers the first way a rule fails: it is
// not there at all.
func TestAMissingBinaryIsRefusedByName(t *testing.T) {
	_, err := host.Load(context.Background(), []host.Plugin{
		{Name: "example", Path: filepath.Join(t.TempDir(), "nosuch")},
	})
	if err == nil {
		t.Fatal("a plugin binary that does not exist must be refused, not skipped")
	}
	for _, want := range []string{`"example"`, "nosuch"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error must name %s: %v", want, err)
		}
	}
}

// TestFailureModesAreNamed holds each way a rule in another process can fail
// to the standard milestone 5 set: the message names the rule, says what
// happened, and says what to do. None of them is a silent skip.
func TestFailureModesAreNamed(t *testing.T) {
	bad := buildBad(t)
	cases := []struct {
		mode string
		// atLoad says the failure is refused when the rules are loaded,
		// before any file is checked.
		atLoad bool
		want   []string
	}{
		{mode: "reserved_namespace", atLoad: true, want: []string{`"bad"`, "stele/no_todo", "reserved"}},
		{mode: "silent", atLoad: true, want: []string{`"bad"`, "announc"}},
		{mode: "chatty", atLoad: true, want: []string{`"bad"`, "stdout"}},
		{mode: "crash", want: []string{"bad/crash", `"bad"`, todoPath, "died"}},
		{mode: "hang", want: []string{"bad/hang", `"bad"`, todoPath, "did not answer"}},
		{mode: "garbage", want: []string{"bad/garbage", `"bad"`, "not a rule response"}},
		{mode: "no_fix", want: []string{"bad/no_fix", "fix"}},
		{mode: "refuses", want: []string{"bad/refuses", "could not check"}},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			t.Setenv("MODE", tc.mode)
			if tc.mode == "hang" {
				// The real budget is a minute. What is under test is that the
				// budget is enforced and named, not how long it is.
				t.Cleanup(host.SetCheckTimeout(2 * time.Second))
			}
			set, err := host.Load(context.Background(), []host.Plugin{{Name: "bad", Path: bad}})
			if set != nil {
				t.Cleanup(func() { set.Close() })
			}
			if tc.atLoad {
				if err == nil {
					t.Fatalf("mode %s must be refused when the rules are loaded", tc.mode)
				}
			} else {
				if err != nil {
					t.Fatalf("loading: %v", err)
				}
				found, checkErr := set.Rules()[0].Check(rule.File{Desc: compileSource(t, todoPath, todoSrc)})
				if checkErr == nil {
					t.Fatalf("mode %s must fail the check, not return %d findings", tc.mode, len(found))
				}
				if found != nil {
					t.Errorf("a rule that failed must return no findings, not %d", len(found))
				}
				err = checkErr
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the message must contain %q:\n%v", want, err)
				}
			}
		})
	}
}

// TestTwoPluginsClaimingOneIDAreRefused decides the question two external
// rules with one ID raise, and decides it as a refusal.
func TestTwoPluginsClaimingOneIDAreRefused(t *testing.T) {
	bin := buildExample(t)
	_, err := host.Load(context.Background(), []host.Plugin{
		{Name: "first", Path: bin},
		{Name: "second", Path: bin},
	})
	if err == nil {
		t.Fatal("two plugins claiming one rule ID must be refused")
	}
	for _, want := range []string{`"first"`, `"second"`, "example/no_todo"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error must name %s: %v", want, err)
		}
	}
}

// TestAFailedRuleStaysFailedForEveryFile pins that a plugin which died on one
// file does not cost a fresh timeout on each of the rest, and does not come
// back as silence either.
func TestAFailedRuleStaysFailedForEveryFile(t *testing.T) {
	t.Setenv("MODE", "crash")
	set, err := host.Load(context.Background(), []host.Plugin{{Name: "bad", Path: buildBad(t)}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { set.Close() })
	r := set.Rules()[0]
	f := rule.File{Desc: compileSource(t, todoPath, todoSrc)}
	if _, err := r.Check(f); err == nil {
		t.Fatal("the first check must fail")
	}
	start := time.Now()
	if _, err := r.Check(f); err == nil {
		t.Fatal("the second check must fail too; a dead rule does not recover into silence")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("a dead rule must fail at once, not after a timeout: took %s", d)
	}
}
