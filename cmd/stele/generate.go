package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"os"
	"path/filepath"

	"github.com/thegorangers/stele/internal/cachedir"
	"github.com/thegorangers/stele/internal/gen"
	"github.com/thegorangers/stele/internal/report"
)

const generateUsage = `stele generate runs code generation plugins over this repository's protos.

Each input of a target is sent to the plugins as its own request, exactly as a
target with one input would be. A target with two inputs is not a target with
both inputs merged: file_to_generate differs, and so does the generated code.

Usage:
  stele generate [flags]

Flags:
  --target NAME       run only this generate target; repeatable. A name no
                      target carries is an error, not a run of nothing.
  --include-imports   generate code for the imports of the selected files too,
                      not only for the files themselves
  --update            re-resolve every ref to the commit it names today and
                      rewrite stele.lock. Without it a run takes the commits
                      the lock records and fails if a fetched tree does not
                      match the hashes recorded beside them.
  --dir DIR           directory holding stele.yaml (default ".")
  --report FILE       write the run's version report to FILE as JSON ("-" for
                      stdout). A one-line-per-component summary always goes to
                      stderr; this is the copy meant to be kept and compared
                      across runs.
  --cache-dir DIR     where fetched repositories are kept
                      (default $XDG_CACHE_HOME/stele, else ~/.cache/stele;
                      $STELE_CACHE_DIR is honoured too)
`

func runGenerate(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("generate", flag.ContinueOnError)
	// As in export: an unknown flag is reported once, by name, rather than
	// buried under the flag package's own dump.
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}

	var (
		dir            = fs.String("dir", ".", "directory holding stele.yaml")
		cacheDir       = fs.String("cache-dir", "", "where fetched repositories are kept")
		includeImports = fs.Bool("include-imports", false, "generate code for imports too")
		update         = fs.Bool("update", false, "re-resolve every ref and rewrite stele.lock")
		reportPath     = fs.String("report", "", "write the version report here as JSON; - for stdout")
		targets        repeated
		help           = fs.Bool("help", false, "show this help")
	)
	fs.Var(&targets, "target", "run only this generate target; repeatable")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, generateUsage)
			return errHelp
		}
		return fmt.Errorf("%w\n\n%s", err, generateUsage)
	}
	if *help {
		fmt.Fprint(stdout, generateUsage)
		return errHelp
	}
	if rest := fs.Args(); len(rest) > 0 {
		return fmt.Errorf("unexpected argument %q; stele generate takes flags only\n\n%s", rest[0], generateUsage)
	}

	root, err := cachedir.Root(*cacheDir)
	if err != nil {
		return err
	}
	rep, err := gen.Run(ctx, gen.Options{
		Dir:            *dir,
		Targets:        targets,
		IncludeImports: *includeImports,
		Update:         *update,
		CacheRoot:      root,
		Warn:           stderr,
	})
	if err != nil {
		return err
	}
	return emitReport(rep, *reportPath, stdout, stderr)
}

// emitReport says what produced the output.
//
// The summary goes to stderr unconditionally, because a run whose provenance
// has to be asked for is a run whose provenance is usually not asked for, and
// stderr keeps it out of any pipeline reading stdout. The JSON goes only where
// it was asked for: it is the artefact a later run is diffed against, and it
// carries no timestamp or host detail so that two runs with the same versions
// produce the same bytes.
//
// A report that cannot be written is an error even though the generation
// succeeded: the files are already on disk, and silently losing the evidence
// about them is exactly the failure this command exists to prevent.
func emitReport(rep *report.Report, path string, stdout, stderr io.Writer) error {
	if rep == nil {
		return nil
	}
	fmt.Fprint(stderr, rep.Summary())
	if path == "" {
		return nil
	}
	raw, err := rep.JSON()
	if err != nil {
		return fmt.Errorf("rendering the version report: %w", err)
	}
	if path == "-" {
		_, err := stdout.Write(raw)
		return err
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("writing the version report: %w", err)
		}
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return fmt.Errorf("writing the version report: %w", err)
	}
	return nil
}
