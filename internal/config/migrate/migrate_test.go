package migrate_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/thegorangers/stele/internal/config"
	"github.com/thegorangers/stele/internal/config/migrate"
)

// mustMigrate translates one set of buf files and fails on any error.
func mustMigrate(t *testing.T, bufYAML, bufGenYAML, makefile string) *migrate.Result {
	t.Helper()
	r, err := migrate.FromBuf([]byte(bufYAML), []byte(bufGenYAML), []byte(makefile))
	if err != nil {
		t.Fatalf("FromBuf: %v", err)
	}
	return r
}

// TestPathsRebasedToModuleRoot is the reason this package exists. In buf the
// paths of an input are relative to the workspace root; in stele they are
// relative to the module root. Copied across verbatim they match nothing, and
// a naive implementation generates nothing instead of failing.
func TestPathsRebasedToModuleRoot(t *testing.T) {
	r := mustMigrate(t, `
version: v2
modules:
  - path: api
`, `
version: v2
plugins:
  - local: protoc-gen-go
    out: gen
    opt: paths=source_relative
inputs:
  - directory: api
    paths:
      - api/example/orders/v1
`, "")
	got := r.File.Generate[0].Inputs[0].Paths
	if !reflect.DeepEqual(got, []string{"example/orders/v1"}) {
		t.Fatalf("paths = %#v, want [example/orders/v1]", got)
	}
}

// TestPathOutsideItsDirectoryIsRefused: a path that does not live under the
// directory of its input cannot be rebased, and guessing would produce an
// input that silently selects nothing.
func TestPathOutsideItsDirectoryIsRefused(t *testing.T) {
	_, err := migrate.FromBuf([]byte("version: v2\nmodules: [{path: api}]\n"), []byte(`
version: v2
plugins: [{local: protoc-gen-go, out: gen}]
inputs:
  - directory: api
    paths: [other/example/v1]
`), nil)
	if err == nil || !strings.Contains(err.Error(), "other/example/v1") {
		t.Fatalf("want an error naming the path, got %v", err)
	}
}

// TestNoInputsExpandsToEveryModule: with no inputs buf falls back to the whole
// workspace. Leaving that implicit would hide which modules are generated.
func TestNoInputsExpandsToEveryModule(t *testing.T) {
	r := mustMigrate(t, `
version: v2
modules:
  - path: api
  - path: internal/api
`, `
version: v2
plugins:
  - local: protoc-gen-go
    out: gen
`, "")
	in := r.File.Generate[0].Inputs
	if len(in) != 2 || in[0].Module != "api" || in[1].Module != "internal/api" {
		t.Fatalf("inputs = %#v, want one explicit input per module", in)
	}
}

// TestExportsInMakefileBecomeDeps: a vendored tree does not record where it
// came from. The Makefile's export invocations do, and they are the only
// written-down source.
func TestExportsInMakefileBecomeDeps(t *testing.T) {
	r := mustMigrate(t, `
version: v2
modules:
  - path: api
  - path: third_party/proto
`, `
version: v2
plugins:
  - local: protoc-gen-go
    out: gen
inputs:
  - directory: api
  - directory: third_party/proto
    paths:
      - third_party/proto/example/orders/v1
`, `
vendor:
	@rm -rf third_party/proto
	@buf export "ssh://git@git.example.com/group/orders.git#subdir=api" \
		--exclude-imports --path example/orders/v1 --output=third_party/proto
`)
	if len(r.File.Deps) != 1 {
		t.Fatalf("deps = %#v, want one recovered dependency", r.File.Deps)
	}
	d := r.File.Deps[0]
	if d.Name != "orders" || d.Git != "https://git.example.com/group/orders.git" || d.Module != "api" {
		t.Fatalf("dep = %#v, want the orders repository at module api", d)
	}
	if !reflect.DeepEqual(d.Paths, []string{"example/orders/v1"}) {
		t.Fatalf("dep paths = %#v, want [example/orders/v1]", d.Paths)
	}
	// The vendored tree is no longer a module of this repository: it is a
	// dependency, and the input that read it must say so.
	for _, m := range r.File.Modules {
		if m.Path == "third_party/proto" {
			t.Fatal("the vendored tree must not survive as a local module")
		}
	}
	in := r.File.Generate[0].Inputs
	if len(in) != 2 || in[1].Dep != "orders" {
		t.Fatalf("inputs = %#v, want the vendored input turned into a dep input", in)
	}
	if !reflect.DeepEqual(in[1].Paths, []string{"example/orders/v1"}) {
		t.Fatalf("dep input paths = %#v, want them rebased onto the vendor root", in[1].Paths)
	}
}

