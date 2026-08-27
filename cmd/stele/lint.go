package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/thegorangers/stele/internal/cachedir"
	"github.com/thegorangers/stele/internal/lint"
)

const lintUsage = `stele lint checks this repository's proto contracts against rules.

Only the modules this repository owns are checked. Dependencies are resolved
and compiled, because an import has to link, and they are not judged: a
finding about somebody else's contract is one nobody here can act on.

Findings are printed as path:line:col: severity: rule: message, with what to
do about it on the line beneath. The run fails when any finding is an error.

A rule that reports more warnings than a handful prints one line saying how
many and where, instead of one line each, and that line names the command
that prints them. Errors are never rolled up: a build that fails without
saying what failed is not one anybody can fix.

The rules in the aip/ namespace implement the API Improvement Proposals
(https://google.aip.dev). They are on for every repository and they warn:
they say where a contract could be a better one, and none of them can fail a
build until stele.yaml says it should.

Usage:
  stele lint [flags]

Flags:
  --rule ID           check only this rule, and print every finding it makes
                      rather than a count. It is what the summary line of a
                      rolled-up rule tells you to run. Repeatable.
  --all-findings      print every finding of every rule, rolling nothing up
  --rules             print the rules a run here would apply, with their ids,
                      and exit. A rule id is what goes in stele.yaml, so this
                      is the list to write one from. It loads the rule plugins
                      the manifest declares and lists what they serve too.
  --dir DIR           directory holding stele.yaml (default ".")
  --update            re-resolve every ref to the commit it names today and
                      rewrite stele.lock, as generate's flag of the same name
                      does. Without it the run takes the commits the lock
                      records.
  --cache-dir DIR     where fetched repositories are kept (default
                      $XDG_CACHE_HOME/stele, else ~/.cache/stele;
                      $STELE_CACHE_DIR is honoured too)

Adopting it over contracts that were never linted:

  Findings on the first run are expected, and the way to a green build is the
  manifest rather than allow_failure in a CI job -- one is reviewed with the
  contracts it is about, the other is invisible from here:

    lint:
      rules:
        - id: stele/enum_value_prefix
          severity: warning       # reported, does not fail the run
        - id: stele/package_version_suffix
          severity: "off"         # not run at all
      ignore:
        - api/third_party

  A severity of warning does not protect new code from the same mistake. It
  buys the time to fix what is there, and it says so in a file people read.

Rules from outside this repository:

  A rule does not have to ship with this tool. A plugin serving rules is
  declared, and pinned on exactly the terms a code generation plugin is:

    lint:
      plugins:
        - name: house_rules
          module: example.com/house/cmd/stele-rule-house
          version: v1.2.0
      rules:
        - id: house/field_comment
          severity: warning

  A plugin that declares nothing -- neither module and version, nor downloads
  with a sha256, nor a path in this repository -- is whatever the machine's
  PATH resolves the name to, and that is refused: a rule that is not pinned can
  say different things on two machines with nothing in the manifest changing,
  and a lint's whole job is to be the same judge twice. If PATH is genuinely
  what is meant, say so with unpinned: true beside the name; the run and
  stele lint --rules then say the rule is not pinned, every time.

  Its rules run beside the built-ins over the same files and are configured the
  same way. A rule that cannot be loaded, dies, hangs or answers with rubbish
  fails the run naming the rule, the plugin and the file: a rule that did not
  run has checked nothing, and no severity applies to a finding it never made.
`

