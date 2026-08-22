package config_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/thegorangers/stele/internal/config"
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
			name: "no modules",
			yaml: "version: 1\n",
			want: "modules",
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
			name: "input without module",
			yaml: "version: 1\nmodules:\n  - path: api\ngenerate:\n  - name: go\n    inputs:\n      - paths: [example/v1]\n    plugins:\n      - local: protoc-gen-go\n        out: gen\n",
			want: "generate[0].inputs[0].module",
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
	p := write(t, "version: 1\n")
	_, err := config.Load(p)
	if err == nil || !strings.Contains(err.Error(), p) {
		t.Fatalf("want an error mentioning %q, got: %v", p, err)
	}
}
