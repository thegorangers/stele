// Package compile turns the resolved closure of proto files into linked
// descriptors.
//
// The compiler never touches the filesystem on its own. Every file it opens
// is one the resolution graph handed it, because the graph is the only thing
// that knows an import path is unambiguous: it has already refused a path
// supplied by two different contents. A compiler that walked import roots
// itself would silently pick whichever root it happened to look in first, and
// the conflict the graph exists to catch would come back as a build that
// compiles one repository's contract against another's copy of it.
package compile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/bufbuild/protocompile"
	"github.com/bufbuild/protocompile/linker"
	"github.com/thegorangers/stele/internal/resolve"
)

// Compile links the named target files, resolving their imports — transitively
// — through the graph.
//
// Targets are import paths, in the coordinates an import statement uses, not
// paths on disk. The returned files are the targets only, in sorted order,
// with their dependencies reachable through each file's own descriptor.
//
// Determinism is a property of the result, not an accident of the call. The
// byte layout of the descriptor is what the whole acceptance criterion rests
// on (design §6.3), so the targets are sorted and deduplicated before
// compiling: naming the same set of files in another order, or naming one of
// them twice, produces the same descriptors in the same order. What varies
// between runs, and must not, would otherwise be invisible until a diff of
// generated code showed it.
//
// SourceCodeInfo is retained for every file compiled, by default. It is not
// the library default, and its absence does not fail anything: it costs every
// comment in the generated code, and shows up only when somebody reads the
// output. Whether to strip it from files that are imports rather than targets
// is a decision for whoever builds the request to a plugin, and this package
// deliberately does not make it here — dropping information early cannot be
// undone downstream. A caller that will never report a position from these
// files — the older side of a breaking-change comparison, say — can ask for
// it to be dropped with WithoutSourceInfo; the default is unchanged.
func Compile(ctx context.Context, g *resolve.Graph, targets []string, opts ...Option) (linker.Files, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	if g == nil {
		return nil, errors.New("compile: no resolution graph")
	}
	want := sortedUnique(targets)
	// An empty result is never a success: a run that compiled nothing has
	// almost certainly been asked the wrong question.
	if len(want) == 0 {
		return nil, errors.New("compile: no target files")
	}
	// Targets are checked against the graph before compiling so that a
	// mistyped target is reported as a mistyped target, rather than as
	// whatever the compiler says about a file it cannot open.
	for _, t := range want {
		if _, ok := g.FileFor(t); !ok {
			return nil, fmt.Errorf("compile: target %q is not provided by any module", t)
		}
	}

	sourceInfo := protocompile.SourceInfoStandard
	if o.dropSourceInfo {
		sourceInfo = protocompile.SourceInfoNone
	}
	c := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(graphResolver(g)),
		// Retaining source info is the whole reason this field is set by
		// default: the library default is SourceInfoNone.
		SourceInfoMode: sourceInfo,
	}
	files, err := c.Compile(ctx, want...)
	if err != nil {
		// protocompile's errors already carry file:line:col; wrapping keeps
		// that and says which stage produced it.
		return nil, fmt.Errorf("compile: %w", err)
	}
	return files, nil
}

// options holds the settings Option functions configure.
type options struct {
	dropSourceInfo bool
}

// Option configures one Compile call.
type Option func(*options)

// WithoutSourceInfo drops source information from the compiled files when
// drop is true. A caller that will never report a position — the older side
// of a breaking-change comparison — pays for every comment in every file
// otherwise. The default (opts omitted, or drop false) is unchanged: dropping
// information early cannot be undone downstream, so it is never the default.
func WithoutSourceInfo(drop bool) Option {
	return func(o *options) { o.dropSourceInfo = drop }
}

// graphResolver opens files through the graph and nothing else.
func graphResolver(g *resolve.Graph) protocompile.Resolver {
	return protocompile.ResolverFunc(func(importPath string) (protocompile.SearchResult, error) {
		f, ok := g.FileFor(importPath)
		if !ok {
			return protocompile.SearchResult{}, protoNotFound(importPath)
		}
		src, err := os.Open(f.Path)
		if err != nil {
			return protocompile.SearchResult{}, fmt.Errorf("%s (from %s): %w", importPath, f.Origin, err)
		}
		// Source, rather than a parsed or descriptor result, because only the
		// source text carries the comments that source info is made of.
		return protocompile.SearchResult{Source: src}, nil
	})
}

// protoNotFound reports an unresolvable import in the form protocompile
// expects, so that the standard-import fallback gets its turn before the
// error is final.
func protoNotFound(importPath string) error {
	return fmt.Errorf("%q is not provided by any module: %w", importPath, os.ErrNotExist)
}

// sortedUnique returns the targets sorted with duplicates removed. Empty
// entries are dropped rather than passed on: they can only come from a
// mis-split list, and the compiler would report them as an unhelpfully
// anonymous missing file.
func sortedUnique(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
