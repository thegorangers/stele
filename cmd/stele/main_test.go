package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thegorangers/stele/internal/config"
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
