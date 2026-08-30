package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/thegorangers/stele/internal/config"
	"github.com/thegorangers/stele/internal/lint"
	"github.com/thegorangers/stele/internal/lockfile"
	"github.com/thegorangers/stele/internal/report"
)

func TestCLI(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		wantErr  string // empty means the run must succeed or ask for help
		help     bool
		wantHelp string // a substring the help text must carry
	}{
		{name: "no arguments prints usage", args: nil, help: true},
		{name: "help", args: []string{"--help"}, help: true},
		{name: "export help", args: []string{"export", "--help"}, help: true},
		{name: "unknown command", args: []string{"nosuch"}, wantErr: `unknown command "nosuch"`},
		{name: "unknown flag is named", args: []string{"export", "--nosuch"}, wantErr: "not defined: -nosuch"},
		{name: "flag before command is named", args: []string{"--nosuch", "export"}, wantErr: `unknown flag "--nosuch"`},
		{name: "output is required", args: []string{"export"}, wantErr: "--output is required"},
		{name: "update is accepted", args: []string{"export", "--update"}, wantErr: "--output is required"},
		{name: "update is documented", args: []string{"export", "--help"}, help: true, wantHelp: "--update"},
		{name: "generate help", args: []string{"generate", "--help"}, help: true},
		{name: "generate documents include-imports", args: []string{"generate", "--help"}, help: true, wantHelp: "--include-imports"},
		{name: "generate documents update", args: []string{"generate", "--help"}, help: true, wantHelp: "--update"},
		{name: "generate documents dir", args: []string{"generate", "--help"}, help: true, wantHelp: "--dir"},
		{name: "generate documents cache-dir", args: []string{"generate", "--help"}, help: true, wantHelp: "--cache-dir"},
		{name: "version is listed in the usage", args: nil, help: true, wantHelp: "version"},
		{name: "generate documents report", args: []string{"generate", "--help"}, help: true, wantHelp: "--report"},
		{name: "generate unknown flag is named", args: []string{"generate", "--nosuch"}, wantErr: "not defined: -nosuch"},
		{name: "generate positional argument refused", args: []string{"generate", "x"}, wantErr: `unexpected argument "x"`},
		{name: "generate is listed in the usage", args: nil, help: true, wantHelp: "generate"},
		{name: "positional argument refused", args: []string{"export", "--output", "o", "x"}, wantErr: `unexpected argument "x"`},
		{name: "migrate help", args: []string{"migrate", "--help"}, help: true},
		{name: "migrate documents write", args: []string{"migrate", "--help"}, help: true, wantHelp: "--write"},
		{name: "migrate documents dir", args: []string{"migrate", "--help"}, help: true, wantHelp: "--dir"},
		{name: "migrate unknown flag is named", args: []string{"migrate", "--nosuch"}, wantErr: "not defined: -nosuch"},
		{name: "migrate positional argument refused", args: []string{"migrate", "x"}, wantErr: `unexpected argument "x"`},
		{name: "migrate is listed in the usage", args: nil, help: true, wantHelp: "migrate"},
		{name: "plugins is listed in the usage", args: nil, help: true, wantHelp: "plugins"},
		{name: "plugins help", args: []string{"plugins", "--help"}, help: true, wantHelp: "install"},
		{name: "plugins needs a subcommand", args: []string{"plugins"}, wantErr: "list or install"},
		{name: "plugins unknown subcommand is named", args: []string{"plugins", "nosuch"}, wantErr: `unknown subcommand "nosuch"`},
		{name: "lint is listed in the usage", args: nil, help: true, wantHelp: "lint"},
		{name: "lint help", args: []string{"lint", "--help"}, help: true},
		{name: "lint documents dir", args: []string{"lint", "--help"}, help: true, wantHelp: "--dir"},
		{name: "lint documents update", args: []string{"lint", "--help"}, help: true, wantHelp: "--update"},
		{name: "lint documents cache-dir", args: []string{"lint", "--help"}, help: true, wantHelp: "--cache-dir"},
		{name: "lint documents rules", args: []string{"lint", "--help"}, help: true, wantHelp: "--rules"},
		// The help is where somebody adopting the tool over unlinted
		// contracts is told how to get to a green build. If it stops saying
		// so, the answer they find instead is allow_failure.
		{name: "lint help states the adoption mechanism", args: []string{"lint", "--help"}, help: true, wantHelp: "severity: warning"},
		{name: "lint unknown flag is named", args: []string{"lint", "--nosuch"}, wantErr: "not defined: -nosuch"},
		{name: "lint positional argument refused", args: []string{"lint", "x"}, wantErr: `unexpected argument "x"`},
		{name: "breaking is listed in the usage", args: nil, help: true, wantHelp: "breaking"},
		{name: "breaking help", args: []string{"breaking", "--help"}, help: true},
		{name: "breaking documents dir", args: []string{"breaking", "--help"}, help: true, wantHelp: "--dir"},
		{name: "breaking documents base", args: []string{"breaking", "--help"}, help: true, wantHelp: "--base"},
		{name: "breaking documents against", args: []string{"breaking", "--help"}, help: true, wantHelp: "--against"},
		{name: "breaking documents cache-dir", args: []string{"breaking", "--help"}, help: true, wantHelp: "--cache-dir"},
		// --against is a manual override, not a CI default; the help has to
		// say so, or origin/master-as-default is one copy-paste away.
		{name: "breaking warns against is not a CI default", args: []string{"breaking", "--help"}, help: true, wantHelp: "NOT a substitute for --base"},
		{name: "breaking unknown flag is named", args: []string{"breaking", "--nosuch"}, wantErr: "not defined: -nosuch"},
		{name: "breaking positional argument refused", args: []string{"breaking", "x"}, wantErr: `unexpected argument "x"`},
		{name: "breaking requires base or against", args: []string{"breaking"}, wantErr: "--base or --against is required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut strings.Builder
			err := run(context.Background(), tc.args, &out, &errOut)
			switch {
			case tc.help:
				if !errors.Is(err, errHelp) {
					t.Fatalf("want help, got %v", err)
				}
				if out.Len() == 0 {
					t.Fatal("help printed nothing")
				}
				if tc.wantHelp != "" && !strings.Contains(out.String(), tc.wantHelp) {
					t.Fatalf("help does not mention %q:\n%s", tc.wantHelp, out.String())
				}
			case tc.wantErr != "":
				if err == nil {
					t.Fatal("want an error, got none")
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not mention %q", err, tc.wantErr)
				}
			}
		})
	}
}

var _ io.Writer = (*strings.Builder)(nil)

