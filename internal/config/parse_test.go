package config_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/thegorangers/stele/internal/config"
	"github.com/thegorangers/stele/internal/managed"
)

// write puts the given YAML into a temporary stele.yaml and returns its path.
func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "stele.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func mustLoad(t *testing.T, body string) *config.File {
	t.Helper()
	f, err := config.Load(write(t, body))
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}
	return f
}

const validConfig = `
version: 1
modules:
  - path: api
deps:
  - name: example
    git: github.com/acme/example
    ref: v1.0.0
    module: api
    paths: [example/v1]
generate:
  - name: go
    inputs:
      - module: api
        paths: [example/v1]
    plugins:
      - local: protoc-gen-go
        out: gen
        opt: [paths=source_relative]
`

func TestLoad_Valid(t *testing.T) {
	f := mustLoad(t, validConfig)

	want := &config.File{
		Version: 1,
		Modules: []config.Module{{Path: "api"}},
		Deps: []config.Dep{{
			Name:   "example",
			Git:    "github.com/acme/example",
			Ref:    "v1.0.0",
			Module: "api",
			Paths:  []string{"example/v1"},
		}},
		Generate: []config.GenTarget{{
			Name:    "go",
			Inputs:  []config.Input{{Module: "api", Paths: []string{"example/v1"}}},
			Plugins: []config.Plugin{{Local: "protoc-gen-go", Out: "gen", Opt: []string{"paths=source_relative"}}},
		}},
	}
	if !reflect.DeepEqual(f, want) {
		t.Fatalf("Load =\n%#v\nwant\n%#v", f, want)
	}
}

