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
  --rules             print the rules this build carries, with their ids, and
                      exit. A rule id is what goes in stele.yaml, so this is
                      the list to write one from.
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
	if *rules {
		writeRules(stdout)
		return nil
	}

	root, err := cachedir.Root(*cacheDir)
	if err != nil {
		return err
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
	if rep.Failed() {
		return fmt.Errorf("lint: %d finding(s) at severity error; fix them, or say what they cost this "+
			"repository under lint.rules in %s", rep.Errors, lint.ManifestName)
	}
	return nil
}

// writeRules prints the loaded rules. It is not decoration: a rule id is a
// public contract that goes into a manifest, and a list somebody has to read
// the source to find is a list they will guess at instead.
func writeRules(w io.Writer) {
	fmt.Fprintf(w, "Rules this build carries. Every one runs at severity error unless %s says otherwise.\n\n",
		lint.ManifestName)
	for _, r := range lint.Builtin() {
		fmt.Fprintf(w, "  %-36s %s\n", r.ID(), r.Description())
	}
}