// TestMigrateWritesAndRefuses covers the two things the command must not get
// wrong: it prints a manifest the tool itself can load, and an incomplete
// migration fails rather than leaving a plausible file behind.
func TestMigrateWritesAndRefuses(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("buf.yaml", "version: v2\nmodules: [{path: api}]\n")
	write("buf.gen.yaml", "version: v2\nplugins: [{local: protoc-gen-go, out: gen}]\ninputs: [{directory: api}]\n")
	// The plugin version lives in the Makefile. Without it the migration is
	// incomplete by design, so a complete one has to carry it.
	const pinned = "V ?= v1.36.6\n\ngen:\n\t@go install google.golang.org/protobuf/cmd/protoc-gen-go@$(V)\n"
	write("Makefile", pinned)

	var out, errOut strings.Builder
	if err := run(context.Background(), []string{"migrate", "--dir", dir}, &out, &errOut); err != nil {
		t.Fatalf("migrate: %v (%s)", err, errOut.String())
	}
	if !strings.Contains(out.String(), "version: 1") {
		t.Fatalf("stdout carries no manifest:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "stele.yaml")); !os.IsNotExist(err) {
		t.Fatal("migrate wrote a file without --write")
	}

	out.Reset()
	errOut.Reset()
	if err := run(context.Background(), []string{"migrate", "--dir", dir, "--write"}, &out, &errOut); err != nil {
		t.Fatalf("migrate --write: %v", err)
	}
	if _, err := config.Load(filepath.Join(dir, "stele.yaml")); err != nil {
		t.Fatalf("the written manifest does not load: %v", err)
	}

	// An incomplete migration must fail: a manifest that looks migrated and
	// is not is worse than no manifest. The reference has to be one this
	// repository actually reads — a vendored tree nothing imports is drift,
	// and reporting it as a demand is what taught readers to skip the list.
	if err := os.MkdirAll(filepath.Join(dir, "api", "example", "v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "third_party", "proto", "example", "schemas", "v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join("api", "example", "v1", "a.proto"),
		"syntax = \"proto3\";\npackage example.v1;\nimport \"example/schemas/v1/s.proto\";\n")
	write(filepath.Join("third_party", "proto", "example", "schemas", "v1", "s.proto"),
		"syntax = \"proto3\";\npackage example.schemas.v1;\n")
	write("buf.yaml", "version: v2\nmodules: [{path: api}, {path: third_party/proto}]\n")
	write("Makefile", pinned+"\nvendor:\n\t@buf export buf.build/example/schemas --output=third_party/proto\n")
	out.Reset()
	errOut.Reset()
	err := run(context.Background(), []string{"migrate", "--dir", dir}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "buf.build/example/schemas") {
		t.Fatalf("want a failure naming the untranslated reference, got %v", err)
	}
	if !strings.Contains(err.Error(), "example/schemas/v1/s.proto") {
		t.Fatalf("the failure does not name the file that is actually read: %v", err)
	}
}

// TestEmitReport: the summary is always said out loud, and --report puts the
// machine-readable copy where it was asked for. Both halves matter — the
// stderr line is what a reader sees, the file is what a later run is diffed
// against.
func TestEmitReport(t *testing.T) {
	rep := report.Build(nil)

	t.Run("summary to stderr, nothing to stdout", func(t *testing.T) {
		var out, errOut strings.Builder
		if err := emitReport(rep, "", &out, &errOut); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(errOut.String(), "stele") {
			t.Errorf("summary does not name stele:\n%s", errOut.String())
		}
		if out.Len() != 0 {
			t.Errorf("stdout was written to without --report: %q", out.String())
		}
	})

	t.Run("file", func(t *testing.T) {
		var out, errOut strings.Builder
		path := filepath.Join(t.TempDir(), "sub", "report.json")
		if err := emitReport(rep, path, &out, &errOut); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var decoded struct {
			Components map[string]struct {
				Version string `json:"version"`
			} `json:"components"`
		}
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("report file is not JSON: %v\n%s", err, raw)
		}
		if decoded.Components["protocompile"].Version == "" {
			t.Errorf("report file omits protocompile:\n%s", raw)
		}
	})

	t.Run("dash is stdout", func(t *testing.T) {
		var out, errOut strings.Builder
		if err := emitReport(rep, "-", &out, &errOut); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(strings.TrimSpace(out.String()), "{") {
			t.Errorf("--report - did not write JSON to stdout: %q", out.String())
		}
	})
}

// TestVersionCommandReportsRealVersions is the end-to-end half of the version
// report, and it has to be end-to-end: a test binary carries no dependency
// metadata, so only a built binary can show that the versions deciding the
// descriptor are actually reachable at runtime. The expected value is read
// from go.mod, so a dependency bump cannot leave this test asserting a stale
// number.
func TestVersionCommandReportsRealVersions(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain unavailable; cannot build the binary under test")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "stele")
	build := exec.Command(goBin, "build", "-o", bin, "./cmd/stele")
	build.Dir = root
	build.Env = append(os.Environ(), "GOPROXY=off")
	if b, err := build.CombinedOutput(); err != nil {
		t.Skipf("could not build the binary under test: %v\n%s", err, b)
	}

	out, err := exec.Command(bin, "version", "--json").Output()
	if err != nil {
		t.Fatalf("stele version --json: %v", err)
	}
	var decoded struct {
		Components map[string]struct {
			Module  string `json:"module"`
			Version string `json:"version"`
		} `json:"components"`
	}
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	for _, name := range []string{"protocompile", "protobuf-go"} {
		c := decoded.Components[name]
		want := moduleVersionFromGoMod(t, root, c.Module)
		if c.Version != want {
			t.Errorf("%s reported %q, go.mod requires %q", name, c.Version, want)
		}
	}
	if v := decoded.Components["stele"].Version; v == "" {
		t.Errorf("stele reported no version of itself: %s", out)
	}
}

// moduleVersionFromGoMod reads the version go.mod requires for a module.
func moduleVersionFromGoMod(t *testing.T, root, module string) string {
	t.Helper()
	if module == "" {
		t.Fatal("the report named no module to check against go.mod")
	}
	raw, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) >= 2 && fields[0] == module {
			return fields[1]
		}
	}
	t.Fatalf("go.mod does not require %s", module)
	return ""
}

