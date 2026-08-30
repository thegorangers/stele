package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/thegorangers/stele/internal/breaking"
	"github.com/thegorangers/stele/internal/cachedir"
	"github.com/thegorangers/stele/internal/config"
	"github.com/thegorangers/stele/internal/gitrepo"
	"github.com/thegorangers/stele/internal/lint"
	"github.com/thegorangers/stele/internal/source"
)

const breakingUsage = `stele breaking reports what changed in this repository's proto contracts that
would break a consumer.

This release is report-only: it always exits zero when it finds something to
report. There is no mechanism yet to permit a breaking change deliberately —
that is a later plan — and a command that failed a build with no way to
accept a finding would leave a repository one option: deleting the CI job.
That is the failure this design exists to avoid.

A failure to compare is different, and still fails the run: a shallow clone,
an unreadable manifest, a revision that cannot be fetched. Only findings are
non-fatal.

The previous revision is chosen the way a code review chooses one: the
merge-base with the base branch on a topic branch, or the first parent when
already on it. See --base and --against below.

Usage:
  stele breaking [flags]

Flags:
  --dir DIR       directory holding stele.yaml (default ".")
  --base BRANCH   the base branch this revision is compared against on a
                  topic branch. A later plan moves this into the manifest as
                  breaking.base; for now it is required unless --against is
                  given.
  --against REF   compare directly against REF, with no merge-base. This is
                  NOT a substitute for --base as a CI default: comparing
                  against a moving upstream ref such as origin/master is
                  exactly the neighbour-blaming comparison this design
                  rejects, one flag away. Use it for a manual, local check
                  against a specific commit.
  --cache-dir DIR where fetched repositories are kept (default
                  $XDG_CACHE_HOME/stele, else ~/.cache/stele;
                  $STELE_CACHE_DIR is honoured too)
`

func runBreaking(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("breaking", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}

	var (
		dir      = fs.String("dir", ".", "directory holding stele.yaml")
		base     = fs.String("base", "", "the base branch to compare against on a topic branch")
		against  = fs.String("against", "", "compare directly against this revision, no merge-base")
		cacheDir = fs.String("cache-dir", "", "where fetched repositories are kept")
		help     = fs.Bool("help", false, "show this help")
	)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, breakingUsage)
			return errHelp
		}
		return fmt.Errorf("%w\n\n%s", err, breakingUsage)
	}
	if *help {
		fmt.Fprint(stdout, breakingUsage)
		return errHelp
	}
	if rest := fs.Args(); len(rest) > 0 {
		return fmt.Errorf("unexpected argument %q; stele breaking takes flags only\n\n%s", rest[0], breakingUsage)
	}
	if *base == "" && *against == "" {
		return fmt.Errorf("stele breaking: --base or --against is required\n\n%s", breakingUsage)
	}

	r, err := gitrepo.Open(*dir)
	if err != nil {
		return err
	}
	if shallow, err := r.IsShallow(); err != nil {
		return err
	} else if shallow {
		return fmt.Errorf("breaking: %s is a shallow clone; merge-base and ancestry cannot be computed "+
			"from a shallow history — run git fetch --unshallow before stele breaking", *dir)
	}

	var prev breaking.Previous
	if *against != "" {
		prev, err = breaking.Against(r, *against)
		if err != nil {
			return err
		}
	} else {
		var ok bool
		prev, ok, err = breaking.Choose(r, *base)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprint(stdout, breaking.Render(nil, breaking.Info{
				Outcome: breaking.NothingToCompare,
				Reason:  "there is no previous revision to compare against here",
			}))
			return nil
		}
	}

	manifestPath := filepath.Join(*dir, lint.ManifestName)
	mf, err := config.Load(manifestPath)
	if err != nil {
		return err
	}
	// The breaking block is validated against the rule registry here, once,
	// from the working manifest, and nowhere else. breaking.Load below reads
	// a manifest for each side of the comparison, including the previous
	// revision's, and deliberately does not validate what it reads: the
	// valve is what this repository says about itself now, not what it said
	// at the previous revision. Letting the previous revision's block
	// configure the run would mean a permission merged yesterday silently
	// governing today's report, and a rule switched off in history going on
	// suppressing findings after somebody switched it back on — and it would
	// also mean a revision from before this block existed could fail to
	// load a manifest that was perfectly fine when it was written.
	if err := breaking.ValidateConfig(mf.Breaking); err != nil {
		return fmt.Errorf("%s: %w", manifestPath, err)
	}
	paths := make([]string, 0, len(mf.Modules)+1)
	for _, m := range mf.Modules {
		paths = append(paths, m.Path)
	}
	paths = append(paths, lint.LockName)

	unchanged, err := breaking.TreesUnchanged(r, prev, paths)
	if err != nil {
		return err
	}
	if unchanged {
		fmt.Fprint(stdout, breaking.Render(nil, breaking.Info{
			Outcome:  breaking.Unchanged,
			Previous: prev.SHA,
			Reason:   prev.Reason,
		}))
		return nil
	}

	root, err := cachedir.Root(*cacheDir)
	if err != nil {
		return err
	}
	fetch := source.NetworkFetch(root)

	cur, err := breaking.Load(ctx, r, prev.Working, fetch, false)
	if err != nil {
		if errors.Is(err, breaking.ErrNoOwnedProtos) {
			fmt.Fprint(stdout, breaking.Render(nil, breaking.Info{Outcome: breaking.NoOwnedProtos}))
			return nil
		}
		return err
	}

	prevRev, err := breaking.Load(ctx, r, prev.SHA, fetch, true)
	if err != nil {
		if errors.Is(err, breaking.ErrNoManifest) || errors.Is(err, breaking.ErrNoOwnedProtos) {
			fmt.Fprint(stdout, breaking.Render(nil, breaking.Info{
				Outcome: breaking.NothingToCompare,
				Reason:  err.Error(),
			}))
			return nil
		}
		return err
	}

	changes := breaking.Diff(prevRev, cur)
	findings := breaking.Classify(changes, prevRev, cur)
	findings = append(findings, breaking.ClassifyClosure(prevRev, cur)...)

	fmt.Fprint(stdout, breaking.Render(findings, breaking.Info{
		Outcome:  breaking.Compared,
		Previous: prev.SHA,
		Reason:   prev.Reason,
	}))
	// This release is report-only: findings never fail the run. Only the
	// errors returned above — a failure to compare — do.
	return nil
}
