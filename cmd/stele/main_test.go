package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/thegorangers/stele/internal/config"
	"github.com/thegorangers/stele/internal/lint"
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
	// is not is worse than no manifest.
	write("Makefile", pinned+"\nvendor:\n\t@buf export buf.build/example/schemas --output=third_party/proto\n")
	out.Reset()
	errOut.Reset()
	err := run(context.Background(), []string{"migrate", "--dir", dir}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "buf.build/example/schemas") {
		t.Fatalf("want a failure naming the untranslated reference, got %v", err)
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