// TestPluginsList: listing is the answer to "which binary will actually run",
// asked without running anything. It must not install: a question about the
// state of the cache that changed the cache would be useless as a diagnostic.
func TestPluginsList(t *testing.T) {
	dir := t.TempDir()
	body := `version: 1
modules:
  - path: api
generate:
  - name: go
    inputs:
      - module: api
    plugins:
      - local: protoc-gen-go
        module: google.golang.org/protobuf/cmd/protoc-gen-go
        version: v1.36.11
        out: gen
      - local: protoc-gen-dart
        out: lib
`
	if err := os.WriteFile(filepath.Join(dir, "stele.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()

	var out, errOut strings.Builder
	err := run(context.Background(), []string{"plugins", "list", "--dir", dir, "--cache-dir", cache}, &out, &errOut)
	if err != nil {
		t.Fatalf("plugins list: %v (%s)", err, errOut.String())
	}
	for _, want := range []string{
		"protoc-gen-go", "google.golang.org/protobuf/cmd/protoc-gen-go", "v1.36.11",
		"protoc-gen-dart", "PATH",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("listing does not mention %q:\n%s", want, out.String())
		}
	}
	entries, err := os.ReadDir(cache)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("listing wrote to the cache: %v", entries)
	}
}

// TestLintRulesListsEveryRule. A rule id goes into somebody's manifest, so the
// list has to be obtainable from the binary. A list somebody has to read the
// source to find is one they will guess at.
func TestLintRulesListsEveryRule(t *testing.T) {
	var out, errOut strings.Builder
	if err := run(context.Background(), []string{"lint", "--rules"}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	for _, r := range lint.Builtin() {
		if !strings.Contains(out.String(), r.ID()) {
			t.Errorf("%s is not listed:\n%s", r.ID(), out.String())
		}
		if !strings.Contains(out.String(), r.Description()) {
			t.Errorf("%s is listed without its description", r.ID())
		}
	}
	// The listing is how somebody decides whether to configure a rule, and
	// the rules no longer all cost the same thing. A listing that showed a
	// rule that warns the way it shows a rule that fails the build would omit
	// the one difference between them.
	if !strings.Contains(out.String(), "warns") {
		t.Errorf("the listing does not say which rules only warn:\n%s", out.String())
	}
}

// TestLintFailsOnFindingsAndSaysWhatToDo. The exit status is the contract with
// CI; the message is the contract with whoever reads the log.
func TestLintFailsOnFindingsAndSaysWhatToDo(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("stele.yaml", "version: 1\nmodules:\n  - path: api\n")
	write("api/example/v1/order.proto", `syntax = "proto3";
package example.v1;

enum OrderStatus {
  ORDER_STATUS_UNSPECIFIED = 0;
  PLACED = 1;
}
`)
	var out, errOut strings.Builder
	err := run(context.Background(), []string{"lint", "--dir", dir}, &out, &errOut)
	if err == nil {
		t.Fatal("a finding at severity error must fail the run")
	}
	if !strings.Contains(err.Error(), "lint.rules") {
		t.Errorf("the failure does not say what to do about it:\n%v", err)
	}
	if !strings.Contains(out.String(), "api/example/v1/order.proto:6:") {
		t.Errorf("the finding does not point at a file a reader can open:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "stele/enum_value_prefix") {
		t.Errorf("the finding does not name the rule:\n%s", out.String())
	}
	if !strings.Contains(errOut.String(), "1 error") {
		t.Errorf("the summary does not reach stderr:\n%s", errOut.String())
	}

	// The same repository, made green by the manifest rather than by a CI
	// flag. This is the whole adoption argument, measured.
	write("stele.yaml", "version: 1\nmodules:\n  - path: api\nlint:\n  rules:\n    - id: stele/enum_value_prefix\n      severity: warning\n")
	out.Reset()
	errOut.Reset()
	if err := run(context.Background(), []string{"lint", "--dir", dir}, &out, &errOut); err != nil {
		t.Fatalf("a demoted rule must not fail the run: %v", err)
	}
	if !strings.Contains(out.String(), ": warning: ") {
		t.Errorf("a demoted rule must still report:\n%s", out.String())
	}
}

// buildRulePlugin compiles the external example rule from source, offline.
func buildRulePlugin(t *testing.T) string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go toolchain on PATH; the external rule cannot be built")
	}
	src, err := filepath.Abs(filepath.Join("..", "..", "internal", "lint", "host", "testdata", "examplerule"))
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

// TestLintRulesListsTheRulesTheManifestLoads. A rule id is a public contract
// that goes into lint.rules, and the ids an external plugin serves are exactly
// the ones a reader cannot get from the source of this repository. A --rules
// that listed only the built-ins would send them to guess.
func TestLintRulesListsTheRulesTheManifestLoads(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "stele.yaml"),
		[]byte("version: 1\nmodules:\n  - path: api\nlint:\n  plugins:\n    - name: example\n      path: "+
			filepath.ToSlash(buildRulePlugin(t))+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut strings.Builder
	if err := run(context.Background(), []string{"lint", "--rules", "--dir", dir}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"example/no_todo", "example", "stele/enum_value_prefix"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the listing does not mention %q:\n%s", want, out.String())
		}
	}
}

// TestLintSaysWhyItFailedWhenARuleDidNotRun. A run can fail with no findings
// at all — every rule that had something to say may be the one that crashed —
// and a message that could only count findings would report "0 findings at
// severity error" and send the reader looking for a finding that is not there.
func TestLintSaysWhyItFailedWhenARuleDidNotRun(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bad, err := filepath.Abs(filepath.Join("..", "..", "internal", "lint", "host", "testdata", "badrule"))
	if err != nil {
		t.Fatal(err)
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go toolchain on PATH")
	}
	bin := filepath.Join(t.TempDir(), "stele-rule-bad")
	cmd := exec.Command(goBin, "build", "-o", bin, ".")
	cmd.Dir = bad
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the misbehaving rule: %v\n%s", err, out)
	}
	t.Setenv("MODE", "crash")

	write("stele.yaml", "version: 1\nmodules:\n  - path: api\nlint:\n  plugins:\n    - name: bad\n      path: "+
		filepath.ToSlash(bin)+"\n")
	write("api/example/v1/order.proto", "syntax = \"proto3\";\npackage example.v1;\n\nmessage Order {\n  string id = 1;\n}\n")

	var out, errOut strings.Builder
	err = run(context.Background(), []string{"lint", "--dir", dir}, &out, &errOut)
	if err == nil {
		t.Fatal("a rule that could not run must fail the run; silence is not cleanliness")
	}
	if strings.Contains(err.Error(), "0 finding") {
		t.Errorf("the failure blames findings that do not exist:\n%v", err)
	}
	for _, want := range []string{"bad/crash", "did not run"} {
		if !strings.Contains(err.Error()+out.String(), want) {
			t.Errorf("the output does not say %q:\n%v\n%s", want, err, out.String())
		}
	}
}

