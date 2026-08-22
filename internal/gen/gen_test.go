package gen_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/thegorangers/stele/internal/gen"
	"github.com/thegorangers/stele/internal/report"
	"github.com/thegorangers/stele/internal/resolve"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/pluginpb"
)

// The tests run a real plugin over the real protocol rather than a stubbed
// function, because what is being checked — how many requests are sent and
// what is in each — is a property of what crosses the process boundary. The
// plugin is this test binary re-executed with an environment variable set, so
// nothing is built, downloaded or installed.
const (
	spyModeEnv = "STELE_TEST_SPY_PLUGIN"
	spyDirEnv  = "STELE_TEST_SPY_DIR"
)

func TestMain(m *testing.M) {
	if mode, ok := os.LookupEnv(spyModeEnv); ok {
		os.Exit(spyPlugin(mode, os.Getenv(spyDirEnv)))
	}
	os.Exit(m.Run())
}

// spyPlugin records every request it is given and answers with one file per
// request, named after the first target.
func spyPlugin(mode, dir string) int {
	raw, err := os.ReadFile("/dev/stdin")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	// The file name is the count of what is already there, so the order of
	// invocations survives into the recording.
	seen, _ := os.ReadDir(dir)
	if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("req-%03d.bin", len(seen))), raw, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var req pluginpb.CodeGeneratorRequest
	if err := proto.Unmarshal(raw, &req); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	resp := &pluginpb.CodeGeneratorResponse{}
	switch mode {
	case "error":
		resp.Error = proto.String("the plugin refused")
	case "empty":
		// Answers successfully with no files at all.
	default:
		for _, t := range req.GetFileToGenerate() {
			resp.File = append(resp.File, &pluginpb.CodeGeneratorResponse_File{
				Name:    proto.String(strings.TrimSuffix(t, ".proto") + ".txt"),
				Content: proto.String("parameter=" + req.GetParameter() + "\n"),
			})
		}
	}
	out, err := proto.Marshal(resp)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	os.Stdout.Write(out)
	return 0
}

// spy is a plugin binary plus the directory its requests land in.
type spy struct {
	bin string
	dir string
}

