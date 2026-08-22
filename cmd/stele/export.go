package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/thegorangers/stele/internal/cachedir"
	"github.com/thegorangers/stele/internal/export"
)

const exportUsage = `stele export writes proto files into a directory tree, laid out by import path.

Usage:
  stele export --output DIR [flags]

Flags:
  --output DIR        directory to write the tree into (required)
  --dep NAME          export the files of this dependency instead of this
                      manifest's own, narrowed by that dependency's own paths.
                      This is how a producer's contract is vendored: the export
                      is pointed at somebody else's repository.
  --path PATH         limit the export to PATH; repeatable. PATH is relative to
                      the root of the module that supplies the file — the same
                      coordinates an import statement uses — and NOT to the
                      workspace. A path matching nothing is an error.
  --exclude-imports   write only the selected files, not the files they import
  --update            re-resolve every ref to the commit it names today and
                      rewrite stele.lock. Without it a run takes the commits
                      the lock records and fails if a fetched tree does not
                      match the hashes recorded beside them.
  --dir DIR           directory holding stele.yaml (default ".")
  --cache-dir DIR     where fetched repositories are kept
                      (default $XDG_CACHE_HOME/stele, else ~/.cache/stele;
                      $STELE_CACHE_DIR is honoured too)
`

// repeated collects a flag given more than once, in the order it was given.
type repeated []string

func (r *repeated) String() string { return fmt.Sprint([]string(*r)) }

func (r *repeated) Set(v string) error {
	*r = append(*r, v)
	return nil
}

func runExport(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	// The flag package's own usage dump is suppressed so that an unknown flag
	// is reported once, by name, rather than followed by a wall of text that
	// buries it.
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}

	var (
		output   = fs.String("output", "", "directory to write the tree into")
		dir      = fs.String("dir", ".", "directory holding stele.yaml")
		cacheDir = fs.String("cache-dir", "", "where fetched repositories are kept")
		dep      = fs.String("dep", "", "export the files of this dependency")
		exclude  = fs.Bool("exclude-imports", false, "write only the selected files")
		update   = fs.Bool("update", false, "re-resolve every ref and rewrite stele.lock")
		paths    repeated
		help     = fs.Bool("help", false, "show this help")
	)
	fs.Var(&paths, "path", "limit the export to this path; repeatable")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, exportUsage)
			return errHelp
		}
		// The message already names the offending flag; the usage text follows
		// so that the right spelling is one screen away.
		return fmt.Errorf("%w\n\n%s", err, exportUsage)
	}
	if *help {
		fmt.Fprint(stdout, exportUsage)
		return errHelp
	}
	if rest := fs.Args(); len(rest) > 0 {
		return fmt.Errorf("unexpected argument %q; stele export takes flags only\n\n%s", rest[0], exportUsage)
	}
	if *output == "" {
		return fmt.Errorf("--output is required\n\n%s", exportUsage)
	}

	root, err := cachedir.Root(*cacheDir)
	if err != nil {
		return err
	}
	return export.Run(ctx, export.Options{
		Dir:            *dir,
		Output:         *output,
		Dep:            *dep,
		Paths:          paths,
		ExcludeImports: *exclude,
		Update:         *update,
		CacheRoot:      root,
	})
}
