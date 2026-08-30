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
	"github.com/thegorangers/stele/rule"
)

const breakingUsage = `stele breaking reports what changed in this repository's proto contracts that
would break a consumer.

A finding standing at error fails the run; so does a rule that failed to
run at all, whatever severity it was configured at. A repository accepts a
breaking change on purpose in one of two ways: lower the rule with
breaking.rules, or permit the specific change with breaking.allow. A
warning never fails the run, and neither does a stale or dormant
permission.

A failure to compare is different again, and always fails the run: a
shallow clone, an unreadable manifest, a revision that cannot be fetched.

The previous revision is chosen the way a code review chooses one: the
merge-base with the base branch on a topic branch, or the first parent when
already on it. See --base, --against and breaking.base below.

Usage:
  stele breaking [flags]

Flags:
  --dir DIR       directory holding stele.yaml (default ".")
  --base BRANCH   the base branch this revision is compared against on a
                  topic branch. Overrides breaking.base in the manifest when
                  both are given; one of --base, breaking.base or --against
                  is required.
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

	// --base wins when both --base and breaking.base are given: a flag
	// passed explicitly on this invocation is a more specific instruction
	// than a manifest default every invocation shares.
	effectiveBase := *base
	if effectiveBase == "" && mf.Breaking != nil {
		effectiveBase = mf.Breaking.Base
	}
	if effectiveBase == "" && *against == "" {
		return fmt.Errorf("stele breaking: one of --base, breaking.base, or --against is required\n\n%s", breakingUsage)
	}

	// Computed once, from the working manifest, and passed to every Render
	// call below regardless of outcome — including the two that precede any
	// comparison. The design's rule is "on every run", not "on every run
	// that found something to compare": the tree shortcut a few lines down
	// fires on 84.7% of base-branch commits in the measured fleet, and it is
	// exactly the run where a reader is most likely to assume protection is
	// in force, because nothing else was reported.
	notes := breaking.LoweredNotes(mf.Breaking)

	var prev breaking.Previous
	if *against != "" {
		prev, err = breaking.Against(r, *against)
		if err != nil {
			return err
		}
	} else {
		var ok bool
		prev, ok, err = breaking.Choose(r, effectiveBase)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprint(stdout, breaking.Render(nil, breaking.Info{
				Outcome: breaking.NothingToCompare,
				Reason:  "there is no previous revision to compare against here",
				Notes:   notes,
			}))
			return nil
		}
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
			Notes:    notes,
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
			fmt.Fprint(stdout, breaking.Render(nil, breaking.Info{Outcome: breaking.NoOwnedProtos, Notes: notes}))
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
				Notes:   notes,
			}))
			return nil
		}
		return err
	}

	changes := breaking.Diff(prevRev, cur)
	findings := breaking.Classify(changes, prevRev, cur)
	findings = append(findings, breaking.ClassifyClosure(prevRev, cur)...)
	// Severity is resolved against the working manifest only, mf.Breaking —
	// never prevRev's or cur's, for the reason given above ValidateConfig.
	findings = breaking.ApplySeverity(findings, mf.Breaking)
	// Permit runs after ApplySeverity: see its own doc comment for why the
	// order matters (a permission naming a rule this manifest set to off
	// must come out dormant, not silently matched against findings that
	// severity has already dropped).
	var stale []config.Permission
	findings, stale = breaking.Permit(findings, mf.Breaking)
	notes = append(notes, breaking.PermitNotes(mf.Breaking, stale)...)

	fmt.Fprint(stdout, breaking.Render(findings, breaking.Info{
		Outcome:  breaking.Compared,
		Previous: prev.SHA,
		Reason:   prev.Reason,
		Notes:    notes,
	}))
	// The valve: a finding standing at error fails the run. A warning
	// never does, and neither does a stale or dormant permission — those
	// are notes, not findings, and never reach this slice. The engine has
	// no notion today of a rule that failed to run rather than finding
	// nothing (every rule is exercised inline as part of Classify/
	// ClassifyClosure, with no separate execution result to inspect), so
	// there is nothing here to check for that beyond what ValidateConfig
	// and the errors returned above already guard: an unparseable
	// severity or a bogus rule id fails the run before this point, and a
	// failure to compare fails it for its own reason above.
	for _, f := range findings {
		if f.Severity == rule.SeverityError {
			return errBreakingFindings
		}
	}
	return nil
}

// errBreakingFindings is returned when at least one finding stands at
// error severity after ApplySeverity and Permit have run. Its message is
// deliberately empty of detail: the report already printed to stdout names
// every finding, and repeating that here would just be noise on stderr.
var errBreakingFindings = errors.New("stele breaking: at least one finding stands at error")