// TestGitRepoInputBecomesDepAndInput covers the consumers that own no protos
// at all and generate everything from somebody else's repository.
func TestGitRepoInputBecomesDepAndInput(t *testing.T) {
	r, err := migrate.FromBuf(nil, []byte(`
version: v2
inputs:
  - git_repo: ssh://git@git.example.com/group/orders.git
    ref: 0123456789abcdef0123456789abcdef01234567
    subdir: api
plugins:
  - local: protoc-gen-dart
    out: lib/gen
    opt:
      - grpc
`), nil)
	if err != nil {
		t.Fatalf("FromBuf: %v", err)
	}
	if len(r.File.Modules) != 0 {
		t.Fatalf("modules = %#v, want none: this repository owns no protos", r.File.Modules)
	}
	d := r.File.Deps[0]
	if d.Name != "orders" || d.Ref != "0123456789abcdef0123456789abcdef01234567" || d.Module != "api" {
		t.Fatalf("dep = %#v", d)
	}
	if r.File.Generate[0].Inputs[0].Dep != "orders" {
		t.Fatalf("inputs = %#v, want a dep input", r.File.Generate[0].Inputs)
	}
}

// TestManagedOverrideTranslated covers the one measured managed form.
func TestManagedOverrideTranslated(t *testing.T) {
	r := mustMigrate(t, "version: v2\nmodules: [{path: api}]\n", `
version: v2
managed:
  enabled: true
  override:
    - file_option: go_package_prefix
      path: example
      value: git.example.com/group/orders/gen
plugins:
  - local: protoc-gen-go
    out: gen
`, "")
	m := r.File.Generate[0].Managed
	if m == nil || len(m.Override) != 1 {
		t.Fatalf("managed = %#v, want one override", m)
	}
	want := config.Override{FileOption: "go_package_prefix", Path: "example", Value: "git.example.com/group/orders/gen"}
	if m.Override[0] != want {
		t.Fatalf("override = %#v, want %#v", m.Override[0], want)
	}
}

