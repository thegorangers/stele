package report_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/thegorangers/stele/internal/report"
)

// TestBuild_IncludesEngineVersions is the point of the whole file: a report
// that named only the plugins would look complete and would not be. The
// descriptor a plugin is handed is built by stele with protocompile and
// protobuf-go, and those three decide the bytes just as much.
func TestBuild_IncludesEngineVersions(t *testing.T) {
	r := report.Build(nil)
	for _, want := range []string{"stele", "protocompile", "protobuf-go"} {
		c, ok := r.Components[want]
		if !ok {
			t.Errorf("the report has no version for %s, and it decides the descriptor", want)
			continue
		}
		if c.Version == "" {
			t.Errorf("%s has an empty version; unobtainable must be spelled %q", want, report.Unknown)
		}
	}
}

// TestBuild_GoPluginVersionFromBuildMetadata: the version of a Go-built plugin
// comes from the module metadata stamped into the binary, not from asking the
// plugin. Asking works for one of the four plugins we know. The fake plugin
// here answers --version with noise and a non-zero exit, so a report that
// interrogated it would carry that noise.
func TestBuild_GoPluginVersionFromBuildMetadata(t *testing.T) {
	bin := buildFakePlugin(t, "example.com/fakeplugin")

	r := report.Build([]report.Plugin{{Path: bin}})

	c, ok := r.Components["protoc-gen-fake"]
	if !ok {
		t.Fatalf("plugin missing from report; components: %v", keys(r))
	}
	if c.Module != "example.com/fakeplugin" {
		t.Errorf("module = %q, want the module path stamped in the binary", c.Module)
	}
	// A binary built from a checkout, not from a module proxy, carries this
	// exact string. Reporting it verbatim is the honest answer.
	if c.Version != "(devel)" {
		t.Errorf("version = %q, want %q", c.Version, "(devel)")
	}
	if strings.Contains(c.Version, "GARBAGE") {
		t.Errorf("version = %q: the plugin was interrogated instead of read", c.Version)
	}
}

// TestBuild_NonGoPluginIsUnknownNotOmitted: protoc-gen-dart carries no module
// metadata. Omitting it would read as "no such plugin ran", which is a lie
// about what produced the bytes; inventing a version would be a worse one.
func TestBuild_NonGoPluginIsUnknownNotOmitted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script plugin is not executable on windows")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "protoc-gen-dart")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	r := report.Build([]report.Plugin{{Path: bin}})

	c, ok := r.Components["protoc-gen-dart"]
	if !ok {
		t.Fatalf("a plugin with no metadata was omitted; components: %v", keys(r))
	}
	if c.Version != report.Unknown {
		t.Errorf("version = %q, want %q", c.Version, report.Unknown)
	}
}

// TestBuild_MissingPluginIsReportedNotDropped: a plugin that could not even be
// found is still evidence about the run.
func TestBuild_MissingPluginIsReportedNotDropped(t *testing.T) {
	r := report.Build([]report.Plugin{{Name: "protoc-gen-nothing-here-at-all"}})
	c, ok := r.Components["protoc-gen-nothing-here-at-all"]
	if !ok {
		t.Fatalf("missing plugin dropped; components: %v", keys(r))
	}
	if c.Version != report.Unknown {
		t.Errorf("version = %q, want %q", c.Version, report.Unknown)
	}
}

// TestReport_JSONIsComparableAcrossRuns: this is evidence someone diffs. Two
// reports of the same run must be byte-identical, which rules out timestamps
// and map iteration order.
func TestReport_JSONIsComparableAcrossRuns(t *testing.T) {
	a, err := report.Build(nil).JSON()
	if err != nil {
		t.Fatal(err)
	}
	b, err := report.Build(nil).JSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatalf("two reports of the same run differ:\n%s\n%s", a, b)
	}
	var decoded struct {
		Components map[string]struct {
			Version string `json:"version"`
		} `json:"components"`
	}
	if err := json.Unmarshal(a, &decoded); err != nil {
		t.Fatalf("report is not machine-readable: %v", err)
	}
	if decoded.Components["stele"].Version == "" {
		t.Errorf("stele version absent from JSON: %s", a)
	}
	if !strings.HasSuffix(string(a), "\n") {
		t.Errorf("report does not end in a newline: %q", a)
	}
}

func keys(r *report.Report) []string {
	var out []string
	for k := range r.Components {
		out = append(out, k)
	}
	return out
}

// buildFakePlugin builds a plugin binary from source so the test depends on no
// installed plugin. The binary answers --version with noise, so reading it and
// asking it cannot be confused.
func buildFakePlugin(t *testing.T, module string) string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain unavailable; nothing to build a fake plugin with")
	}
	src := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(src, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module "+module+"\n\ngo 1.21\n")
	write("main.go", "package main\n\nimport \"os\"\n\nfunc main() {\n\tos.Stdout.WriteString(\"GARBAGE\\x00\\x01\")\n\tos.Exit(1)\n}\n")

	out := filepath.Join(t.TempDir(), "protoc-gen-fake")
	cmd := exec.Command(goBin, "build", "-o", out, ".")
	cmd.Dir = src
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOPROXY=off", "GOWORK=off")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("could not build a fake plugin: %v\n%s", err, b)
	}
	return out
}

// TestBuild_SaysWhereAPluginCameFrom: a plugin the tool installed at a
// declared version and a plugin it merely found on PATH are different claims
// about reproducibility, and the evidence has to tell them apart.
func TestBuild_SaysWhereAPluginCameFrom(t *testing.T) {
	bin := buildFakePlugin(t, "example.com/fakeplugin")

	r := report.Build([]report.Plugin{
		{Name: "protoc-gen-fake", Path: bin, Module: "example.com/fakeplugin", Version: "(devel)", Origin: report.OriginManaged},
		{Name: "protoc-gen-dart", Path: "", Version: report.Unknown, Origin: report.OriginPath},
	})

	if got := r.Components["protoc-gen-fake"].Origin; got != report.OriginManaged {
		t.Errorf("origin of the installed plugin = %q, want %q", got, report.OriginManaged)
	}
	if got := r.Components["protoc-gen-dart"].Origin; got != report.OriginPath {
		t.Errorf("origin of the PATH plugin = %q, want %q", got, report.OriginPath)
	}
	if s := r.Summary(); !strings.Contains(s, report.OriginPath) || !strings.Contains(s, report.OriginManaged) {
		t.Errorf("the summary does not say where the plugins came from:\n%s", s)
	}
}
