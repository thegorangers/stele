package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thegorangers/stele/internal/config"
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
	write("Makefile", "vendor:\n\t@buf export buf.build/example/schemas --output=third_party/proto\n")
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