// TestRefusals lists every shape outside the supported subset. A silent
// partial translation is the worst possible outcome: it yields a manifest that
// looks migrated and is not.
func TestRefusals(t *testing.T) {
	cases := []struct {
		name, bufYAML, bufGenYAML, want string
	}{
		{"config format v1", "version: v1\n", "version: v2\nplugins: [{local: protoc-gen-go, out: gen}]\n", "v1"},
		{"gen format v1", "", "version: v1\nplugins: [{local: protoc-gen-go, out: gen}]\n", "v1"},
		{"remote plugin", "", "version: v2\nplugins: [{remote: buf.build/protocolbuffers/go, out: gen}]\n", "remote"},
		{"managed disable", "", `
version: v2
managed:
  enabled: true
  disable:
    - file_option: go_package
plugins: [{local: protoc-gen-go, out: gen}]
`, "disable"},
		{"override module selector", "", `
version: v2
managed:
  enabled: true
  override:
    - file_option: go_package_prefix
      module: buf.build/example/orders
      value: x
plugins: [{local: protoc-gen-go, out: gen}]
`, "module"},
		{"override file option", "", `
version: v2
managed:
  enabled: true
  override:
    - file_option: java_package_prefix
      value: x
plugins: [{local: protoc-gen-go, out: gen}]
`, "java_package_prefix"},
		{"plugin strategy", "", "version: v2\nplugins: [{local: protoc-gen-go, out: gen, strategy: all}]\n", "strategy"},
		{"input types", "", `
version: v2
plugins: [{local: protoc-gen-go, out: gen}]
inputs: [{directory: api, types: [example.orders.v1.Order]}]
`, "types"},
		{"input exclude_paths", "", `
version: v2
plugins: [{local: protoc-gen-go, out: gen}]
inputs: [{directory: api, exclude_paths: [api/example/internal]}]
`, "exclude_paths"},
		{"unknown key", "", "version: v2\nplugins: [{local: protoc-gen-go, out: gen}]\nnonsense: 1\n", "nonsense"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var by []byte
			if c.bufYAML != "" {
				by = []byte(c.bufYAML)
			}
			_, err := migrate.FromBuf(by, []byte(c.bufGenYAML), nil)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want an error naming %q, got %v", c.want, err)
			}
		})
	}
}

// TestUnresolvedExportsAreReported: a registry export cannot become a git
// dependency, because the registry is not a git repository we can name. It
// must be reported by name, never dropped.
func TestUnresolvedExportsAreReported(t *testing.T) {
	r := mustMigrate(t, "version: v2\nmodules: [{path: api}]\n",
		"version: v2\nplugins: [{local: protoc-gen-go, out: gen}]\ninputs: [{directory: api}]\n",
		"vendor:\n\t@buf export buf.build/googleapis/googleapis --output=third_party/proto\n")
	if !r.Complete() {
		found := false
		for _, u := range r.Unresolved {
			if strings.Contains(u, "buf.build/googleapis/googleapis") {
				found = true
			}
		}
		if !found {
			t.Fatalf("unresolved = %#v, want the registry reference named", r.Unresolved)
		}
		return
	}
	t.Fatal("a registry export must leave the migration incomplete")
}

// TestExportWithoutRefLeavesTheRefUnset: buf export pins nothing, so there is
// no commit to carry over. Inventing a branch name would be a guess that reads
// as a decision; leaving it unset makes the manifest fail to load until a
// human pins it.
func TestExportWithoutRefLeavesTheRefUnset(t *testing.T) {
	r := mustMigrate(t, "version: v2\nmodules: [{path: api}, {path: third_party/proto}]\n",
		"version: v2\nplugins: [{local: protoc-gen-go, out: gen}]\ninputs: [{directory: api}]\n",
		"vendor:\n\t@buf export \"ssh://git@git.example.com/group/orders.git#subdir=api\" --output=third_party/proto\n")
	if r.File.Deps[0].Ref != "" {
		t.Fatalf("ref = %q, want it left unset", r.File.Deps[0].Ref)
	}
	if r.Complete() {
		t.Fatal("an unpinned dependency must leave the migration incomplete")
	}
}

// TestYAMLRoundTrips: the emitted manifest is what the tool itself loads. If
// it does not parse, the migration produced a document, not a manifest.
func TestYAMLRoundTrips(t *testing.T) {
	r := mustMigrate(t, `
version: v2
modules:
  - path: api
`, `
version: v2
managed:
  enabled: true
  override:
    - file_option: go_package_prefix
      path: example
      value: git.example.com/group/orders/gen
plugins:
  - local: protoc-gen-go
    out: gen
    opt: paths=source_relative
inputs:
  - directory: api
    paths: [api/example/orders/v1]
`, "")
	b, err := r.YAML()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "#") {
		t.Error("the emitted manifest carries no comment; a migration is meant to be reviewed")
	}
	p := filepath.Join(t.TempDir(), "stele.yaml")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := config.Load(p)
	if err != nil {
		t.Fatalf("the emitted manifest does not load: %v\n%s", err, b)
	}
	if !reflect.DeepEqual(got, r.File) {
		t.Errorf("loaded manifest differs from the migrated one:\n got %#v\nwant %#v", got, r.File)
	}
}