// TestLintRuleFlagExpandsARolledUpRule is the second half of the roll-up: the
// summary line names a command, and the command has to be the one it names.
// A summary that points at a flag that does not exist is worse than no summary.
func TestLintRuleFlagExpandsARolledUpRule(t *testing.T) {
	dir := t.TempDir()
	writeFile := func(name, body string) {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeFile("stele.yaml", "version: 1\nmodules:\n  - path: api\n")
	// Six timestamps named the way the measured fleet names them: one more
	// than the threshold, so the report rolls them up.
	var fields strings.Builder
	for i := 1; i <= 6; i++ {
		fmt.Fprintf(&fields, "  google.protobuf.Timestamp f%d_at = %d;\n", i, i)
	}
	writeFile("api/example/v1/order.proto", `syntax = "proto3";
package example.v1;
import "google/protobuf/timestamp.proto";

message Order {
`+fields.String()+`}
`)
	const ruleID = "aip/142_timestamp_field_time_suffix"

	var out, errOut strings.Builder
	if err := run(context.Background(), []string{"lint", "--dir", dir}, &out, &errOut); err != nil {
		t.Fatalf("no aip rule may fail a build by default: %v", err)
	}
	want := "see `stele lint --rule " + ruleID + "`"
	if !strings.Contains(out.String(), want) {
		t.Fatalf("the report does not roll the rule up and name the command:\n%s", out.String())
	}
	if strings.Count(out.String(), ruleID+":") > 1 {
		t.Errorf("the rolled-up rule printed its findings as well:\n%s", out.String())
	}

	out.Reset()
	errOut.Reset()
	if err := run(context.Background(), []string{"lint", "--dir", dir, "--rule", ruleID}, &out, &errOut); err != nil {
		t.Fatalf("asking for one rule must not fail the run: %v", err)
	}
	if n := strings.Count(out.String(), ruleID); n != 6 {
		t.Errorf("want the 6 findings of the rule asked for, got %d:\n%s", n, out.String())
	}
	if strings.Contains(out.String(), "--rule") {
		t.Errorf("a rule asked for by name must not be summarised again:\n%s", out.String())
	}

	// A rule that does not exist is a typo, and a run that silently checked
	// nothing would report a clean repository.
	out.Reset()
	errOut.Reset()
	err := run(context.Background(), []string{"lint", "--dir", dir, "--rule", "aip/no_such_rule"}, &out, &errOut)
	if err == nil {
		t.Fatal("--rule with an id nothing carries must be refused, not silently ignored")
	}
	if !strings.Contains(err.Error(), "aip/no_such_rule") {
		t.Errorf("the failure does not name what was asked for: %v", err)
	}
}

// writeTree writes a set of files under a fresh directory and returns it.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestLintBaselineHoldsWhatIsThereAndFailsOnWhatIsNew is the mechanism
// end to end, at the command, which is where a repository meets it.
func TestLintBaselineHoldsWhatIsThereAndFailsOnWhatIsNew(t *testing.T) {
	const manifest = "version: 1\nmodules:\n  - path: api\n"
	const before = `syntax = "proto3";
package example.v1;

enum OrderStatus {
  ORDER_STATUS_UNSPECIFIED = 0;
  PLACED = 1;
  PAID = 2;
}
`
	dir := writeTree(t, map[string]string{
		"stele.yaml":                 manifest,
		"api/example/v1/order.proto": before,
	})
	lintIn := func(args ...string) (string, string, error) {
		t.Helper()
		var out, errOut strings.Builder
		err := run(context.Background(), append([]string{"lint", "--dir", dir}, args...), &out, &errOut)
		return out.String(), errOut.String(), err
	}
	baselinePath := filepath.Join(dir, "stele.baseline")

	// Without a baseline the run fails, which is what it should do.
	if _, _, err := lintIn(); err == nil {
		t.Fatal("two unprefixed enum values must fail a run with no baseline")
	}
	// And no baseline was written. A tool that wrote one on an ordinary run
	// would turn every red build green by running it again.
	if _, err := os.Stat(baselinePath); !os.IsNotExist(err) {
		t.Fatal("an ordinary run wrote a baseline")
	}

	out, _, err := lintIn("--update-baseline")
	if err != nil {
		t.Fatalf("taking a baseline must not fail: %v\n%s", err, out)
	}
	body, err := os.ReadFile(baselinePath)
	if err != nil {
		t.Fatalf("--update-baseline wrote no baseline: %v", err)
	}
	for _, want := range []string{"example/v1/order.proto", "example.v1.PLACED", "stele/enum_value_prefix"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the baseline does not name %q:\n%s", want, body)
		}
	}
	if strings.Contains(string(body), "line:") {
		t.Errorf("the baseline records a line number, which is the identity it exists to avoid:\n%s", body)
	}

	// The same repository now passes.
	if out, _, err := lintIn(); err != nil {
		t.Fatalf("a baselined finding failed the run: %v\n%s", err, out)
	}

	// An unrelated edit above the findings must not disturb it.
	moved := strings.Replace(before, "enum OrderStatus {",
		"message Receipt {\n  string id = 1;\n}\n\nenum OrderStatus {", 1)
	rewrite(t, dir, "api/example/v1/order.proto", moved)
	out, errOut, err := lintIn()
	if err != nil {
		t.Fatalf("moving the findings down the file reddened the build: %v\n%s", err, out)
	}
	if strings.Contains(out, "--update-baseline") {
		t.Errorf("an unrelated edit made the baseline look stale:\n%s", out)
	}
	if !strings.Contains(errOut, "held by stele.baseline") {
		t.Errorf("the summary does not say what the baseline is holding:\n%s", errOut)
	}

	// One more unprefixed value is a new finding, and it fails.
	grown := strings.Replace(moved, "  PAID = 2;", "  PAID = 2;\n  REFUNDED = 3;", 1)
	rewrite(t, dir, "api/example/v1/order.proto", grown)
	out, _, err = lintIn()
	if err == nil {
		t.Fatal("a new violation of a baselined rule must fail the run")
	}
	if !strings.Contains(out, "REFUNDED") {
		t.Errorf("the failing run does not name the new finding:\n%s", out)
	}

	// And fixing one is reported, not punished.
	fixed := strings.Replace(grown, "  REFUNDED = 3;", "  ORDER_STATUS_REFUNDED = 3;", 1)
	fixed = strings.Replace(fixed, "  PLACED = 1;", "  ORDER_STATUS_PLACED = 1;", 1)
	rewrite(t, dir, "api/example/v1/order.proto", fixed)
	out, _, err = lintIn()
	if err != nil {
		t.Fatalf("fixing a baselined finding failed the run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "--update-baseline") {
		t.Errorf("a stale entry was kept silently:\n%s", out)
	}
}

// TestLintUpdateBaselineRefusesANarrowedRun holds the writer to the same rule
// the reader is held to: a baseline derived from a run that applied one rule
// would silently drop every other rule's entries, and the file it replaced is
// the only record of them.
func TestLintUpdateBaselineRefusesANarrowedRun(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"stele.yaml":                 "version: 1\nmodules:\n  - path: api\n",
		"api/example/v1/order.proto": "syntax = \"proto3\";\npackage example.v1;\n",
	})
	var out, errOut strings.Builder
	err := run(context.Background(), []string{"lint", "--dir", dir,
		"--update-baseline", "--rule", "stele/package_version_suffix"}, &out, &errOut)
	if err == nil {
		t.Fatal("a baseline taken from a narrowed run was written")
	}
	if !strings.Contains(err.Error(), "--rule") {
		t.Errorf("the refusal does not name the flag: %v", err)
	}
}

