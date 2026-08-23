package migrate_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thegorangers/stele/internal/config"
	"github.com/thegorangers/stele/internal/config/migrate"
)

// onlyPlugin returns the single translated plugin, failing if there is not
// exactly one.
func onlyPlugin(t *testing.T, r *migrate.Result) config.Plugin {
	t.Helper()
	ps := r.File.Generate[0].Plugins
	if len(ps) != 1 {
		t.Fatalf("plugins = %#v, want exactly one", ps)
	}
	return ps[0]
}

// hasUnresolved reports whether some unresolved entry mentions every fragment.
func hasUnresolved(r *migrate.Result, fragments ...string) bool {
	for _, u := range r.Unresolved {
		ok := true
		for _, f := range fragments {
			if !strings.Contains(u, f) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

const oneModule = "version: v2\nmodules: [{path: api}]\n"

const oneGen = `
version: v2
plugins:
  - local: protoc-gen-go
    out: gen
    opt: paths=source_relative
inputs: [{directory: api}]
`

// TestPluginVersionRecoveredFromMakefile is the gap this change closes. The
// version is written down in the Makefile; a migration that drops it hands
// back a manifest whose plugins fall back to a PATH lookup, which is the
// drift the pinning exists to end.
func TestPluginVersionRecoveredFromMakefile(t *testing.T) {
	r := mustMigrate(t, oneModule, oneGen, `
PROTOC_GEN_GO_VERSION ?= v1.36.6

gen-proto:
	@go install google.golang.org/protobuf/cmd/protoc-gen-go@$(PROTOC_GEN_GO_VERSION)
`)
	p := onlyPlugin(t, r)
	if p.Module != "google.golang.org/protobuf/cmd/protoc-gen-go" || p.Version != "v1.36.6" {
		t.Fatalf("plugin = %#v, want the module and version from the Makefile", p)
	}
	if hasUnresolved(r, "protoc-gen-go") {
		t.Fatalf("unresolved = %#v, want nothing left to decide for a pinned plugin", r.Unresolved)
	}
}

// TestPluginVersionInsideShellFallback covers the second measured shape: the
// install sits inside a `have it already || (install it)` shell group spread
// over continuation lines, so the argument arrives with a trailing paren.
func TestPluginVersionInsideShellFallback(t *testing.T) {
	r := mustMigrate(t, oneModule, oneGen, `
PROTOC_GEN_GO_VERSION ?= v1.36.6

gen-proto:
	@protoc-gen-go --version 2>/dev/null | grep -q $(PROTOC_GEN_GO_VERSION) || \
		(echo "Installing protoc-gen-go $(PROTOC_GEN_GO_VERSION)..." && \
		go install google.golang.org/protobuf/cmd/protoc-gen-go@$(PROTOC_GEN_GO_VERSION))
`)
	p := onlyPlugin(t, r)
	if p.Module != "google.golang.org/protobuf/cmd/protoc-gen-go" || p.Version != "v1.36.6" {
		t.Fatalf("plugin = %#v, want the module and version from the Makefile", p)
	}
}

// TestPluginVersionWithInstallFlagsAndSuffix covers the remaining measured
// shapes: build tags before the module argument, and an ignore-failure suffix
// after it.
func TestPluginVersionWithInstallFlagsAndSuffix(t *testing.T) {
	r := mustMigrate(t, oneModule, oneGen, `
V ?= v1.36.6

gen-proto:
	@go install -tags 'sometag' google.golang.org/protobuf/cmd/protoc-gen-go@$(V) 2>/dev/null || true
`)
	p := onlyPlugin(t, r)
	if p.Version != "v1.36.6" {
		t.Fatalf("plugin = %#v, want the version recovered past the flags and the suffix", p)
	}
}

// TestPseudoVersionIsExact: a pseudo-version names one commit and is as exact
// as a tag. Refusing it would refuse a version every measured repository pins.
func TestPseudoVersionIsExact(t *testing.T) {
	r := mustMigrate(t, oneModule, `
version: v2
plugins: [{local: protoc-gen-go-vtproto, out: gen}]
inputs: [{directory: api}]
`, `
VT ?= v0.6.1-0.20240319094008-0393e58bdf10

gen-proto:
	@go install github.com/planetscale/vtprotobuf/cmd/protoc-gen-go-vtproto@$(VT)
`)
	p := onlyPlugin(t, r)
	if p.Version != "v0.6.1-0.20240319094008-0393e58bdf10" {
		t.Fatalf("plugin = %#v, want the pseudo-version carried over", p)
	}
}

// TestFloatingPluginVersionIsUnresolved: `latest` is the drift itself. It is
// reported, never carried over and never guessed at.
func TestFloatingPluginVersionIsUnresolved(t *testing.T) {
	r := mustMigrate(t, oneModule, oneGen, `
gen-proto:
	@go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
`)
	p := onlyPlugin(t, r)
	if p.Module != "" || p.Version != "" {
		t.Fatalf("plugin = %#v, want no version claimed for a floating install", p)
	}
	if !hasUnresolved(r, "protoc-gen-go", "latest") {
		t.Fatalf("unresolved = %#v, want the floating version named", r.Unresolved)
	}
}

// TestUnexpandableVariableIsUnresolved: a version computed by the shell is not
// a version this tool can know, and a plausible-looking guess is worse than a
// report.
func TestUnexpandableVariableIsUnresolved(t *testing.T) {
	r := mustMigrate(t, oneModule, oneGen, `
V ?= $(shell cat .protoc-gen-go-version)

gen-proto:
	@go install google.golang.org/protobuf/cmd/protoc-gen-go@$(V)
`)
	if p := onlyPlugin(t, r); p.Version != "" {
		t.Fatalf("plugin = %#v, want no version claimed", p)
	}
	if !hasUnresolved(r, "protoc-gen-go", "$(V)") {
		t.Fatalf("unresolved = %#v, want the unexpanded variable named", r.Unresolved)
	}
}

// TestUndefinedVariableIsUnresolved: a variable with no assignment expands to
// nothing in make, which would install the module at its default branch.
func TestUndefinedVariableIsUnresolved(t *testing.T) {
	r := mustMigrate(t, oneModule, oneGen, `
gen-proto:
	@go install google.golang.org/protobuf/cmd/protoc-gen-go@$(NOWHERE_DEFINED)
`)
	if p := onlyPlugin(t, r); p.Version != "" {
		t.Fatalf("plugin = %#v, want no version claimed", p)
	}
	if !hasUnresolved(r, "protoc-gen-go") {
		t.Fatalf("unresolved = %#v, want the plugin named", r.Unresolved)
	}
}

// TestBranchPluginVersionIsUnresolved: a branch pins nothing, exactly as it
// pins nothing for a dependency.
func TestBranchPluginVersionIsUnresolved(t *testing.T) {
	r := mustMigrate(t, oneModule, oneGen, `
gen-proto:
	@go install google.golang.org/protobuf/cmd/protoc-gen-go@main
`)
	if p := onlyPlugin(t, r); p.Version != "" {
		t.Fatalf("plugin = %#v, want no version claimed", p)
	}
	if !hasUnresolved(r, "protoc-gen-go", "main") {
		t.Fatalf("unresolved = %#v, want the branch named", r.Unresolved)
	}
}

// TestDisagreeingPluginVersionsAreReported: where the same plugin is installed
// twice at different versions, the Makefile does not say which one the
// generated bytes came from. Picking one would be a coin toss recorded as a
// decision.
func TestDisagreeingPluginVersionsAreReported(t *testing.T) {
	r := mustMigrate(t, oneModule, oneGen, `
A ?= v1.36.6
B ?= v1.35.2

gen-proto:
	@go install google.golang.org/protobuf/cmd/protoc-gen-go@$(A)

other:
	@go install google.golang.org/protobuf/cmd/protoc-gen-go@$(B)
`)
	if p := onlyPlugin(t, r); p.Version != "" {
		t.Fatalf("plugin = %#v, want no version claimed when the Makefile disagrees with itself", p)
	}
	if !hasUnresolved(r, "protoc-gen-go", "v1.36.6", "v1.35.2") {
		t.Fatalf("unresolved = %#v, want both versions named", r.Unresolved)
	}
}

// TestDisagreeingPluginModulesAreReported: two different modules providing one
// command name is the same ambiguity one level up.
func TestDisagreeingPluginModulesAreReported(t *testing.T) {
	r := mustMigrate(t, oneModule, oneGen, `
V ?= v1.36.6

gen-proto:
	@go install google.golang.org/protobuf/cmd/protoc-gen-go@$(V)

other:
	@go install git.example.com/group/fork/cmd/protoc-gen-go@$(V)
`)
	if p := onlyPlugin(t, r); p.Module != "" {
		t.Fatalf("plugin = %#v, want no module claimed when two modules provide the name", p)
	}
	if !hasUnresolved(r, "protoc-gen-go", "git.example.com/group/fork/cmd/protoc-gen-go") {
		t.Fatalf("unresolved = %#v, want both modules named", r.Unresolved)
	}
}

// TestRepeatedIdenticalInstallIsNotAConflict: every measured Makefile installs
// the same plugin from several targets. Agreement is not disagreement.
func TestRepeatedIdenticalInstallIsNotAConflict(t *testing.T) {
	r := mustMigrate(t, oneModule, oneGen, `
V ?= v1.36.6

a:
	@go install google.golang.org/protobuf/cmd/protoc-gen-go@$(V)

b:
	@go install google.golang.org/protobuf/cmd/protoc-gen-go@$(V)
`)
	if p := onlyPlugin(t, r); p.Version != "v1.36.6" {
		t.Fatalf("plugin = %#v, want the agreed version", p)
	}
}

// TestNonGoPluginIsReportedWithItsTiers: a plugin that is not a Go program
// cannot be `go install`ed, and this tool will not invent a download URL and a
// digest it has not verified. It comes out as the bare PATH lookup it already
// was, with the available tiers named so a human can choose one.
func TestNonGoPluginIsReportedWithItsTiers(t *testing.T) {
	r := mustMigrate(t, oneModule, `
version: v2
plugins: [{local: protoc-gen-dart, out: lib/gen}]
inputs: [{directory: api}]
`, `
proto:
	@buf generate
`)
	p := onlyPlugin(t, r)
	if p.Module != "" || p.Version != "" || len(p.Downloads) != 0 || p.Path != "" {
		t.Fatalf("plugin = %#v, want a bare PATH plugin", p)
	}
	if !hasUnresolved(r, "protoc-gen-dart", "downloads", "path") {
		t.Fatalf("unresolved = %#v, want the plugin and the available tiers named", r.Unresolved)
	}
}

// TestRecoveredVersionReachesTheManifest: the recovered pin is worth nothing
// if the emitted document does not carry it.
func TestRecoveredVersionReachesTheManifest(t *testing.T) {
	r := mustMigrate(t, oneModule, oneGen, `
V ?= v1.36.6

gen-proto:
	@go install google.golang.org/protobuf/cmd/protoc-gen-go@$(V)
`)
	b, err := r.YAML()
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, "module: google.golang.org/protobuf/cmd/protoc-gen-go") || !strings.Contains(s, "version: v1.36.6") {
		t.Fatalf("emitted manifest does not carry the pin:\n%s", s)
	}
	// And the document has to be a manifest, not just text containing the
	// right words: the pin is worth nothing if the tool's own parser refuses
	// the file it was written into.
	path := filepath.Join(t.TempDir(), "stele.yaml")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("the emitted manifest does not load: %v\n%s", err, s)
	}
	if p := got.Generate[0].Plugins[0]; p.Module != "google.golang.org/protobuf/cmd/protoc-gen-go" || p.Version != "v1.36.6" {
		t.Fatalf("loaded plugin = %#v, want the pin to survive the round trip", p)
	}
}