func newSpy(t *testing.T, mode string) *spy {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	recordings := filepath.Join(base, "recordings")
	if err := os.MkdirAll(recordings, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(base, "protoc-gen-spy")
	script := fmt.Sprintf("#!/bin/sh\nexec env %s=%q %s=%q %q \"$@\"\n",
		spyModeEnv, mode, spyDirEnv, recordings, self)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return &spy{bin: bin, dir: recordings}
}

// requests returns every request the plugin received, in the order it received
// them.
func (s *spy) requests(t *testing.T) []*pluginpb.CodeGeneratorRequest {
	t.Helper()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	out := make([]*pluginpb.CodeGeneratorRequest, 0, len(names))
	for _, n := range names {
		raw, err := os.ReadFile(filepath.Join(s.dir, n))
		if err != nil {
			t.Fatal(err)
		}
		var req pluginpb.CodeGeneratorRequest
		if err := proto.Unmarshal(raw, &req); err != nil {
			t.Fatal(err)
		}
		out = append(out, &req)
	}
	return out
}

func (s *spy) calls(t *testing.T) int { return len(s.requests(t)) }

func write(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// world builds a consumer with two modules — so that a target can have two
// inputs — importing a file only the producer supplies.
func world(t *testing.T, manifest string) (string, resolve.FetchFunc) {
	t.Helper()

	producer := t.TempDir()
	write(t, producer, "stele.yaml", "version: 1\nmodules:\n  - path: api\n")
	write(t, producer, "api/dep/v1/b.proto", "syntax = \"proto3\";\npackage dep.v1;\nmessage B { string name = 1; }\n")

	consumer := t.TempDir()
	write(t, consumer, "stele.yaml", manifest)
	write(t, consumer, "api/own/v1/a.proto", "syntax = \"proto3\";\npackage own.v1;\nimport \"dep/v1/b.proto\";\nmessage A { dep.v1.B b = 1; }\n")
	write(t, consumer, "extra/side/v1/c.proto", "syntax = \"proto3\";\npackage side.v1;\nmessage C { int32 n = 1; }\n")

	fetch := func(_ context.Context, git, ref string) (string, string, error) {
		return producer, "0123456789abcdef0123456789abcdef01234567", nil
	}
	return consumer, fetch
}

// manifest returns a manifest with two modules and one target whose plugins
// section is given.
func manifest(plugins string, inputs ...string) string {
	body := `version: 1
modules:
  - path: api
  - path: extra
deps:
  - name: dep
    git: https://example.test/dep.git
    ref: main
    module: api
generate:
  - name: text
    inputs:
`
	for _, in := range inputs {
		body += "      - module: " + in + "\n"
	}
	return body + "    plugins:\n" + plugins
}

func plugin(bin, out string) string {
	return "      - local: " + bin + "\n        out: " + out + "\n        opt: [mode=text]\n"
}

func run(t *testing.T, dir string, fetch resolve.FetchFunc, opts gen.Options) error {
	t.Helper()
	_, err := runReport(t, dir, fetch, opts)
	return err
}

func runReport(t *testing.T, dir string, fetch resolve.FetchFunc, opts gen.Options) (*report.Report, error) {
	t.Helper()
	opts.Dir = dir
	opts.Fetch = fetch
	return gen.Run(context.Background(), opts)
}

// TestRun_ReportsWhatProducedTheBytes: a run is reproducible only if what ran
// can be named, and the plugins are only part of that. The report is built
// from the plugins actually invoked, not from the manifest, so a target that
// was not selected cannot contribute a plugin the run never executed.
func TestRun_ReportsWhatProducedTheBytes(t *testing.T) {
	s := newSpy(t, "ok")
	dir, fetch := world(t, manifest(plugin(s.bin, "gen"), "api"))

	rep, err := runReport(t, dir, fetch, gen.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if rep == nil {
		t.Fatal("no report from a successful run")
	}
	for _, want := range []string{"stele", "protocompile", "protobuf-go", "protoc-gen-spy"} {
		if _, ok := rep.Components[want]; !ok {
			t.Errorf("the report does not name %s", want)
		}
	}
	// A test binary carries no dependency metadata — the Go toolchain does not
	// stamp it — so the libraries read Unknown here, with a note saying why.
	// That the shipped binary reports their real versions is asserted where it
	// can be: against a built binary, in TestVersionCommandReportsRealVersions.
	for _, want := range []string{"protocompile", "protobuf-go"} {
		c := rep.Components[want]
		if c.Version == "" || (c.Version == report.Unknown && c.Note == "") {
			t.Errorf("%s = %+v, want a version or an explained %q", want, c, report.Unknown)
		}
	}
	// The spy is a shell script: no module metadata, and the honest answer is
	// that the version is unknown rather than that no plugin ran.
	if got := rep.Components["protoc-gen-spy"].Version; got != report.Unknown {
		t.Errorf("spy version = %q, want %q", got, report.Unknown)
	}
}

// Six repositories in the measured fleet declare two inputs. A single merged
// request would carry a different file_to_generate and produce different
// output, so the split is a property of the tool, not an implementation
// detail.
func TestRun_OneRequestPerInput(t *testing.T) {
	s := newSpy(t, "ok")
	dir, fetch := world(t, manifest(plugin(s.bin, "gen"), "api", "extra"))
	if err := run(t, dir, fetch, gen.Options{}); err != nil {
		t.Fatal(err)
	}
	if got := s.calls(t); got != 2 {
		t.Fatalf("plugin invocations = %d, want 2 (one per input)", got)
	}
	reqs := s.requests(t)
	if got := reqs[0].GetFileToGenerate(); !slices.Equal(got, []string{"own/v1/a.proto"}) {
		t.Fatalf("first request generates %v, want the first input's files only", got)
	}
	if got := reqs[1].GetFileToGenerate(); !slices.Equal(got, []string{"side/v1/c.proto"}) {
		t.Fatalf("second request generates %v, want the second input's files only", got)
	}
}

// Both mobile repositories in the measured fleet pass --include-imports;
// without it the external types disappear from their output.
func TestRun_IncludeImportsAddsImportsToFileToGenerate(t *testing.T) {
	s := newSpy(t, "ok")
	dir, fetch := world(t, manifest(plugin(s.bin, "gen"), "api"))
	if err := run(t, dir, fetch, gen.Options{IncludeImports: true}); err != nil {
		t.Fatal(err)
	}
	got := s.requests(t)[0].GetFileToGenerate()
	if !slices.Contains(got, "dep/v1/b.proto") {
		t.Fatalf("file_to_generate = %v; with --include-imports the imports must be in it", got)
	}
	if !slices.Contains(got, "own/v1/a.proto") {
		t.Fatalf("file_to_generate = %v; the target itself must still be in it", got)
	}
}

// The default is the other way round, and it is the default that matches what
// the fleet generates today.
func TestRun_ImportsAreNotGeneratedByDefault(t *testing.T) {
	s := newSpy(t, "ok")
	dir, fetch := world(t, manifest(plugin(s.bin, "gen"), "api"))
	if err := run(t, dir, fetch, gen.Options{}); err != nil {
		t.Fatal(err)
	}
	got := s.requests(t)[0].GetFileToGenerate()
	if slices.Contains(got, "dep/v1/b.proto") {
		t.Fatalf("file_to_generate = %v; an import is not a target", got)
	}
}

func TestRun_WritesResponseFilesUnderOut(t *testing.T) {
	s := newSpy(t, "ok")
	dir, fetch := world(t, manifest(plugin(s.bin, "gen"), "api"))
	if err := run(t, dir, fetch, gen.Options{}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "gen", "own", "v1", "a.txt"))
	if err != nil {
		t.Fatalf("the plugin's file was not written: %v", err)
	}
	// The opt list reaches the plugin as the request's parameter.
	if got := strings.TrimSpace(string(body)); got != "parameter=mode=text" {
		t.Fatalf("written content = %q, want the plugin's parameter to have been passed", got)
	}
}

func TestRun_PluginErrorSurfaces(t *testing.T) {
	s := newSpy(t, "error")
	dir, fetch := world(t, manifest(plugin(s.bin, "gen"), "api"))
	err := run(t, dir, fetch, gen.Options{})
	if err == nil || !strings.Contains(err.Error(), "the plugin refused") {
		t.Fatalf("the plugin's error must reach the user, got %v", err)
	}
}

// A run that writes no file is not an empty success.
func TestRun_EmptyResultIsAnError(t *testing.T) {
	s := newSpy(t, "empty")
	dir, fetch := world(t, manifest(plugin(s.bin, "gen"), "api"))
	err := run(t, dir, fetch, gen.Options{})
	if err == nil {
		t.Fatal("a run that generated no files must fail, not succeed quietly")
	}
	if !strings.Contains(err.Error(), "no files") {
		t.Fatalf("error %q does not say that nothing was generated", err)
	}
}

// paths narrow an input, in the coordinates of the module root. A path that
// matches nothing says so, and says what there was to match.
func TestRun_PathsNarrowAndUnmatchedPathIsAnError(t *testing.T) {
	s := newSpy(t, "ok")
	body := manifest(plugin(s.bin, "gen"))
	body = strings.Replace(body, "    inputs:\n", "    inputs:\n      - module: api\n        paths: [nowhere/v1]\n", 1)
	dir, fetch := world(t, body)
	err := run(t, dir, fetch, gen.Options{})
	if err == nil || !strings.Contains(err.Error(), "nowhere/v1") {
		t.Fatalf("want an error naming the unmatched path, got %v", err)
	}
	if !strings.Contains(err.Error(), "own/v1/a.proto") {
		t.Fatalf("error %q does not say what the module does supply", err)
	}
}

// Resolution goes through the lock, so a first run leaves one behind. This is
// not a lock test; it is the assertion that generate did not resolve around
// the enforcement every other command inherits.
func TestRun_WritesTheLock(t *testing.T) {
	s := newSpy(t, "ok")
	dir, fetch := world(t, manifest(plugin(s.bin, "gen"), "api"))
	if err := run(t, dir, fetch, gen.Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "stele.lock")); err != nil {
		t.Fatalf("generate did not go through the lock: %v", err)
	}
}

// A manifest may hold several targets; naming one runs only that one, and
// naming one that does not exist is an error rather than a run of nothing.
func TestRun_SelectsTargetsByName(t *testing.T) {
	s := newSpy(t, "ok")
	dir, fetch := world(t, manifest(plugin(s.bin, "gen"), "api"))
	if err := run(t, dir, fetch, gen.Options{Targets: []string{"nosuch"}}); err == nil ||
		!strings.Contains(err.Error(), "nosuch") {
		t.Fatalf("want an error naming the unknown target, got %v", err)
	}
	if err := run(t, dir, fetch, gen.Options{Targets: []string{"text"}}); err != nil {
		t.Fatal(err)
	}
	if got := s.calls(t); got != 1 {
		t.Fatalf("plugin invocations = %d, want 1", got)
	}
}

// The managed block reaches the descriptors the plugin is handed. Without
// this, a config that asks for managed options generates as if it had not.
func TestRun_ManagedOverrideReachesThePlugin(t *testing.T) {
	s := newSpy(t, "ok")
	body := manifest(plugin(s.bin, "gen"), "api")
	body = strings.Replace(body, "    plugins:\n",
		"    managed:\n      override:\n        - file_option: go_package_prefix\n          value: example.com/acme/gen\n    plugins:\n", 1)
	dir, fetch := world(t, body)
	if err := run(t, dir, fetch, gen.Options{}); err != nil {
		t.Fatal(err)
	}
	for _, f := range s.requests(t)[0].GetProtoFile() {
		if f.GetName() == "own/v1/a.proto" {
			if got := f.GetOptions().GetGoPackage(); got != "example.com/acme/gen/own/v1;ownv1" {
				t.Fatalf("go_package = %q, want the managed override applied", got)
			}
			return
		}
	}
	t.Fatal("the target file was not in the request")
}

// depWorld builds a consumer whose manifest is given verbatim, next to a
// producer that owns two modules — api, holding the files a consumer would
// generate from, and internal, which it does not.
func depWorld(t *testing.T, manifest string) (string, resolve.FetchFunc) {
	t.Helper()

	producer := t.TempDir()
	write(t, producer, "stele.yaml", "version: 1\nmodules:\n  - path: api\n  - path: internal\n")
	write(t, producer, "api/dep/v1/b.proto", "syntax = \"proto3\";\npackage dep.v1;\nimport \"shared/v1/s.proto\";\nmessage B { shared.v1.S s = 1; }\n")
	write(t, producer, "api/other/v1/o.proto", "syntax = \"proto3\";\npackage other.v1;\nmessage O { int32 n = 1; }\n")
	write(t, producer, "internal/shared/v1/s.proto", "syntax = \"proto3\";\npackage shared.v1;\nmessage S { string v = 1; }\n")

	consumer := t.TempDir()
	write(t, consumer, "stele.yaml", manifest)

	fetch := func(_ context.Context, git, ref string) (string, string, error) {
		return producer, "0123456789abcdef0123456789abcdef01234567", nil
	}
	return consumer, fetch
}

// depManifest is a manifest that owns no protos at all: no modules, one
// dependency, and one target generating from it.
func depManifest(plugins, input string) string {
	return `version: 1
deps:
  - name: dep
    git: https://example.test/dep.git
    ref: main
    module: api
generate:
  - name: text
    inputs:
` + input + "    plugins:\n" + plugins
}

// The real-world case: a repository that owns nothing, whose every generated
// line comes from somebody else's repository. Two of the measured repositories
// look exactly like this, and today they cannot be expressed at all.
func TestRun_GeneratesFromADependencyWithNoLocalModules(t *testing.T) {
	s := newSpy(t, "ok")
	dir, fetch := depWorld(t, depManifest(plugin(s.bin, "gen"), "      - dep: dep\n"))
	if err := run(t, dir, fetch, gen.Options{}); err != nil {
		t.Fatal(err)
	}
	// Two directories, so two requests; what the target selected is their
	// union, not any one of them.
	var got []string
	for _, req := range s.requests(t) {
		got = append(got, req.GetFileToGenerate()...)
	}
	slices.Sort(got)
	want := []string{"dep/v1/b.proto", "other/v1/o.proto"}
	if !slices.Equal(got, want) {
		t.Fatalf("file_to_generate across requests = %v, want the dependency's module %v", got, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "gen", "dep", "v1", "b.txt")); err != nil {
		t.Fatalf("the plugin's file was not written: %v", err)
	}
}

// The dependency's own imports are still imports, not targets, unless asked
// for — the same rule a local input obeys.
func TestRun_DependencyInputImportsFollowTheSameRule(t *testing.T) {
	s := newSpy(t, "ok")
	dir, fetch := depWorld(t, depManifest(plugin(s.bin, "gen"), "      - dep: dep\n"))
	if err := run(t, dir, fetch, gen.Options{IncludeImports: true}); err != nil {
		t.Fatal(err)
	}
	if got := s.requests(t)[0].GetFileToGenerate(); !slices.Contains(got, "shared/v1/s.proto") {
		t.Fatalf("file_to_generate = %v; with --include-imports the imports must be in it", got)
	}
}

// paths on a dependency input are relative to the root of the dependency's
// module, exactly as they are on the dep entry itself.
func TestRun_DependencyInputPathsAreRelativeToTheDepsModuleRoot(t *testing.T) {
	s := newSpy(t, "ok")
	dir, fetch := depWorld(t, depManifest(plugin(s.bin, "gen"), "      - dep: dep\n        paths: [dep/v1]\n"))
	if err := run(t, dir, fetch, gen.Options{}); err != nil {
		t.Fatal(err)
	}
	if got := s.requests(t)[0].GetFileToGenerate(); !slices.Equal(got, []string{"dep/v1/b.proto"}) {
		t.Fatalf("file_to_generate = %v, want the narrowed selection", got)
	}
}

// A path written in the coordinates of the producer's repository rather than
// of its module matches nothing, and that is an error naming the dependency.
func TestRun_DependencyInputUnmatchedPathIsAnError(t *testing.T) {
	s := newSpy(t, "ok")
	dir, fetch := depWorld(t, depManifest(plugin(s.bin, "gen"), "      - dep: dep\n        paths: [api/dep/v1]\n"))
	err := run(t, dir, fetch, gen.Options{})
	if err == nil || !strings.Contains(err.Error(), "api/dep/v1") {
		t.Fatalf("want an error naming the unmatched path, got %v", err)
	}
	if !strings.Contains(err.Error(), "dep/v1/b.proto") {
		t.Fatalf("error %q does not say what the dependency's module does supply", err)
	}
}

// Only the module the dep entry asked for is a candidate. The producer's other
// roots are in the graph so that imports resolve; generating from them would
// take code the manifest never asked for.
func TestRun_DependencyInputDoesNotReachTheProducersOtherModules(t *testing.T) {
	s := newSpy(t, "ok")
	dir, fetch := depWorld(t, depManifest(plugin(s.bin, "gen"), "      - dep: dep\n        paths: [shared/v1]\n"))
	err := run(t, dir, fetch, gen.Options{})
	if err == nil || !strings.Contains(err.Error(), "shared/v1") {
		t.Fatalf("want an error: shared/v1 is outside the module the dep entry names, got %v", err)
	}
}

// TestRun_SplitsRequestsByDirectory pins the split the acceptance measurement
// found: one request per directory of the files being generated, not one per
// input. A plugin that asks what else is in file_to_generate — vtprotobuf
// emits a fast path only for messages generated alongside it — writes
// different code for a cross-directory reference under the two splits, so this
// is an output property, not an internal one.
func TestRun_SplitsRequestsByDirectory(t *testing.T) {
	s := newSpy(t, "ok")
	dir, fetch := depWorld(t, depManifest(plugin(s.bin, "gen"), "      - dep: dep\n"))
	if err := run(t, dir, fetch, gen.Options{}); err != nil {
		t.Fatal(err)
	}
	var got [][]string
	for _, req := range s.requests(t) {
		got = append(got, req.GetFileToGenerate())
	}
	want := [][]string{{"dep/v1/b.proto"}, {"other/v1/o.proto"}}
	if len(got) != len(want) {
		t.Fatalf("%d request(s) %v, want one per directory %v", len(got), got, want)
	}
	for i := range want {
		if !slices.Equal(got[i], want[i]) {
			t.Fatalf("request %d file_to_generate = %v, want %v", i, got[i], want[i])
		}
	}
}