func TestLoad_UnknownKeyIsError(t *testing.T) {
	p := write(t, `
version: 1
modules:
  - path: api
    strategy: all
`)
	_, err := config.Load(p)
	if err == nil || !strings.Contains(err.Error(), "strategy") {
		t.Fatalf("want an error naming the key \"strategy\", got: %v", err)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := config.Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err == nil {
		t.Fatal("want an error for a missing file, got nil")
	}
}

// Scalar and list forms both occur in real configs; both normalise to []string.
func TestLoad_ScalarAndListForms(t *testing.T) {
	for _, tc := range []struct {
		name string
		yaml string
		want []string
		get  func(*config.File) []string
	}{
		{
			name: "opt scalar",
			yaml: "opt: paths=source_relative",
			want: []string{"paths=source_relative"},
		},
		{
			name: "opt list",
			yaml: "opt:\n          - paths=source_relative",
			want: []string{"paths=source_relative"},
		},
		{
			name: "opt list of two",
			yaml: "opt:\n          - paths=source_relative\n          - require_unimplemented_servers=false",
			want: []string{"paths=source_relative", "require_unimplemented_servers=false"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := mustLoad(t, `
version: 1
modules:
  - path: api
generate:
  - name: go
    inputs:
      - module: api
    plugins:
      - local: protoc-gen-go
        out: gen
        `+tc.yaml+"\n")
			got := f.Generate[0].Plugins[0].Opt
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("opt = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestLoad_PathsScalarAndList(t *testing.T) {
	for _, tc := range []struct {
		name string
		yaml string
	}{
		{"paths scalar", "paths: example/v1"},
		{"paths list", "paths: [example/v1]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := mustLoad(t, `
version: 1
modules:
  - path: api
generate:
  - name: go
    inputs:
      - module: api
        `+tc.yaml+`
    plugins:
      - local: protoc-gen-go
        out: gen
`)
			if got := f.Generate[0].Inputs[0].Paths; !reflect.DeepEqual(got, []string{"example/v1"}) {
				t.Fatalf("paths = %#v", got)
			}
		})
	}
}

func TestLoad_WrongTypeForStringList(t *testing.T) {
	_, err := config.Load(write(t, `
version: 1
modules:
  - path: api
generate:
  - name: go
    inputs:
      - module: api
    plugins:
      - local: protoc-gen-go
        out: gen
        opt:
          key: value
`))
	if err == nil || !strings.Contains(err.Error(), "string or a list of strings") {
		t.Fatalf("want a type error mentioning the expected shape, got: %v", err)
	}
}

func TestLoad_Invalid(t *testing.T) {
	for _, tc := range []struct {
		name string
		yaml string
		want string // substring the error must name
	}{
		{
			name: "malformed yaml",
			yaml: "version: 1\nmodules: [",
			want: "yaml",
		},
		{
			name: "missing version",
			yaml: "modules:\n  - path: api\n",
			want: "version",
		},
		{
			name: "unsupported version",
			yaml: "version: 2\nmodules:\n  - path: api\n",
			want: "version",
		},
		{
			name: "input with neither module nor dep",
			yaml: "version: 1\nmodules:\n  - path: api\ngenerate:\n  - name: go\n    inputs:\n      - paths: [example/v1]\n    plugins:\n      - local: protoc-gen-go\n        out: gen\n",
			want: "generate[0].inputs[0]",
		},
		{
			name: "input with both module and dep",
			yaml: "version: 1\nmodules:\n  - path: api\ndeps:\n  - name: example\n    git: github.com/acme/example\n    ref: v1\ngenerate:\n  - name: go\n    inputs:\n      - module: api\n        dep: example\n    plugins:\n      - local: protoc-gen-go\n        out: gen\n",
			want: "generate[0].inputs[0]",
		},
		{
			name: "input references undeclared dep",
			yaml: "version: 1\nmodules:\n  - path: api\ndeps:\n  - name: example\n    git: github.com/acme/example\n    ref: v1\ngenerate:\n  - name: go\n    inputs:\n      - dep: other\n    plugins:\n      - local: protoc-gen-go\n        out: gen\n",
			want: "\"other\" is not declared in deps (declared: example)",
		},
		{
			name: "module without path",
			yaml: "version: 1\nmodules:\n  - path: \"\"\n",
			want: "modules[0].path",
		},
		{
			name: "duplicate module path",
			yaml: "version: 1\nmodules:\n  - path: api\n  - path: api\n",
			want: "duplicate",
		},
		{
			name: "dep without name",
			yaml: "version: 1\nmodules:\n  - path: api\ndeps:\n  - git: github.com/acme/example\n    ref: v1.0.0\n",
			want: "deps[0].name",
		},
		{
			name: "dep without git",
			yaml: "version: 1\nmodules:\n  - path: api\ndeps:\n  - name: example\n    ref: v1.0.0\n",
			want: "deps[0].git",
		},
		{
			name: "dep without ref",
			yaml: "version: 1\nmodules:\n  - path: api\ndeps:\n  - name: example\n    git: github.com/acme/example\n",
			want: "deps[0].ref",
		},
		{
			name: "duplicate dep name",
			yaml: "version: 1\nmodules:\n  - path: api\ndeps:\n  - name: example\n    git: github.com/acme/example\n    ref: v1\n  - name: example\n    git: github.com/acme/other\n    ref: v1\n",
			want: "duplicate",
		},
		{
			name: "generate target without name",
			yaml: "version: 1\nmodules:\n  - path: api\ngenerate:\n  - inputs:\n      - module: api\n    plugins:\n      - local: protoc-gen-go\n        out: gen\n",
			want: "generate[0].name",
		},
		{
			name: "generate target without inputs",
			yaml: "version: 1\nmodules:\n  - path: api\ngenerate:\n  - name: go\n    plugins:\n      - local: protoc-gen-go\n        out: gen\n",
			want: "generate[0].inputs",
		},
		{
			name: "generate target without plugins",
			yaml: "version: 1\nmodules:\n  - path: api\ngenerate:\n  - name: go\n    inputs:\n      - module: api\n",
			want: "generate[0].plugins",
		},
		{
			name: "input references undeclared module",
			yaml: "version: 1\nmodules:\n  - path: api\ngenerate:\n  - name: go\n    inputs:\n      - module: other\n    plugins:\n      - local: protoc-gen-go\n        out: gen\n",
			want: "\"other\"",
		},
		{
			name: "plugin without local",
			yaml: "version: 1\nmodules:\n  - path: api\ngenerate:\n  - name: go\n    inputs:\n      - module: api\n    plugins:\n      - out: gen\n",
			want: "generate[0].plugins[0].local",
		},
		{
			name: "plugin without out",
			yaml: "version: 1\nmodules:\n  - path: api\ngenerate:\n  - name: go\n    inputs:\n      - module: api\n    plugins:\n      - local: protoc-gen-go\n",
			want: "generate[0].plugins[0].out",
		},
		{
			name: "duplicate generate target name",
			yaml: "version: 1\nmodules:\n  - path: api\ngenerate:\n  - name: go\n    inputs:\n      - module: api\n    plugins:\n      - local: protoc-gen-go\n        out: gen\n  - name: go\n    inputs:\n      - module: api\n    plugins:\n      - local: protoc-gen-go\n        out: gen2\n",
			want: "duplicate",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.Load(write(t, tc.yaml))
			if err == nil {
				t.Fatalf("want an error naming %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not name %q", err, tc.want)
			}
		})
	}
}

// Every error must be prefixed with the file path so the user knows what to open.
func TestLoad_ErrorNamesFile(t *testing.T) {
	p := write(t, "version: 1\nmodules:\n  - path: api\n  - path: api\n")
	_, err := config.Load(p)
	if err == nil || !strings.Contains(err.Error(), p) {
		t.Fatalf("want an error mentioning %q, got: %v", p, err)
	}
}

// A generate target may ask for managed-mode file options. The shape mirrors
// the one measured on real configs: a list of overrides, each naming a file
// option, an optional path selector and a value.
func TestLoad_ManagedOverride(t *testing.T) {
	f := mustLoad(t, `
version: 1
modules:
  - path: api
generate:
  - name: go
    managed:
      override:
        - file_option: go_package_prefix
          path: acme
          value: example.com/acme/gen
    inputs:
      - module: api
    plugins:
      - local: protoc-gen-go
        out: gen
`)
	got := f.Generate[0].Managed
	if got == nil {
		t.Fatal("managed block was dropped")
	}
	want := &config.Managed{Override: []config.Override{{
		FileOption: "go_package_prefix",
		Path:       "acme",
		Value:      "example.com/acme/gen",
	}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("managed =\n%#v\nwant\n%#v", got, want)
	}

	mc := got.Config()
	if len(mc.GoPackagePrefix) != 1 ||
		mc.GoPackagePrefix[0].Path != "acme" || mc.GoPackagePrefix[0].Value != "example.com/acme/gen" {
		t.Fatalf("Config() = %#v, want the prefix override carried over", mc)
	}
}

// A target with no managed block asks for no managed options at all: a nil
// pointer, not an empty config that would silently rewrite every descriptor.
func TestLoad_NoManagedBlockIsNil(t *testing.T) {
	f := mustLoad(t, validConfig)
	if f.Generate[0].Managed != nil {
		t.Fatalf("managed = %#v, want nil when the block is absent", f.Generate[0].Managed)
	}
}

func TestLoad_ManagedInvalid(t *testing.T) {
	const head = "version: 1\nmodules:\n  - path: api\ngenerate:\n  - name: go\n    inputs:\n      - module: api\n    plugins:\n      - local: protoc-gen-go\n        out: gen\n    managed:\n"
	for _, tc := range []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "unknown key inside managed",
			yaml: head + "      disable: true\n      override: []\n",
			want: "disable",
		},
		{
			name: "unknown key inside override",
			yaml: head + "      override:\n        - file_option: go_package_prefix\n          value: x\n          module: y\n",
			want: "module",
		},
		{
			name: "unknown file option",
			yaml: head + "      override:\n        - file_option: java_package_prefix\n          value: x\n",
			want: "java_package_prefix",
		},
		{
			name: "override without file option",
			yaml: head + "      override:\n        - value: x\n",
			want: "generate[0].managed.override[0].file_option",
		},
		{
			name: "override without value",
			yaml: head + "      override:\n        - file_option: go_package_prefix\n",
			want: "generate[0].managed.override[0].value",
		},
		{
			name: "empty managed block",
			yaml: head + "      override: []\n",
			want: "generate[0].managed.override",
		},
		{
			name: "duplicate file option",
			yaml: head + "      override:\n        - file_option: go_package_prefix\n          value: x\n        - file_option: go_package_prefix\n          value: y\n",
			want: "duplicate",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.Load(write(t, tc.yaml))
			if err == nil {
				t.Fatalf("want an error naming %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not name %q", err, tc.want)
			}
		})
	}
}

// A consumer that owns no protos at all declares no modules: every generated
// line comes from somebody else's repository. Requiring a module here would
// make two of the measured repositories unable to migrate.
func TestLoad_ManifestWithNoModulesOfItsOwn(t *testing.T) {
	const y = `version: 1
deps:
  - name: example
    git: github.com/acme/example
    ref: v1
    module: api
generate:
  - name: go
    inputs:
      - dep: example
        paths: [acme/v1]
    plugins:
      - local: protoc-gen-go
        out: gen
`
	f, err := config.Load(write(t, y))
	if err != nil {
		t.Fatalf("a manifest that owns nothing must load: %v", err)
	}
	if len(f.Modules) != 0 {
		t.Fatalf("modules = %v, want none", f.Modules)
	}
	in := f.Generate[0].Inputs[0]
	if in.Dep != "example" || in.Module != "" {
		t.Fatalf("input = %+v, want the dependency selector set and the module empty", in)
	}
}

// A plugin may declare the module it is installed from, so that its version is
// stated in the manifest rather than left to whatever is on PATH. The older
// form, a bare local name, must keep parsing unchanged.
func TestLoad_PluginDeclaredVersion(t *testing.T) {
	f := mustLoad(t, `
version: 1
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
`)
	want := []config.Plugin{
		{Local: "protoc-gen-go", Module: "google.golang.org/protobuf/cmd/protoc-gen-go", Version: "v1.36.11", Out: "gen"},
		{Local: "protoc-gen-dart", Out: "lib"},
	}
	if got := f.Generate[0].Plugins; !reflect.DeepEqual(got, want) {
		t.Errorf("plugins:\n got %+v\nwant %+v", got, want)
	}
}

func TestLoad_PluginVersionInvalid(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
	}{
		{
			name: "version without module",
			body: `
version: 1
modules:
  - path: api
generate:
  - name: go
    inputs:
      - module: api
    plugins:
      - local: protoc-gen-go
        version: v1.36.11
        out: gen
`,
			want: "version: v1.36.11 is declared without a module",
		},
		{
			name: "module without version",
			body: `
version: 1
modules:
  - path: api
generate:
  - name: go
    inputs:
      - module: api
    plugins:
      - local: protoc-gen-go
        module: google.golang.org/protobuf/cmd/protoc-gen-go
        out: gen
`,
			want: "version: missing for plugin",
		},
		{
			name: "inexact version",
			body: `
version: 1
modules:
  - path: api
generate:
  - name: go
    inputs:
      - module: api
    plugins:
      - local: protoc-gen-go
        module: google.golang.org/protobuf/cmd/protoc-gen-go
        version: latest
        out: gen
`,
			want: "latest",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.Load(write(t, tc.body))
			if err == nil {
				t.Fatalf("Load: expected an error mentioning %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Load: error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// A plugin that is not a Go program can still be pinned, but the bytes it is
// pinned to are per-platform: one digest cannot name the linux/amd64 binary
// and the darwin/arm64 one at once. So the download tier is a list, one entry
// per platform, spelled the way Go spells it.
func TestLoad_PluginTiers(t *testing.T) {
	f := mustLoad(t, `
version: 1
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
        downloads:
          - os: linux
            arch: amd64
            url: https://example.com/protoc-gen-dart-linux-x64.tar.gz
            sha256: 0000000000000000000000000000000000000000000000000000000000000000
            archive_path: bin/protoc-gen-dart
          - os: darwin
            arch: arm64
            url: https://example.com/protoc-gen-dart-macos-arm64.tar.gz
            sha256: 1111111111111111111111111111111111111111111111111111111111111111
            archive_path: bin/protoc-gen-dart
        out: lib
      - local: protoc-gen-house
        path: tools/protoc-gen-house
        out: house
      - local: protoc-gen-found
        out: found
`)
	want := []config.Plugin{
		{Local: "protoc-gen-go", Module: "google.golang.org/protobuf/cmd/protoc-gen-go", Version: "v1.36.11", Out: "gen"},
		{
			Local: "protoc-gen-dart",
			Downloads: []config.Download{
				{
					OS: "linux", Arch: "amd64",
					URL:         "https://example.com/protoc-gen-dart-linux-x64.tar.gz",
					SHA256:      "0000000000000000000000000000000000000000000000000000000000000000",
					ArchivePath: "bin/protoc-gen-dart",
				},
				{
					OS: "darwin", Arch: "arm64",
					URL:         "https://example.com/protoc-gen-dart-macos-arm64.tar.gz",
					SHA256:      "1111111111111111111111111111111111111111111111111111111111111111",
					ArchivePath: "bin/protoc-gen-dart",
				},
			},
			Out: "lib",
		},
		{Local: "protoc-gen-house", Path: "tools/protoc-gen-house", Out: "house"},
		{Local: "protoc-gen-found", Out: "found"},
	}
	if got := f.Generate[0].Plugins; !reflect.DeepEqual(got, want) {
		t.Errorf("plugins:\n got %+v\nwant %+v", got, want)
	}
}

func TestLoad_PluginTierInvalid(t *testing.T) {
	body := func(lines string) string {
		return `
version: 1
modules:
  - path: api
generate:
  - name: go
    inputs:
      - module: api
    plugins:
      - local: protoc-gen-x
        out: gen
` + lines
	}
	const oneDownload = "        downloads:\n" +
		"          - os: linux\n            arch: amd64\n" +
		"            url: https://example.com/x\n" +
		"            sha256: 0000000000000000000000000000000000000000000000000000000000000000\n"
	for _, tc := range []struct {
		name, lines, want string
	}{
		{
			name:  "module and downloads",
			lines: "        module: example.com/x\n        version: v1.0.0\n" + oneDownload,
			want:  `plugin "protoc-gen-x" declares both module and downloads`,
		},
		{
			name:  "module and path",
			lines: "        module: example.com/x\n        version: v1.0.0\n        path: tools/x\n",
			want:  `plugin "protoc-gen-x" declares both module and path`,
		},
		{
			name:  "downloads and path",
			lines: oneDownload + "        path: tools/x\n",
			want:  `plugin "protoc-gen-x" declares both downloads and path`,
		},
		{
			name:  "empty list",
			lines: "        downloads: []\n",
			want:  "downloads: declared with no entries",
		},
		{
			name:  "url without sha256",
			lines: "        downloads:\n          - os: linux\n            arch: amd64\n            url: https://example.com/x\n",
			want:  "downloads[0].sha256: missing",
		},
		{
			name:  "sha256 without url",
			lines: "        downloads:\n          - os: linux\n            arch: amd64\n            sha256: 0000000000000000000000000000000000000000000000000000000000000000\n",
			want:  "downloads[0].url: missing",
		},
		{
			name:  "sha256 not a hash",
			lines: "        downloads:\n          - os: linux\n            arch: amd64\n            url: https://example.com/x\n            sha256: nonsense\n",
			want:  "downloads[0].sha256: ",
		},
		{
			name:  "no os",
			lines: "        downloads:\n          - arch: amd64\n            url: https://example.com/x\n            sha256: 0000000000000000000000000000000000000000000000000000000000000000\n",
			want:  "downloads[0].os: missing",
		},
		{
			name:  "no arch",
			lines: "        downloads:\n          - os: linux\n            url: https://example.com/x\n            sha256: 0000000000000000000000000000000000000000000000000000000000000000\n",
			want:  "downloads[0].arch: missing",
		},
		{
			name: "the same platform twice",
			lines: "        downloads:\n" +
				"          - os: linux\n            arch: amd64\n            url: https://example.com/a\n" +
				"            sha256: 0000000000000000000000000000000000000000000000000000000000000000\n" +
				"          - os: linux\n            arch: amd64\n            url: https://example.com/b\n" +
				"            sha256: 1111111111111111111111111111111111111111111111111111111111111111\n",
			want: "downloads[1]: linux/amd64 is already declared by downloads[0]",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.Load(write(t, body(tc.lines)))
			if err == nil {
				t.Fatal("Load: expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// TestLoad_PathScopedManagedOverrides pins that one file option may be
// overridden several times, once per path.
//
// Declaring it once was a manifest limitation, not a corpus one: the reference
// tool accepts several path-scoped overrides of go_package_prefix, and a
// repository that splits its generated Go across two import prefixes cannot be
// migrated without them. Order is meaning, and is preserved: the reference
// tool applies the LAST matching entry, not the most specific one.
func TestLoad_PathScopedManagedOverrides(t *testing.T) {
	f := mustLoad(t, `
version: 1
modules:
  - path: api
generate:
  - name: go
    managed:
      override:
        - file_option: go_package_prefix
          value: example.com/gen
        - file_option: go_package_prefix
          path: acme/widget
          value: example.com/widget/gen
    inputs:
      - module: api
    plugins:
      - local: protoc-gen-go
        out: gen
`)
	want := []managed.Override{
		{Path: "", Value: "example.com/gen"},
		{Path: "acme/widget", Value: "example.com/widget/gen"},
	}
	if got := f.Generate[0].Managed.Config().GoPackagePrefix; !reflect.DeepEqual(got, want) {
		t.Fatalf("Config().GoPackagePrefix =\n%#v\nwant\n%#v", got, want)
	}
}

// TestLoad_RefusesRepeatedManagedOverridePath refuses two overrides of one
// file option that select the same path, and refuses a path spelled in a way
// the reference tool refuses too.
//
// The reference tool takes the last of two entries for one path silently. This
// tool refuses: one of the two lines describes output nothing will ever have,
// and a dead line in a committed manifest is the kind that gets edited by
// somebody who then wonders why nothing changed.
func TestLoad_RefusesRepeatedManagedOverridePath(t *testing.T) {
	const head = "version: 1\nmodules:\n  - path: api\ngenerate:\n  - name: go\n    managed:\n      override:\n"
	const tail = "    inputs:\n      - module: api\n    plugins:\n      - local: protoc-gen-go\n        out: gen\n"
	for _, tc := range []struct{ name, overrides, want string }{
		{
			"same path twice",
			"        - file_option: go_package_prefix\n          path: acme\n          value: example.com/a\n        - file_option: go_package_prefix\n          path: acme\n          value: example.com/b\n",
			"duplicate",
		},
		{
			"no path twice",
			"        - file_option: go_package_prefix\n          value: example.com/a\n        - file_option: go_package_prefix\n          value: example.com/b\n",
			"duplicate",
		},
		{
			"leading slash",
			"        - file_option: go_package_prefix\n          path: /acme\n          value: example.com/a\n",
			"generate[0].managed.override[0].path",
		},
		{
			"trailing slash",
			"        - file_option: go_package_prefix\n          path: acme/\n          value: example.com/a\n",
			"generate[0].managed.override[0].path",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.Load(write(t, head+tc.overrides+tail))
			if err == nil {
				t.Fatalf("want an error naming %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not name %q", err, tc.want)
			}
		})
	}
}
