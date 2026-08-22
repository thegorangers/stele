package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/thegorangers/stele/internal/cachedir"
	"github.com/thegorangers/stele/internal/gen"
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
	return gen.Run(ctx, gen.Options{
		Dir:            *dir,
		Targets:        targets,
		IncludeImports: *includeImports,
		Update:         *update,
		CacheRoot:      root,
	})
}
