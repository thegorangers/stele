package lint_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/bufbuild/protocompile"
	"github.com/bufbuild/protocompile/linker"
	"github.com/thegorangers/stele/internal/lint"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// compileSource links one proto file held in memory, so that a rule is
// measured against a real linked descriptor rather than a hand-built one. A
// descriptor assembled by hand can hold a shape the compiler would never
// produce, and a rule that only ever sees those is a rule nobody has run.
func compileSource(t *testing.T, path, src string) protoreflect.FileDescriptor {
	t.Helper()
	c := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(protocompile.ResolverFunc(
			func(p string) (protocompile.SearchResult, error) {
				if p != path {
					return protocompile.SearchResult{}, fmt.Errorf("%s: %w", p, os.ErrNotExist)
				}
				return protocompile.SearchResult{Source: strings.NewReader(src)}, nil
			})),
		SourceInfoMode: protocompile.SourceInfoStandard,
	}
	files, err := c.Compile(context.Background(), path)
	if err != nil {
		t.Fatalf("compiling the fixture: %v", err)
	}
	return files[0].(linker.File)
}

// TestRulesFireAndDoNotFire is the whole claim a rule makes: it says something
// about a file that breaks it, and says nothing about a file that does not.
// Only the first half is usually tested, and a rule that fires on everything
// passes that half.
func TestRulesFireAndDoNotFire(t *testing.T) {
	cases := []struct {
		rule  string
		clean string // a file the rule must say nothing about
		dirty string // a file the rule must report exactly once
		path  string
		// fileLevel says the rule reports the absence of a declaration, so
		// there is no line to point at and the path is the whole location.
		fileLevel bool
	}{
		{
			rule:      "stele/syntax_declared",
			fileLevel: true,
			path:      "example/v1/a.proto",
			clean: `syntax = "proto3"; package example.v1;
				message Order { string id = 1; }`,
			dirty: `package example.v1;
				message Order { optional string id = 1; }`,
		},
		{
			rule:      "stele/package_declared",
			fileLevel: true,
			path:      "a.proto",
			clean:     `syntax = "proto3"; package v1; message Order { string id = 1; }`,
			dirty:     `syntax = "proto3"; message Order { string id = 1; }`,
		},
		{
			rule:  "stele/package_lower_snake_case",
			path:  "Example/v1/a.proto",
			clean: `syntax = "proto3"; package example.v1;`,
			dirty: `syntax = "proto3"; package Example.v1;`,
		},
		{
			rule:  "stele/package_version_suffix",
			path:  "example/a.proto",
			clean: `syntax = "proto3"; package example.v1;`,
			dirty: `syntax = "proto3"; package example;`,
		},
		{
			rule:  "stele/directory_matches_package",
			path:  "elsewhere/a.proto",
			clean: `syntax = "proto3"; package elsewhere;`,
			dirty: `syntax = "proto3"; package example.v1;`,
		},
		{
			rule: "stele/enum_value_prefix",
			path: "example/v1/a.proto",
			clean: `syntax = "proto3"; package example.v1;
				enum OrderStatus { ORDER_STATUS_UNSPECIFIED = 0; ORDER_STATUS_PLACED = 1; }`,
			dirty: `syntax = "proto3"; package example.v1;
				enum OrderStatus { ORDER_STATUS_UNSPECIFIED = 0; PLACED = 1; }`,
		},
		{
			rule: "stele/enum_value_upper_snake_case",
			path: "example/v1/a.proto",
			clean: `syntax = "proto3"; package example.v1;
				enum Colour { COLOUR_UNSPECIFIED = 0; COLOUR_RED = 1; }`,
			dirty: `syntax = "proto3"; package example.v1;
				enum Colour { COLOUR_UNSPECIFIED = 0; Colour_Red = 1; }`,
		},
		{
			rule: "stele/enum_zero_value_unspecified",
			path: "example/v1/a.proto",
			clean: `syntax = "proto3"; package example.v1;
				enum Colour { COLOUR_UNSPECIFIED = 0; COLOUR_RED = 1; }`,
			dirty: `syntax = "proto3"; package example.v1;
				enum Colour { COLOUR_RED = 0; COLOUR_BLUE = 1; }`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.rule, func(t *testing.T) {
			e, err := lint.New(lint.Builtin(), lint.Config{})
			if err != nil {
				t.Fatal(err)
			}
			clean := e.Check([]protoreflect.FileDescriptor{compileSource(t, tc.path, tc.clean)})
			if got := findingsFor(clean, tc.rule); len(got) != 0 {
				t.Errorf("the rule fires on a file that keeps it: %v", got)
			}
			dirty := e.Check([]protoreflect.FileDescriptor{compileSource(t, tc.path, tc.dirty)})
			got := findingsFor(dirty, tc.rule)
			if len(got) != 1 {
				t.Fatalf("want exactly one finding on a file that breaks the rule, got %d: %v", len(got), got)
			}
			if got[0].Pos.Line == 0 && !tc.fileLevel {
				t.Errorf("the finding carries no line; it is read in a CI log by somebody who must open the file")
			}
			if got[0].Pos.Line != 0 && tc.fileLevel {
				t.Errorf("the finding points at line %d, but the rule reports a declaration that is not there",
					got[0].Pos.Line)
			}
			if !strings.Contains(got[0].String(), tc.path) {
				t.Errorf("the rendered finding does not name the file:\n%s", got[0])
			}
			if got[0].Fix == "" {
				t.Errorf("the finding says what is wrong but not what to do about it")
			}
		})
	}
}

func findingsFor(r lint.Result, rule string) []lint.Finding {
	var out []lint.Finding
	for _, f := range r.Findings {
		if f.Rule == rule {
			out = append(out, f)
		}
	}
	return out
}