func runLint(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("lint", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}

	var (
		dir      = fs.String("dir", ".", "directory holding stele.yaml")
		cacheDir = fs.String("cache-dir", "", "where fetched repositories are kept")
		update   = fs.Bool("update", false, "re-resolve every ref and rewrite stele.lock")
		rules    = fs.Bool("rules", false, "print the rules this build carries and exit")
		only     multiFlag
		all      = fs.Bool("all-findings", false, "print every finding rather than rolling warnings up")
		help     = fs.Bool("help", false, "show this help")
	)
	fs.Var(&only, "rule", "check only this rule and print every finding it makes; repeatable")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, lintUsage)
			return errHelp
		}
		return fmt.Errorf("%w\n\n%s", err, lintUsage)
	}
	if *help {
		fmt.Fprint(stdout, lintUsage)
		return errHelp
	}
	if rest := fs.Args(); len(rest) > 0 {
		return fmt.Errorf("unexpected argument %q; stele lint takes flags only\n\n%s", rest[0], lintUsage)
	}
	root, err := cachedir.Root(*cacheDir)
	if err != nil {
		return err
	}
	if *rules {
		return writeRules(ctx, stdout, *dir, root)
	}
	rep, err := lint.Run(ctx, lint.Options{
		Dir:       *dir,
		Update:    *update,
		Only:      only,
		CacheRoot: root,
		Warn:      stderr,
	})
	if err != nil {
		return err
	}
	if *all {
		// Naming every rule is the same request as naming one, made of all of
		// them. There is no second mode in the renderer for it, because a
		// second mode is a second thing to be wrong.
		for _, r := range allRuleIDs(ctx, *dir, root) {
			rep.Detail = append(rep.Detail, r)
		}
	}
	// Findings go to stdout: they are the output of the command, and a
	// pipeline that collects them should not have to read stderr. The
	// summary goes to stderr beside the resolution warnings, so that piping
	// the findings somewhere does not swallow the count.
	rep.Write(stdout)
	fmt.Fprint(stderr, rep.Summary())
	// The two ways a run fails are different failures with different fixes,
	// and they are reported apart. A rule that could not run has said nothing
	// about this repository: no severity applies to it, because severity says
	// what a finding costs and there was no finding. Counting it as a finding
	// would send the reader looking for one that does not exist.
	switch {
	case rep.Errors > 0 && len(rep.Failures) > 0:
		return fmt.Errorf("lint: %d finding(s) at severity error, and %d rule check(s) did not run; "+
			"fix the findings, or say what they cost this repository under lint.rules in %s — and see the "+
			"lines above the findings for the rules that failed, whose silence is not a pass",
			rep.Errors, len(rep.Failures), lint.ManifestName)
	case len(rep.Failures) > 0:
		return fmt.Errorf("lint: %d rule check(s) did not run, and nothing they check has been checked; "+
			"the reason is printed above, one line each. Fix the rule, or drop its plugin from %s while it "+
			"is fixed — severity cannot silence this, because a rule that did not run produced no finding "+
			"for a severity to apply to", len(rep.Failures), lint.ManifestName)
	case rep.Errors > 0:
		return fmt.Errorf("lint: %d finding(s) at severity error; fix them, or say what they cost this "+
			"repository under lint.rules in %s", rep.Errors, lint.ManifestName)
	}
	return nil
}

// writeRules prints the rules a run here would apply. It is not decoration: a
// rule id is a public contract that goes into a manifest, and a list somebody
// has to read the source to find is a list they will guess at instead.
//
// The rules the manifest's plugins serve are listed beside the built-ins, and
// each says which plugin serves it. Those are the ids nobody can read out of
// this repository's source, so a listing without them would be a listing that
// omitted exactly the part that has to be looked up.
// multiFlag collects a repeatable string flag.
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ", ") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// allRuleIDs returns every rule a run here would apply. A failure to load the
// plugins is not reported: the run this is called after has already loaded
// them successfully, and what is being built is a list of rules to print in
// full rather than a judgement about anything.
func allRuleIDs(ctx context.Context, dir, cacheRoot string) []string {
	loaded, stop, err := lint.Rules(ctx, dir, cacheRoot)
	if err != nil {
		return nil
	}
	defer stop()
	out := make([]string, 0, len(loaded))
	for _, r := range loaded {
		out = append(out, r.ID())
	}
	return out
}

func writeRules(ctx context.Context, w io.Writer, dir, cacheRoot string) error {
	rules, stop, err := lint.Rules(ctx, dir, cacheRoot)
	if err != nil {
		return err
	}
	defer stop()
	fmt.Fprintf(w, "Rules this run would apply, with what a finding costs when %s says nothing "+
		"about the rule.\n\n", lint.ManifestName)
	for _, r := range rules {
		origin := ""
		if r.Plugin != "" {
			origin = fmt.Sprintf("  (from the plugin %q)", r.Plugin)
		}
		// A rule that only warns and one that fails the build are different
		// things to a reader deciding whether to act, and the difference is
		// not visible in the id.
		cost := "fails the build"
		if lint.DefaultSeverity(r.ID()) == lint.SeverityWarning {
			cost = "warns"
		}
		fmt.Fprintf(w, "  %-38s %-16s %s%s\n", r.ID(), cost, r.Description(), origin)
		// An unpinned rule listed the way a pinned one is would be the one
		// thing in this listing a reader has to act on, printed as if it were
		// not there.
		if r.Unpinned != "" {
			fmt.Fprintf(w, "  %-38s %-16s   %s\n", "", "", r.Unpinned)
		}
	}
	return nil
}
