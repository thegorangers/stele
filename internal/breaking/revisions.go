package breaking

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/bufbuild/protocompile/linker"
	"github.com/thegorangers/stele/internal/compile"
	"github.com/thegorangers/stele/internal/config"
	"github.com/thegorangers/stele/internal/gitrepo"
	"github.com/thegorangers/stele/internal/lint"
	"github.com/thegorangers/stele/internal/pin"
	"github.com/thegorangers/stele/internal/resolve"
)

// ErrNoManifest reports that a revision predates adoption of this tool: it
// has no stele.yaml at all, and there is nothing to compare it against.
var ErrNoManifest = errors.New("breaking: the revision has no manifest")

// ErrNoOwnedProtos reports that a revision's manifest owns no proto files of
// its own. It is its own condition, distinct from a compile failure and from
// a clean comparison: compile.Compile refuses an empty target list outright,
// so this has to be caught before it is ever called.
var ErrNoOwnedProtos = errors.New("breaking: the revision owns no proto files")

// Revision is one side of a breaking-change comparison: the compiled files
// this revision owns, and the import paths that make up Owned.
type Revision struct {
	Files linker.Files
	Owned []string
}

// Load materialises sha into a temporary directory and compiles it with the
// dependencies that revision itself recorded, not with whatever the working
// tree resolves today.
//
// That is the whole point: compiling an old revision's protos against
// today's pins either fails outright — a dependency that has since moved on
// — or, worse, silently attributes another repository's change to this one.
// So the revision's own stele.lock is what pins fetch, and a revision with a
// manifest but no lock is refused rather than resolved fresh, because
// resolving it fresh would pin every dependency to whatever commit its ref
// names today and compare this revision against dependencies it never used.
//
// prev says this is the older side of the comparison: it is compiled without
// source info, since nothing will ever report a position in it.
func Load(ctx context.Context, r *gitrepo.Repo, sha string, fetch resolve.FetchFunc, prev bool) (Revision, error) {
	dir, err := os.MkdirTemp("", "stele-breaking-")
	if err != nil {
		return Revision{}, err
	}
	defer os.RemoveAll(dir)

	if err := r.Materialise(sha, dir); err != nil {
		// Materialise can leave a partially extracted tree behind on
		// failure; the deferred RemoveAll above ensures dir is never reused.
		return Revision{}, err
	}

	manifestPath := filepath.Join(dir, lint.ManifestName)
	if _, err := os.Stat(manifestPath); errors.Is(err, fs.ErrNotExist) {
		return Revision{}, fmt.Errorf("%w: %s", ErrNoManifest, shortSHA(sha))
	} else if err != nil {
		return Revision{}, err
	}

	mf, err := config.Load(manifestPath)
	if err != nil {
		return Revision{}, err
	}

	lockPath := filepath.Join(dir, lint.LockName)
	if _, err := os.Stat(lockPath); errors.Is(err, fs.ErrNotExist) {
		return Revision{}, fmt.Errorf(
			"revision %s has a manifest and no %s: resolving it afresh would pin every "+
				"dependency to the commit its ref names today, and compare this revision "+
				"against dependencies it never used",
			shortSHA(sha), lint.LockName)
	} else if err != nil {
		return Revision{}, err
	}

	g, err := pin.Resolve(ctx, pin.Options{
		Dir:      dir,
		Manifest: mf,
		LockPath: lockPath,
		Fetch:    fetch,
	})
	if err != nil {
		return Revision{}, err
	}

	owned := ownedPaths(g)
	if len(owned) == 0 {
		return Revision{}, fmt.Errorf("%w: %s", ErrNoOwnedProtos, shortSHA(sha))
	}

	files, err := compile.Compile(ctx, g, owned, compile.WithoutSourceInfo(prev))
	if err != nil {
		return Revision{}, err
	}
	return Revision{Files: files, Owned: owned}, nil
}

// ownedPaths returns the import paths this revision's own repository
// supplies: a file is this repository's when the graph resolved it from no
// dependency. This mirrors internal/lint's unexported owned() predicate — a
// file with no git origin is this repository's — which is the one thing both
// must agree on; if it changes there, it changes here too.
func ownedPaths(g *resolve.Graph) []string {
	var out []string
	for _, p := range g.ImportPaths() {
		if f, ok := g.FileFor(p); ok && f.Origin.Git == "" {
			out = append(out, p)
		}
	}
	return out
}

// shortSHA truncates a commit to the 12-character form errors report it in.
// A revision materialised from a short ref is reported as-is; there is
// nothing to truncate that isn't already short.
func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
