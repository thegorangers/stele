package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/thegorangers/stele/internal/cachedir"
	"github.com/thegorangers/stele/internal/lint"
)

const lintUsage = `stele lint checks this repository's proto contracts against rules.

Only the modules this repository owns are checked. Dependencies are resolved
and compiled, because an import has to link, and they are not judged: a
finding about somebody else's contract is one nobody here can act on.

Findings are printed as path:line:col: severity: rule: message, with what to
do about it on the line beneath. The run fails when any finding is an error.

Usage:
  stele lint [flags]

Flags:
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
		help     = fs.Bool("help", false, "show this help")
	)
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
		CacheRoot: root,
		Warn:      stderr,
	})
	if err != nil {
		return err
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
func writeRules(ctx context.Context, w io.Writer, dir, cacheRoot string) error {
	rules, stop, err := lint.Rules(ctx, dir, cacheRoot)
	if err != nil {
		return err
	}
	defer stop()
	fmt.Fprintf(w, "Rules this run would apply. Every one runs at severity error unless %s says otherwise.\n\n",
		lint.ManifestName)
	for _, r := range rules {
		origin := ""
		if r.Plugin != "" {
			origin = fmt.Sprintf("  (from the plugin %q)", r.Plugin)
		}
		fmt.Fprintf(w, "  %-36s %s%s\n", r.ID(), r.Description(), origin)
	}
	return nil
}