func rewrite(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// --- stele breaking end-to-end tests ---
//
// These exercise the command against real git repositories: Choose,
// TreesUnchanged and Load all read git history directly, and nothing short
// of a real repository proves they are wired together correctly.

// breakingGit runs git in dir, failing the test on error. The environment is
// pinned so a developer's global git configuration cannot change what these
// tests mean.
func breakingGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// breakingRepo makes a git repository with a self-referential remote:
// gitrepo.BaseRef always fetches from a configured remote, and the tests
// never push or pull for real, so a file:// remote pointed at itself is
// sufficient.
func breakingRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	breakingGit(t, dir, "init", "-q", "-b", "main")
	breakingGit(t, dir, "remote", "add", "origin", "file://"+dir)
	return dir
}

func breakingWrite(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func breakingCommit(t *testing.T, dir, name, body, msg string) string {
	t.Helper()
	breakingWrite(t, dir, name, body)
	breakingGit(t, dir, "add", ".")
	breakingGit(t, dir, "commit", "-qm", msg)
	return breakingGit(t, dir, "rev-parse", "HEAD")
}

const breakingManifest = "version: 1\nmodules:\n  - path: api\n"
const breakingLock = "version: 1\n"

func breakingOrder(status string) string {
	return `syntax = "proto3";
package example.v1;

message Order {
  int64 id = 1;
  string status = 2;` + status + `
}
`
}

// TestBreakingCleanRunExitsZero: an ordinary commit on a topic branch, ahead
// of the base with no proto change at all beyond a comment, must not fail —
// there is nothing here for a consumer to notice.
func TestBreakingCleanRunExitsZero(t *testing.T) {
	dir := breakingRepo(t)
	breakingWrite(t, dir, "stele.yaml", breakingManifest)
	breakingWrite(t, dir, "stele.lock", breakingLock)
	breakingCommit(t, dir, "api/example/v1/order.proto", breakingOrder(""), "base")

	breakingGit(t, dir, "checkout", "-q", "-b", "topic")
	breakingCommit(t, dir, "README.md", "notes", "unrelated topic work")

	var out, errOut strings.Builder
	err := run(context.Background(), []string{"breaking", "--dir", dir, "--base", "main"}, &out, &errOut)
	if err != nil {
		t.Fatalf("a clean run must exit zero: %v\n%s", err, out.String())
	}
	// README.md is outside the watched module and lock paths, so this hits
	// the tree shortcut: the run must still say the comparison was skipped
	// rather than silently reporting nothing.
	if !strings.Contains(out.String(), "unchanged") {
		t.Errorf("a clean run does not say so:\n%s", out.String())
	}
}

// TestBreakingRemovedFieldExitsZeroAndNamesTheField: this release is
// report-only. A finding — even one as serious as a removed field — must
// still exit zero, and the field must be named in the output.
func TestBreakingRemovedFieldExitsZeroAndNamesTheField(t *testing.T) {
	dir := breakingRepo(t)
	breakingWrite(t, dir, "stele.yaml", breakingManifest)
	breakingWrite(t, dir, "stele.lock", breakingLock)
	breakingCommit(t, dir, "api/example/v1/order.proto", breakingOrder(""), "base")

	breakingGit(t, dir, "checkout", "-q", "-b", "topic")
	breakingCommit(t, dir, "api/example/v1/order.proto", `syntax = "proto3";
package example.v1;

message Order {
  int64 id = 1;
}
`, "remove status")

	var out, errOut strings.Builder
	err := run(context.Background(), []string{"breaking", "--dir", dir, "--base", "main"}, &out, &errOut)
	if err != nil {
		t.Fatalf("a finding must not fail the run: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "example.v1.Order.status") {
		t.Errorf("the removed field is not named:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "report-only") {
		t.Errorf("the report does not say it is report-only:\n%s", out.String())
	}
}

// TestBreakingNothingToCompareExitsZero: the first commit in a repository's
// history has no previous revision at all. That is not a finding and not a
// failure; the run says so and exits zero.
func TestBreakingNothingToCompareExitsZero(t *testing.T) {
	dir := breakingRepo(t)
	breakingWrite(t, dir, "stele.yaml", breakingManifest)
	breakingWrite(t, dir, "stele.lock", breakingLock)
	breakingCommit(t, dir, "api/example/v1/order.proto", breakingOrder(""), "first commit")

	var out, errOut strings.Builder
	err := run(context.Background(), []string{"breaking", "--dir", dir, "--base", "main"}, &out, &errOut)
	if err != nil {
		t.Fatalf("nothing to compare must exit zero: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "nothing to compare") {
		t.Errorf("the report does not say there was nothing to compare:\n%s", out.String())
	}
}

// TestBreakingBogusRuleIDNamesWorkingTreePath: the breaking block is
// validated once, from the working manifest, before any revision is
// materialised — never from a temporary copy of a revision. The message
// must name the path the user can actually open and edit.
func TestBreakingBogusRuleIDNamesWorkingTreePath(t *testing.T) {
	dir := breakingRepo(t)
	breakingWrite(t, dir, "stele.yaml", breakingManifest)
	breakingWrite(t, dir, "stele.lock", breakingLock)
	breakingCommit(t, dir, "api/example/v1/order.proto", breakingOrder(""), "base")

	breakingGit(t, dir, "checkout", "-q", "-b", "topic")
	breakingWrite(t, dir, "stele.yaml", breakingManifest+
		"breaking:\n  rules:\n    - id: break/no_such_rule\n      severity: error\n")
	breakingCommit(t, dir, "README.md", "notes", "add a bogus breaking rule id")

	var out, errOut strings.Builder
	err := run(context.Background(), []string{"breaking", "--dir", dir, "--base", "main"}, &out, &errOut)
	if err == nil {
		t.Fatal("a bogus rule id must fail the run, not exit zero")
	}
	wantPath := filepath.Join(dir, "stele.yaml")
	if !strings.Contains(err.Error(), wantPath) {
		t.Errorf("the failure does not name the working tree's manifest path %q: %v", wantPath, err)
	}
	if strings.Contains(err.Error(), "stele-breaking-") {
		t.Errorf("the failure names a materialised temporary path instead of the working tree's: %v", err)
	}
	if !strings.Contains(err.Error(), "break/no_such_rule") {
		t.Errorf("the failure does not name the offending rule id: %v", err)
	}
}

// TestBreakingPreviousRevisionConfigIsIgnored pins which side's breaking
// block configures the run: the working tree's, and only the working
// tree's. The base revision here lowers break/field_removed to off with a
// reason; the working tree's manifest carries no breaking block at all. If
// the previous revision's configuration governed the run, this would be
// exactly the shape that silently suppressed the finding below. It must
// not: the finding is reported regardless, because nothing but the working
// manifest is ever consulted for it.
func TestBreakingPreviousRevisionConfigIsIgnored(t *testing.T) {
	dir := breakingRepo(t)
	breakingWrite(t, dir, "stele.yaml", breakingManifest+
		"breaking:\n  rules:\n    - id: break/field_removed\n      severity: off\n      reason: was noisy while the field was still churning\n")
	breakingWrite(t, dir, "stele.lock", breakingLock)
	breakingCommit(t, dir, "api/example/v1/order.proto", breakingOrder(""), "base")

	breakingGit(t, dir, "checkout", "-q", "-b", "topic")
	breakingWrite(t, dir, "stele.yaml", breakingManifest)
	breakingCommit(t, dir, "api/example/v1/order.proto", `syntax = "proto3";
package example.v1;

message Order {
  int64 id = 1;
}
`, "remove status")

	var out, errOut strings.Builder
	err := run(context.Background(), []string{"breaking", "--dir", dir, "--base", "main"}, &out, &errOut)
	if err != nil {
		t.Fatalf("a finding must not fail the run: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "example.v1.Order.status") {
		t.Errorf("the base revision's own severity:off did not stay in the past; "+
			"the finding is missing from a run whose working manifest carries no breaking block at all:\n%s", out.String())
	}
}

// TestBreakingShallowCloneFailsAndNamesShallowness: a failure to compare is
// still a failure, unlike a finding. A shallow clone cannot support
// merge-base or ancestry queries, and the run must fail loudly rather than
// guess.
func TestBreakingShallowCloneFailsAndNamesShallowness(t *testing.T) {
	origin := breakingRepo(t)
	breakingWrite(t, origin, "stele.yaml", breakingManifest)
	breakingWrite(t, origin, "stele.lock", breakingLock)
	breakingCommit(t, origin, "api/example/v1/order.proto", breakingOrder(""), "base")
	breakingCommit(t, origin, "api/example/v1/order.proto", breakingOrder("\n  string note = 3;"), "second")

	parent := t.TempDir()
	shallow := filepath.Join(parent, "shallow")
	cmd := exec.Command("git", "clone", "-q", "--depth", "1", "file://"+origin, shallow)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone --depth 1: %v\n%s", err, out)
	}

	var out, errOut strings.Builder
	err := run(context.Background(), []string{"breaking", "--dir", shallow, "--base", "main"}, &out, &errOut)
	if err == nil {
		t.Fatal("a shallow clone must fail the run, not exit zero")
	}
	if !strings.Contains(err.Error(), "shallow") {
		t.Errorf("the failure does not name shallowness: %v", err)
	}
}

// TestBreakingReportsClosureFinding is the command-level guard for the
// closure comparison: internal/breaking has thorough unit coverage of
// ClassifyClosure, but nothing before this test failed if the call to it
// were simply deleted from runBreaking — the command's own protos were
// still diffed, the run still exited zero, and every other test in this
// file stayed green. That is the branch's headline feature with no
// protection at the seam that actually ships it.
//
// The fixture: a consumer repository whose own protos do not move between
// the two revisions compared, but whose lock moves a dependency to a
// revision where a message it re-exports has lost a field. The dependency
// is a real git repository on disk, reached through a fake HTTPS address
// redirected to it with git's own url.insteadOf — the same mechanism a
// real CI runner's global git config uses for an internal host, just
// pointed at a temp directory instead.
func TestBreakingReportsClosureFinding(t *testing.T) {
	depDir := t.TempDir()
	breakingGit(t, depDir, "init", "-q", "-b", "main")
	breakingWrite(t, depDir, "stele.yaml", "version: 1\nmodules:\n  - path: proto\n")
	depSHA1 := breakingCommit(t, depDir, "proto/example/dep.proto", `syntax = "proto3";
package example;

message Dep {
  int64 value = 1;
  string extra = 2;
}
`, "dep with extra field")
	depSHA2 := breakingCommit(t, depDir, "proto/example/dep.proto", `syntax = "proto3";
package example;

message Dep {
  int64 value = 1;
}
`, "dep loses the extra field")

	// Redirect the fake dependency address to the real repository on disk,
	// through a global git config scoped to this test by GIT_CONFIG_GLOBAL —
	// the same variable breakingGit already pins for every git invocation
	// these tests make.
	const depGit = "https://example.invalid/example/closure-consumer-dep.git"
	gitConfig := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(gitConfig, []byte(
		"[url \"file://"+depDir+"\"]\n\tinsteadOf = "+depGit+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", gitConfig)
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")

	dir := breakingRepo(t)
	breakingWrite(t, dir, "stele.yaml", "version: 1\nmodules:\n  - path: own\n"+
		"deps:\n  - name: dep\n    git: "+depGit+"\n    ref: main\n    module: proto\n")
	breakingWrite(t, dir, "own/example/a.proto", `syntax = "proto3";
package example;

import "example/dep.proto";

message Owned {
  Dep dep = 1;
}
`)
	l := &lockfile.Lock{Version: lockfile.Version,
		Deps: []lockfile.Entry{{Name: "dep", Git: depGit, Ref: "main", SHA: depSHA1}}}
	if err := lockfile.Save(filepath.Join(dir, "stele.lock"), l); err != nil {
		t.Fatal(err)
	}
	breakingGit(t, dir, "add", ".")
	breakingGit(t, dir, "commit", "-qm", "base")
	base := breakingGit(t, dir, "rev-parse", "HEAD")

	breakingGit(t, dir, "checkout", "-q", "-b", "topic")
	l2 := &lockfile.Lock{Version: lockfile.Version,
		Deps: []lockfile.Entry{{Name: "dep", Git: depGit, Ref: "main", SHA: depSHA2}}}
	if err := lockfile.Save(filepath.Join(dir, "stele.lock"), l2); err != nil {
		t.Fatal(err)
	}
	breakingGit(t, dir, "add", ".")
	breakingGit(t, dir, "commit", "-qm", "bump dep, losing a re-exported field")

	var out, errOut strings.Builder
	err := run(context.Background(), []string{"breaking", "--dir", dir, "--against", base}, &out, &errOut)
	if err != nil {
		t.Fatalf("a closure finding must not fail the run: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "example.Dep.extra") {
		t.Errorf("the closure finding does not name the removed field example.Dep.extra:\n%s", out.String())
	}
}

// TestBreakingWarningSeverityStillExitsZeroAndIsMarked: the valve lowers a
// rule to warning; the finding is still reported, and it is still safe.
func TestBreakingWarningSeverityStillExitsZeroAndIsMarked(t *testing.T) {
	dir := breakingRepo(t)
	breakingWrite(t, dir, "stele.yaml", breakingManifest+
		"breaking:\n  rules:\n    - id: break/field_removed\n      severity: warning\n      reason: still churning\n")
	breakingWrite(t, dir, "stele.lock", breakingLock)
	breakingCommit(t, dir, "api/example/v1/order.proto", breakingOrder(""), "base")

	breakingGit(t, dir, "checkout", "-q", "-b", "topic")
	breakingCommit(t, dir, "api/example/v1/order.proto", `syntax = "proto3";
package example.v1;

message Order {
  int64 id = 1;
}
`, "remove status")

	var out, errOut strings.Builder
	err := run(context.Background(), []string{"breaking", "--dir", dir, "--base", "main"}, &out, &errOut)
	if err != nil {
		t.Fatalf("a warning-severity finding must not fail the run: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "example.v1.Order.status") {
		t.Errorf("the finding is missing:\n%s", out.String())
	}
	if !strings.Contains(out.String(), ": warning: break/field_removed:") {
		t.Errorf("the finding is not marked as a warning:\n%s", out.String())
	}
	// Every lowered rule is named in the report, on every run.
	if !strings.Contains(out.String(), "break/field_removed") || !strings.Contains(out.String(), "still churning") {
		t.Errorf("the report does not announce the lowered rule and its reason:\n%s", out.String())
	}
}

// TestBreakingOffSeverityProducesNoFindingAtAll: off is an unasked
// question, not a suppressed answer — it must not appear anywhere in the
// findings, only in the announcement of what this repository lowered.
func TestBreakingOffSeverityProducesNoFindingAtAll(t *testing.T) {
	dir := breakingRepo(t)
	breakingWrite(t, dir, "stele.yaml", breakingManifest+
		"breaking:\n  rules:\n    - id: break/field_removed\n      severity: off\n      reason: known and accepted\n")
	breakingWrite(t, dir, "stele.lock", breakingLock)
	breakingCommit(t, dir, "api/example/v1/order.proto", breakingOrder(""), "base")

	breakingGit(t, dir, "checkout", "-q", "-b", "topic")
	breakingCommit(t, dir, "api/example/v1/order.proto", `syntax = "proto3";
package example.v1;

message Order {
  int64 id = 1;
}
`, "remove status")

	var out, errOut strings.Builder
	err := run(context.Background(), []string{"breaking", "--dir", dir, "--base", "main"}, &out, &errOut)
	if err != nil {
		t.Fatalf("off must not fail the run: %v\n%s", err, out.String())
	}
	if strings.Contains(out.String(), "example.v1.Order.status") {
		t.Errorf("an off rule must produce no finding at all:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "break/field_removed") || !strings.Contains(out.String(), "known and accepted") {
		t.Errorf("the report must still announce the rule this repository turned off:\n%s", out.String())
	}
}

// TestBreakingErrorSeverityStillExitsZero pins Plan A's guarantee for this
// task: the exit status does not change here, whatever severity a finding
// carries. Task 5 changes this.
func TestBreakingErrorSeverityStillExitsZero(t *testing.T) {
	dir := breakingRepo(t)
	breakingWrite(t, dir, "stele.yaml", breakingManifest)
	breakingWrite(t, dir, "stele.lock", breakingLock)
	breakingCommit(t, dir, "api/example/v1/order.proto", breakingOrder(""), "base")

	breakingGit(t, dir, "checkout", "-q", "-b", "topic")
	breakingCommit(t, dir, "api/example/v1/order.proto", `syntax = "proto3";
package example.v1;

message Order {
  int64 id = 1;
}
`, "remove status")

	var out, errOut strings.Builder
	err := run(context.Background(), []string{"breaking", "--dir", dir, "--base", "main"}, &out, &errOut)
	if err != nil {
		t.Fatalf("an error-severity finding must still exit zero today: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), ": error: break/field_removed:") {
		t.Errorf("the finding is not marked as an error:\n%s", out.String())
	}
}

// TestBreakingUnchangedTreeStillAnnouncesLoweredRule: the tree-shortcut path
// (TestBreakingCleanRunExitsZero) is measured at 84.7% of base-branch
// commits in the fleet, and it is exactly the run where a reader is most
// likely to assume protection is in force, because nothing else is
// reported. A rule this repository turned off must still be named here,
// not only on a run that happened to compare something.
func TestBreakingUnchangedTreeStillAnnouncesLoweredRule(t *testing.T) {
	dir := breakingRepo(t)
	breakingWrite(t, dir, "stele.yaml", breakingManifest+
		"breaking:\n  rules:\n    - id: break/field_removed\n      severity: off\n      reason: known and accepted\n")
	breakingWrite(t, dir, "stele.lock", breakingLock)
	breakingCommit(t, dir, "api/example/v1/order.proto", breakingOrder(""), "base")

	breakingGit(t, dir, "checkout", "-q", "-b", "topic")
	breakingCommit(t, dir, "README.md", "notes", "unrelated topic work")

	var out, errOut strings.Builder
	err := run(context.Background(), []string{"breaking", "--dir", dir, "--base", "main"}, &out, &errOut)
	if err != nil {
		t.Fatalf("a clean run must exit zero: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "unchanged") {
		t.Errorf("a clean run does not say so:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "break/field_removed") || !strings.Contains(out.String(), "known and accepted") {
		t.Errorf("the tree-shortcut run does not announce the lowered rule and its reason:\n%s", out.String())
	}
}

// TestBreakingUnchangedTreeWithNothingLoweredSaysNothingExtra is the
// negative of the test above: on the same tree-shortcut path, a manifest
// with no breaking block prints no severity-related note.
func TestBreakingUnchangedTreeWithNothingLoweredSaysNothingExtra(t *testing.T) {
	dir := breakingRepo(t)
	breakingWrite(t, dir, "stele.yaml", breakingManifest)
	breakingWrite(t, dir, "stele.lock", breakingLock)
	breakingCommit(t, dir, "api/example/v1/order.proto", breakingOrder(""), "base")

	breakingGit(t, dir, "checkout", "-q", "-b", "topic")
	breakingCommit(t, dir, "README.md", "notes", "unrelated topic work")

	var out, errOut strings.Builder
	err := run(context.Background(), []string{"breaking", "--dir", dir, "--base", "main"}, &out, &errOut)
	if err != nil {
		t.Fatalf("a clean run must exit zero: %v\n%s", err, out.String())
	}
	if strings.Contains(out.String(), "break/") {
		t.Errorf("a run with nothing lowered must say nothing extra:\n%s", out.String())
	}
}

// TestBreakingMatchingPermissionRemovesFinding is the command-level guard
// for Permit: this cannot be proven by a unit test on Permit alone, because
// Permit was wired into runBreaking after the fact — a unit test cannot
// tell whether anything actually calls it. A permission naming the exact
// (rule, subject) of a discriminant-less finding makes it disappear from
// the command's own output, and the run still exits zero.
func TestBreakingMatchingPermissionRemovesFinding(t *testing.T) {
	dir := breakingRepo(t)
	breakingWrite(t, dir, "stele.yaml", breakingManifest+
		"breaking:\n  allow:\n    - rule: break/field_removed\n"+
			"      subject: example.v1.Order.status\n      reason: dropped in the v2 rollout\n")
	breakingWrite(t, dir, "stele.lock", breakingLock)
	breakingCommit(t, dir, "api/example/v1/order.proto", breakingOrder(""), "base")

	breakingGit(t, dir, "checkout", "-q", "-b", "topic")
	breakingCommit(t, dir, "api/example/v1/order.proto", `syntax = "proto3";
package example.v1;

message Order {
  int64 id = 1;
}
`, "remove status")

	var out, errOut strings.Builder
	err := run(context.Background(), []string{"breaking", "--dir", dir, "--base", "main"}, &out, &errOut)
	if err != nil {
		t.Fatalf("a permitted finding must not fail the run: %v\n%s", err, out.String())
	}
	if strings.Contains(out.String(), "example.v1.Order.status") {
		t.Errorf("a matching permission must remove the finding from the command's output:\n%s", out.String())
	}
	if strings.Contains(out.String(), "is stale") || strings.Contains(out.String(), "is dormant") {
		t.Errorf("a matched permission must not be reported at all:\n%s", out.String())
	}
}

// TestBreakingMismatchedChangePermissionLeavesFindingAndReportsStale pins
// the negative half of the same wiring: a permission whose change differs
// from the finding's must not remove it, and the permission itself comes
// back named as stale — spent, worth deleting — because its rule stands at
// error and a finding of it could have matched.
func TestBreakingMismatchedChangePermissionLeavesFindingAndReportsStale(t *testing.T) {
	dir := breakingRepo(t)
	breakingWrite(t, dir, "stele.yaml", breakingManifest+
		"breaking:\n  allow:\n    - rule: break/field_type_changed\n"+
			"      subject: example.v1.Order.status\n      change: string -> bytes\n"+
			"      reason: was already approved for a different change\n")
	breakingWrite(t, dir, "stele.lock", breakingLock)
	breakingCommit(t, dir, "api/example/v1/order.proto", breakingOrder(""), "base")

	breakingGit(t, dir, "checkout", "-q", "-b", "topic")
	breakingCommit(t, dir, "api/example/v1/order.proto", `syntax = "proto3";
package example.v1;

message Order {
  int64 id = 1;
  int32 status = 2;
}
`, "retype status")

	var out, errOut strings.Builder
	err := run(context.Background(), []string{"breaking", "--dir", dir, "--base", "main"}, &out, &errOut)
	if err != nil {
		t.Fatalf("the command still exits zero: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "example.v1.Order.status") ||
		!strings.Contains(out.String(), ": error: break/field_type_changed:") {
		t.Errorf("the finding must still stand: the permission's change does not match:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "string -> int32") {
		t.Errorf("the rendered finding must carry the discriminant, spelled as a permission must spell it:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "is stale") {
		t.Errorf("the unmatched permission must be reported stale:\n%s", out.String())
	}
	if strings.Contains(out.String(), "is dormant") {
		t.Errorf("this permission's rule is at error, not off: it must not be called dormant:\n%s", out.String())
	}
}

// TestBreakingDormantPermissionOnOffRuleIsNotCalledStale is the other half
// of the stale/dormant distinction: a permission naming a rule this same
// manifest set to off matches nothing too, but for a different reason — no
// finding of that rule can exist at all right now. Deleting it would be
// wrong, so it must come back worded as dormant, never stale.
func TestBreakingDormantPermissionOnOffRuleIsNotCalledStale(t *testing.T) {
	dir := breakingRepo(t)
	breakingWrite(t, dir, "stele.yaml", breakingManifest+
		"breaking:\n  rules:\n    - id: break/field_removed\n      severity: off\n      reason: known and accepted\n"+
			"  allow:\n    - rule: break/field_removed\n"+
			"      subject: example.v1.Order.status\n      reason: kept for when the rule is raised again\n")
	breakingWrite(t, dir, "stele.lock", breakingLock)
	breakingCommit(t, dir, "api/example/v1/order.proto", breakingOrder(""), "base")

	breakingGit(t, dir, "checkout", "-q", "-b", "topic")
	breakingCommit(t, dir, "api/example/v1/order.proto", `syntax = "proto3";
package example.v1;

message Order {
  int64 id = 1;
}
`, "remove status")

	var out, errOut strings.Builder
	err := run(context.Background(), []string{"breaking", "--dir", dir, "--base", "main"}, &out, &errOut)
	if err != nil {
		t.Fatalf("a dormant permission must not fail the run: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "is dormant") {
		t.Errorf("the permission for an off rule must be reported dormant:\n%s", out.String())
	}
	if strings.Contains(out.String(), "is stale") {
		t.Errorf("a dormant permission must never be worded as stale: a reader would delete what they still need:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "break/field_removed") {
		t.Errorf("the dormant note must name the rule:\n%s", out.String())
	}
}