// TestFromDir reads the three files from a working copy, which is how the
// command is actually used.
func TestFromDir(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("buf.yaml", "version: v2\nmodules: [{path: api}]\n")
	write("buf.gen.yaml", "version: v2\nplugins: [{local: protoc-gen-go, out: gen}]\n")
	r, err := migrate.FromDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.File.Modules) != 1 || r.File.Modules[0].Path != "api" {
		t.Fatalf("modules = %#v", r.File.Modules)
	}
}

// TestMissingBufGenYAMLIsAnError: there is nothing to migrate without it.
func TestMissingBufGenYAMLIsAnError(t *testing.T) {
	_, err := migrate.FromDir(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "buf.gen.yaml") {
		t.Fatalf("want an error naming buf.gen.yaml, got %v", err)
	}
}

// TestLintAndBreakingBlocksAreAccepted: every measured buf.yaml carries a lint
// and a breaking block with nested keys. This tool implements neither check,
// but refusing to read them would refuse every real config there is.
func TestLintAndBreakingBlocksAreAccepted(t *testing.T) {
	r := mustMigrate(t, `
version: v2
modules:
  - path: api
lint:
  use:
    - STANDARD
  ignore:
    - api/example/legacy
breaking:
  use:
    - FILE
  ignore:
    - api/example/legacy
`, "version: v2\nplugins: [{local: protoc-gen-go, out: gen}]\n", "")
	if len(r.Notes) == 0 {
		t.Fatal("dropping the lint and breaking configuration must be reported")
	}
}

// TestSSHAddressIsMigratedToHTTPS: a Makefile's `buf export` addresses a
// producer over ssh://, because that is what a workstation with an ssh key
// uses. A typical CI image has no ssh binary at all, so a manifest carrying
// that form over fails there — and only once a pipeline runs. The manifest is
// authored here and committed for everyone, so the address it is authored
// with must be the portable one.
func TestSSHAddressIsMigratedToHTTPS(t *testing.T) {
	r := mustMigrate(t, `
version: v2
modules:
  - path: api
  - path: third_party/proto
`, `
version: v2
plugins:
  - local: protoc-gen-go
    out: gen
inputs:
  - directory: api
  - directory: third_party/proto
`, `
vendor:
	@rm -rf third_party/proto
	@buf export "ssh://git@gitlab.com/acme/services/orders.git#subdir=api,ref=9f1c2b3" \
		--exclude-imports --path acme/orders/v1 --output=third_party/proto
`)
	if len(r.File.Deps) != 1 {
		t.Fatalf("deps = %#v, want one recovered dependency", r.File.Deps)
	}
	if got, want := r.File.Deps[0].Git, "https://gitlab.com/acme/services/orders.git"; got != want {
		t.Fatalf("dep git = %q, want %q", got, want)
	}
	out, err := r.YAML()
	if err != nil {
		t.Fatalf("YAML: %v", err)
	}
	if strings.Contains(string(out), "git: ssh://") {
		t.Fatalf("the emitted manifest still carries an ssh:// address:\n%s", out)
	}
	if !strings.Contains(string(out), "git: https://gitlab.com/acme/services/orders.git") {
		t.Fatalf("the emitted manifest does not carry the https:// address:\n%s", out)
	}
	// The rewrite is reported rather than silent: it is a change to what the
	// source said, and the reader of a migration is asked to review it.
	if len(r.Notes) == 0 || !strings.Contains(strings.Join(r.Notes, "\n"), "ssh://git@gitlab.com/acme/services/orders.git") {
		t.Fatalf("notes = %#v, want the rewritten address named", r.Notes)
	}
}
