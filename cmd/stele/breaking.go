package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
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

A finding standing at error fails the run. A repository accepts a
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
  --audit         report this repository's stale permissions and lowered
                  rules instead of comparing for a merge. Exits non-zero
                  only when it finds a stale permission — a fact about a
                  file that needs an edit — and never for what this
                  repository has lowered, which is a decision, not a
                  defect. That non-zero exit is meant for a scheduled job
                  that reddens alone, not for the merge path: putting
                  --audit there blocks a merge on staleness, which is
                  exactly the fate an ordinary run refuses it. This reports
                  about this repository alone; there is no fleet-wide
                  aggregator behind it. Cannot be combined with --prune.
  --prune         delete this repository's stale permissions from the
                  manifest and nothing else, leaving every other byte of
                  the file unchanged. A dormant permission — one whose rule
                  is off — is never pruned: raising the rule back to error
                  makes it needed again. Cannot be combined with --audit.
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
		audit    = fs.Bool("audit", false, "report stale and lowered permissions instead of comparing for a merge")
		prune    = fs.Bool("prune", false, "delete stale permissions from the manifest and nothing else")
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
	if *audit && *prune {
		return fmt.Errorf("stele breaking: --audit and --prune cannot be combined\n\n%s", breakingUsage)
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
	if _, statErr := os.Stat(manifestPath); errors.Is(statErr, iofs.ErrNotExist) {
		// The working revision predates this repository's adoption of stele:
		// there is no stele.yaml at all, so there are no module roots to
		// check and no breaking configuration to read. That is exactly the
		// "nothing to compare" shape breaking.Load already gives the
		// previous revision (see ErrNoManifest) — the same reasoning
		// carries over to this side. Only the manifest's absence is
		// forgiven here: a manifest that exists but fails to parse falls
		// through to config.Load below and fails the run as it always has.
		//
		// --audit and --prune are also about a manifest that is not there —
		// there is nothing to audit and nothing to prune — so they take
		// this same exit rather than a confusing attempt to open a file
		// that does not exist.
		fmt.Fprint(stdout, breaking.Render(nil, breaking.Info{
			Outcome: breaking.NothingToCompare,
			Reason: "this revision has no " + lint.ManifestName + ": it predates this repository's " +
				"adoption of stele, so there are no module roots to check",
		}))
		return nil
	} else if statErr != nil {
		return statErr
	}
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
	// breaking.base and --base name a branch, fetched as refs/heads/<name>;
	// passing what "git branch -r" prints instead — origin/master — sends
	// the tool looking for refs/heads/origin/master and it is not there,
	// surfacing a confusing git error rather than the obvious mistake it
	// is. Caught here, before Choose ever runs, so the message names the
	// mistake and the fix rather than git's own wording.
	if effectiveBase != "" {
		if branch, ok := r.RemoteQualifiedBase(effectiveBase); ok {
			return fmt.Errorf("stele breaking: --base %s names a remote-tracking ref, not a branch; "+
				"pass the branch name alone: --base %s", effectiveBase, branch)
		}
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

	// --audit and --prune need the full comparison even when the trees
	// are unchanged: a stale permission is defined by what still matches
	// among today's findings, and an unchanged tree does not mean there
	// are none — it means, if anything, that every permission naming a
	// removed field is stale, which is exactly the fact these flags exist
	// to report. Taking the shortcut here would make --audit blind on
	// most runs, the same 84.7%-of-commits case the comment above notes.
	unchanged, err := breaking.TreesUnchanged(r, prev, paths)
	if err != nil {
		return err
	}
	if unchanged && !*audit && !*prune {
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
	rawFindings := breaking.Classify(changes, prevRev, cur)
	rawFindings = append(rawFindings, breaking.ClassifyClosure(prevRev, cur)...)
	// Severity is resolved against the working manifest only, mf.Breaking —
	// never prevRev's or cur's, for the reason given above ValidateConfig.
	findings := breaking.ApplySeverity(rawFindings, mf.Breaking)

	if *audit || *prune {
		// rawFindings, not findings: AuditLowered asks whether an ignore
		// list covers every path a rule actually fired on, and
		// ApplySeverity has already dropped exactly those findings from
		// findings — the ones an ignore list silenced are the evidence, so
		// the audit needs them, not the report a merge would see.
		return runBreakingAudit(mf, manifestPath, findings, rawFindings, prev, *prune, stdout)
	}

	// Permit runs after ApplySeverity: see its own doc comment for why the
	// order matters (a permission naming a rule this manifest set to off
	// must come out dormant, not silently matched against findings that
	// severity has already dropped).
	kept, stale := breaking.Permit(findings, mf.Breaking)
	notes = append(notes, breaking.PermitNotes(mf.Breaking, stale, findings)...)
	findings = kept

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
var errBreakingFindings = errors.New("breaking: at least one finding stands at error")

// runBreakingAudit implements --audit and --prune. Both start from the same
// place: which of mf.Breaking.Allow matched nothing among findings, split
// three ways: stale (spent — the change it approved is behind the base),
// dormant (its rule is off, not spent), and mismatched (its rule and
// subject match a live finding, but its change is spelled differently —
// not spent, not stale, misspelled). --audit reports all three, plus every
// rule this manifest has lowered, counting an ignore list that covers
// every path where the rule actually fired this run as lowered even while
// severity stays at error — the ignore-mechanism gap the design calls out
// by name. --prune deletes the stale entries only — never the dormant ones,
// never the mismatched ones, which name a live finding under the wrong
// spelling and would be destroyed, along with the reason a human wrote for
// them, by exactly the remedy a stale permission calls for.
//
// findings is post-ApplySeverity — the same set a merge run would see, and
// what StaleAllowIndices and permission matching compare against. rawFindings
// is the set before ApplySeverity dropped anything an ignore list or
// severity: off removed; AuditLowered needs it to see what an ignore list
// actually silenced. Neither slice is used to decide --prune or --audit's
// own exit status beyond producing the stale/dormant split: the valve
// itself (a finding standing at error failing the run) belongs to the
// ordinary path in runBreaking and is deliberately not reached here — an
// audit that could fail a merge on a finding would not be the valve this
// design asked for.
func runBreakingAudit(mf *config.File, manifestPath string, findings, rawFindings []breaking.Finding, prev breaking.Previous, prune bool, stdout io.Writer) error {
	idx := breaking.StaleAllowIndices(findings, mf.Breaking)

	// idx is "matched nothing", which is necessary but not sufficient for
	// stale: a permission naming the right rule and subject but the wrong
	// change also matches nothing by indexOfMatch's exact test, and it is
	// not stale — it is misspelled, and the finding it should have matched
	// still stands. That third case is split out here (staleMismatched) so
	// neither --audit's exit status nor --prune's deletion ever treats it
	// as stale; only PermitNotes' message differs for it, and PermitNotes
	// is given every bucket so it can print that message.
	var staleSpent, staleDormant, staleMismatched []config.Permission
	for _, i := range idx {
		p := mf.Breaking.Allow[i]
		if breaking.IsDormant(mf.Breaking, p) {
			staleDormant = append(staleDormant, p)
			continue
		}
		if _, ok := breaking.MismatchedChange(findings, p); ok {
			staleMismatched = append(staleMismatched, p)
			continue
		}
		staleSpent = append(staleSpent, p)
	}

	var notes []string
	notes = append(notes, breaking.AuditLowered(mf.Breaking, rawFindings)...)
	allStale := append(append(append([]config.Permission{}, staleSpent...), staleDormant...), staleMismatched...)
	notes = append(notes, breaking.PermitNotes(mf.Breaking, allStale, findings)...)

	fmt.Fprint(stdout, breaking.Render(nil, breaking.Info{
		Outcome:  breaking.Audited,
		Previous: prev.SHA,
		Reason:   prev.Reason,
		Notes:    notes,
		Findings: len(findings),
	}))

	if prune {
		// Prune matches staleSpent against the manifest's own current text
		// by (rule, subject, change), not by position — see its own doc
		// comment for why that matters even within one invocation.
		if err := breaking.Prune(manifestPath, staleSpent); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "stele: breaking: --prune removed %d stale permission(s); dormant permissions were left in place\n", len(staleSpent))
		return nil
	}

	// The audit valve: a stale permission is a fact about a file that
	// needs an edit, and --audit exists to fail on exactly that — never
	// on what this repository has lowered, which is a decision that
	// needed nobody's approval to make and needs none here to keep.
	if len(staleSpent) > 0 {
		return errAuditStale
	}
	return nil
}

// errAuditStale is returned by --audit when it finds at least one stale
// (spent, not dormant) permission. Its message is deliberately empty of
// detail: the report already printed to stdout names every stale
// permission, and repeating that here would just be noise on stderr.
var errAuditStale = errors.New("breaking --audit: at least one permission is stale")
